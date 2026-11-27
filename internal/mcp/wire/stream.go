package wire

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// Stream is one connection's flow of messages from a server: a stdio
// server's standard output, or one HTTP response.
type Stream struct {
	// Messages yields the server's messages.
	Messages MessageReader
	// Reply sends one encoded message back to the server, without a line
	// ending. It is used only to refuse a request the server sends. Nil
	// means nothing can be sent back, and such a request is only discarded.
	Reply func(msg []byte) error
	// Closer closes the connection. It is called when a bound is passed.
	Closer io.Closer
	// Limits bounds what is read; a zero field takes the design's value.
	Limits Limits

	closeOnce sync.Once
}

// RefusalMessage is the message of the error a request from the server is
// answered with.
const RefusalMessage = "the client accepts no requests"

func (s *Stream) close() {
	s.closeOnce.Do(func() {
		if s.Closer != nil {
			_ = s.Closer.Close()
		}
	})
}

// Next returns the next message. Passing a size or depth bound closes the
// connection and returns that bound's error.
func (s *Stream) Next() ([]byte, error) {
	return s.checked(s.Messages.Next())
}

// checked closes the connection when a read passed a bound.
func (s *Stream) checked(b []byte, err error) ([]byte, error) {
	if errors.Is(err, ErrBound) {
		s.close()
	}
	return b, err
}

// Await reads until the response to the request with the given id and
// returns it; a JSON-RPC error response is returned as it is, for the caller
// to judge.
//
// Everything before the response is bounded. A notification is discarded. A
// request from the server is answered with a JSON-RPC error and never acted
// on. A response to another id, left over from an earlier request, is
// discarded. Each of these counts against the notification bound, so a
// server cannot hold a call open with any of them; the 257th closes the
// connection and returns ErrTooManyNotifications. A response whose id is
// null, which a server sends when it cannot read the request's id, is taken
// as the response.
//
// A message that does not decode returns an error wrapping ErrMalformed;
// the connection is left to the caller.
func (s *Stream) Await(id int64) (Response, error) {
	return s.await(id, s.Next)
}

func (s *Stream) await(id int64, next func() ([]byte, error)) (Response, error) {
	lim := s.Limits.orDefault()
	discarded := 0
	for {
		b, err := next()
		if err != nil {
			return Response{}, err
		}
		m, err := Decode(b)
		if err != nil {
			return Response{}, err
		}
		if m.Kind == KindResponse && (idIs(m.ID, id) || string(m.ID) == "null") {
			return m.Response(), nil
		}
		discarded++
		if discarded > lim.MaxNotifications {
			s.close()
			return Response{}, ErrTooManyNotifications
		}
		if m.Kind == KindRequest && s.Reply != nil {
			if err := s.refuse(m); err != nil {
				return Response{}, err
			}
		}
	}
}

// refuse answers a request from the server with an error.
func (s *Stream) refuse(m Message) error {
	b, err := Encode(Response{
		JSONRPC: Version,
		ID:      m.ID,
		Error:   &Error{Code: CodeMethodNotFound, Message: RefusalMessage},
	})
	if err != nil {
		return err
	}
	if err := s.Reply(b); err != nil {
		return fmt.Errorf("wire: answering a server request: %w", err)
	}
	return nil
}
