package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/mcptest"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

// rig is one fake server of one revision over one transport, with the
// client the engine would use for it.
type rig struct {
	name string
	srv  *mcptest.Server
	// ts is the HTTP server, nil over stdio.
	ts *httptest.Server
	// request sends one request other than tools/call.
	request func(method string, params json.RawMessage) (wire.Response, error)
	// exchange returns the exchange for the next call.
	exchange func() mcp.Exchange
	// cancelsByNotification is false where the cancel is closing the
	// connection.
	cancelsByNotification bool
}

var rigKinds = []string{"current/http", "legacy/http", "current/stdio", "legacy/stdio"}

// newRig starts a fake server for kind with cfg, whose revision it sets, and
// connects to it with limits lim.
func newRig(t *testing.T, kind string, cfg mcptest.Config, lim wire.Limits) *rig {
	t.Helper()
	return newRigOver(t, kind, cfg, lim, http.DefaultTransport)
}

// newRigOver is newRig with the HTTP transport tr.
func newRigOver(t *testing.T, kind string, cfg mcptest.Config, lim wire.Limits, tr http.RoundTripper) *rig {
	t.Helper()
	ctx := context.Background()
	info := client.DefaultClientInfo()
	legacy := strings.HasPrefix(kind, "legacy/")
	cfg.Revisions = []string{mcptest.Current}
	if legacy {
		cfg.Revisions = []string{mcptest.Legacy}
	}
	r := &rig{name: kind, srv: mcptest.New(cfg), cancelsByNotification: kind != "current/http"}
	var ids wire.IDs

	if strings.HasSuffix(kind, "/http") {
		r.ts = r.srv.HTTP(t)
		if !legacy {
			c := &client.CurrentHTTP{URL: r.ts.URL, Transport: tr, Info: info, Limits: lim}
			r.request = func(method string, params json.RawMessage) (wire.Response, error) {
				rep, err := c.Send(ctx, ids.Next(), method, params)
				return rep.Response, err
			}
			r.exchange = func() mcp.Exchange { return client.CurrentHTTPCall(c, ids.Next()) }
			return r
		}
		l, err := client.OpenLegacyHTTP(ctx, client.LegacyHTTPConfig{
			URL: r.ts.URL, Transport: tr, Info: info, Limits: lim,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close(ctx) })
		r.request = func(method string, params json.RawMessage) (wire.Response, error) {
			rep, err := l.Call(ctx, method, params)
			return rep.Response, err
		}
		r.exchange = func() mcp.Exchange { return client.LegacyHTTPCall(l) }
		return r
	}

	conn := r.srv.Stdio(t)
	s := &wire.Stream{
		Messages: wire.NewLineReader(conn.R, lim),
		Reply:    func(b []byte) error { _, err := conn.W.Write(append(b, '\n')); return err },
		Limits:   lim,
	}
	if !legacy {
		r.request = func(method string, params json.RawMessage) (wire.Response, error) {
			return client.SendCurrentStdio(conn.W, s, ids.Next(), method, params, info)
		}
		r.exchange = func() mcp.Exchange { return client.CurrentStdioCall(conn.W, s, ids.Next(), info) }
		return r
	}
	l, err := client.OpenLegacyStdio(conn.W, s, info)
	if err != nil {
		t.Fatal(err)
	}
	r.request = func(method string, params json.RawMessage) (wire.Response, error) {
		return l.Call(method, params)
	}
	r.exchange = func() mcp.Exchange { return client.LegacyStdioCall(l) }
	return r
}

