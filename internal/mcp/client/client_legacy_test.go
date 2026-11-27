package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
