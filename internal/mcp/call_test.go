package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/mcptest"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

const callParams = `{"name":"echo","arguments":{"a":1}}`

func echoServer(revs []string, calls map[string][]mcptest.Reply) *mcptest.Server {
	return mcptest.New(mcptest.Config{
		Revisions: revs,
		Tools:     []json.RawMessage{mcptest.NewTool("echo", "d")},
		Calls:     calls,
	})
}

func fromWire(status int, r wire.Response) mcp.CallReply {
	rep := mcp.CallReply{Status: status, Result: r.Result}
	if r.Error != nil {
		rep.Error = &mcp.RPCError{Code: r.Error.Code, Message: r.Error.Message, Data: r.Error.Data}
	}
	return rep
}

func asReply(rep client.HTTPReply, err error) (mcp.CallReply, error) {
	var se *client.StatusError
	if errors.As(err, &se) {
		return mcp.CallReply{Status: se.Status}, err
	}
	return fromWire(rep.Status, rep.Response), err
}

func classifyWire(err error) (malformed, bound bool) {
	return errors.Is(err, wire.ErrMalformed) || errors.Is(err, wire.ErrContentType) || errors.Is(err, wire.ErrBound),
		errors.Is(err, wire.ErrBound)
}

func currentHTTP(url string) *client.CurrentHTTP {
	return &client.CurrentHTTP{URL: url, Transport: http.DefaultTransport, Info: client.DefaultClientInfo()}
}

func currentSend(c *client.CurrentHTTP) mcp.SendFunc {
	return func(ctx context.Context) (mcp.CallReply, error) {
		return asReply(c.Send(ctx, 1, "tools/call", json.RawMessage(callParams)))
	}
}

func TestCallCurrentHTTPListsAndCalls(t *testing.T) {
	srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {mcptest.TextResult("hi")}})
	ts := srv.HTTP(t)
	if l, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{}); err != nil || len(l.Tools) != 1 {
		t.Fatalf("list: %v %+v", err, l)
	}
	out := mcp.Call(context.Background(), mcp.CallOptions{Send: currentSend(currentHTTP(ts.URL)), Classify: classifyWire})
	if out.Kind != mcp.CallResult || out.Rendered == nil || out.Rendered.Text != "hi" || out.Sends != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

func TestCallLegacyHTTPListsAndCalls(t *testing.T) {
	srv := mcptest.New(mcptest.Config{
		Revisions: []string{mcptest.Legacy},
		Tools:     []json.RawMessage{mcptest.NewTool("echo", "d")},
		SessionID: "s1",
	})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := client.OpenLegacyHTTP(ctx, client.LegacyHTTPConfig{URL: ts.URL, Transport: http.DefaultTransport, Info: client.DefaultClientInfo()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(ctx)
	list, err := mcp.ListTools(func(cursor string) (json.RawMessage, error) {
		rep, err := l.Call(ctx, "tools/list", json.RawMessage(`{}`))
		if err != nil {
			return nil, err
		}
		return rep.Response.Result, nil
	}, mcp.ListOptions{})
	if err != nil || len(list.Tools) != 1 {
		t.Fatalf("list: %v", err)
	}
	id := l.NextID()
	out := mcp.Call(ctx, mcp.CallOptions{Send: func(ctx context.Context) (mcp.CallReply, error) {
		return asReply(l.Send(ctx, id, "tools/call", json.RawMessage(callParams)))
	}})
	if out.Kind != mcp.CallResult || out.Rendered.Text != "ok" {
		t.Fatalf("outcome = %+v", out)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

func TestCallStdioBothRevisions(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		srv := echoServer(nil, nil)
		conn := srv.Stdio(t)
		s := &wire.Stream{Messages: wire.NewLineReader(conn.R, wire.Limits{})}
		out := mcp.Call(context.Background(), mcp.CallOptions{Send: func(context.Context) (mcp.CallReply, error) {
			r, err := client.SendCurrentStdio(conn.W, s, 1, "tools/call", json.RawMessage(callParams), client.DefaultClientInfo())
			return fromWire(0, r), err
		}})
		if out.Kind != mcp.CallResult || srv.Count("tools/call") != 1 {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		srv := mcptest.New(mcptest.Config{Revisions: []string{mcptest.Legacy}, Tools: []json.RawMessage{mcptest.NewTool("echo", "d")}})
		conn := srv.Stdio(t)
		s := &wire.Stream{Messages: wire.NewLineReader(conn.R, wire.Limits{})}
		l, err := client.OpenLegacyStdio(conn.W, s, client.DefaultClientInfo())
		if err != nil {
			t.Fatal(err)
		}
		id := l.NextID()
		out := mcp.Call(context.Background(), mcp.CallOptions{Send: func(context.Context) (mcp.CallReply, error) {
			r, err := l.Send(id, "tools/call", json.RawMessage(callParams))
			return fromWire(0, r), err
		}})
		if out.Kind != mcp.CallResult || srv.Count("tools/call") != 1 {
			t.Fatalf("outcome = %+v", out)
		}
	})
}

func TestCallClassifiesReplies(t *testing.T) {
	big := mcptest.Reply{Oversize: 5 << 20}
	cases := []struct {
		name  string
		reply mcptest.Reply
		kind  mcp.CallKind
		bound bool
	}{
		{"result", mcptest.TextResult("x"), mcp.CallResult, false},
		{"server error", mcptest.Reply{Result: map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "bad"}}}}, mcp.CallServerError, false},
		{"input required", mcptest.InputRequired(), mcp.CallInputRequired, false},
		{"task", mcptest.Task(), mcp.CallTask, false},
		{"protocol error", mcptest.ErrorReply(mcptest.CodeInvalidParams, "no"), mcp.CallProtocolError, false},
		{"malformed", mcptest.Reply{Body: []byte(`{nope`)}, mcp.CallMalformed, false},
		{"unknown resultType", mcptest.Reply{Result: map[string]any{"resultType": "later"}}, mcp.CallMalformed, false},
		{"too deep", mcptest.Reply{Body: mcptest.NestedJSON(100)}, mcp.CallMalformed, true},
		{"too large", big, mcp.CallMalformed, true},
		{"too many notifications", mcptest.Reply{Notifications: 300, Result: map[string]any{"content": []any{}}}, mcp.CallMalformed, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {c.reply}})
			ts := srv.HTTP(t)
			out := mcp.Call(context.Background(), mcp.CallOptions{Send: currentSend(currentHTTP(ts.URL)), Classify: classifyWire})
			if out.Kind != c.kind || out.Bound != c.bound {
				t.Fatalf("outcome = %+v (%v)", out, out.Err)
			}
			if srv.Count("tools/call") != 1 {
				t.Fatal("sent more than once")
			}
		})
	}
}

