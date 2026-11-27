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

// DiscoverHTTP chooses the revision of the server c talks to. A response that
// carried a JSON-RPC message is judged by its shape and code whatever its
// status; one that carried none is judged by its status.
func DiscoverHTTP(ctx context.Context, protocol string, c *CurrentHTTP) mcp.Decision {
	return mcp.DetectHTTP(ctx, protocol, func(ctx context.Context) (mcp.Answer, error) {
		rep, err := c.Send(ctx, 1, "server/discover", json.RawMessage(`{}`))
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

// handoffReader lets a read outlive the wait for it. A Next that times out
// leaves the read running, and the next Next takes its message, so a late
// message is never lost to an abandoned reader.
type handoffReader struct {
	inner wire.MessageReader

	mu      sync.Mutex
	pending chan handoffResult
	// timeout bounds the next Next calls; zero waits without bound.
	timeout time.Duration
}

type handoffResult struct {
	b   []byte
	err error
}

func (r *handoffReader) setTimeout(d time.Duration) {
	r.mu.Lock()
	r.timeout = d
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
	ch, timeout := r.pending, r.timeout
	r.mu.Unlock()
	var expire <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	select {
	case res := <-ch:
		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
		return res.b, res.err
	case <-expire:
		return nil, errDiscoverTimeout
	}
}

// DiscoverStdio chooses the revision of a stdio server. It writes one
// server/discover to w and reads s, waiting at most timeout (zero waits
// without bound). It replaces s.Messages with a reader that keeps a read the
// wait gave up on, so s is safe to use for the legacy initialize that
// follows an OutcomeLegacy, and the late answer, if any, is discarded there
// as a response to another id.
func DiscoverStdio(protocol string, timeout time.Duration, w io.Writer, s *wire.Stream, info ClientInfo) mcp.Decision {
	if protocol != mcp.ProtocolAuto {
		return mcp.DetectStdio(protocol, 0, nil)
	}
	hr, isHandoff := s.Messages.(*handoffReader)
	if !isHandoff {
		hr = &handoffReader{inner: s.Messages}
		s.Messages = hr
	}
	hr.setTimeout(timeout)
	defer hr.setTimeout(0)
	return mcp.DetectStdio(protocol, 0, func() (mcp.Answer, error) {
		r, err := SendCurrentStdio(w, s, 1, "server/discover", json.RawMessage(`{}`), info)
		if err != nil {
			return mcp.Answer{}, err
		}
		return toAnswer(r, 0), nil
	})
}
