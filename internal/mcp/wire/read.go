package wire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// The bounds of design v9 section 3.
const (
	// MaxMessageBytes is mcp.max_message_bytes: one protocol message, a
	// line on stdio or a body or event over HTTP, is at most this long.
	MaxMessageBytes = 4 << 20
	// MaxDepth is the deepest JSON nesting one message may have.
	MaxDepth = 64
	// MaxNotifications is how many notifications are read, and discarded,
	// while a response is awaited.
	MaxNotifications = 256
	// MaxStderrBytes is mcp.max_stderr_bytes: how much of a local server's
	// standard error is kept for one instance.
	MaxStderrBytes = 1 << 20
)

// ErrBound is wrapped by every error for a message, depth or notification
// bound passed. Each such error closes the connection it came from.
var ErrBound = errors.New("wire: bound passed")

// The bound errors, each distinct and each wrapping ErrBound.
var (
	ErrMessageTooLarge      = fmt.Errorf("%w: message over the size bound", ErrBound)
	ErrTooDeep              = fmt.Errorf("%w: message nested over the depth bound", ErrBound)
	ErrTooManyNotifications = fmt.Errorf("%w: notifications over the bound", ErrBound)
)

// ErrContentType is returned for an HTTP response that is neither a JSON
// body nor an event stream.
var ErrContentType = errors.New("wire: response is neither JSON nor an event stream")

// Limits holds the bounds a reader applies. A zero field takes the design's
// value.
type Limits struct {
	MaxMessageBytes  int
	MaxDepth         int
	MaxNotifications int
}

// DefaultLimits returns the design's bounds.
func DefaultLimits() Limits {
	return Limits{
		MaxMessageBytes:  MaxMessageBytes,
		MaxDepth:         MaxDepth,
		MaxNotifications: MaxNotifications,
	}
}

func (l Limits) orDefault() Limits {
	d := DefaultLimits()
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxNotifications <= 0 {
		l.MaxNotifications = d.MaxNotifications
	}
	return l
}

// MessageReader yields the raw bytes of one protocol message at a time. It
// returns io.EOF when the stream ends between messages.
type MessageReader interface {
	Next() ([]byte, error)
}

// depth follows JSON nesting byte by byte as a message is read, so the depth
// bound is enforced before the message is complete. Brackets inside strings
// do not count. It does not validate JSON; decoding does that.
type depth struct {
	max   int
	n     int
	inStr bool
	esc   bool
}

func (d *depth) byte(c byte) error {
	if d.inStr {
		switch {
		case d.esc:
			d.esc = false
		case c == '\\':
			d.esc = true
		case c == '"':
			d.inStr = false
		}
		return nil
	}
	switch c {
	case '"':
		d.inStr = true
	case '{', '[':
		d.n++
		if d.n > d.max {
			return ErrTooDeep
		}
	case '}', ']':
		d.n--
	}
	return nil
}

func (d *depth) feed(p []byte) error {
	for _, c := range p {
		if err := d.byte(c); err != nil {
			return err
		}
	}
	return nil
}

// LineReader reads newline-delimited messages from a local server's standard
// output. A line ends at a line feed; one carriage return before it is not
// part of the message. Empty lines are skipped. A line is never buffered
// past the size bound, plus one byte for a carriage return.
type LineReader struct {
	br  *bufio.Reader
	lim Limits
}

// NewLineReader returns a reader of the lines of r.
func NewLineReader(r io.Reader, lim Limits) *LineReader {
	return &LineReader{br: bufio.NewReader(r), lim: lim.orDefault()}
}

// Next returns the next non-empty line. A stream that ends inside a line
// returns io.ErrUnexpectedEOF.
func (l *LineReader) Next() ([]byte, error) {
	for {
		msg, err := l.line()
		if err != nil {
			return nil, err
		}
		if len(msg) > 0 {
			return msg, nil
		}
	}
}

func (l *LineReader) line() ([]byte, error) {
	limit := l.lim.MaxMessageBytes
	d := depth{max: l.lim.MaxDepth}
	var buf []byte
	for {
		chunk, err := l.br.ReadSlice('\n')
		end := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if end {
			chunk = chunk[:len(chunk)-1]
		}
		// One byte past the bound is allowed in flight: it may be the
		// carriage return of a CRLF line ending.
		if len(buf)+len(chunk) > limit+1 {
			return nil, ErrMessageTooLarge
		}
		if derr := d.feed(chunk); derr != nil {
			return nil, derr
		}
		buf = append(buf, chunk...)
		if end {
			if n := len(buf); n > 0 && buf[n-1] == '\r' {
				buf = buf[:n-1]
			}
			if len(buf) > limit {
				return nil, ErrMessageTooLarge
			}
			return buf, nil
		}
		switch {
		case err == bufio.ErrBufferFull:
		case err == io.EOF && len(buf) == 0:
			return nil, io.EOF
		case err == io.EOF:
			return nil, io.ErrUnexpectedEOF
		case err != nil:
			return nil, err
		}
	}
}