func (r *rig) fetch(cursor string) (json.RawMessage, error) {
	params := json.RawMessage(`{}`)
	if cursor != "" {
		params, _ = json.Marshal(map[string]string{"cursor": cursor})
	}
	resp, err := r.request("tools/list", params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (r *rig) call(ctx context.Context, name string, opts mcp.CallOptions) mcp.CallReply {
	return mcp.Call(ctx, r.exchange(), name, json.RawMessage(`{"a":1}`), opts)
}

// requests returns the messages r's server received with method.
func (r *rig) requests(method string) []mcptest.Request {
	var out []mcptest.Request
	for _, q := range r.srv.Requests() {
		if q.Method == method {
			out = append(out, q)
		}
	}
	return out
}

// eventually fails t unless cond holds within a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCallListedAndCalledUnderEachRevision(t *testing.T) {
	for _, kind := range rigKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, kind, mcptest.Config{
				Tools: []json.RawMessage{mcptest.NewTool("echo", "d"), mcptest.NewTool("other", "d")},
				Calls: map[string][]mcptest.Reply{"echo": {mcptest.TextResult("hi")}},
			}, wire.Limits{})
			l, err := mcp.ListTools(r.fetch, mcp.ListOptions{})
			if err != nil || len(l.Tools) != 2 || l.Tools[0].Definition == nil {
				t.Fatalf("listing = %+v, %v", l, err)
			}
			rep := r.call(context.Background(), "echo", mcp.CallOptions{})
			if rep.Kind != mcp.CallResult || !rep.Sent || rep.Rendered.Text != "hi" {
				t.Fatalf("reply = %+v", rep)
			}
			calls := r.requests("tools/call")
			if len(calls) != 1 {
				t.Fatalf("tools/call sent %d times", len(calls))
			}
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(calls[0].Params, &p); err != nil || p.Name != "echo" || string(p.Arguments) != `{"a":1}` {
				t.Errorf("params = %s", calls[0].Params)
			}
		})
	}
}

func TestCallClassifiesReplies(t *testing.T) {
	serverError := mcptest.Reply{Result: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "no"}}, "isError": true,
	}}
	cases := []struct {
		name  string
		reply mcptest.Reply
		want  mcp.CallKind
	}{
		{"result", mcptest.TextResult("ok"), mcp.CallResult},
		{"complete named", mcptest.Reply{Result: map[string]any{"resultType": "complete", "content": []any{}}}, mcp.CallResult},
		{"server error", serverError, mcp.CallServerError},
		{"input required", mcptest.InputRequired(), mcp.CallInputRequired},
		{"task", mcptest.Task(), mcp.CallTask},
		{"protocol error", mcptest.ErrorReply(mcptest.CodeInvalidParams, "bad"), mcp.CallProtocolError},
		{"unknown result type", mcptest.Reply{Result: map[string]any{"resultType": "later"}}, mcp.CallMalformed},
		{"result type not a string", mcptest.Reply{Result: map[string]any{"resultType": 1}}, mcp.CallMalformed},
		{"content not an array", mcptest.Reply{Result: map[string]any{"content": "x"}}, mcp.CallMalformed},
		{"not JSON", mcptest.Reply{Body: []byte("not json")}, mcp.CallMalformed},
		{"over the size bound", mcptest.Reply{Oversize: 2048}, mcp.CallMalformed},
		{"over the notification bound", mcptest.Reply{Notifications: 257}, mcp.CallMalformed},
		{"dropped", mcptest.Reply{Drop: true, Notifications: 1}, mcp.CallTransport},
	}
	for _, kind := range rigKinds {
		for _, c := range cases {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				r := newRig(t, kind, mcptest.Config{
					Calls: map[string][]mcptest.Reply{"t": {c.reply}},
				}, wire.Limits{MaxMessageBytes: 1024})
				rep := r.call(context.Background(), "t", mcp.CallOptions{})
				if rep.Kind != c.want || !rep.Sent {
					t.Fatalf("reply = %v (%+v)", rep.Kind, rep)
				}
				if c.want == mcp.CallProtocolError && (rep.Error == nil || rep.Error.Code != mcptest.CodeInvalidParams) {
					t.Errorf("error = %+v", rep.Error)
				}
				if c.want == mcp.CallServerError && !strings.HasPrefix(rep.Rendered.Text, mcp.ErrorLine) {
					t.Errorf("rendered = %q", rep.Rendered.Text)
				}
				if n := r.srv.Count("tools/call"); n != 1 {
					t.Errorf("tools/call sent %d times", n)
				}
			})
		}
	}
}

func TestCallStreamDropSentOnce(t *testing.T) {
	for _, kind := range rigKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, kind, mcptest.Config{
				Calls: map[string][]mcptest.Reply{"t": {{Drop: true, Notifications: 2}}},
			}, wire.Limits{})
			rep := r.call(context.Background(), "t", mcp.CallOptions{})
			if rep.Kind != mcp.CallTransport {
				t.Fatalf("reply = %v (%v)", rep.Kind, rep.Err)
			}
			if n := r.srv.Count("tools/call"); n != 1 {
				t.Errorf("tools/call sent %d times", n)
			}
		})
	}
}

