// Package wire is the lowest layer of the MCP client: JSON-RPC 2.0 messages
// over a line stream and over HTTP bodies and event streams, with the bounds
// of design v9 section 3.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
)

// Version is the JSON-RPC version every message carries.
const Version = "2.0"

// JSON-RPC error codes this layer uses.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ErrMalformed is wrapped by every error for a message that is not a
// well-formed JSON-RPC 2.0 message.
var ErrMalformed = errors.New("wire: malformed message")

// Request is a request the client sends. Its id is chosen by the client.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// NewRequest returns a request with the given id, method and params, which
// are omitted when nil.
func NewRequest(id int64, method string, params json.RawMessage) Request {
	return Request{JSONRPC: Version, ID: id, Method: method, Params: params}
}

// Notification is a message that expects no response.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// NewNotification returns a notification with the given method and params,
// which are omitted when nil.
func NewNotification(method string, params json.RawMessage) Notification {
	return Notification{JSONRPC: Version, Method: method, Params: params}
}

// Response is the answer to a request. Exactly one of Result and Error is
// set. ID is kept as the JSON the peer sent, so that an answer to a server's
// request echoes the server's id exactly.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return "wire: JSON-RPC error " + strconv.Itoa(e.Code) + ": " + e.Message
}

// IDs hands out request ids. The zero value is ready to use and starts at 1.
// It is safe for concurrent use.
type IDs struct {
	last atomic.Int64
}

// Next returns an id not returned before by this source.
func (s *IDs) Next() int64 {
	return s.last.Add(1)
}

// Kind tells what a received message is.
type Kind int

const (
	// KindRequest is a request the peer sends, which expects a response.
	KindRequest Kind = iota + 1
	// KindNotification is a message with a method and no id.
	KindNotification
	// KindResponse is a result or an error answering a request.
	KindResponse
)

func (k Kind) String() string {
	switch k {
	case KindRequest:
		return "request"
	case KindNotification:
		return "notification"
	case KindResponse:
		return "response"
	}
	return "unknown"
}

// Message is a received message of any kind, its parts kept as raw JSON.
type Message struct {
	Kind   Kind
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *Error
}

// Response returns the message as a Response. It is meaningful only for
// KindResponse.
func (m Message) Response() Response {
	return Response{JSONRPC: Version, ID: m.ID, Result: m.Result, Error: m.Error}
}

// Decode classifies one received message. A message that is not a single
// well-formed JSON-RPC 2.0 request, notification or response returns an
// error wrapping ErrMalformed. Batches are not part of MCP and are refused.
func Decode(b []byte) (Message, error) {
	var raw struct {
		JSONRPC *string         `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  *string         `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Message{}, fmt.Errorf("%w: not a JSON object", ErrMalformed)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&raw); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Message{}, fmt.Errorf("%w: data after the message", ErrMalformed)
	}
	if raw.JSONRPC == nil || *raw.JSONRPC != Version {
		return Message{}, fmt.Errorf("%w: jsonrpc is not %q", ErrMalformed, Version)
	}
	if raw.ID != nil && !validID(raw.ID) {
		return Message{}, fmt.Errorf("%w: id is not a string, number or null", ErrMalformed)
	}
	if raw.Params != nil && raw.Params[0] != '{' && raw.Params[0] != '[' {
		return Message{}, fmt.Errorf("%w: params is not structured", ErrMalformed)
	}

	if raw.Method != nil {
		if raw.Result != nil || raw.Error != nil {
			return Message{}, fmt.Errorf("%w: a method with a result or an error", ErrMalformed)
		}
		m := Message{Method: *raw.Method, Params: raw.Params, ID: raw.ID}
		if raw.ID == nil {
			m.Kind = KindNotification
		} else {
			m.Kind = KindRequest
		}
		return m, nil
	}

	if raw.ID == nil {
		return Message{}, fmt.Errorf("%w: neither a method nor an id", ErrMalformed)
	}
	if (raw.Result == nil) == (raw.Error == nil) {
		return Message{}, fmt.Errorf("%w: a response needs exactly one of result and error", ErrMalformed)
	}
	m := Message{Kind: KindResponse, ID: raw.ID, Result: raw.Result}
	if raw.Error != nil {
		var e Error
		if err := json.Unmarshal(raw.Error, &e); err != nil || raw.Error[0] != '{' {
			return Message{}, fmt.Errorf("%w: error is not an error object", ErrMalformed)
		}
		m.Error = &e
	}
	return m, nil
}

func validID(id json.RawMessage) bool {
	switch id[0] {
	case '"', 'n':
		return true
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

// idIs reports whether a received id is the client's id n.
func idIs(id json.RawMessage, n int64) bool {
	return string(id) == strconv.FormatInt(n, 10)
}

// Encode returns the JSON of one message to send. The result holds no line
// feed, so it is fit for a stdio line as it stands.
func Encode(msg any) ([]byte, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if bytes.ContainsAny(b, "\r\n") {
		// json.Marshal escapes line breaks in strings and compacts raw
		// messages, so this is unreachable; the check keeps the line format
		// safe should that ever change.
		return nil, errors.New("wire: encoded message holds a line break")
	}
	return b, nil
}

// WriteLine writes one message to a stdio stream as one line, in one Write.
func WriteLine(w io.Writer, msg any) error {
	b, err := Encode(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
