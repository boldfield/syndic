package wire

import (
	"context"
	"io"
	"sync"
)

// Handoff is a MessageReader whose waits can end before its reads do, as a
// cancelled call needs over stdio, where one stream serves every request for
// the life of the server process. One read of the underlying reader is in
// flight at a time. A message it yields after its waiter gave up is held for
// the next waiter, so a late message is never lost to an abandoned read and
// no two reads of the underlying reader ever run at once.
type Handoff struct {
	inner MessageReader

	mu      sync.Mutex
	reading bool
	// done is closed when the read in flight ends.
	done chan struct{}
	held *handoffMessage
}

type handoffMessage struct {
	b   []byte
	err error
}

// NewHandoff returns a Handoff reading r. Nothing else may read r afterwards.
func NewHandoff(r MessageReader) *Handoff {
	return &Handoff{inner: r}
}

// Next waits without bound for the next message.
func (h *Handoff) Next() ([]byte, error) {
	return h.NextContext(context.Background())
}

// NextContext waits for the next message until ctx ends, and then returns
// ctx's error. A waiter whose ctx has ended takes nothing: the message, if one
// arrives, is held for the next waiter.
func (h *Handoff) NextContext(ctx context.Context) ([]byte, error) {
	for {
		h.mu.Lock()
		if err := ctx.Err(); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		if m := h.held; m != nil {
			h.held = nil
			h.mu.Unlock()
			return m.b, m.err
		}
		if !h.reading {
			h.reading = true
			h.done = make(chan struct{})
			go h.read(h.done)
		}
		done := h.done
		h.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}

func (h *Handoff) read(done chan struct{}) {
	b, err := h.inner.Next()
	h.mu.Lock()
	h.held = &handoffMessage{b: b, err: err}
	h.reading = false
	close(done)
	h.mu.Unlock()
}

// AwaitContext is Await that stops reading when ctx ends and returns ctx's
// error. It reads through a Handoff, putting one in place of s.Messages
// first if s.Messages is not one, so a message that arrives after ctx ended
// is kept for the next Await on s. Await and AwaitContext on one stream must
// not run at once.
func (s *Stream) AwaitContext(ctx context.Context, id int64) (Response, error) {
	h, ok := s.Messages.(*Handoff)
	if !ok {
		h = NewHandoff(s.Messages)
		s.Messages = h
	}
	return s.await(id, func() ([]byte, error) {
		return s.checked(h.NextContext(ctx))
	})
}

// WriteLineContext is WriteLine that returns ctx's error when ctx ends before
// the write does. The abandoned write goes on in the background, so w must
// keep concurrent writes whole and apart, as *os.File and io.Pipe do; a later
// write then waits behind it rather than interleaving with it.
func WriteLineContext(ctx context.Context, w io.Writer, msg any) error {
	b, err := Encode(msg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(append(b, '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