func TestCallHTTPConnectionDropSentOnce(t *testing.T) {
	for _, kind := range []string{"current/http", "legacy/http"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, kind, mcptest.Config{
				Calls: map[string][]mcptest.Reply{"t": {{Drop: true}}},
			}, wire.Limits{})
			rep := r.call(context.Background(), "t", mcp.CallOptions{})
			if rep.Kind != mcp.CallTransport {
				t.Fatalf("reply = %v (%v)", rep.Kind, rep.Err)
			}
			if n := r.srv.Count("tools/call"); n != 1 {
				t.Errorf("tools/call sent %d times", n)
			}
		})
	}
}

func TestCallReMadeConnectionSentOnce(t *testing.T) {
	for _, kind := range []string{"current/http", "legacy/http"} {
		for _, drop := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/answered", true: "/dropped"}[drop], func(t *testing.T) {
				var mu sync.Mutex
				dials := 0
				base := &net.Dialer{}
				tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					mu.Lock()
					dials++
					mu.Unlock()
					return base.DialContext(ctx, network, addr)
				}}
				t.Cleanup(tr.CloseIdleConnections)
				reply, want := mcptest.TextResult("ok"), mcp.CallResult
				if drop {
					reply, want = mcptest.Reply{Drop: true}, mcp.CallTransport
				}
				r := newRigOver(t, kind, mcptest.Config{Calls: map[string][]mcptest.Reply{"t": {reply}}}, wire.Limits{}, tr)
				if _, err := r.fetch(""); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				before := dials
				mu.Unlock()
				// The connection the listing left open is gone, so the call
				// goes out on a connection made again.
				tr.CloseIdleConnections()
				rep := r.call(context.Background(), "t", mcp.CallOptions{})
				if rep.Kind != want {
					t.Fatalf("reply = %v (%v)", rep.Kind, rep.Err)
				}
				if n := r.srv.Count("tools/call"); n != 1 {
					t.Errorf("tools/call sent %d times", n)
				}
				mu.Lock()
				defer mu.Unlock()
				if dials != before+1 {
					t.Errorf("dialled %d times for the call", dials-before)
				}
			})
		}
	}
}

func TestCallTokenRefreshSentOnce(t *testing.T) {
	for _, kind := range []string{"current/http", "legacy/http"} {
		for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
			t.Run(kind+"/"+http.StatusText(status), func(t *testing.T) {
				reply := mcptest.TextResult("ok")
				want := mcp.CallResult
				if status != http.StatusOK {
					reply, want = mcptest.Reply{Status: status, Silent: true}, mcp.CallTransport
				}
				srv := mcptest.New(mcptest.Config{
					Revisions: []string{map[string]string{"current/http": mcptest.Current, "legacy/http": mcptest.Legacy}[kind]},
					Calls:     map[string][]mcptest.Reply{"t": {reply}},
				})
				ts := srv.HTTP(t)
				hdr := http.Header{"Authorization": {"Bearer old"}}
				var ex mcp.Exchange
				if kind == "current/http" {
					c := &client.CurrentHTTP{URL: ts.URL, Transport: http.DefaultTransport, Header: hdr, Info: client.DefaultClientInfo()}
					ex = client.CurrentHTTPCall(c, 1)
				} else {
					l, err := client.OpenLegacyHTTP(context.Background(), client.LegacyHTTPConfig{
						URL: ts.URL, Transport: http.DefaultTransport, Header: hdr, Info: client.DefaultClientInfo(),
					})
					if err != nil {
						t.Fatal(err)
					}
					defer l.Close(context.Background())
					ex = client.LegacyHTTPCall(l)
				}
				refreshes := 0
				refresh := func(context.Context) error {
					refreshes++
					hdr.Set("Authorization", "Bearer new")
					return nil
				}
				rep := mcp.Call(context.Background(), ex, "t", nil, mcp.CallOptions{Refresh: refresh})
				if rep.Kind != want {
					t.Fatalf("reply = %v (%v)", rep.Kind, rep.Err)
				}
				if refreshes != 1 {
					t.Errorf("refreshed %d times", refreshes)
				}
				var calls []mcptest.Request
				for _, q := range srv.Requests() {
					if q.Method == "tools/call" {
						calls = append(calls, q)
					}
				}
				if len(calls) != 1 {
					t.Fatalf("tools/call sent %d times", len(calls))
				}
				if got := calls[0].Header.Get("Authorization"); got != "Bearer new" {
					t.Errorf("Authorization = %q", got)
				}
			})
		}
	}
}

