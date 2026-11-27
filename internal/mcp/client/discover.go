package client

// Revision detection over the real clients (v9 delta, section 3): these send
// the one server/discover and hand the answer to package mcp to decide.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

// discoverID is the id server/discover is sent under. Session ids start at 1
// (wire.IDs), so a late discovery answer on a stdio stream is never taken as
// the answer to the legacy initialize that follows.
const discoverID int64 = 0

// DiscoverHTTP chooses the revision of the server c talks to. A response that
// carried a JSON-RPC message is judged by its shape and code whatever its
// status; one that carried none is judged by its status.
func DiscoverHTTP(ctx context.Context, protocol string, c *CurrentHTTP) mcp.Decision {
	return mcp.DetectHTTP(ctx, protocol, func(ctx context.Context) (mcp.Answer, error) {
		rep, err := c.Send(ctx, discoverID, "server/discover", json.RawMessage(`{}`))
		var se *StatusError
		if errors.As(err, &se) {
			return mcp.Answer{Status: se.Status, Err: se.Err}, nil
		}
		if err != nil {
			return mcp.Answer{}, err
		}
		return toAnswer(rep.Response, rep.Status), nil
	})
}

func toAnswer(r wire.Response, status int) mcp.Answer {
	a := mcp.Answer{Status: status, Result: r.Result}
	if r.Error != nil {
		a.Error = &mcp.AnswerError{Code: r.Error.Code, Message: r.Error.Message, Data: r.Error.Data}
	}
	return a
}

// errDiscoverTimeout ends a discovery read that got no answer in time.
var errDiscoverTimeout = errors.New("mcp: no answer to server/discover")

// handoffReader lets a read outlive the wait for it. A Next that passes the
// deadline leaves the read running, and the next Next takes its message, so a
// late message is never lost to an abandoned reader.
type handoffReader struct {
	inner wire.MessageReader

	mu      sync.Mutex
	pending chan handoffResult
	// deadline bounds every Next call until it is cleared; zero waits
	// without bound.
	deadline time.Time
}

type handoffResult struct {
	b   []byte
	err error
}

func (r *handoffReader) setDeadline(t time.Time) {
	r.mu.Lock()
	r.deadline = t
	r.mu.Unlock()
}

func (r *handoffReader) Next() ([]byte, error) {
	r.mu.Lock()
	if r.pending == nil {
		ch := make(chan handoffResult, 1)
		r.pending = ch
		go func() {
			b, err := r.inner.Next()
			ch <- handoffResult{b, err}
		}()
	}
	ch, deadline := r.pending, r.deadline
	r.mu.Unlock()
	take := func(res handoffResult) ([]byte, error) {
		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
		return res.b, res.err
	}
	var expire <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		expire = t.C
	}
	select {
	case res := <-ch:
		return take(res)
	case <-expire:
		// A message that is already in is taken, not left for the timer.
		select {
		case res := <-ch:
			return take(res)
		default:
		}
		return nil, errDiscoverTimeout
	}
}

// DiscoverStdio chooses the revision of a stdio server. It writes one
// server/discover to w and reads s, waiting at most timeout for the whole
// exchange, notifications included (zero waits without bound). It replaces
// s.Messages with a reader that keeps a read the wait gave up on, so s is
// safe to use for the legacy initialize that follows an OutcomeLegacy; the
// late answer, if any, is discarded there as a response to another id
// (discoverID). A failure to write server/discover is wrapped in
// mcp.SendError and leaves the server unoffered.
func DiscoverStdio(protocol string, timeout time.Duration, w io.Writer, s *wire.Stream, info ClientInfo) mcp.Decision {
	if protocol != mcp.ProtocolAuto {
		return mcp.DetectStdio(protocol, 0, nil)
	}
	hr, isHandoff := s.Messages.(*handoffReader)
	if !isHandoff {
		hr = &handoffReader{inner: s.Messages}
		s.Messages = hr
	}
	if timeout > 0 {
		hr.setDeadline(time.Now().Add(timeout))
		defer hr.setDeadline(time.Time{})
	}
	return mcp.DetectStdio(protocol, 0, func() (mcp.Answer, error) {
		req, err := CurrentRequest(discoverID, "server/discover", json.RawMessage(`{}`), info)
		if err != nil {
			return mcp.Answer{}, &mcp.SendError{Err: err}
		}
		if err := wire.WriteLine(w, req); err != nil {
			return mcp.Answer{}, &mcp.SendError{Err: err}
		}
		r, err := s.Await(discoverID)
		if err != nil {
			return mcp.Answer{}, err
		}
		return toAnswer(r, 0), nil
	})
}
