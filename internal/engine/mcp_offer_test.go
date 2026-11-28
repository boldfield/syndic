package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// Tests of which MCP tools are offered for a turn and what the model is shown
// (v9 delta, section 2, "Pins", "Definitions", "Provider names").

const mcpRosterExtra = `
[mcp.docs]
transport = "http"
url = "https://mcp.example.com/mcp"
[mcp.docs.tools]
alpha = "read"
nopin = "read"
drift = "read"
gone = "read"

[presets.mcpsolo]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["mcp:docs"]
`

func mcpToolJSON(name, desc string) string {
	return fmt.Sprintf(`{"name":%q,"description":%q,"inputSchema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}`, name, desc)
}

// mcpFakeList lists tools, the raw tool objects of one page, under revision.
func mcpFakeList(revision string, rawTools ...string) func(context.Context, string, config.MCPServer, mcp.ListOptions) (engine.MCPListed, error) {
	return func(_ context.Context, _ string, _ config.MCPServer, o mcp.ListOptions) (engine.MCPListed, error) {
		l, err := mcp.ListTools(func(string) (json.RawMessage, error) {
			return json.RawMessage(`{"tools":[` + strings.Join(rawTools, ",") + `]}`), nil
		}, o)
		return engine.MCPListed{Listing: l, Revision: revision}, err
	}
}