func TestCallRefreshFailureSendsNothing(t *testing.T) {
	sends := 0
	ex := mcp.Exchange{Send: func(context.Context, json.RawMessage) (mcp.Response, error) {
		sends++
		return mcp.Response{Result: json.RawMessage(`{"content":[]}`)}, nil
	}}
	rep := mcp.Call(context.Background(), ex, "t", nil, mcp.CallOptions{
		Refresh: func(context.Context) error { return errors.New("refresh refused") },
	})
	if rep.Kind != mcp.CallNotSent || rep.Sent || sends != 0 {
		t.Fatalf("reply = %+v, sends = %d", rep, sends)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep = mcp.Call(ctx, ex, "t", nil, mcp.CallOptions{})
	if rep.Kind != mcp.CallNotSent || sends != 0 {
		t.Fatalf("reply = %+v, sends = %d", rep, sends)
	}
}

func TestCallCancelEachRevisionAndTransport(t *testing.T) {
	for _, kind := range rigKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, kind, mcptest.Config{
				Calls: map[string][]mcptest.Reply{"t": {{Delay: time.Hour}, mcptest.TextResult("second")}},
			}, wire.Limits{})
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				for r.srv.Count("tools/call") == 0 {
					time.Sleep(time.Millisecond)
				}
				cancel()
			}()
			rep := r.call(ctx, "t", mcp.CallOptions{})
			if rep.Kind != mcp.CallCancelled || !errors.Is(rep.Err, context.Canceled) || rep.CancelErr != nil {
				t.Fatalf("reply = %v (%v, cancel %v)", rep.Kind, rep.Err, rep.CancelErr)
			}
			first := r.requests("tools/call")[0]
			eventually(t, "the server to abandon the call", func() bool { return r.srv.Aborted() == 1 })
			cancels := r.requests("notifications/cancelled")
			if !r.cancelsByNotification {
				if len(cancels) != 0 {
					t.Errorf("sent %d notifications/cancelled", len(cancels))
				}
			} else {
				if len(cancels) != 1 {
					t.Fatalf("sent %d notifications/cancelled", len(cancels))
				}
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				if err := json.Unmarshal(cancels[0].Params, &p); err != nil || string(p.RequestID) != string(first.ID) {
					t.Errorf("cancel params = %s, call id = %s", cancels[0].Params, first.ID)
				}
			}

			// The connection serves the next call, which gets its own reply.
			rep = r.call(context.Background(), "t", mcp.CallOptions{})
			if rep.Kind != mcp.CallResult || rep.Rendered.Text != "second" {
				t.Fatalf("next reply = %v (%+v)", rep.Kind, rep)
			}
			calls := r.requests("tools/call")
			if len(calls) != 2 || string(calls[0].ID) == string(calls[1].ID) {
				t.Errorf("tools/call requests = %d", len(calls))
			}
		})
	}
}

