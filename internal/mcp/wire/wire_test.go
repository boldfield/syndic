package wire

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type closeSpy struct{ closed bool }

func (c *closeSpy) Close() error { c.closed = true; return nil }

func awaitLines(t *testing.T, in string, lim Limits, reply func(Response) error) (Message, *closeSpy, error) {
	t.Helper()
	spy := &closeSpy{}
	m, err := Await(NewLineReader(strings.NewReader(in), lim), 1, reply, spy, lim)
	return m, spy, err
}

func TestServerRequestAnsweredWithError(t *testing.T) {
	var got []Response
	in := `{"jsonrpc":"2.0","id":"s1","method":"sampling/createMessage"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"
	m, spy, err := awaitLines(t, in, Limits{}, func(r Response) error { got = append(got, r); return nil })
	if err != nil || string(m.Result) != `{"ok":true}` || spy.closed {
		t.Fatalf("m=%v err=%v closed=%v", m, err, spy.closed)
	}
	if len(got) != 1 || string(got[0].ID) != `"s1"` || got[0].Error == nil || got[0].Error.Code != CodeMethodNotFound {
		t.Fatalf("reply = %+v", got)
	}
}

func TestMessageOverSizeCloses(t *testing.T) {
	lim := Limits{MaxMessageBytes: 64}
	in := `{"jsonrpc":"2.0","id":1,"result":"` + strings.Repeat("x", 100) + `"}` + "\n"
	_, spy, err := awaitLines(t, in, lim, nil)
	if !errors.Is(err, ErrMessageTooLarge) || !spy.closed {
		t.Fatalf("err=%v closed=%v", err, spy.closed)
	}
}

func TestSizeBoundDoesNotBuffer(t *testing.T) {
	// An endless line is refused after the bound, not read to its end.
	r := io.MultiReader(strings.NewReader(`{"a":"`), neverEnding{})
	_, err := NewLineReader(r, Limits{MaxMessageBytes: 1000}).Next()
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestDepthOverBoundCloses(t *testing.T) {
	deep := strings.Repeat("[", 65) + strings.Repeat("]", 65)
	in := `{"jsonrpc":"2.0","id":1,"result":` + deep + "}\n"
	_, spy, err := awaitLines(t, in, Limits{}, nil)
	if !errors.Is(err, ErrTooDeep) || !spy.closed {
		t.Fatalf("err=%v closed=%v", err, spy.closed)
	}
	// Brackets inside strings do not count; depth 64 is allowed.
	ok := strings.Repeat("[", 62) + strings.Repeat("]", 62)
	in = `{"jsonrpc":"2.0","id":1,"result":{"s":"[[[[[[` + strings.Repeat("[", 100) + `","v":` + ok + "}}\n"
	if _, _, err = awaitLines(t, in, Limits{}, nil); err != nil {
		t.Fatal(err)
	}
}

func notes(n int) string {
	return strings.Repeat(`{"jsonrpc":"2.0","method":"notifications/progress"}`+"\n", n)
}

func TestNotificationBound(t *testing.T) {
	final := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
	if _, spy, err := awaitLines(t, notes(256)+final, Limits{}, nil); err != nil || spy.closed {
		t.Fatalf("256: err=%v closed=%v", err, spy.closed)
	}
	_, spy, err := awaitLines(t, notes(257)+final, Limits{}, nil)
	if !errors.Is(err, ErrTooManyNotifications) || !spy.closed {
		t.Fatalf("257: err=%v closed=%v", err, spy.closed)
	}
}

func TestPipe(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_ = WriteLine(pw, NewNotification("notifications/x", nil), Limits{})
		_ = WriteLine(pw, Response{JSONRPC: "2.0", ID: []byte("1"), Result: []byte(`{}`)}, Limits{})
	}()
	m, err := Await(NewLineReader(pr, Limits{}), 1, nil, nil, Limits{})
	if err != nil || !m.IsResponse() {
		t.Fatal(m, err)
	}
}

func TestWriteLineBound(t *testing.T) {
	var b bytes.Buffer
	err := WriteLine(&b, NewRequest(1, "m", strings.Repeat("x", 100)), Limits{MaxMessageBytes: 50})
	if !errors.Is(err, ErrMessageTooLarge) || b.Len() != 0 {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.HandlerFunc) *http.Response {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestHTTPJSONBody(t *testing.T) {
	resp := get(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"a":1}}`)
	})
	src, err := NewHTTPReader(resp, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Await(src, 1, nil, nil, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPBodyBounds(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":1,"result":"` + strings.Repeat("x", 200) + `"}`
	resp := get(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, big)
	})
	src, _ := NewHTTPReader(resp, Limits{MaxMessageBytes: 100})
	spy := &closeSpy{}
	if _, err := Await(src, 1, nil, spy, Limits{}); !errors.Is(err, ErrMessageTooLarge) || !spy.closed {
		t.Fatalf("err=%v closed=%v", err, spy.closed)
	}
	deep := `{"jsonrpc":"2.0","id":1,"result":` + strings.Repeat("[", 70) + `}`
	resp = get(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = fmt.Fprint(w, deep)
	})
	src, _ = NewHTTPReader(resp, Limits{})
	if _, err := Await(src, 1, nil, nil, Limits{}); !errors.Is(err, ErrTooDeep) {
		t.Fatal(err)
	}
}