// ReadBody reads a whole HTTP body as one message, never holding more than
// the size bound.
func ReadBody(r io.Reader, lim Limits) ([]byte, error) {
	lim = lim.orDefault()
	d := depth{max: lim.MaxDepth}
	var buf []byte
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			if len(buf)+n > lim.MaxMessageBytes {
				return nil, ErrMessageTooLarge
			}
			if derr := d.feed(chunk[:n]); derr != nil {
				return nil, derr
			}
			buf = append(buf, chunk[:n]...)
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// bodyReader yields one JSON body as one message, then io.EOF.
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
	return ReadBody(b.r, b.lim)
}

// EventReader reads the events of a text/event-stream body and yields the
// data of each as one message.
//
// Lines end at CRLF, LF or CR. The data lines of one event are joined by line
// feeds, and only that reconstructed data counts against the size bound: a
// field name, the colon and space after it, line endings, comments and every
// other field are framing and do not. Data is read straight into the message,
// byte by byte, so an event is refused the moment its data passes the bound
// and no more than the bound is ever held. A line that is not data is
// discarded as it is read; it is held to the size bound of its own. An event
// whose data is empty, such as a priming event that carries only an id, is
// skipped. A stream that ends inside an event discards that event.
type EventReader struct {
	br      *bufio.Reader
	lim     Limits
	started bool
	// afterCR is set when a line ended at a carriage return, so that a line
	// feed right after it is taken as the rest of a CRLF.
	afterCR bool
}

// NewEventReader returns a reader of the events of r.
func NewEventReader(r io.Reader, lim Limits) *EventReader {
	return &EventReader{br: bufio.NewReader(r), lim: lim.orDefault()}
}

func (e *EventReader) readByte() (byte, error) {
	for {
		c, err := e.br.ReadByte()
		if err != nil {
			return 0, err
		}
		if e.afterCR {
			e.afterCR = false
			if c == '\n' {
				continue
			}
		}
		return c, nil
	}
}

// eol reports whether c ends a line, and notes a carriage return.
func (e *EventReader) eol(c byte) bool {
	switch c {
	case '\n':
		return true
	case '\r':
		e.afterCR = true
		return true
	}
	return false
}

// skipBOM drops one byte order mark at the start of the stream.
func (e *EventReader) skipBOM() {
	if e.started {
		return
	}
	e.started = true
	if b, err := e.br.Peek(1); err != nil || b[0] != 0xEF {
		return
	}
	if b, err := e.br.Peek(3); err == nil && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = e.br.Discard(3)
	}
}

// Next returns the data of the next event that has any.
func (e *EventReader) Next() ([]byte, error) {
	e.skipBOM()
	limit := e.lim.MaxMessageBytes
	var data []byte
	hasData := false
	d := depth{max: e.lim.MaxDepth}

	put := func(c byte) error {
		if len(data) >= limit {
			return ErrMessageTooLarge
		}
		if err := d.byte(c); err != nil {
			return err
		}
		data = append(data, c)
		return nil
	}

	for {
		c, err := e.readByte()
		if err != nil {
			return nil, err
		}

		if e.eol(c) {
			// A blank line ends the event.
			if len(data) > 0 {
				return data, nil
			}
			data, hasData, d = data[:0], false, depth{max: e.lim.MaxDepth}
			continue
		}

		// The field name runs to the first colon or the end of the line.
		// Only enough of it is kept to tell "data" from anything else.
		var name [5]byte
		nameLen, lineLen, ended := 0, 0, false
		for c != ':' {
			if nameLen < len(name) {
				name[nameLen] = c
			}
			nameLen++
			lineLen++
			if lineLen > limit {
				return nil, ErrMessageTooLarge
			}
			if c, err = e.readByte(); err != nil {
				return nil, err
			}
			if e.eol(c) {
				ended = true
				break
			}
		}
		isData := nameLen == 4 && string(name[:4]) == "data"

		if isData {
			// Each data line after the first is joined by a line feed,
			// which is part of the message.
			if hasData {
				if err := put('\n'); err != nil {
					return nil, err
				}
			}
			hasData = true
		}
		if ended {
			continue
		}

		// After the colon: one optional space, then the value.
		first := true
		for {
			if c, err = e.readByte(); err != nil {
				return nil, err
			}
			if e.eol(c) {
				break
			}
			if first {
				first = false
				if c == ' ' {
					continue
				}
			}
			if isData {
				if err := put(c); err != nil {
					return nil, err
				}
				continue
			}
			lineLen++
			if lineLen > limit {
				return nil, ErrMessageTooLarge
			}
		}
	}
}

// NewResponseReader returns the reader for an HTTP response's body by its
// media type: a JSON body is one message, an event stream yields one message
// per event.
func NewResponseReader(resp *http.Response, lim Limits) (MessageReader, error) {
	ct := resp.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrContentType, ct)
	}
	switch mt {
	case "application/json":
		return &bodyReader{r: resp.Body, lim: lim.orDefault()}, nil
	case "text/event-stream":
		return NewEventReader(resp.Body, lim), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrContentType, ct)
}