// stubbornServer is a stdio server that answers a cancelled call anyway,
// after the cancel, with a notification and the late reply, and answers
// every later call with "second".
func stubbornServer(t *testing.T) (w io.Writer, s *wire.Stream, methods func() []string, first <-chan struct{}) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	var mu sync.Mutex
	var seen []string
	got := make(chan struct{})
	// Output is written from a queue of its own, as a pipe's buffer would
	// let a real server write while the client is not reading.
	out := make(chan []byte, 16)
	go func() {
		defer func() { _ = outW.Close() }()
		for b := range out {
			if _, err := outW.Write(b); err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(out)
		reply := func(id json.RawMessage, text string) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			}})
			out <- append(b, '\n')
		}
		br := bufio.NewReader(inR)
		var firstID json.RawMessage
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			m, err := wire.Decode(line)
			if err != nil {
				continue
			}
			mu.Lock()
			seen = append(seen, m.Method)
			mu.Unlock()
			switch {
			case m.Method == "tools/call" && firstID == nil:
				firstID = m.ID
				close(got)
			case m.Method == "tools/call":
				reply(m.ID, "second")
			case m.Method == "notifications/cancelled":
				out <- []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}` + "\n")
				reply(firstID, "late")
			}
		}
	}()
	methods = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	return inW, &wire.Stream{Messages: wire.NewLineReader(outR, wire.Limits{})}, methods, got
}

func TestCallCancelStopsReadingWhateverTheServerDoes(t *testing.T) {
	w, s, methods, first := stubbornServer(t)
	info := client.DefaultClientInfo()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-first
		cancel()
	}()
	rep := mcp.Call(ctx, client.CurrentStdioCall(w, s, 1, info), "t", nil, mcp.CallOptions{})
	if rep.Kind != mcp.CallCancelled {
		t.Fatalf("reply = %v (%+v)", rep.Kind, rep)
	}
	eventually(t, "the cancel to arrive", func() bool { return len(methods()) == 2 })

	rep = mcp.Call(context.Background(), client.CurrentStdioCall(w, s, 2, info), "t", nil, mcp.CallOptions{})
	if rep.Kind != mcp.CallResult || rep.Rendered.Text != "second" {
		t.Fatalf("next reply = %v (%+v)", rep.Kind, rep)
	}
	if got := strings.Join(methods(), " "); got != "tools/call notifications/cancelled tools/call" {
		t.Errorf("server saw %s", got)
	}
}

func TestCallStalledCancelStillEnds(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var cancels atomic.Int32
	ex := mcp.Exchange{
		Send: func(ctx context.Context, _ json.RawMessage) (mcp.Response, error) {
			<-ctx.Done()
			return mcp.Response{}, ctx.Err()
		},
		Cancel: func(context.Context) error {
			cancels.Add(1)
			<-release
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	rep := mcp.Call(ctx, ex, "t", nil, mcp.CallOptions{CancelTimeout: 50 * time.Millisecond})
	if rep.Kind != mcp.CallCancelled || !errors.Is(rep.Err, context.DeadlineExceeded) || rep.CancelErr == nil {
		t.Fatalf("reply = %v (%v, cancel %v)", rep.Kind, rep.Err, rep.CancelErr)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Call took %v", d)
	}
	if n := cancels.Load(); n != 1 {
		t.Errorf("cancelled %d times", n)
	}
}

func TestCallLegacyHTTPStalledCancelStillEnds(t *testing.T) {
	srv := mcptest.New(mcptest.Config{
		Revisions: []string{mcptest.Legacy},
		Calls:     map[string][]mcptest.Reply{"t": {{Delay: time.Hour}}},
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"notifications/cancelled"`)) {
			<-r.Context().Done() // the cancel is never answered
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	l, err := client.OpenLegacyHTTP(context.Background(), client.LegacyHTTPConfig{
		URL: ts.URL, Transport: http.DefaultTransport, Info: client.DefaultClientInfo(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for srv.Count("tools/call") == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	rep := mcp.Call(ctx, client.LegacyHTTPCall(l), "t", nil, mcp.CallOptions{CancelTimeout: 100 * time.Millisecond})
	if rep.Kind != mcp.CallCancelled || rep.CancelErr == nil {
		t.Fatalf("reply = %v (%v, cancel %v)", rep.Kind, rep.Err, rep.CancelErr)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Call took %v", d)
	}
	if n := srv.Count("tools/call"); n != 1 {
		t.Errorf("tools/call sent %d times", n)
	}
}

func TestCallStdioServerNotReadingStillEnds(t *testing.T) {
	// Nothing reads the server's standard input or writes its output, so
	// both the request and the cancel block in their writes.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inR.Close()
		_ = outW.Close()
	})
	s := &wire.Stream{Messages: wire.NewLineReader(outR, wire.Limits{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	rep := mcp.Call(ctx, client.CurrentStdioCall(inW, s, 1, client.DefaultClientInfo()), "t", nil,
		mcp.CallOptions{CancelTimeout: 50 * time.Millisecond})
	if rep.Kind != mcp.CallCancelled || rep.CancelErr == nil {
		t.Fatalf("reply = %v (%v, cancel %v)", rep.Kind, rep.Err, rep.CancelErr)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Call took %v", d)
	}
}

func TestCallReplyAsCancelledIsNotCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancels := 0
	ex := mcp.Exchange{
		Send: func(context.Context, json.RawMessage) (mcp.Response, error) {
			cancel()
			return mcp.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`)}, nil
		},
		Cancel: func(context.Context) error {
			cancels++
			return nil
		},
	}
	rep := mcp.Call(ctx, ex, "t", nil, mcp.CallOptions{})
	if rep.Kind != mcp.CallResult || rep.Rendered.Text != "done" || cancels != 0 {
		t.Fatalf("reply = %+v, cancels = %d", rep, cancels)
	}
}
