package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