func mcpDef(t *testing.T, raw string) *mcp.Definition {
	t.Helper()
	d, err := mcp.BuildDefinition([]byte(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mcpPinFile writes the pin file beside the roster, pinning each raw tool, and
// returns its hash.
func mcpPinFile(t *testing.T, rosterPath string, rawTools ...string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(rosterPath), mcp.PinFileName)
	snap, _, err := mcp.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range rawTools {
		if snap, err = snap.Set("docs", mcpDef(t, raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := snap.Save(path); err != nil {
		t.Fatal(err)
	}
	_, hash, err := mcp.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// mcpHarness starts an engine whose roster has the docs server, with the pin
// file written before start. pinned are pinned; list is what the server lists.
func mcpHarness(t *testing.T, pinned []string, list func(context.Context, string, config.MCPServer, mcp.ListOptions) (engine.MCPListed, error), scripts map[string][]fake.Step) *enginetest.Harness {
	t.Helper()
	h := enginetest.New(t, refereeRoster(mcpRosterExtra), scripts, append(refereeOpts(),
		enginetest.WithMCP(engine.MCPOptions{List: list}), enginetest.NoStart())...)
	mcpPinFile(t, h.RosterPath, pinned...)
	if err := h.Engine.Start(); err != nil {
		t.Fatal(err)
	}
	return h
}

func mcpSubmit(t *testing.T, h *enginetest.Harness, id string) string {
	t.Helper()
	reply, err := h.Engine.Submit(toolReq(id, "mcpsolo", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	return reply.TurnID
}

func authorOff(t *testing.T, h *enginetest.Harness) map[string]string {
	t.Helper()
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range st.Participants {
		if p.ID == "author" {
			for _, o := range p.MCPOff {
				out[o.Server+"/"+o.Tool] = o.Reason
			}
		}
	}
	return out
}

func TestMCPOfferedOnlyWhenListedDefinitionHashesToPin(t *testing.T) {
	alpha := mcpToolJSON("alpha", "searches")
	stale := mcpToolJSON("drift", "old text")
	h := mcpHarness(t,
		[]string{alpha, stale, mcpToolJSON("gone", "x")},
		mcpFakeList(client2026, alpha, mcpToolJSON("drift", "new text"), mcpToolJSON("nopin", "p"), mcpToolJSON("secret", "not in the roster")),
		map[string][]fake.Step{"author": {enginetest.Reply(authorText)}})
	turn := mcpSubmit(t, h, "s1")
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")

	reqs := requestsOf(h, "author")
	if len(reqs) != 1 || len(reqs[0].Tools) != 1 {
		t.Fatalf("author request tools: %+v", reqs)
	}
	got := reqs[0].Tools[0].Function
	if got.Name != "mcp_0_alpha" {
		t.Fatalf("provider name %q", got.Name)
	}
	if !strings.HasPrefix(got.Description, "MCP tool alpha of server docs.\n") {
		t.Fatalf("description does not begin with the engine's line: %q", got.Description)
	}
	// The pinned bytes, fenced and labelled as content from the server.
	want := string(mcpDef(t, alpha).Bytes)
	if !strings.Contains(got.Description, "\n```\n"+want+"\n```") || !strings.Contains(got.Description, "content from the server") {
		t.Fatalf("description lacks the fenced pinned bytes %q: %q", want, got.Description)
	}

	off := authorOff(t, h)
	for tool, reason := range map[string]string{"docs/nopin": "no_pin", "docs/drift": "pin_mismatch", "docs/gone": "absent_from_listing"} {
		if off[tool] != reason {
			t.Fatalf("%s: reason %q, want %q (all: %v)", tool, off[tool], reason, off)
		}
	}
	if len(off) != 3 {
		t.Fatalf("not offered: %v", off)
	}
}

const client2026 = "2026-07-28"

func TestMCPToolNotInRosterIsNeverOfferedAndACallToItIsAToolError(t *testing.T) {
	alpha := mcpToolJSON("alpha", "searches")
	secret := mcpToolJSON("secret", "not in the roster")
	// secret is listed and pinned, but the roster does not name it.
	h := mcpHarness(t, []string{alpha, secret}, mcpFakeList(client2026, alpha, secret),
		map[string][]fake.Step{"author": {
			toolStepWith(call("c1", "mcp_1_secret", `{"q":"x"}`), call("c2", "mcp_0_secret", `{"q":"x"}`)),
			enginetest.Reply(authorText),
		}})
	turn := mcpSubmit(t, h, "s1")
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	for _, d := range requestsOf(h, "author")[0].Tools {
		if strings.Contains(d.Function.Name, "secret") {
			t.Fatalf("offered %q", d.Function.Name)
		}
	}
	calls := toolCalls(t, h, turn)
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	for _, c := range calls {
		if c.Result == nil || c.Result.Status != journal.ToolCallError || c.Result.ErrorCode != "not_offered" {
			t.Fatalf("call %s result %+v, want a not_offered tool error", c.Name, c.Result)
		}
	}
}

func TestMCPPinFileChangedDuringTurnDoesNotChangeTheTurn(t *testing.T) {
	alpha := mcpToolJSON("alpha", "searches")
	var rosterPath string
	var hash string
	list := func(ctx context.Context, s string, c config.MCPServer, o mcp.ListOptions) (engine.MCPListed, error) {
		return mcpFakeList(client2026, alpha)(ctx, s, c, o)
	}
	listAfterSubmit := false
	var mu sync.Mutex
	wrapped := func(ctx context.Context, s string, c config.MCPServer, o mcp.ListOptions) (engine.MCPListed, error) {
		mu.Lock()
		defer mu.Unlock()
		if listAfterSubmit {
			// The pin file changes after submit and before the turn lists.
			path := filepath.Join(filepath.Dir(rosterPath), mcp.PinFileName)
			if err := os.WriteFile(path, []byte(`{"servers":{}}`), 0o600); err != nil {
				t.Error(err)
			}
		}
		return list(ctx, s, c, o)
	}
	h := mcpHarness(t, []string{alpha}, wrapped, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}})
	rosterPath = h.RosterPath
	_, hash, _ = mcp.Load(filepath.Join(filepath.Dir(rosterPath), mcp.PinFileName))
	mu.Lock()
	listAfterSubmit = true
	mu.Unlock()
	turn := mcpSubmit(t, h, "s1")
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	if reqs := requestsOf(h, "author"); len(reqs) != 1 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Function.Name != "mcp_0_alpha" {
		t.Fatalf("the changed pin file changed the turn: %+v", reqs)
	}
	var recorded string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		var err error
		recorded, err = tx.TurnMCPPinFile(turn)
		return err
	})
	if err != nil || recorded != hash {
		t.Fatalf("turn records pin file hash %q (%v), want %q", recorded, err, hash)
	}
}

func TestMCPManifestNamesRevisionAndPinFileHash(t *testing.T) {
	alpha := mcpToolJSON("alpha", "searches")
	h := mcpHarness(t, []string{alpha}, mcpFakeList("2025-11-25", alpha), nil)
	run, err := h.Store.GetRun(h.Engine.Hello().RunID)
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		MCP struct {
			PinFileSHA256 string `json:"pin_file_sha256"`
			Servers       map[string]struct {
				Revision string `json:"revision"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(run.Manifest, &top); err != nil {
		t.Fatal(err)
	}
	_, hash, err := mcp.Load(filepath.Join(filepath.Dir(h.RosterPath), mcp.PinFileName))
	if err != nil {
		t.Fatal(err)
	}
	if top.MCP.PinFileSHA256 != hash || top.MCP.Servers["docs"].Revision != "2025-11-25" {
		t.Fatalf("manifest mcp member: %+v", top.MCP)
	}
}

func TestMCPListingFailureLeavesServerUnoffered(t *testing.T) {
	h := mcpHarness(t, nil, func(context.Context, string, config.MCPServer, mcp.ListOptions) (engine.MCPListed, error) {
		return engine.MCPListed{}, fmt.Errorf("boom")
	}, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}})
	off := authorOff(t, h)
	if len(off) != 4 || !strings.HasPrefix(off["docs/alpha"], "listing_failed:") {
		t.Fatalf("off = %v", off)
	}
	turn := mcpSubmit(t, h, "s1")
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	if n := len(requestsOf(h, "author")[0].Tools); n != 0 {
		t.Fatalf("%d tools offered", n)
	}
}

func TestMCPWithoutTransportIsUnoffered(t *testing.T) {
	h := mcpHarness(t, nil, nil, nil)
	if off := authorOff(t, h); off["docs/alpha"] != "no_transport" {
		t.Fatalf("off = %v", off)
	}
}
