// Package wire is the lowest layer of the MCP client: JSON-RPC 2.0 messages
// over a line stream (stdio) and over HTTP bodies and event streams, with the
// bounds the v9 delta sets in section 3 ("Bounds").
//
// Every bound is enforced while reading: a reader never holds more than the
// message bound of one message, and nesting is tracked byte by byte. Passing a
// bound closes the connection and returns a distinct error. This layer never
// re-sends anything.
package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
)

// Bounds from the design.
const (
	MaxMessageBytes  = 4_194_304 // mcp.max_message_bytes
	MaxDepth         = 64
	MaxNotifications = 256
	MaxStderrBytes   = 1_048_576 // mcp.max_stderr_bytes
)

// JSON-RPC error codes this layer sends.
const (
	CodeMethodNotFound = -32601
)

// Errors for a bound passed; each closes the connection.
var (
	ErrMessageTooLarge      = errors.New("wire: message over the size bound")
	ErrTooDeep              = errors.New("wire: message nested over the depth bound")
	ErrTooManyNotifications = errors.New("wire: notification bound passed")
)

// Other errors.
var (
	ErrContentType = errors.New("wire: response is neither JSON nor an event stream")
	ErrMalformed   = errors.New("wire: malformed JSON-RPC message")
)

// IsBound reports whether err is one of the three bound errors.
func IsBound(err error) bool {
	return errors.Is(err, ErrMessageTooLarge) || errors.Is(err, ErrTooDeep) ||
		errors.Is(err, ErrTooManyNotifications)
}

// Limits are the bounds a reader applies. The zero value means the design's.
type Limits struct {
	MaxMessageBytes  int
	MaxDepth         int
	MaxNotifications int
}

func (l Limits) norm() Limits {
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = MaxMessageBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = MaxDepth
	}
	if l.MaxNotifications <= 0 {
		l.MaxNotifications = MaxNotifications
	}
	return l
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message) }

// Request is a client request; ids are chosen by the client.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// NewRequest returns a request with the version member set.
func NewRequest(id int64, method string, params any) Request {
	return Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
}

// Notification is a message with no id, in either direction.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// NewNotification returns a notification with the version member set.
func NewNotification(method string, params any) Notification {
	return Notification{JSONRPC: "2.0", Method: method, Params: params}
}

// Response is a JSON-RPC response. ID is raw so a server's own id is echoed
// exactly when this client answers a request from the server.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Message is any inbound message, decoded loosely.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// ParseMessage decodes one message and classifies it.
func ParseMessage(b []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if m.JSONRPC != "2.0" {
		return m, fmt.Errorf("%w: jsonrpc is not 2.0", ErrMalformed)
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "":
	case hasID && (m.Result != nil || m.Error != nil):
	default:
		return m, fmt.Errorf("%w: neither request, notification nor response", ErrMalformed)
	}
	return m, nil
}

// IsRequest: a request the server sends to the client.
func (m Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" }

// IsNotification: a message with a method and no id.
func (m Message) IsNotification() bool { return m.Method != "" && !m.IsRequest() }

// IsResponse: a result or error with an id.
func (m Message) IsResponse() bool { return m.Method == "" }

// depthTracker follows JSON nesting across chunks, ignoring brackets in strings.
type depthTracker struct {
	max      int
	depth    int
	inString bool
	escape   bool
}

func (d *depthTracker) feed(p []byte) error {
	for _, c := range p {
		if d.inString {
			switch {
			case d.escape:
				d.escape = false
			case c == '\\':
				d.escape = true
			case c == '"':
				d.inString = false
			}
			continue
		}
		switch c {
		case '"':
			d.inString = true
		case '{', '[':
			d.depth++
			if d.depth > d.max {
				return ErrTooDeep
			}
		case '}', ']':
			d.depth--
		}
	}
	return nil
}

// Source yields inbound messages one at a time; io.EOF at the end.
type Source interface {
	Next() ([]byte, error)
}

// readLine reads one line without its terminator, holding at most max bytes
// (the raw line may be up to slack longer, to leave room for framing the
// caller strips), feeding each kept chunk to dt. A final unterminated line is
// returned.
func readLine(br *bufio.Reader, max, slack int, dt *depthTracker) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		terminated := err == nil
		if terminated {
			chunk = chunk[:len(chunk)-1]
		}
		if len(line)+len(chunk) > max+slack {
			return nil, ErrMessageTooLarge
		}
		if dt != nil {
			if derr := dt.feed(chunk); derr != nil {
				return nil, derr
			}
		}
		line = append(line, chunk...)
		switch {
		case terminated:
			return trimCR(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return trimCR(line), nil
		default:
			return nil, err
		}
	}
}

func trimCR(line []byte) []byte { return bytes.TrimSuffix(line, []byte("\r")) }

// LineReader reads newline-delimited messages (stdio).
type LineReader struct {
	br  *bufio.Reader
	lim Limits
}

// NewLineReader reads messages from r under lim.
func NewLineReader(r io.Reader, lim Limits) *LineReader {
	return &LineReader{br: bufio.NewReaderSize(r, 4096), lim: lim.norm()}
}

