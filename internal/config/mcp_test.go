package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadMCP(t *testing.T, body string) (*Roster, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestMCPDecodeDefaults(t *testing.T) {
	r, err := loadMCP(t, `
[mcp]
max_effects = 3
[mcp.docs]
transport = "stdio"
[mcp.docs.tools]
search = "read"
"files.write" = "effect"
`)
	if err != nil {
		t.Fatal(err)
	}
	m := r.MCP
	if *m.MaxEffects != 3 || *m.MaxTools != 256 || *m.MaxHistoryResultBytes != 4096 || *m.MaxMessageBytes != 4194304 {
		t.Fatalf("limits: %+v", m)
	}
	s := m.Servers["docs"]
	if *s.TimeoutS != 60 || s.Protocol != "auto" || s.FileEgress || s.Tools["files.write"] != "effect" {
		t.Fatalf("server: %+v", s)
	}
}

func TestMCPUnknownKeyRefused(t *testing.T) {
	if _, err := loadMCP(t, "[mcp.a]\ntransport = \"stdio\"\nbogus = 1\n"); err == nil {
		t.Fatal("want unknown key error")
	}
}

func TestMCPRules(t *testing.T) {
	cases := []struct {
		name string
		srv  string
		cfg  MCPServer
		want string
	}{
		{"ok", "a_b-1", MCPServer{Transport: "http", Tools: map[string]string{"x.Y-z_1": "read"}}, ""},
		{"upper server", "Docs", MCPServer{Transport: "stdio"}, "server name"},
		{"long server", strings.Repeat("a", 33), MCPServer{Transport: "stdio"}, "server name"},
		{"dot server", "a.b", MCPServer{Transport: "stdio"}, "server name"},
		{"empty server", "", MCPServer{Transport: "stdio"}, "server name"},
		{"tool space", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"a b": "read"}}, "tool name"},
		{"tool wildcard", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"*": "read"}}, "tool name"},
		{"tool long", "a", MCPServer{Transport: "stdio", Tools: map[string]string{strings.Repeat("t", 129): "read"}}, "tool name"},
		{"tool max ok", "a", MCPServer{Transport: "stdio", Tools: map[string]string{strings.Repeat("t", 128): "effect"}}, ""},
		{"bad class", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"t": "write"}}, "class"},
		{"bad transport", "a", MCPServer{Transport: "ws"}, "transport"},
		{"bad protocol", "a", MCPServer{Transport: "stdio", Protocol: "2024-01-01"}, "protocol"},
		{"pinned protocol", "a", MCPServer{Transport: "stdio", Protocol: "2025-11-25"}, ""},
		{"timeout max", "a", MCPServer{Transport: "stdio", TimeoutS: intp(300)}, ""},
		{"timeout over", "a", MCPServer{Transport: "stdio", TimeoutS: intp(301)}, "timeout_s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := makeBaseRoster()
			r.MCP = &MCPConfig{Servers: map[string]MCPServer{c.srv: c.cfg}}
			r.ApplyDefaults()
			err := r.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestMCPLimitMustBePositive(t *testing.T) {
	r := makeBaseRoster()
	r.MCP = &MCPConfig{MaxTools: intp(0)}
	r.ApplyDefaults()
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "mcp.max_tools") {
		t.Fatalf("got %v", err)
	}
}
