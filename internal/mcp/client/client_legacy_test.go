package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boldfield/syndic/internal/mcp/mcptest"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

func legacyServer(cfg mcptest.Config) *mcptest.Server {
	cfg.Revisions = []string{mcptest.Legacy}
	if cfg.Tools == nil {
		cfg.Tools = []json.RawMessage{mcptest.NewTool("t", "d")}
	}
	return mcptest.New(cfg)
}

func legacyCfg(url string) LegacyHTTPConfig {
	return LegacyHTTPConfig{URL: url, Transport: http.DefaultTransport, Info: ClientInfo{Name: "syndic", Version: "1"}}
}

func TestLegacyInitializeParams(t *testing.T) {
	srv := legacyServer(mcptest.Config{})
	ts := srv.HTTP(t)
	l, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(context.Background())
	var p struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      ClientInfo      `json:"clientInfo"`
	}
	if err := json.Unmarshal(srv.Requests()[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != "2025-11-25" || string(p.Capabilities) != "{}" || p.ClientInfo.Name != "syndic" {
		t.Errorf("params = %+v", p)
	}
	if got := srv.Methods(); len(got) != 2 || got[1] != "notifications/initialized" {
		t.Errorf("methods = %v", got)
	}
}

func TestLegacyOtherVersionNotFollowedByInitialized(t *testing.T) {
	other := mcptest.Reply{Result: map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"serverInfo":      map[string]any{"name": "x", "version": "1"},
	}}
	srv := legacyServer(mcptest.Config{Initialize: &other})
	ts := srv.HTTP(t)
	_, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if !errors.Is(err, ErrLegacyVersion) {
		t.Fatalf("err = %v", err)
	}
	if srv.Called("notifications/initialized") || len(srv.Requests()) != 1 {
		t.Errorf("methods = %v", srv.Methods())
	}

	srv = legacyServer(mcptest.Config{Initialize: &other})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	if _, err := OpenLegacyStdio(c.W, s, DefaultClientInfo()); !errors.Is(err, ErrLegacyVersion) {
		t.Fatalf("stdio err = %v", err)
	}
	if srv.Called("notifications/initialized") {
		t.Error("initialized sent over stdio")
	}
}

func TestLegacyMalformedInitializeResult(t *testing.T) {
	bad := mcptest.Reply{Result: map[string]any{"protocolVersion": "2025-11-25"}}
	srv := legacyServer(mcptest.Config{Initialize: &bad})
	ts := srv.HTTP(t)
	_, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if srv.Called("notifications/initialized") {
		t.Error("initialized sent")
	}
}

