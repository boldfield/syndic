package client

// The exchanges mcp.Call sends a tools/call over (v9 delta, section 3, "One
// send" and "Both"): one for each revision over each transport. Each sends
// the request once and cancels it by the means its revision and transport
// define.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

// cancelReason is the reason a cancel names.
const cancelReason = "cancelled by the client"

// callResponse returns the response to a tools/call, or the failure to read
// one marked as mcp.Call classifies it. A reply past a bound is malformed on
// any status. One that could not be read as a message is malformed on a
// success, and on a failing status, which then carried no response, is a
// failure of transport, as is anything else.
func callResponse(r wire.Response, err error) (mcp.Response, error) {
	if err != nil {
		var se *StatusError
		switch {
		case errors.Is(err, wire.ErrBound):
		case errors.As(err, &se):
			return mcp.Response{}, err
		case errors.Is(err, wire.ErrMalformed), errors.Is(err, wire.ErrContentType):
		default:
			return mcp.Response{}, err
		}
		return mcp.Response{}, fmt.Errorf("%w: %w", mcp.ErrMalformedReply, err)
	}
	out := mcp.Response{Result: r.Result}
	if r.Error != nil {
		out.Error = &mcp.AnswerError{Code: r.Error.Code, Message: r.Error.Message, Data: r.Error.Data}
	}
	return out, nil
}

// CurrentHTTPCall is the exchange for a call sent under id to c. The
// current revision cancels a request over HTTP by closing the connection,
// which ending Send's ctx does, so there is nothing else to send.
func CurrentHTTPCall(c *CurrentHTTP, id int64) mcp.Exchange {
	return mcp.Exchange{
		Send: func(ctx context.Context, params json.RawMessage) (mcp.Response, error) {
			rep, err := c.Send(ctx, id, "tools/call", params)
			return callResponse(rep.Response, err)
		},
	}
}

// CurrentStdioCall is the exchange for a call sent under id to the server
// whose standard input is w and whose output s yields. The cancel is
// notifications/cancelled naming id.
func CurrentStdioCall(w io.Writer, s *wire.Stream, id int64, info ClientInfo) mcp.Exchange {
	return mcp.Exchange{
		Send: func(ctx context.Context, params json.RawMessage) (mcp.Response, error) {
			return callResponse(SendCurrentStdioContext(ctx, w, s, id, "tools/call", params, info))
		},
		Cancel: func(ctx context.Context) error {
			return CancelCurrentStdio(ctx, w, id, cancelReason)
		},
	}
}

// LegacyHTTPCall is the exchange for a call in the session l, under a fresh
// id. Ending Send's ctx closes the connection, which under this revision
// does not cancel the request, so the cancel is notifications/cancelled
// naming the id, posted in the session.
func LegacyHTTPCall(l *LegacyHTTP) mcp.Exchange {
	id := l.NextID()
	return mcp.Exchange{
		Send: func(ctx context.Context, params json.RawMessage) (mcp.Response, error) {
			rep, err := l.Send(ctx, id, "tools/call", params)
			return callResponse(rep.Response, err)
		},
		Cancel: func(ctx context.Context) error {
			return l.Cancel(ctx, id, cancelReason)
		},
	}
}

// LegacyStdioCall is the exchange for a call in the session l, under a
// fresh id. The cancel is notifications/cancelled naming the id.
func LegacyStdioCall(l *LegacyStdio) mcp.Exchange {
	id := l.NextID()
	return mcp.Exchange{
		Send: func(ctx context.Context, params json.RawMessage) (mcp.Response, error) {
			return callResponse(l.SendContext(ctx, id, "tools/call", params))
		},
		Cancel: func(ctx context.Context) error {
			return l.CancelContext(ctx, id, cancelReason)
		},
	}
}