func TestHTTPEventStream(t *testing.T) {
	stream := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < n; i++ {
				_, _ = fmt.Fprint(w, ": keepalive\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/p\"}\n\n")
			}
			_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\ndata: \"result\":{}}\n\n")
		}
	}
	src, _ := NewHTTPReader(get(t, stream(3)), Limits{})
	if _, err := Await(src, 1, nil, nil, Limits{}); err != nil {
		t.Fatal(err)
	}
	src, _ = NewHTTPReader(get(t, stream(257)), Limits{})
	spy := &closeSpy{}
	if _, err := Await(src, 1, nil, spy, Limits{}); !errors.Is(err, ErrTooManyNotifications) || !spy.closed {
		t.Fatalf("err=%v closed=%v", err, spy.closed)
	}
}

func TestHTTPEventOverSize(t *testing.T) {
	resp := get(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 10; i++ {
			_, _ = fmt.Fprintf(w, "data: %s\n", strings.Repeat("x", 30))
		}
		_, _ = fmt.Fprint(w, "\n")
	})
	src, _ := NewHTTPReader(resp, Limits{MaxMessageBytes: 100})
	if _, err := src.Next(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

func TestHTTPContentType(t *testing.T) {
	resp := get(t, func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "text/html") })
	if _, err := NewHTTPReader(resp, Limits{}); !errors.Is(err, ErrContentType) {
		t.Fatal(err)
	}
}

func TestStderrSink(t *testing.T) {
	s := NewStderrSink(10)
	for _, p := range []string{"abcdef", "ghijkl", "mnop"} {
		if n, err := s.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	if string(s.Bytes()) != "abcdefghij" || s.Dropped() != 6 {
		t.Fatalf("%q dropped %d", s.Bytes(), s.Dropped())
	}
}

func TestMessageAtBoundAcceptedOverFraming(t *testing.T) {
	msg := `{"jsonrpc":"2.0","id":1,"result":{}}`
	lim := Limits{MaxMessageBytes: len(msg)}.norm()
	over := msg[:len(msg)-1] + ` }`
	cases := []struct {
		name string
		src  func(string) Source
		in   func(string) string
	}{
		{"crlf", func(s string) Source { return NewLineReader(strings.NewReader(s), lim) }, func(m string) string { return m + "\r\n" }},
		{"sse", func(s string) Source { return &eventReader{br: bufio.NewReader(strings.NewReader(s)), lim: lim} }, func(m string) string { return "data: " + m + "\n\n" }},
	}
	for _, c := range cases {
		got, err := c.src(c.in(msg)).Next()
		if err != nil || string(got) != msg {
			t.Errorf("%s at bound: %q %v", c.name, got, err)
		}
		if _, err := c.src(c.in(over)).Next(); !errors.Is(err, ErrMessageTooLarge) {
			t.Errorf("%s over bound: %v", c.name, err)
		}
	}
}

func TestSSEMultiLineDataBound(t *testing.T) {
	lim := Limits{MaxMessageBytes: 7}.norm()
	r := &eventReader{br: bufio.NewReader(strings.NewReader("data: abc\ndata: def\n\n")), lim: lim}
	if got, err := r.Next(); err != nil || string(got) != "abc\ndef" {
		t.Fatalf("at bound: %q %v", got, err)
	}
	r = &eventReader{br: bufio.NewReader(strings.NewReader("data: abc\ndata: defg\n\n")), lim: lim}
	if _, err := r.Next(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("over bound: %v", err)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestSSEMultiLineBoundStopsReadingEarly(t *testing.T) {
	const bound = 1000
	in := "data: " + strings.Repeat("a", 990) + "\ndata: " + strings.Repeat("b", 3000) + "\n\n"
	cr := &countingReader{r: strings.NewReader(in)}
	r := &eventReader{br: bufio.NewReaderSize(cr, 16), lim: Limits{MaxMessageBytes: bound}.norm()}
	if _, err := r.Next(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// first line (996+1) plus what the second may add: room, framing, one buffer.
	if limit := 997 + 10 + eventSlack + 6 + 16; cr.n > limit {
		t.Fatalf("consumed %d bytes, want at most %d", cr.n, limit)
	}
}