func TestLegacyNoSessionIdentifierSendsNone(t *testing.T) {
	srv := legacyServer(mcptest.Config{})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := OpenLegacyHTTP(ctx, legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := l.Call(ctx, "tools/list", nil); err != nil || rep.Response.Error != nil {
		t.Fatalf("list: %+v %v", rep, err)
	}
	l.Close(ctx)
	for _, r := range srv.Requests() {
		if r.Header.Get("Mcp-Session-Id") != "" {
			t.Errorf("%s carried a session", r.Method)
		}
		if r.HTTPMethod == http.MethodDelete {
			t.Error("delete sent without a session")
		}
	}
}

func TestLegacySessionIdentifierOnEveryRequestAndDelete(t *testing.T) {
	srv := legacyServer(mcptest.Config{SessionID: "sess-1"})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := OpenLegacyHTTP(ctx, legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Call(ctx, "tools/list", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Call(ctx, "tools/call", json.RawMessage(`{"name":"t"}`)); err != nil {
		t.Fatal(err)
	}
	l.Close(ctx)
	l.Close(ctx)
	reqs := srv.Requests()
	if len(reqs) != 5 {
		t.Fatalf("%d requests: %v", len(reqs), srv.Methods())
	}
	if reqs[0].Header.Get("Mcp-Session-Id") != "" {
		t.Error("initialize carried a session")
	}
	for _, r := range reqs[1:] {
		if got := r.Header.Get("Mcp-Session-Id"); got != "sess-1" {
			t.Errorf("%s %s session = %q", r.HTTPMethod, r.Method, got)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != "2025-11-25" {
			t.Errorf("version = %q", got)
		}
	}
	if last := reqs[4]; last.HTTPMethod != http.MethodDelete {
		t.Errorf("last = %s", last.HTTPMethod)
	}
	if _, err := l.Call(ctx, "tools/list", nil); err == nil {
		t.Error("call after close succeeded")
	}
}

func TestLegacyDeleteMethodNotAllowedTolerated(t *testing.T) {
	srv := legacyServer(mcptest.Config{SessionID: "s", DeleteStatus: http.StatusMethodNotAllowed})
	ts := srv.HTTP(t)
	l, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	l.Close(context.Background())
	reqs := srv.Requests()
	if last := reqs[len(reqs)-1]; last.HTTPMethod != http.MethodDelete {
		t.Errorf("last = %s %s", last.HTTPMethod, last.Method)
	}
}

func TestLegacyFailedHandshakeEndsIssuedSession(t *testing.T) {
	other := mcptest.Reply{Result: map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"serverInfo":      map[string]any{"name": "x", "version": "1"},
	}}
	srv := legacyServer(mcptest.Config{SessionID: "s1", Initialize: &other})
	ts := srv.HTTP(t)
	if _, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL)); !errors.Is(err, ErrLegacyVersion) {
		t.Fatalf("err = %v", err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[1].HTTPMethod != http.MethodDelete || reqs[1].Header.Get("Mcp-Session-Id") != "s1" {
		t.Errorf("requests = %v", srv.Methods())
	}
	if srv.Called("notifications/initialized") {
		t.Error("initialized sent")
	}
}

func TestLegacyServerInfoWithoutVersionNotFollowedByInitialized(t *testing.T) {
	bad := mcptest.Reply{Result: map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"serverInfo":      map[string]any{"name": "x"},
	}}
	srv := legacyServer(mcptest.Config{Initialize: &bad})
	ts := srv.HTTP(t)
	if _, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL)); !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if srv.Called("notifications/initialized") {
		t.Error("initialized sent")
	}
}

func TestLegacyCancelNamesRequestID(t *testing.T) {
	srv := legacyServer(mcptest.Config{})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := OpenLegacyHTTP(ctx, legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(ctx)
	id := l.NextID()
	if _, err := l.Send(ctx, id, "tools/list", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Cancel(ctx, id, "stop"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	var p struct {
		RequestID int64  `json:"requestId"`
		Reason    string `json:"reason"`
	}
	if last.Method != "notifications/cancelled" || json.Unmarshal(last.Params, &p) != nil || p.RequestID != id || p.Reason != "stop" {
		t.Errorf("last = %s %s", last.Method, last.Params)
	}
}

func TestLegacyStdioCancelNamesRequestID(t *testing.T) {
	srv := legacyServer(mcptest.Config{})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	l, err := OpenLegacyStdio(c.W, s, DefaultClientInfo())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	id := l.NextID()
	if _, err := l.Send(id, "tools/list", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Cancel(id, ""); err != nil {
		t.Fatal(err)
	}
	// The server reads asynchronously; a later round trip orders the two.
	if _, err := l.Call("tools/list", nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if r.Method == "notifications/cancelled" {
			var p struct {
				RequestID int64 `json:"requestId"`
			}
			if json.Unmarshal(r.Params, &p) != nil || p.RequestID != id {
				t.Errorf("params = %s", r.Params)
			}
			return
		}
	}
	t.Error("no notifications/cancelled")
}

func TestLegacyStdioHandshakeAndCall(t *testing.T) {
	srv := legacyServer(mcptest.Config{})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	l, err := OpenLegacyStdio(c.W, s, DefaultClientInfo())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	resp, err := l.Call("tools/list", nil)
	if err != nil || resp.Error != nil {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestLegacyMalformedInitializeBodyEndsIssuedSession(t *testing.T) {
	var methods, sids []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		sids = append(sids, r.Header.Get("Mcp-Session-Id"))
		if r.Method != http.MethodPost {
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":`))
	}))
	defer ts.Close()
	_, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if len(methods) != 2 || methods[1] != http.MethodDelete || sids[1] != "s1" {
		t.Errorf("methods = %v, sessions = %v", methods, sids)
	}
}

func TestLegacyMalformedInitializeOnFailingStatusEndsIssuedSession(t *testing.T) {
	var methods, sids []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		sids = append(sids, r.Header.Get("Mcp-Session-Id"))
		if r.Method != http.MethodPost {
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":`))
	}))
	defer ts.Close()
	_, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Errorf("err = %v, want the status kept", err)
	}
	if len(methods) != 2 || methods[1] != http.MethodDelete || sids[1] != "s1" {
		t.Errorf("methods = %v, sessions = %v", methods, sids)
	}
}

func TestLegacyUnreadableInitializeEndsIssuedSession(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter){
		"wrong media type": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		},
		"stream without the response": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n"))
		},
		"result on a failing status": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var methods []string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method+" "+r.Header.Get("Mcp-Session-Id"))
				if r.Method != http.MethodPost {
					return
				}
				w.Header().Set("Mcp-Session-Id", "s1")
				write(w)
			}))
			defer ts.Close()
			if _, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL)); !errors.Is(err, ErrLegacyInitialize) {
				t.Fatalf("err = %v", err)
			}
			if len(methods) != 2 || methods[1] != "DELETE s1" {
				t.Errorf("requests = %v", methods)
			}
		})
	}
}

