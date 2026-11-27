package mcp

// This file sends one tools/call (v9 delta, section 3, "One send" and
// "Both"). The exchange is a pair of functions, so the call is the same under
// either revision and over either transport; package client builds the
// exchange for each. The call is sent once: nothing here sends it again, and
// an exchange must not either. A cancelled call is cancelled by the means the
// revision and transport define and, whatever the server then does, is read
// no further and has ended.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// DefaultCancelTimeout bounds the cancel of a call when CallOptions does not
// set a bound.
const DefaultCancelTimeout = 5 * time.Second

// Result types a tools/call result may carry. An absent resultType means
// ResultComplete.
const (
	ResultComplete      = "complete"
	ResultInputRequired = "input_required"
	ResultTask          = "task"
)

// CallKind classifies the outcome of one call.
type CallKind int

const (
	// CallNotSent means nothing was sent: the request could not be built,
	// the token refresh failed, or ctx had ended before the send.
	CallNotSent CallKind = iota + 1
	// CallResult is a complete result the server did not mark as an error.
	CallResult
	// CallServerError is a complete result the server marked as an error.
	CallServerError
	// CallInputRequired is a result asking the client for input. None is
	// supplied.
	CallInputRequired
	// CallTask is a result deferring the call to a task. Nothing polls it.
	CallTask
	// CallProtocolError is a JSON-RPC error response.
	CallProtocolError
	// CallMalformed is a reply that could not be read as a response or a
	// result, or that passed a size, depth or notification bound.
	CallMalformed
	// CallTransport is a failure to carry the request or its reply: a
	// dropped stream or connection, a failing HTTP status with no JSON-RPC
	// response.
	CallTransport
	// CallCancelled means ctx ended while the call was in flight. The
	// cancel was sent and the reply, if any, is not read.
	CallCancelled
)

func (k CallKind) String() string {
	switch k {
	case CallNotSent:
		return "not sent"
	case CallResult:
		return "result"
	case CallServerError:
		return "server error"
	case CallInputRequired:
		return "input required"
	case CallTask:
		return "task"
	case CallProtocolError:
		return "protocol error"
	case CallMalformed:
		return "malformed"
	case CallTransport:
		return "transport failure"
	case CallCancelled:
		return "cancelled"
	}
	return "unknown"
}

// Response is the JSON-RPC response to a tools/call: exactly one of Result
// and Error is set.
type Response struct {
	Result json.RawMessage
	Error  *AnswerError
}

// Exchange carries one tools/call request under one revision and transport.
type Exchange struct {
	// Send sends the request with params once and reads until its response,
	// which it returns, a JSON-RPC error response included. It must not send
	// the request again for any reason, and must return promptly once ctx
	// ends, having stopped reading, with ctx's error. A reply that could not
	// be read as a response, or that passed a size, depth or notification
	// bound, is an error wrapping ErrMalformedReply; any other error is a
	// failure of transport.
	Send func(ctx context.Context, params json.RawMessage) (Response, error)
	// Cancel tells the server the request Send sent is cancelled, by the
	// means the revision and transport define. It is nil where ending Send's
	// ctx is itself that means: the current revision over HTTP, which
	// cancels by closing the connection. It is called at most once, after
	// Send has returned, with a ctx bounded by the cancel timeout.
	Cancel func(ctx context.Context) error
}

// CallOptions configures one call.
type CallOptions struct {
	// Refresh, when set, is the token-refresh hook. It is called once,
	// before the send; when it fails nothing is sent. It never leads to a
	// second send.
	Refresh func(ctx context.Context) error
	// CancelTimeout bounds the cancel, whether or not the server completes
	// it; zero means DefaultCancelTimeout.
	CancelTimeout time.Duration
}

// CallReply is the outcome of one call.
type CallReply struct {
	Kind CallKind
	// Sent reports whether the request may have reached the server: it is
	// false only for CallNotSent.
	Sent bool
	// Result is the result object for CallResult, CallServerError,
	// CallInputRequired and CallTask.
	Result json.RawMessage
	// Rendered is the rendering of a CallResult or CallServerError.
	Rendered Rendered
	// Error is the JSON-RPC error of a CallProtocolError.
	Error *AnswerError
	// Err is why the call is CallNotSent, CallMalformed, CallTransport or
	// CallCancelled; for CallCancelled it is ctx's error.
	Err error
	// CancelErr is the failure of the cancel of a CallCancelled, nil when it
	// was sent or the means needs nothing sent.
	CancelErr error
}

