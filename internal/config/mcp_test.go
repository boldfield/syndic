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
		{"ok", "a_b-1", MCPServer{Transport: "http", URL: "https://a.example", Tools: map[string]string{"x.Y-z_1": "read"}}, ""},
		{"upper server", "Docs", MCPServer{Transport: "stdio"}, "server name"},
		{"long server", strings.Repeat("a", 33), MCPServer{Transport: "stdio"}, "server name"},
		{"dot server", "a.b", MCPServer{Transport: "stdio"}, "server name"},
		{"empty server", "", MCPServer{Transport: "stdio"}, "server name"},
		{"tool space", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"a b": "read"}}, "tool name"},
		{"tool wildcard", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"*": "read"}}, "tool name"},
		{"tool long", "a", MCPServer{Transport: "stdio", Tools: map[string]string{strings.Repeat("t", 129): "read"}}, "tool name"},
		{"tool max ok", "a", MCPServer{Transport: "stdio", Command: "x", Tools: map[string]string{strings.Repeat("t", 128): "effect"}}, ""},
		{"bad class", "a", MCPServer{Transport: "stdio", Tools: map[string]string{"t": "write"}}, "class"},
		{"bad transport", "a", MCPServer{Transport: "ws"}, "transport"},
		{"bad protocol", "a", MCPServer{Transport: "stdio", Protocol: "2024-01-01"}, "protocol"},
		{"pinned protocol", "a", MCPServer{Transport: "stdio", Command: "x", Protocol: "2025-11-25"}, ""},
		{"timeout max", "a", MCPServer{Transport: "stdio", Command: "x", TimeoutS: intp(300)}, ""},
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

func TestMCPTransportSettings(t *testing.T) {
	sec := &SecretRef{Source: "env", Name: "TOK"}
	cases := []struct {
		name string
		cfg  MCPServer
		sbx  string
		want string
	}{
		{"local ok", MCPServer{Transport: "stdio", Command: "x", Args: []string{"a"}, Env: map[string]SecretRef{"K": *sec}, Instance: "run", State: "persistent", NetworkAllow: []string{"api.example.com"}}, "", ""},
		{"local no command", MCPServer{Transport: "stdio"}, "", "command"},
		{"workspace effect", MCPServer{Transport: "stdio", Command: "x", Workspace: "scratch", Tools: map[string]string{"w": "effect"}}, "", "effect tool"},
		{"workspace read ok", MCPServer{Transport: "stdio", Command: "x", Workspace: "scratch", Tools: map[string]string{"r": "read"}}, "", ""},
		{"run scratch", MCPServer{Transport: "stdio", Command: "x", Instance: "run", Workspace: "scratch"}, "", "instance"},
		{"bad instance", MCPServer{Transport: "stdio", Command: "x", Instance: "x"}, "", "instance"},
		{"bad state", MCPServer{Transport: "stdio", Command: "x", State: "x"}, "", "state"},
		{"bad workspace", MCPServer{Transport: "stdio", Command: "x", Workspace: "x"}, "", "workspace"},
		{"bad env ref", MCPServer{Transport: "stdio", Command: "x", Env: map[string]SecretRef{"K": {Source: "lit", Name: "v"}}}, "", "secret"},
		{"network none sandbox", MCPServer{Transport: "stdio", Command: "x", NetworkAllow: []string{"a.example.com"}}, "none", "sandbox"},
		{"url on local", MCPServer{Transport: "stdio", Command: "x", URL: "https://a.example"}, "", "remote"},
		{"auth on local", MCPServer{Transport: "stdio", Command: "x", Auth: sec}, "", "remote"},
		{"remote ok", MCPServer{Transport: "http", URL: "https://a.example/mcp", Auth: sec, AddressAllow: []string{"10.1.0.0/16", "fd00::/8", "192.168.1.5"}, AuthOrigins: []string{"https://id.example"}, Scopes: []string{"s"}, ClientID: "c"}, "", ""},
		{"loopback http", MCPServer{Transport: "http", URL: "http://127.0.0.1:8080/mcp"}, "", ""},
		{"localhost http", MCPServer{Transport: "http", URL: "http://localhost/mcp"}, "", ""},
		{"v6 loopback http", MCPServer{Transport: "http", URL: "http://[::1]:9/mcp"}, "", ""},
		{"plain http", MCPServer{Transport: "http", URL: "http://a.example/mcp"}, "", "plain http"},
		{"plain http private ip", MCPServer{Transport: "http", URL: "http://10.0.0.1/mcp"}, "", "plain http"},
		{"no url", MCPServer{Transport: "http"}, "", "url"},
		{"public range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"8.8.8.0/24"}}, "", "address_allow"},
		{"wide range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"10.0.0.0/7"}}, "", "address_allow"},
		{"loopback range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"127.0.0.0/8"}}, "", "address_allow"},
		{"link-local range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"fe80::/10"}}, "", "address_allow"},
		{"mapped range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"::ffff:8.8.8.8/128"}}, "", "address_allow"},
		{"garbage range", MCPServer{Transport: "http", URL: "https://a.example", AddressAllow: []string{"nope"}}, "", "address_allow"},
		{"origin with path", MCPServer{Transport: "http", URL: "https://a.example", AuthOrigins: []string{"https://id.example/x"}}, "", "origin"},
		{"command on remote", MCPServer{Transport: "http", URL: "https://a.example", Command: "x"}, "", "local"},
		{"workspace on remote", MCPServer{Transport: "http", URL: "https://a.example", Workspace: "scratch"}, "", "local"},
		{"network_allow on remote", MCPServer{Transport: "http", URL: "https://a.example", NetworkAllow: []string{"a.example.com"}}, "", "local"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := makeBaseRoster()
			if c.sbx != "" {
				r.Tools = &ToolsConfig{Exec: &ExecConfig{Sandbox: c.sbx}}
			}
			r.MCP = &MCPConfig{Servers: map[string]MCPServer{"s": c.cfg}}
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

func TestMCPLocalDefaults(t *testing.T) {
	r := makeBaseRoster()
	r.MCP = &MCPConfig{Servers: map[string]MCPServer{"s": {Transport: "stdio", Command: "x"}}}
	r.ApplyDefaults()
	s := r.MCP.Servers["s"]
	if s.Instance != "turn" || s.State != "fresh" || s.Workspace != "none" {
		t.Fatalf("%+v", s)
	}
}

func TestMCPEmptyWrongTransportSettingsRefused(t *testing.T) {
	remote := "transport = \"http\"\nurl = \"https://a.example/mcp\"\n"
	local := "transport = \"stdio\"\ncommand = \"x\"\n"
	cases := []struct{ name, body, want string }{
		{"args", remote + "args = []\n", "args"},
		{"env", remote + "env = {}\n", "env"},
		{"instance", remote + "instance = \"\"\n", "instance"},
		{"network_allow", remote + "network_allow = []\n", "network_allow"},
		{"command", remote + "command = \"\"\n", "command"},
		{"address_allow", local + "address_allow = []\n", "address_allow"},
		{"auth_origins", local + "auth_origins = []\n", "auth_origins"},
		{"scopes", local + "scopes = []\n", "scopes"},
		{"url", local + "url = \"\"\n", "url"},
		{"client_id", local + "client_id = \"\"\n", "client_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := loadMCP(t, "[mcp.s]\n"+c.body)
			if err != nil {
				t.Fatal(err)
			}
			r.ApplyDefaults()
			err = r.Validate()
			if err == nil || !strings.Contains(err.Error(), "mcp.s."+c.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