func TestLegacyInitializeErrorEndsIssuedSession(t *testing.T) {
	var methods []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.Header.Get("Mcp-Session-Id"))
		if r.Method != http.MethodPost {
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"no"}}`))
	}))
	defer ts.Close()
	if _, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL)); !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if len(methods) != 2 || methods[1] != "DELETE s1" {
		t.Errorf("requests = %v", methods)
	}
}

func TestLegacyUnusableSessionIdentifierEndsAttempt(t *testing.T) {
	var methods []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Mcp-Session-Id", "bad id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"x","version":"1"}}}`))
	}))
	defer ts.Close()
	if _, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL)); !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("requests = %v", methods)
	}
}

func TestLegacyHTTPStatusIsTheRealOne(t *testing.T) {
	srv := legacyServer(mcptest.Config{Statuses: map[string]int{"tools/call": http.StatusBadRequest},
		Calls: map[string][]mcptest.Reply{"t": {mcptest.ErrorReply(mcptest.CodeInvalidParams, "bad")}}})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := OpenLegacyHTTP(ctx, legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(ctx)
	rep, err := l.Call(ctx, "tools/call", json.RawMessage(`{"name":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != http.StatusBadRequest || rep.Response.Error == nil || rep.Response.Error.Code != mcptest.CodeInvalidParams {
		t.Errorf("reply = %+v", rep)
	}
}

func TestLegacyHTTPServerRequestAnsweredWithError(t *testing.T) {
	srv := legacyServer(mcptest.Config{SessionID: "s1", Calls: map[string][]mcptest.Reply{"t": {{ServerRequests: 1, Result: map[string]any{"content": []any{}}}}}})
	ts := srv.HTTP(t)
	ctx := context.Background()
	l, err := OpenLegacyHTTP(ctx, legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(ctx)
	rep, err := l.Call(ctx, "tools/call", json.RawMessage(`{"name":"t"}`))
	if err != nil || rep.Response.Error != nil {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, r := range srv.Requests() {
		if r.Method == "" && r.ID != nil {
			var m struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(r.Raw, &m) != nil || m.Error == nil || string(r.ID) != `"srv-1"` {
				t.Errorf("answer = %s", r.Raw)
			}
			if r.Header.Get("Mcp-Session-Id") != "s1" {
				t.Error("answer carried no session")
			}
			return
		}
	}
	t.Errorf("no answer to the server's request: %v", srv.Methods())
}

func TestLegacyCloseDeletesWhenContextDone(t *testing.T) {
	srv := legacyServer(mcptest.Config{SessionID: "s1"})
	ts := srv.HTTP(t)
	l, err := OpenLegacyHTTP(context.Background(), legacyCfg(ts.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Close(ctx)
	reqs := srv.Requests()
	if last := reqs[len(reqs)-1]; last.HTTPMethod != http.MethodDelete || last.Header.Get("Mcp-Session-Id") != "s1" {
		t.Errorf("last = %s %s", last.HTTPMethod, last.Method)
	}
}

func TestLegacyStdioMalformedResultNotFollowedByInitialized(t *testing.T) {
	bad := mcptest.Reply{Result: map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}}}
	srv := legacyServer(mcptest.Config{Initialize: &bad})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	if _, err := OpenLegacyStdio(c.W, s, DefaultClientInfo()); !errors.Is(err, ErrLegacyInitialize) {
		t.Fatalf("err = %v", err)
	}
	if srv.Called("notifications/initialized") {
		t.Error("initialized sent")
	}
}