// CallParams returns the params of a tools/call request. arguments is a JSON
// object or empty.
func CallParams(name string, arguments json.RawMessage) (json.RawMessage, error) {
	p := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{name, arguments}
	return json.Marshal(p)
}

// Call sends one tools/call for the tool name with arguments over ex and
// returns its reply, classified. It supplies no input and polls nothing. It
// sends the request once: not again after a dropped stream, a refreshed
// token, a re-made connection or a failure, and not under the other
// revision. When ctx ends before the reply is read, Call cancels the request
// through ex.Cancel, waits for that at most opts.CancelTimeout, and returns
// CallCancelled without reading further.
func Call(ctx context.Context, ex Exchange, name string, arguments json.RawMessage, opts CallOptions) CallReply {
	params, err := CallParams(name, arguments)
	if err != nil {
		return CallReply{Kind: CallNotSent, Err: fmt.Errorf("mcp: tools/call params: %w", err)}
	}
	if opts.Refresh != nil {
		if err := opts.Refresh(ctx); err != nil {
			return CallReply{Kind: CallNotSent, Err: fmt.Errorf("mcp: token refresh: %w", err)}
		}
	}
	if err := ctx.Err(); err != nil {
		return CallReply{Kind: CallNotSent, Err: err}
	}

	resp, err := ex.Send(ctx, params)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return CallReply{
				Kind: CallCancelled, Sent: true, Err: cerr,
				CancelErr: cancelCall(ctx, ex.Cancel, opts.CancelTimeout),
			}
		}
		kind := CallTransport
		if errors.Is(err, ErrMalformedReply) {
			kind = CallMalformed
		}
		return CallReply{Kind: kind, Sent: true, Err: err}
	}
	// A reply that was read has ended the call, even when ctx ended as it
	// arrived; nothing is cancelled.
	if resp.Error != nil {
		return CallReply{Kind: CallProtocolError, Sent: true, Error: resp.Error}
	}
	return classifyResult(resp.Result)
}

// cancelCall runs cancel, if any, with a ctx of its own bounded by timeout,
// and stops waiting for it at the bound whether or not it has returned.
func cancelCall(ctx context.Context, cancel func(context.Context) error, timeout time.Duration) error {
	if cancel == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultCancelTimeout
	}
	cctx, stop := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- cancel(cctx) }()
	select {
	case err := <-done:
		return err
	case <-cctx.Done():
		return fmt.Errorf("mcp: cancel not completed: %w", cctx.Err())
	}
}

// classifyResult classifies a tools/call result by its resultType and, for
// a complete result, renders it.
func classifyResult(result json.RawMessage) CallReply {
	malformed := func(format string, args ...any) CallReply {
		return CallReply{Kind: CallMalformed, Sent: true, Result: result,
			Err: fmt.Errorf("%w: %s", ErrMalformedReply, fmt.Sprintf(format, args...))}
	}
	if _, err := jcs.Canonicalize(result); err != nil {
		return malformed("%v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(result, &obj); err != nil || obj == nil {
		return malformed("result is not an object")
	}
	rt := ResultComplete
	if raw, ok := obj["resultType"]; ok {
		if s, ok := stringMember(raw); ok {
			rt = s
		} else {
			return malformed("resultType is not a string")
		}
	}
	switch rt {
	case ResultInputRequired:
		return CallReply{Kind: CallInputRequired, Sent: true, Result: result}
	case ResultTask:
		return CallReply{Kind: CallTask, Sent: true, Result: result}
	case ResultComplete:
	default:
		return malformed("unknown resultType %q", rt)
	}
	r, err := Render(result)
	if err != nil {
		return CallReply{Kind: CallMalformed, Sent: true, Result: result, Err: err}
	}
	kind := CallResult
	if r.IsError {
		kind = CallServerError
	}
	return CallReply{Kind: kind, Sent: true, Result: result, Rendered: r}
}
