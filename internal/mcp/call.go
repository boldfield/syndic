package mcp

// This file sends one tools/call (v9 delta, section 3, "One send" and
// "Both"). The send is a function, so the call is the same under either
// revision and over either transport; this package carries no network code.
// The call is sent once: nothing here sends it again, not after a failure,
// not after the token refresh hook ran, not on another revision. It supplies
// no input to a server that asks for some and polls no task.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// statusUnauthorized is the HTTP status that asks for a fresh token.
const statusUnauthorized = 401

// CallKind classifies what became of one call.
type CallKind int

const (
	// CallResult is a complete result the server did not mark as an error.
	CallResult CallKind = iota + 1
	// CallServerError is a complete result the server marked isError.
	CallServerError
	// CallInputRequired is a reply asking the client for input.
	CallInputRequired
	// CallTask is a reply deferring its result to a task.
	CallTask
	// CallProtocolError is a JSON-RPC error response.
	CallProtocolError
	// CallMalformed is a reply that is malformed, or passed a bound
	// (CallOutcome.Bound).
	CallMalformed
	// CallTransport is a failure to send or to read a reply.
	CallTransport
	// CallCancelled is a call the caller cancelled before it was answered.
	CallCancelled
)

func (k CallKind) String() string {
	switch k {
	case CallResult:
		return "result"
	case CallServerError:
		return "server_error"
	case CallInputRequired:
		return "input_required"
	case CallTask:
		return "task"
	case CallProtocolError:
		return "protocol_error"
	case CallMalformed:
		return "malformed"
	case CallTransport:
		return "transport"
	case CallCancelled:
		return "cancelled"
	}
	return "unknown"
}

// RPCError is a JSON-RPC error object as the server sent it.
type RPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp: JSON-RPC error %d: %s", e.Code, e.Message)
}

// CallReply is what a send read: the response to the call, which holds a
// Result or an Error. Status is the HTTP status, zero over stdio. This
// package carries no network code, so the wire layer's types are the
// caller's to convert.
type CallReply struct {
	Status int
	Result json.RawMessage
	Error  *RPCError
}

// SendFunc sends the call once and reads until its response. It must not
// retry. Cancelling ctx must stop the read; over HTTP under the current
// revision that closes the connection, which is how that revision cancels.
// When it fails after a reply arrived it may return that reply's Status with
// the error, so that the status is judged.
type SendFunc func(ctx context.Context) (CallReply, error)

// CallOptions is what a call needs besides its send.
type CallOptions struct {
	// Send sends the call. It is invoked exactly once.
	Send SendFunc
	// Cancel cancels the call by the means the revision and transport define
	// when they are not closing the connection: notifications/cancelled. It
	// is invoked at most once, only when ctx ends before the reply, and may
	// be nil where closing the connection is the whole of it.
	Cancel func() error
	// Refresh is the token refresh hook. It runs when the server answers
	// 401, so that the next call has a fresh token; it never causes a second
	// send of this one. It may be nil.
	Refresh func() error
	// Classify tells a failure of Send that is a malformed reply or one that
	// passed a message, depth or notification bound (bound implies
	// malformed) from a failure of the transport. Nil treats every failure as
	// the transport's.
	Classify func(err error) (malformed, bound bool)
}

// CallOutcome is the classified reply to one call.
type CallOutcome struct {
	Kind CallKind
	// Rendered is set for CallResult and CallServerError.
	Rendered *Rendered
	// Error is the JSON-RPC error of a CallProtocolError.
	Error *RPCError
	// Err is why the reply was malformed or could not be had, and the error
	// of a failed cancel.
	Err error
	// Bound is set for a CallMalformed that passed a message, depth or
	// notification bound; the connection was closed.
	Bound bool
	// Status is the HTTP status when one was read.
	Status int
	// Refreshed is set when the refresh hook ran; RefreshErr is its error.
	Refreshed  bool
	RefreshErr error
	// Sends is how many times the call was sent: always one.
	Sends int
}

type sent struct {
	rep CallReply
	err error
}

