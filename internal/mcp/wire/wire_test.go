package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// msgOf returns a JSON-RPC notification exactly n bytes long.
func msgOf(t *testing.T, n int) string {
	t.Helper()
	head, tail := `{"jsonrpc":"2.0","method":"x","params":{"p":"`, `"}}`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("message of %d bytes is too short", n)
	}
	return head + strings.Repeat("a", pad) + tail
}

func nested(n int) string {
	return strings.Repeat("[", n) + strings.Repeat("]", n)
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// oneByte hands out at most one byte per Read, so a bufio.Reader above it
// never reads ahead of what the reader asks for by more than one byte.
type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return o.r.Read(p)
}

type closeCounter struct {
	mu sync.Mutex
	n  int
}

func (c *closeCounter) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil
}

func (c *closeCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestDecodeClassifies(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"ping"}`, KindRequest},
		{`{"jsonrpc":"2.0","id":"a","method":"sampling/createMessage","params":{}}`, KindRequest},
		{`{"jsonrpc":"2.0","method":"notifications/progress","params":{"p":1}}`, KindNotification},
		{`{"jsonrpc":"2.0","id":1,"result":{}}`, KindResponse},
		{`{"jsonrpc":"2.0","id":1,"result":null}`, KindResponse},
		{`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse"}}`, KindResponse},
	}
	for _, c := range cases {
		m, err := Decode([]byte(c.in))
		if err != nil {
			t.Errorf("Decode(%s): %v", c.in, err)
			continue
		}
		if m.Kind != c.kind {
			t.Errorf("Decode(%s) kind %v, want %v", c.in, m.Kind, c.kind)
		}
	}
}

