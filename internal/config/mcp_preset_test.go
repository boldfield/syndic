package config

import (
	"strings"
	"testing"
)

func mcpPresetRoster(mode string, tools, reviewerTools []string) *Roster {
	r := makeBaseRoster()
	r.Endpoints[0].RefereeModel = "ref"
	r.MCP = &MCPConfig{Servers: map[string]MCPServer{
		"docs":   {Transport: MCPTransportStdio, Command: "x", Tools: map[string]string{"search": "read", "post": "effect"}},
		"remote": {Transport: MCPTransportHTTP, URL: "https://a.example", Tools: map[string]string{"get": "read"}},
		"net":    {Transport: MCPTransportStdio, Command: "x", NetworkAllow: []string{"a.example"}, Tools: map[string]string{"get": "read"}},
	}}
	if mode == "solo" {
		r.Presets["solo"] = Preset{Mode: mode, Author: "author", Tools: tools, ReviewerTools: reviewerTools}
		return r
	}
	r.Presets["review"] = Preset{Mode: mode, Author: "author", Tools: tools, ReviewerTools: reviewerTools}
	if mode == "review" {
		p := r.Presets["review"]
		p.RequiredReviewers = []string{"reviewer"}
		r.Presets["review"] = p
	}
	return r
}

func wantErr(t *testing.T, r *Roster, want string) {
	t.Helper()
	err := r.Validate()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestPresetMCPGrants(t *testing.T) {
	cases := []struct {
		name          string
		mode          string
		tools, rtools []string
		mutate        func(*Roster)
		want          string
	}{
		{"server grant, review", "review", []string{"mcp:docs"}, nil, nil, ""},
		{"one tool, reviewer read", "review", nil, []string{"mcp:docs/search"}, nil, ""},
		{"effect in reviewer_tools by server", "review", nil, []string{"mcp:docs"}, nil, "reviewer_tools may hold only read tools"},
		{"effect in reviewer_tools by tool", "review", nil, []string{"mcp:docs/post"}, nil, "reviewer_tools may hold only read tools"},
		{"effect in solo", "solo", []string{"mcp:docs/post"}, nil, nil, "not solo"},
		{"read in solo", "solo", []string{"mcp:docs/search"}, nil, nil, ""},
		{"unknown server", "review", []string{"mcp:nope"}, nil, nil, "not in the roster"},
		{"unknown tool", "review", []string{"mcp:docs/nope"}, nil, nil, "does not allow"},
		{"no referee_model", "review", []string{"mcp:docs/search"}, nil, func(r *Roster) { r.Endpoints[0].RefereeModel = "" }, "no referee_model"},
		{"reviewer no referee_model", "review", nil, []string{"mcp:docs/search"}, func(r *Roster) { r.Endpoints[0].RefereeModel = "" }, "no referee_model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mcpPresetRoster(c.mode, c.tools, c.rtools)
			if c.mutate != nil {
				c.mutate(r)
			}
			wantErr(t, r, c.want)
		})
	}
}

func TestPresetMCPWebWithRead(t *testing.T) {
	off := func(r *Roster) {
		f := false
		r.Tools = &ToolsConfig{AllowWebWithRead: &f}
	}
	cases := []struct {
		name   string
		tools  []string
		mutate func(*Roster)
		want   string
	}{
		{"remote tool and read", []string{"read", "mcp:remote"}, off, "allow_web_with_read"},
		{"networked local tool and read", []string{"read", "mcp:net/get"}, off, "allow_web_with_read"},
		{"local no network and read", []string{"read", "mcp:docs/search"}, off, ""},
		{"remote tool, no read", []string{"mcp:remote"}, off, ""},
		{"remote tool and read, allowed", []string{"read", "mcp:remote"}, nil, ""},
		{"scratch with network", []string{"mcp:docs/search"}, func(r *Roster) {
			off(r)
			s := r.MCP.Servers["net"]
			s.Workspace = MCPWorkspaceScratch
			r.MCP.Servers["net"] = s
		}, "workspace"},
		{"scratch with network, allowed", nil, func(r *Roster) {
			s := r.MCP.Servers["net"]
			s.Workspace = MCPWorkspaceScratch
			r.MCP.Servers["net"] = s
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mcpPresetRoster("review", c.tools, nil)
			if c.mutate != nil {
				c.mutate(r)
			}
			wantErr(t, r, c.want)
		})
	}
}
