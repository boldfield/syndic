package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

func lastUser(c fake.CompleteCall) string {
	msgs := c.Request.Messages
	return msgs[len(msgs)-1].Content
}

// Each proposer critiques once, carrying every proposal verbatim, its own as
// "your proposal"; critiques are recorded with numbered labels and
// critique_max_output_tokens bounds them. The turn then reaches integration,
// which no task has built.
func TestDeliberateCritiquesCompleteOnFirstReply(t *testing.T) {
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply("author proposal"), enginetest.Reply("author critique")},
		"reviewer": {enginetest.Reply("reviewer proposal"), enginetest.Reply("reviewer critique")},
	})
	turnID := submitDeliberate(t, h)
	wantEnd(t, h, turnID, engine.StateIncomplete, "deliberate_phase_unsupported:integrate")

	items, _, err := h.Store.DeliberationItems(turnID, "", 10)
	if err != nil || len(items) != 4 {
		t.Fatalf("items = %+v, err %v", items, err)
	}
	var crit []journal.DeliberationItem
	for _, it := range items {
		if it.Kind == journal.DeliberationCritique {
			crit = append(crit, it)
		}
	}
	if len(crit) != 2 || crit[0].Participant != "author" || crit[0].Label != "critique 1" || crit[1].Participant != "reviewer" || crit[1].Label != "critique 2" {
		t.Fatalf("critiques = %+v", crit)
	}
	for id, a := range h.Adapters {
		calls := a.Calls()
		if len(calls) != 2 {
			t.Fatalf("%s: %d calls", id, len(calls))
		}
		c := calls[1]
		if len(c.Request.Tools) != 0 || c.Request.MaxOutputTokens != 2048 {
			t.Fatalf("%s's critique: tools %d, bound %d", id, len(c.Request.Tools), c.Request.MaxOutputTokens)
		}
		u := lastUser(c)
		if !strings.Contains(u, "author proposal") || !strings.Contains(u, "reviewer proposal") || strings.Contains(u, "author critique") {
			t.Fatalf("%s's critique prompt: %s", id, u)
		}
		own := "YOUR PROPOSAL"
		if !strings.Contains(u, own) {
			t.Fatalf("%s's prompt lacks %q", id, own)
		}
	}
	if eventCount(h, journal.EventCritiquesDispatched) != 1 || eventCount(h, journal.EventCritiquesComplete) != 1 {
		t.Fatal("critique events not journaled once")
	}
	if n := len(phaseAttempts(t, h, turnID, "critique")); n != 2 {
		t.Fatalf("%d critique attempts", n)
	}
}

// A failed, incomplete or late critique ends the turn incomplete with
// critique_failed:<id>, and no integration is dispatched.
func TestDeliberateCritiqueFailureEndsTurn(t *testing.T) {
	truncated := enginetest.Reply("cut off")
	truncated.Status = transport.StatusTruncated
	cases := map[string]fake.Step{
		"gate":       {Kind: fake.StepKindReply, Text: "x", Status: transport.StatusComplete, TransportStatus: "refused"},
		"incomplete": truncated,
		"tool call":  toolStep(),
	}
	for name, step := range cases {
		t.Run(name, func(t *testing.T) {
			h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
				"author":   {enginetest.Reply("p1"), enginetest.Reply("c1")},
				"reviewer": {enginetest.Reply("p2"), step},
			})
			turnID := submitDeliberate(t, h)
			wantEnd(t, h, turnID, engine.StateIncomplete, "critique_failed:reviewer")
			if eventCount(h, journal.EventCritiquesComplete) != 0 {
				t.Fatal("critiques completed")
			}
		})
	}
}

func TestDeliberateLateCritiqueEndsTurn(t *testing.T) {
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply("p1"), enginetest.Reply("c1")},
		"reviewer": {enginetest.Reply("p2"), {Kind: fake.StepKindHang}},
	})
	turnID := submitDeliberate(t, h)
	for deadline := time.Now().Add(10 * time.Second); len(h.Adapters["reviewer"].Calls()) < 2; {
		if time.Now().After(deadline) {
			t.Fatal("the reviewer was never asked to critique")
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.Clock.Advance(24 * time.Hour)
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "critique_failed:reviewer" {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	if eventCount(h, journal.EventCritiquesComplete) != 0 {
		t.Fatal("critiques completed")
	}
}

// Proposals longer than the projection's stand-ins make the critique prompt
// not fit its recipient: the turn ends budget:context:<participant>.
func TestDeliberateCritiqueOverContextEndsTurn(t *testing.T) {
	huge := strings.Repeat("a", 2000000)
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply(huge)},
		"reviewer": {enginetest.Reply("p2")},
	})
	turnID := submitDeliberate(t, h)
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:context:author")
	wantCritiqueDispatchRefused(t, h)
}

// The last proposal reports more usage than the turn total, so both proposals
// are accepted and the critique reservation that follows would pass the total:
// the turn ends budget:turn although the prompt fits its recipient.
func TestDeliberateCritiqueOverTurnTotalEndsTurn(t *testing.T) {
	heavy := enginetest.Reply("p2")
	heavy.Usage = &transport.Usage{Prompt: 10, Completion: 300000}
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply("p1"), enginetest.Reply("c1")},
		"reviewer": {heavy, enginetest.Reply("c2")},
	})
	turnID := submitDeliberate(t, h)
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:turn")
	wantCritiqueDispatchRefused(t, h)
}

// wantCritiqueDispatchRefused checks that the proposal phase completed, the
// critique dispatch was journaled, and no critique reached an adapter.
func wantCritiqueDispatchRefused(t *testing.T, h *enginetest.Harness) {
	t.Helper()
	if eventCount(h, journal.EventProposalsComplete) != 1 || eventCount(h, journal.EventCritiquesDispatched) != 1 {
		t.Fatal("the refusal did not come from the critique dispatch")
	}
	if eventCount(h, journal.EventCritiquesComplete) != 0 {
		t.Fatal("critiques completed")
	}
	for id, a := range h.Adapters {
		if n := len(a.Calls()); n != 1 {
			t.Fatalf("%s: %d calls, want only its proposal", id, n)
		}
	}
}