func TestDecodeRefusesMalformed(t *testing.T) {
	for _, in := range []string{
		``,
		`[]`,
		`[{"jsonrpc":"2.0","id":1,"result":{}}]`,
		`{"id":1,"result":{}}`,
		`{"jsonrpc":"1.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0"}`,
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"m"}}`,
		`{"jsonrpc":"2.0","id":1,"error":null}`,
		`{"jsonrpc":"2.0","id":1,"error":"x"}`,
		`{"jsonrpc":"2.0","id":{},"result":{}}`,
		`{"jsonrpc":"2.0","method":"m","params":3}`,
		`{"jsonrpc":"2.0","method":"m","result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}} {}`,
		`{"jsonrpc":"2.0","id":1,"result":{`,
	} {
		if _, err := Decode([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("Decode(%q) = %v, want ErrMalformed", in, err)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	var ids IDs
	a, b := ids.Next(), ids.Next()
	if a != 1 || b != 2 {
		t.Fatalf("ids %d, %d; want 1, 2", a, b)
	}
	var buf bytes.Buffer
	params := json.RawMessage("{\n  \"name\": \"t\"\n}")
	if err := WriteLine(&buf, NewRequest(b, "tools/call", params)); err != nil {
		t.Fatal(err)
	}
	if err := WriteLine(&buf, NewNotification("notifications/initialized", nil)); err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"t"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	if buf.String() != want {
		t.Fatalf("wrote %q, want %q", buf.String(), want)
	}
	r := NewLineReader(&buf, Limits{})
	for _, kind := range []Kind{KindRequest, KindNotification} {
		b, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode(b)
		if err != nil || m.Kind != kind {
			t.Fatalf("Decode(%s) = %v, %v", b, m.Kind, err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("end: %v, want io.EOF", err)
	}
}

func TestLineReader(t *testing.T) {
	in := "{\"a\":1}\n\n\r\n{\"b\":2}\r\n{\"c\":3}"
	r := NewLineReader(strings.NewReader(in), Limits{})
	for _, want := range []string{`{"a":1}`, `{"b":2}`} {
		got, err := r.Next()
		if err != nil || string(got) != want {
			t.Fatalf("Next = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := r.Next(); err != io.ErrUnexpectedEOF {
		t.Fatalf("unterminated line: %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestLineReaderSizeBound(t *testing.T) {
	const n = 100
	at := msgOf(t, n)
	over := msgOf(t, n+1)
	lim := Limits{MaxMessageBytes: n}
	for _, c := range []struct {
		name, in string
		err      error
	}{
		{"LF at bound", at + "\n", nil},
		{"CRLF at bound", at + "\r\n", nil},
		{"LF over", over + "\n", ErrMessageTooLarge},
		{"CRLF over", over + "\r\n", ErrMessageTooLarge},
		{"CR CRLF over", at + "\r\r\n", ErrMessageTooLarge},
	} {
		got, err := NewLineReader(strings.NewReader(c.in), lim).Next()
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.err)
		}
		if c.err == nil && string(got) != at {
			t.Errorf("%s: got %q", c.name, got)
		}
	}
}

func TestLineReaderStopsAtBound(t *testing.T) {
	const n = 1000
	cr := &countingReader{r: strings.NewReader(strings.Repeat("a", 100*n))}
	_, err := NewLineReader(cr, Limits{MaxMessageBytes: n}).Next()
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err %v, want ErrMessageTooLarge", err)
	}
	if cr.n > n+4096+1 {
		t.Fatalf("read %d bytes for a %d-byte bound", cr.n, n)
	}
}

func TestDepthBound(t *testing.T) {
	ok := nested(64)
	deep := nested(65)
	stringy := `{"a":"` + strings.Repeat("[{", 100) + `\"[[["}`
	for _, c := range []struct {
		name, in string
		err      error
	}{
		{"64 deep", ok, nil},
		{"65 deep", deep, ErrTooDeep},
		{"brackets in strings", stringy, nil},
	} {
		if _, err := NewLineReader(strings.NewReader(c.in+"\n"), Limits{}).Next(); !errors.Is(err, c.err) {
			t.Errorf("line %s: %v, want %v", c.name, err, c.err)
		}
		if _, err := ReadBody(strings.NewReader(c.in), Limits{}); !errors.Is(err, c.err) {
			t.Errorf("body %s: %v, want %v", c.name, err, c.err)
		}
		if _, err := NewEventReader(strings.NewReader("data: "+c.in+"\n\n"), Limits{}).Next(); !errors.Is(err, c.err) {
			t.Errorf("event %s: %v, want %v", c.name, err, c.err)
		}
	}
}

func TestDepthBoundStopsReading(t *testing.T) {
	in := strings.Repeat("[", 1<<20)
	cr := &countingReader{r: strings.NewReader(in)}
	if _, err := NewLineReader(cr, Limits{}).Next(); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err %v, want ErrTooDeep", err)
	}
	// A line reader checks each buffered slice as it arrives.
	if cr.n > 4096 {
		t.Fatalf("read %d bytes before refusing", cr.n)
	}
	cr = &countingReader{r: in2(in)}
	if _, err := NewEventReader(cr, Limits{}).Next(); err == nil {
		t.Fatal("no error for a stream of brackets")
	}
	cr = &countingReader{r: in2("data: " + in)}
	if _, err := NewEventReader(cr, Limits{}).Next(); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("event err %v, want ErrTooDeep", err)
	}
	// An event reader checks each byte: "data: " and the 65th bracket.
	if cr.n != 6+65 {
		t.Fatalf("event: read %d bytes before refusing", cr.n)
	}
}

func TestReadBodySizeBound(t *testing.T) {
	const n = 100
	at := msgOf(t, n)
	got, err := ReadBody(strings.NewReader(at), Limits{MaxMessageBytes: n})
	if err != nil || string(got) != at {
		t.Fatalf("at bound: %q, %v", got, err)
	}
	if _, err := ReadBody(strings.NewReader(msgOf(t, n+1)), Limits{MaxMessageBytes: n}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("over: %v, want ErrMessageTooLarge", err)
	}
	cr := &countingReader{r: strings.NewReader(strings.Repeat("a", 10<<20))}
	if _, err := ReadBody(cr, Limits{MaxMessageBytes: n}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("long: %v", err)
	}
	if cr.n > 32<<10 {
		t.Fatalf("read %d bytes for a %d-byte bound", cr.n, n)
	}
}

func readEvents(t *testing.T, in string, lim Limits) ([]string, error) {
	t.Helper()
	r := NewEventReader(strings.NewReader(in), lim)
	var out []string
	for {
		b, err := r.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, string(b))
	}
}

func TestEventReader(t *testing.T) {
	in := "\xEF\xBB\xBF" +
		": a comment\n" +
		"id: prime\ndata:\n\n" + // a priming event with empty data
		"event: message\ndata: {\"a\":1}\n\n" +
		"data:{\"b\":\ndata:  2}\nid: 7\nretry: 10\n\n" + // joined; only one space is taken
		"data: one\r\ndata: two\r\n\r\n" +
		"data: three\rdata: four\r\r" +
		"data\ndata\n\n" + // a bare field name is an empty value
		"datum: x\nDATA: y\ndatas: z\n\n" + // none of these is data
		"data: unterminated\n"
	got, err := readEvents(t, in, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`{"a":1}`, "{\"b\":\n 2}", "one\ntwo", "three\nfour", "\n"}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("events %q, want %q", got, want)
	}
}

func TestEventReaderSizeBound(t *testing.T) {
	const n = 100
	at := msgOf(t, n)
	lim := Limits{MaxMessageBytes: n}
	// Framing lines longer than any slack, each under the bound on its own.
	longID := "id: 0b6c2f4e-6a3d-4d7e-9a51-3f0e1c2b7d88\n"
	longComment := ": " + strings.Repeat("keep-alive ", 8) + "\n"
	// at[60] is a padding byte; the joining line feed takes its place.
	split := at[:60] + "\ndata: " + at[61:]
	splitWant := at[:60] + "\n" + at[61:]
	for _, c := range []struct {
		name, in, want string
		err            error
	}{
		{"at bound", "data: " + at + "\n\n", at, nil},
		{"at bound, no space", "data:" + at + "\n\n", at, nil},
		{"at bound, CRLF", "data: " + at + "\r\n\r\n", at, nil},
		{"at bound, then long id", "data: " + at + "\n" + longID + "\n", at, nil},
		{"at bound, then comment", "data: " + at + "\n" + longComment + "\n", at, nil},
		{"at bound, then event", "data: " + at + "\nevent: " + strings.Repeat("m", 90) + "\n\n", at, nil},
		{"multi-line at bound", "data: " + split + "\n\n", splitWant, nil},
		{"multi-line at bound, comment between", "data: " + at[:60] + "\n" + longComment + longID + "data: " + at[61:] + "\n\n", splitWant, nil},
		{"over by one", "data: " + msgOf(t, n+1) + "\n\n", "", ErrMessageTooLarge},
		{"multi-line over by one", "data: " + at[:60] + "\ndata: " + at[60:] + "\n\n", "", ErrMessageTooLarge},
		{"non-data line at its own bound", ": " + strings.Repeat("c", n) + "\ndata: " + at + "\n\n", at, nil},
		{"non-data line over its own bound", ": " + strings.Repeat("c", n+1) + "\n", "", ErrMessageTooLarge},
		{"field name over the bound", strings.Repeat("f", n+1) + "\n", "", ErrMessageTooLarge},
	} {
		got, err := readEvents(t, c.in, lim)
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.err)
			continue
		}
		if c.err == nil && (len(got) != 1 || got[0] != c.want) {
			t.Errorf("%s: events %q, want [%q]", c.name, got, c.want)
		}
	}
}

