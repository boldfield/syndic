package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/mcp"
)

type noSecrets struct{}

func (noSecrets) Resolve(config.SecretRef) (string, error) { return "", nil }

func TestNewMCPOptionsListsRemoteServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	opts := newMCPOptions(noSecrets{})
	cfg := config.MCPServer{Transport: "http", URL: srv.URL, Protocol: mcp.CurrentRevision}
	got, err := opts.List(context.Background(), "s", cfg, mcp.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != mcp.CurrentRevision || len(got.Listing.Tools) != 1 || got.Listing.Tools[0].Name != "echo" {
		t.Fatalf("listing = %+v", got)
	}
	if _, err := opts.List(context.Background(), "l", config.MCPServer{Transport: "stdio"}, mcp.ListOptions{}); err != engine.ErrMCPNoTransport {
		t.Fatalf("local err = %v", err)
	}
}

// autoServer answers server/discover as a current-era server and tools/list
// with the status fail holds, 200 when zero. It counts discoveries.
type autoServer struct {
	discovers atomic.Int32
	fail      atomic.Int32
}

func (a *autoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(b, &req)
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "server/discover":
		a.discovers.Add(1)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"supportedVersions":["` + mcp.CurrentRevision + `"],"capabilities":{},"resultType":"complete","ttlMs":0,"cacheScope":"none"}}`))
	case "tools/list":
		if s := a.fail.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`))
	default:
		w.WriteHeader(404)
	}
}

func TestNewMCPOptionsKeepsDetectedRevisionForRun(t *testing.T) {
	a := &autoServer{}
	srv := httptest.NewServer(a)
	defer srv.Close()
	opts := newMCPOptions(noSecrets{})
	cfg := config.MCPServer{Transport: "http", URL: srv.URL, Protocol: mcp.ProtocolAuto}
	list := func() error {
		got, err := opts.List(context.Background(), "s", cfg, mcp.ListOptions{})
		if err == nil && got.Revision != mcp.CurrentRevision {
			t.Fatalf("revision = %q", got.Revision)
		}
		return err
	}
	for i := 0; i < 3; i++ {
		if err := list(); err != nil {
			t.Fatal(err)
		}
	}
	if n := a.discovers.Load(); n != 1 {
		t.Fatalf("discoveries over three listings = %d, want 1", n)
	}

	// A 5xx is no evidence of era: the server is unoffered, the decision kept.
	a.fail.Store(503)
	if err := list(); err == nil {
		t.Fatal("503 listing succeeded")
	}
	a.fail.Store(0)
	if err := list(); err != nil {
		t.Fatal(err)
	}
	if n := a.discovers.Load(); n != 1 {
		t.Fatalf("discoveries after a 503 = %d, want 1", n)
	}

	// A 404 contradicts the current revision: unoffered for that listing,
	// detected again before the next.
	a.fail.Store(404)
	if err := list(); err == nil {
		t.Fatal("404 listing succeeded")
	}
	if n := a.discovers.Load(); n != 1 {
		t.Fatalf("discoveries during the contradicting listing = %d, want 1", n)
	}
	a.fail.Store(0)
	if err := list(); err != nil {
		t.Fatal(err)
	}
	if err := list(); err != nil {
		t.Fatal(err)
	}
	if n := a.discovers.Load(); n != 2 {
		t.Fatalf("discoveries after a contradiction = %d, want 2", n)
	}

	// Another server is detected on its own.
	if _, err := opts.List(context.Background(), "t", cfg, mcp.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := a.discovers.Load(); n != 3 {
		t.Fatalf("discoveries for a second server = %d, want 3", n)
	}
}
