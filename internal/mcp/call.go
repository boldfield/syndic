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

// Call sends the call once and classifies the reply. When ctx ends before
// the reply, it cancels as the revision and transport define, stops reading
// whatever the server then does, and returns CallCancelled.
func Call(ctx context.Context, opts CallOptions) CallOutcome {
	if opts.Send == nil {
		return CallOutcome{Kind: CallTransport, Err: errors.New("mcp: no send for the call")}
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	type sent struct {
		rep CallReply
		err error
	}
	ch := make(chan sent, 1)
	go func() {
		rep, err := opts.Send(sctx)
		ch <- sent{rep, err}
	}()

	var s sent
	select {
	case s = <-ch:
		if ctx.Err() != nil && s.err != nil {
			return cancelled(ctx, opts, stop)
		}
	case <-ctx.Done():
		return cancelled(ctx, opts, stop)
	}
	out := classifyCall(s.rep, s.err, opts.Classify)
	out.Sends = 1
	if out.Status == statusUnauthorized && opts.Refresh != nil {
		out.Refreshed = true
		out.RefreshErr = opts.Refresh()
	}
	return out
}

func cancelled(ctx context.Context, opts CallOptions, stop context.CancelFunc) CallOutcome {
	out := CallOutcome{Kind: CallCancelled, Sends: 1, Err: ctx.Err()}
	if opts.Cancel != nil {
		if err := opts.Cancel(); err != nil {
			out.Err = fmt.Errorf("mcp: cancelling the call: %w", err)
		}
	}
	// Closing the send's context stops its read; a reply that follows is
	// never looked at.
	stop()
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