// TestEventReaderStopsAtBound shows the bound is enforced while reading: an
// event whose data passes the bound across several lines is refused as soon
// as the data passes it, having read no more than the bound plus framing.
func TestEventReaderStopsAtBound(t *testing.T) {
	const n = 1000
	in := "data: " + strings.Repeat("a", 990) + "\n" +
		": " + strings.Repeat("c", 500) + "\n" +
		"data: " + strings.Repeat("b", 100*n) + "\n\n"
	cr := &countingReader{r: in2(in)}
	_, err := NewEventReader(cr, Limits{MaxMessageBytes: n}).Next()
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err %v, want ErrMessageTooLarge", err)
	}
	// Data: 990 + the joining line feed + 9 more bytes fills the bound; the
	// 10th "b" is refused. Framing: "data: ", "\n", the comment line, "data: ".
	framing := 6 + 1 + 503 + 6
	if max := n + framing + 1; cr.n > max {
		t.Fatalf("read %d bytes; at most %d should be needed", cr.n, max)
	}
}

func in2(s string) io.Reader { return oneByte{strings.NewReader(s)} }

func TestNewResponseReader(t *testing.T) {
	note := `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`
	resp := `{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, resp)
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "id: 1\ndata:\n\n")
			_, _ = io.WriteString(w, "event: message\ndata: "+note+"\n\n")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "data: "+resp+"\n\n")
		case "/html":
			w.Header().Set("Content-Type", "text/html")
		}
	}))
	defer srv.Close()

	for _, path := range []string{"/json", "/sse"} {
		hr, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		mr, err := NewResponseReader(hr, Limits{})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		s := &Stream{Messages: mr, Closer: hr.Body}
		got, err := s.Await(3)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if string(got.Result) != `{"ok":true}` {
			t.Fatalf("%s: result %s", path, got.Result)
		}
		_ = hr.Body.Close()
	}

	hr, err := http.Get(srv.URL + "/html")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hr.Body.Close() }()
	if _, err := NewResponseReader(hr, Limits{}); !errors.Is(err, ErrContentType) {
		t.Fatalf("html: %v, want ErrContentType", err)
	}
}

// stdioPair is an in-memory local server: the client reads what the test
// writes to out, and answers go to in.
type stdioPair struct {
	out    *io.PipeWriter
	stream *Stream
	in     *bufio.Reader
}

func newStdioPair(lim Limits) *stdioPair {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	p := &stdioPair{out: outW, in: bufio.NewReader(inR)}
	p.stream = &Stream{
		Messages: NewLineReader(outR, lim),
		Reply: func(b []byte) error {
			_, err := inW.Write(append(b, '\n'))
			return err
		},
		Closer: outR,
		Limits: lim,
	}
	return p
}

// Acceptance: a request from the server to the client is answered with an
// error.
func TestServerRequestAnsweredWithError(t *testing.T) {
	p := newStdioPair(Limits{})
	go func() {
		_, _ = io.WriteString(p.out, `{"jsonrpc":"2.0","id":"s-1","method":"sampling/createMessage","params":{"messages":[]}}`+"\n")
		_, _ = io.WriteString(p.out, `{"jsonrpc":"2.0","id":9,"method":"roots/list"}`+"\n")
		_, _ = io.WriteString(p.out, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n")
	}()
	type result struct {
		r   Response
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := p.stream.Await(1)
		done <- result{r, err}
	}()

	for _, id := range []string{`"s-1"`, `9`} {
		line, err := p.in.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode([]byte(line))
		if err != nil {
			t.Fatalf("answer %q: %v", line, err)
		}
		if m.Kind != KindResponse || string(m.ID) != id || m.Error == nil || m.Error.Code != CodeMethodNotFound || m.Result != nil {
			t.Fatalf("answer %q is not an error for id %s", line, id)
		}
	}
	res := <-done
	if res.err != nil || string(res.r.ID) != "1" {
		t.Fatalf("Await = %+v, %v", res.r, res.err)
	}
}

func TestAwaitReturnsErrorResponse(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":4,"error":{"code":-32602,"message":"bad"}}` + "\n"
	s := &Stream{Messages: NewLineReader(strings.NewReader(in), Limits{})}
	r, err := s.Await(4)
	if err != nil || r.Error == nil || r.Error.Code != CodeInvalidParams {
		t.Fatalf("Await = %+v, %v", r, err)
	}
}