func TestCallDroppedStreamIsSentOnce(t *testing.T) {
	srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {{Drop: true, Notifications: 2}}})
	ts := srv.HTTP(t)
	sends := 0
	send := currentSend(currentHTTP(ts.URL))
	out := mcp.Call(context.Background(), mcp.CallOptions{Send: func(ctx context.Context) (mcp.CallReply, error) {
		sends++
		return send(ctx)
	}})
	if out.Kind != mcp.CallTransport || sends != 1 || srv.Count("tools/call") != 1 {
		t.Fatalf("outcome = %+v sends=%d calls=%d", out, sends, srv.Count("tools/call"))
	}
	// A closed connection before any header.
	srv2 := echoServer(nil, map[string][]mcptest.Reply{"echo": {{Drop: true}}})
	ts2 := srv2.HTTP(t)
	out = mcp.Call(context.Background(), mcp.CallOptions{Send: currentSend(currentHTTP(ts2.URL))})
	if out.Kind != mcp.CallTransport || srv2.Count("tools/call") != 1 {
		t.Fatalf("outcome = %+v calls=%d", out, srv2.Count("tools/call"))
	}
}

func TestCallRefreshedTokenIsSentOnce(t *testing.T) {
	srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {
		{Status: http.StatusUnauthorized, Body: []byte(`unauthorized`)},
		mcptest.TextResult("second"),
	}})
	ts := srv.HTTP(t)
	refreshes := 0
	out := mcp.Call(context.Background(), mcp.CallOptions{
		Send:     currentSend(currentHTTP(ts.URL)),
		Classify: classifyWire,
		Refresh:  func() error { refreshes++; return nil },
	})
	if out.Kind != mcp.CallTransport || out.Status != http.StatusUnauthorized || !out.Refreshed || refreshes != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Fatalf("%d calls after a refresh", n)
	}
}

func TestCallRemadeConnectionIsSentOnce(t *testing.T) {
	srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {{Drop: true}, mcptest.TextResult("again")}})
	ts := srv.HTTP(t)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	c := &client.CurrentHTTP{URL: ts.URL, Transport: tr, Info: client.DefaultClientInfo()}
	// A first exchange leaves a kept-alive connection; the call's server then
	// closes it, and the transport must not send the POST again.
	if _, err := c.Send(context.Background(), 1, "tools/list", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	out := mcp.Call(context.Background(), mcp.CallOptions{Send: currentSend(c)})
	if out.Kind != mcp.CallTransport {
		t.Fatalf("outcome = %+v", out)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

func TestCallCancelCurrentHTTPClosesConnection(t *testing.T) {
	srv := echoServer(nil, map[string][]mcptest.Reply{"echo": {{Delay: 30 * time.Second}}})
	ts := srv.HTTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for srv.Count("tools/call") == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	out := mcp.Call(ctx, mcp.CallOptions{Send: currentSend(currentHTTP(ts.URL)), Classify: classifyWire})
	if out.Kind != mcp.CallCancelled {
		t.Fatalf("outcome = %+v", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.Aborted() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Aborted() == 0 {
		t.Error("server never saw the connection close")
	}
	if srv.Count("tools/call") != 1 {
		t.Error("sent more than once")
	}
}

func TestCallCancelLegacySendsNotification(t *testing.T) {
	srv := mcptest.New(mcptest.Config{
		Revisions: []string{mcptest.Legacy},
		Tools:     []json.RawMessage{mcptest.NewTool("echo", "d")},
		Calls:     map[string][]mcptest.Reply{"echo": {{Delay: 30 * time.Second}}},
	})
	conn := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(conn.R, wire.Limits{})}
	l, err := client.OpenLegacyStdio(conn.W, s, client.DefaultClientInfo())
	if err != nil {
		t.Fatal(err)
	}
	id := l.NextID()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for srv.Count("tools/call") == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	out := mcp.Call(ctx, mcp.CallOptions{
		Send: func(context.Context) (mcp.CallReply, error) {
			r, err := l.Send(id, "tools/call", json.RawMessage(callParams))
			return fromWire(0, r), err
		},
		Cancel: func() error { return l.Cancel(id, "stop") },
	})
	if out.Kind != mcp.CallCancelled || out.Err == nil || strings.Contains(out.Err.Error(), "cancelling") {
		t.Fatalf("outcome = %+v", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.Count("notifications/cancelled") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Count("notifications/cancelled") != 1 || srv.Count("tools/call") != 1 {
		t.Fatalf("methods = %v", srv.Methods())
	}
}

func TestCallWithoutSend(t *testing.T) {
	if out := mcp.Call(context.Background(), mcp.CallOptions{}); out.Kind != mcp.CallTransport {
		t.Fatalf("outcome = %+v", out)
	}
}