// Call sends the call once and classifies the reply. When ctx ends before
// the reply, it cancels as the revision and transport define, stops reading
// whatever the server then does, and returns CallCancelled.
func Call(ctx context.Context, opts CallOptions) CallOutcome {
	if opts.Send == nil {
		return CallOutcome{Kind: CallTransport, Err: errors.New("mcp: no send for the call")}
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	ch := make(chan sent, 1)
	go func() {
		rep, err := opts.Send(sctx)
		ch <- sent{rep, err}
	}()

	var s sent
	select {
	case s = <-ch:
		if ctx.Err() != nil && s.err != nil {
			return cancelled(ctx, opts, stop, nil)
		}
	case <-ctx.Done():
		return cancelled(ctx, opts, stop, ch)
	}
	out := classifyCall(s.rep, s.err, opts.Classify)
	out.Sends = 1
	if out.Status == statusUnauthorized && opts.Refresh != nil {
		out.Refreshed = true
		out.RefreshErr = opts.Refresh()
	}
	return out
}

func cancelled(ctx context.Context, opts CallOptions, stop context.CancelFunc, done <-chan sent) CallOutcome {
	out := CallOutcome{Kind: CallCancelled, Sends: 1, Err: ctx.Err()}
	if opts.Cancel != nil {
		if err := opts.Cancel(); err != nil {
			out.Err = fmt.Errorf("mcp: cancelling the call: %w", err)
		}
	}
	// Closing the send's context stops its read; a reply that follows is
	// never looked at. The send has ended before this returns, so no reader
	// is left to take the next call's reply.
	stop()
	if done != nil {
		<-done
	}
	return out
}

func classifyCall(rep CallReply, err error, classify func(error) (bool, bool)) CallOutcome {
	out := CallOutcome{Status: rep.Status}
	if err != nil {
		out.Err = err
		out.Kind = CallTransport
		var malformed bool
		if classify != nil {
			malformed, out.Bound = classify(err)
		}
		// A failing status with nothing to read is the status's failure,
		// unless a bound closed the connection.
		failing := rep.Status != 0 && (rep.Status < 200 || rep.Status > 299)
		if out.Bound || (malformed && !failing) {
			out.Kind = CallMalformed
		}
		return out
	}
	if rep.Error != nil {
		out.Kind, out.Error = CallProtocolError, rep.Error
		return out
	}
	var head struct {
		ResultType json.RawMessage `json:"resultType"`
	}
	if json.Unmarshal(rep.Result, &head) != nil {
		out.Kind = CallMalformed
		out.Err = fmt.Errorf("%w: result is not an object", ErrMalformedReply)
		return out
	}
	if len(head.ResultType) > 0 {
		var rt string
		if json.Unmarshal(head.ResultType, &rt) != nil {
			out.Kind = CallMalformed
			out.Err = fmt.Errorf("%w: resultType is not a string", ErrMalformedReply)
			return out
		}
		switch rt {
		case "complete":
		case "input_required":
			out.Kind = CallInputRequired
			return out
		case "task":
			out.Kind = CallTask
			return out
		default:
			out.Kind = CallMalformed
			out.Err = fmt.Errorf("%w: unknown resultType %q", ErrMalformedReply, rt)
			return out
		}
	}
	r, err := Render(rep.Result)
	if err != nil {
		out.Kind, out.Err = CallMalformed, err
		return out
	}
	out.Rendered = &r
	out.Kind = CallResult
	if r.IsError {
		out.Kind = CallServerError
	}
	return out
}

// ErrReadCancelled is what a StdioReader returns when the context it is bound
// to has ended.
var ErrReadCancelled = errors.New("mcp: read cancelled")

// StdioReader makes a stdio server's output readable under a context. A
// blocked read of a pipe cannot be interrupted, so one goroutine owns the
// underlying reader for the life of the connection and hands messages over;
// Next gives up when the bound context ends. A message read after that is
// held for the next Next, which the stream discards if it answers an earlier
// id. Use it as wire.Stream.Messages, and bind the send's context with Bind.
type StdioReader struct {
	msgs chan stdioMsg
	mu   sync.Mutex
	ctx  context.Context
}

type stdioMsg struct {
	b   []byte
	err error
}

// NewStdioReader starts the reader's goroutine, which ends with the first
// error of next (the end of the output included).
func NewStdioReader(next func() ([]byte, error)) *StdioReader {
	r := &StdioReader{msgs: make(chan stdioMsg), ctx: context.Background()}
	go func() {
		defer close(r.msgs)
		for {
			b, err := next()
			r.msgs <- stdioMsg{b, err}
			if err != nil {
				return
			}
		}
	}()
	return r
}

// Bind makes Next stop when ctx ends. It is for the call's send to call
// before it reads.
func (r *StdioReader) Bind(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

// Next returns the next message, or ErrReadCancelled once the bound context
// has ended.
func (r *StdioReader) Next() ([]byte, error) {
	r.mu.Lock()
	ctx := r.ctx
	r.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ErrReadCancelled
	}
	select {
	case m, ok := <-r.msgs:
		if !ok {
			return nil, errors.New("mcp: the server's output has ended")
		}
		return m.b, m.err
	case <-ctx.Done():
		return nil, ErrReadCancelled
	}
}