func TestAwaitSkipsOtherResponses(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":3,"result":{"old":true}}` + "\n" +
		`{"jsonrpc":"2.0","id":"4","result":{"string":true}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"result":{"ok":true}}` + "\n"
	s := &Stream{Messages: NewLineReader(strings.NewReader(in), Limits{})}
	r, err := s.Await(4)
	if err != nil || string(r.Result) != `{"ok":true}` {
		t.Fatalf("Await = %+v, %v", r, err)
	}
}

func TestAwaitMalformed(t *testing.T) {
	cc := &closeCounter{}
	s := &Stream{Messages: NewLineReader(strings.NewReader("not json\n"), Limits{}), Closer: cc}
	if _, err := s.Await(1); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err %v, want ErrMalformed", err)
	}
}

func notifications(n int) string {
	return strings.Repeat(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`+"\n", n)
}

// Acceptance: a 257th notification closes the connection.
func TestNotificationBound(t *testing.T) {
	answer := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"

	cc := &closeCounter{}
	s := &Stream{Messages: NewLineReader(strings.NewReader(notifications(256)+answer), Limits{}), Closer: cc}
	if _, err := s.Await(1); err != nil {
		t.Fatalf("256 notifications: %v", err)
	}
	if cc.count() != 0 {
		t.Fatal("256 notifications closed the connection")
	}

	cc = &closeCounter{}
	s = &Stream{Messages: NewLineReader(strings.NewReader(notifications(257)+answer), Limits{}), Closer: cc}
	if _, err := s.Await(1); !errors.Is(err, ErrTooManyNotifications) || !errors.Is(err, ErrBound) {
		t.Fatalf("257 notifications: %v, want ErrTooManyNotifications", err)
	}
	if cc.count() != 1 {
		t.Fatalf("closed %d times, want 1", cc.count())
	}
}

// Acceptance: a 257th notification over a live stdio pipe closes the
// connection, and the server's next write fails.
func TestNotificationBoundOverPipe(t *testing.T) {
	p := newStdioPair(Limits{})
	writeErr := make(chan error, 1)
	go func() {
		_, err := io.WriteString(p.out, notifications(257))
		if err == nil {
			_, err = io.WriteString(p.out, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n")
		}
		writeErr <- err
	}()
	if _, err := p.stream.Await(1); !errors.Is(err, ErrTooManyNotifications) {
		t.Fatalf("err %v, want ErrTooManyNotifications", err)
	}
	if err := <-writeErr; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("server write after close: %v, want io.ErrClosedPipe", err)
	}
}

func TestServerRequestsCountAgainstBound(t *testing.T) {
	req := strings.Repeat(`{"jsonrpc":"2.0","id":5,"method":"ping"}`+"\n", 257)
	cc := &closeCounter{}
	replies := 0
	s := &Stream{
		Messages: NewLineReader(strings.NewReader(req), Limits{}),
		Reply:    func([]byte) error { replies++; return nil },
		Closer:   cc,
	}
	if _, err := s.Await(1); !errors.Is(err, ErrTooManyNotifications) {
		t.Fatalf("err %v, want ErrTooManyNotifications", err)
	}
	if replies != 256 || cc.count() != 1 {
		t.Fatalf("replies %d, closes %d; want 256, 1", replies, cc.count())
	}
}

// Acceptance: a message over the size bound and nesting over the depth bound
// each close the connection.
func TestMessageAndDepthBoundsClose(t *testing.T) {
	big := msgOf(t, 2000)
	deep := `{"jsonrpc":"2.0","method":"x","params":` + nested(64) + `}`
	lim := Limits{MaxMessageBytes: 1000}
	for _, c := range []struct {
		name string
		mk   func(cc *closeCounter) *Stream
		err  error
	}{
		{"line too large", func(cc *closeCounter) *Stream {
			return &Stream{Messages: NewLineReader(strings.NewReader(big+"\n"), lim), Closer: cc}
		}, ErrMessageTooLarge},
		{"line too deep", func(cc *closeCounter) *Stream {
			return &Stream{Messages: NewLineReader(strings.NewReader(deep+"\n"), lim), Closer: cc}
		}, ErrTooDeep},
		{"body too large", func(cc *closeCounter) *Stream {
			return &Stream{Messages: &bodyReader{r: strings.NewReader(big), lim: lim.orDefault()}, Closer: cc}
		}, ErrMessageTooLarge},
		{"event too large", func(cc *closeCounter) *Stream {
			return &Stream{Messages: NewEventReader(strings.NewReader("data: "+big+"\n\n"), lim), Closer: cc}
		}, ErrMessageTooLarge},
		{"event too deep", func(cc *closeCounter) *Stream {
			return &Stream{Messages: NewEventReader(strings.NewReader("data: "+deep+"\n\n"), lim), Closer: cc}
		}, ErrTooDeep},
	} {
		cc := &closeCounter{}
		_, err := c.mk(cc).Await(1)
		if !errors.Is(err, c.err) || !errors.Is(err, ErrBound) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.err)
		}
		if cc.count() != 1 {
			t.Errorf("%s: closed %d times, want 1", c.name, cc.count())
		}
	}
	if errors.Is(ErrMessageTooLarge, ErrTooDeep) || errors.Is(ErrTooDeep, ErrTooManyNotifications) ||
		errors.Is(ErrTooManyNotifications, ErrMessageTooLarge) {
		t.Fatal("bound errors are not distinct")
	}
}

// Acceptance: a too-large event over HTTP closes the response, so a server
// still writing sees the connection end.
func TestEventBoundClosesHTTP(t *testing.T) {
	writeFailed := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ")
		chunk := strings.Repeat("a", 64<<10)
		for i := 0; i < 1024; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				writeFailed <- true
				return
			}
			w.(http.Flusher).Flush()
		}
		writeFailed <- false
	}))
	defer srv.Close()
	hr, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := NewResponseReader(hr, Limits{MaxMessageBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s := &Stream{Messages: mr, Closer: hr.Body}
	if _, err := s.Await(1); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err %v, want ErrMessageTooLarge", err)
	}
	if !<-writeFailed {
		t.Fatal("the server wrote its whole stream; the connection was not closed")
	}
}

// Acceptance: standard error past its bound is dropped and counted.
func TestStderrSink(t *testing.T) {
	s := NewStderrSink(10)
	for _, w := range []string{"abcd", "efgh", "ijkl", "mnop"} {
		n, err := s.Write([]byte(w))
		if n != len(w) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", w, n, err)
		}
	}
	if string(s.Bytes()) != "abcdefghij" || s.Dropped() != 6 {
		t.Fatalf("kept %q, dropped %d", s.Bytes(), s.Dropped())
	}
	if NewStderrSink(0).max != MaxStderrBytes {
		t.Fatal("default bound is not mcp.max_stderr_bytes")
	}
}

func TestStderrSinkFromPipe(t *testing.T) {
	r, w := io.Pipe()
	s := NewStderrSink(0)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(s, r)
		done <- err
	}()
	const total = 3 << 20
	chunk := bytes.Repeat([]byte("e"), 4096)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < total/4/len(chunk); j++ {
				_, _ = w.Write(chunk)
			}
		}()
	}
	wg.Wait()
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(s.Bytes()) != MaxStderrBytes || s.Dropped() != total-MaxStderrBytes {
		t.Fatalf("kept %d, dropped %d", len(s.Bytes()), s.Dropped())
	}
}

// gateReader yields each message only when the test sends it, and tells
// the test when a read has begun.
type gateReader struct {
	msgs    chan []byte
	reading chan struct{}
}

func newGate() *gateReader {
	return &gateReader{msgs: make(chan []byte), reading: make(chan struct{}, 8)}
}

func (g *gateReader) Next() ([]byte, error) {
	g.reading <- struct{}{}
	b, ok := <-g.msgs
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}

func TestHandoffKeepsAMessageItsWaiterGaveUpOn(t *testing.T) {
	g := newGate()
	h := NewHandoff(g)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.NextContext(ctx)
		done <- err
	}()
	<-g.reading
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	g.msgs <- []byte("late")
	if _, err := h.NextContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter whose ctx ended took a message: %v", err)
	}
	b, err := h.Next()
	if err != nil || string(b) != "late" {
		t.Fatalf("next = %q, %v", b, err)
	}
	if len(g.reading) != 0 {
		t.Error("the held message was read again")
	}
	close(g.msgs)
	if _, err := h.Next(); err != io.EOF {
		t.Fatalf("err = %v", err)
	}
}

func TestAwaitContextStopsAndTheNextAwaitGetsItsReply(t *testing.T) {
	g := newGate()
	s := &Stream{Messages: g}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := s.AwaitContext(ctx, 1)
		errc <- err
	}()
	<-g.reading
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	go func() {
		g.msgs <- []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
		g.msgs <- []byte(`{"jsonrpc":"2.0","id":2,"result":{"n":2}}`)
	}()
	r, err := s.AwaitContext(context.Background(), 2)
	if err != nil || string(r.Result) != `{"n":2}` {
		t.Fatalf("response = %+v, %v", r, err)
	}
}