// Next returns the next non-empty line as one message.
func (l *LineReader) Next() ([]byte, error) {
	for {
		dt := &depthTracker{max: l.lim.MaxDepth}
		line, err := readLine(l.br, l.lim.MaxMessageBytes, 1, dt)
		if err != nil {
			return nil, err
		}
		if len(line) > l.lim.MaxMessageBytes {
			return nil, ErrMessageTooLarge
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		return line, nil
	}
}

// WriteLine writes v as one line. A message over the size bound is refused
// before anything is written.
func WriteLine(w io.Writer, v any, lim Limits) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > lim.norm().MaxMessageBytes {
		return ErrMessageTooLarge
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// eventReader reads server-sent events; each event's data is one message.
type eventReader struct {
	br  *bufio.Reader
	lim Limits
}

func (e *eventReader) Next() ([]byte, error) {
	for {
		dt := &depthTracker{max: e.lim.MaxDepth}
		var data []byte
		have := false
		for {
			line, err := readLine(e.br, e.lim.MaxMessageBytes, len("data: ")+1, nil)
			if err != nil {
				if errors.Is(err, io.EOF) && have {
					return data, nil
				}
				return nil, err
			}
			if len(line) == 0 {
				if have {
					return data, nil
				}
				continue
			}
			field, value, _ := bytes.Cut(line, []byte(":"))
			if string(field) != "data" {
				continue
			}
			value = bytes.TrimPrefix(value, []byte(" "))
			add := len(value)
			if have {
				add++
			}
			if len(data)+add > e.lim.MaxMessageBytes {
				return nil, ErrMessageTooLarge
			}
			if have {
				data = append(data, '\n')
			}
			if err := dt.feed(value); err != nil {
				return nil, err
			}
			data = append(data, value...)
			have = true
		}
	}
}

// bodyReader reads a body that is one JSON message.
type bodyReader struct {
	r    io.Reader
	lim  Limits
	done bool
}

func (b *bodyReader) Next() ([]byte, error) {
	if b.done {
		return nil, io.EOF
	}
	b.done = true
	dt := &depthTracker{max: b.lim.MaxDepth}
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := b.r.Read(buf)
		if len(out)+n > b.lim.MaxMessageBytes {
			return nil, ErrMessageTooLarge
		}
		if derr := dt.feed(buf[:n]); derr != nil {
			return nil, derr
		}
		out = append(out, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

// NewHTTPReader returns a Source for an HTTP response body, chosen by its
// Content-Type: one JSON message, or an event stream of them.
func NewHTTPReader(resp *http.Response, lim Limits) (Source, error) {
	lim = lim.norm()
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		return &bodyReader{r: resp.Body, lim: lim}, nil
	case "text/event-stream":
		return &eventReader{br: bufio.NewReaderSize(resp.Body, 4096), lim: lim}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrContentType, resp.Header.Get("Content-Type"))
}

// Await reads from src until the response to request id and returns it.
// Notifications are read and discarded, at most lim.MaxNotifications of them;
// a response to another id counts as one. A request from the server is
// answered with a JSON-RPC error through reply (if non-nil) and never acted
// on; it counts toward the same bound. Passing any bound closes closer (if
// non-nil) and returns the bound's error.
func Await(src Source, id int64, reply func(Response) error, closer io.Closer, lim Limits) (Message, error) {
	lim = lim.norm()
	want := strconv.FormatInt(id, 10)
	skipped := 0
	fail := func(err error) (Message, error) {
		if IsBound(err) && closer != nil {
			_ = closer.Close()
		}
		return Message{}, err
	}
	for {
		b, err := src.Next()
		if err != nil {
			return fail(err)
		}
		m, err := ParseMessage(b)
		if err != nil {
			return fail(err)
		}
		if m.IsResponse() && string(bytes.TrimSpace(m.ID)) == want {
			return m, nil
		}
		skipped++
		if skipped > lim.MaxNotifications {
			return fail(ErrTooManyNotifications)
		}
		if m.IsRequest() && reply != nil {
			if err := reply(Response{JSONRPC: "2.0", ID: m.ID,
				Error: &Error{Code: CodeMethodNotFound, Message: "this client serves no requests"}}); err != nil {
				return fail(err)
			}
		}
	}
}

// StderrSink keeps up to a bound of a local server's standard error for one
// instance, drops the rest and counts what it dropped. It is an io.Writer
// that never fails.
type StderrSink struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	dropped int64
}

// NewStderrSink keeps at most max bytes (MaxStderrBytes when max <= 0).
func NewStderrSink(max int) *StderrSink {
	if max <= 0 {
		max = MaxStderrBytes
	}
	return &StderrSink{max: max}
}

// Write implements io.Writer.
func (s *StderrSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.max - len(s.buf)
	if room > len(p) {
		room = len(p)
	}
	s.buf = append(s.buf, p[:room]...)
	s.dropped += int64(len(p) - room)
	return len(p), nil
}

// Bytes returns a copy of what was kept.
func (s *StderrSink) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf...)
}

// Dropped returns the number of bytes dropped.
func (s *StderrSink) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}
