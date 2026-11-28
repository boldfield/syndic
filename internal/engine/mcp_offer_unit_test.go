package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/mcp"
)

// An annotation that disagrees with the operator's class warns and the class
// stands (v9 delta, section 2, "Classes").
func TestMCPAnnotationDisagreementWarnsAndClassStands(t *testing.T) {
	roster := &config.Roster{MCP: &config.MCPConfig{Servers: map[string]config.MCPServer{
		"docs": {Transport: config.MCPTransportHTTP, Tools: map[string]string{
			"reads": config.MCPClassRead, "acts": config.MCPClassEffect, "agrees": config.MCPClassRead,
		}},
	}}}
	defs := map[string]string{
		"reads":  `{"name":"reads","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false}}`,
		"acts":   `{"name":"acts","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`,
		"agrees": `{"name":"agrees","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`,
	}
	snap, _, err := mcp.Load(t.TempDir() + "/none.json")
	if err != nil {
		t.Fatal(err)
	}
	var raws []string
	for _, raw := range defs {
		d, err := mcp.BuildDefinition([]byte(raw), 0)
		if err != nil {
			t.Fatal(err)
		}
		if snap, err = snap.Set("docs", d); err != nil {
			t.Fatal(err)
		}
		raws = append(raws, raw)
	}
	listing, err := mcp.ListTools(func(string) (json.RawMessage, error) {
		return json.RawMessage(`{"tools":[` + strings.Join(raws, ",") + `]}`), nil
	}, mcp.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]mcpListResult{"docs": {listed: MCPListed{Listing: listing, Revision: "2026-07-28"}}}
	refs := allMCPTools(roster)
	mt := buildMCPTurn(roster, snap, "h", nil, results, refs, nil)
	if len(mt.warnings) != 2 {
		t.Fatalf("warnings = %q, want one for reads and one for acts", mt.warnings)
	}
	for tool, class := range map[string]string{"reads": config.MCPClassRead, "acts": config.MCPClassEffect, "agrees": config.MCPClassRead} {
		o, ok := mt.offered[mcp.Tool{Server: "docs", Tool: tool}]
		if !ok || o.class != class {
			t.Fatalf("%s: offered %v class %q, want offered under %q", tool, ok, o.class, class)
		}
	}
}
