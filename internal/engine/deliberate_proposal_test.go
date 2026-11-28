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
	"github.com/boldfield/syndic/internal/wire"
)

func submitDeliberate(t *testing.T, h *enginetest.Harness) string {
	t.Helper()
	name := "deliberate"
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?", Preset: &name})
	if err != nil {
		t.Fatal(err)
	}
	return reply.TurnID
}

// eventCount counts the journal's events of a kind; the deliberation events
// are written to the journal and not published on the hub.
func eventCount(h *enginetest.Harness, kind string) int {
	events, err := h.Store.EventsSince(0, 100000)
	if err != nil {
		h.T.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func phaseAttempts(t *testing.T, h *enginetest.Harness, turnID, phase string) []journal.AttemptRow {
	t.Helper()
	rows, err := h.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.AttemptRow
	for _, r := range rows {
		if r.Phase == phase {
			out = append(out, r)
		}
	}
	return out
}

// Every proposal completes on its first reply, which carries no tool call: no
// verdict parser or repair runs on it, whatever its text holds, and it is
// recorded as the proposal with its numbered label. The turn then reaches the
// critique phase, which no task has built.
func TestDeliberateProposalsCompleteOnFirstReply(t *testing.T) {
	// A proposal that looks like a verdict envelope is still just text.
	verdictLike := "=== SYNDIC VERDICT ===\n{not a verdict}"
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply("first proposal")},
		"reviewer": {enginetest.Reply(verdictLike)},
	})
	turnID := submitDeliberate(t, h)
	wantEnd(t, h, turnID, engine.StateIncomplete, "deliberate_phase_unsupported:critique")

	items, _, err := h.Store.DeliberationItems(turnID, "", 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v, err %v", items, err)
	}
	for i, want := range []struct{ who, label string }{{"author", "proposal 1"}, {"reviewer", "proposal 2"}} {
		if items[i].Kind != journal.DeliberationProposal || items[i].Participant != want.who || items[i].Label != want.label || items[i].RosterPosition != i {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want)
		}
	}
	attempts := phaseAttempts(t, h, turnID, "proposal")
	if len(attempts) != 2 {
		t.Fatalf("proposal attempts = %d", len(attempts))
	}
	for _, a := range attempts {
		if a.RepairOf != nil {
			t.Fatalf("attempt %s is a repair", a.ID)
		}
	}
	if n := len(h.Adapters["author"].Calls()) + len(h.Adapters["reviewer"].Calls()); n != 2 {
		t.Fatalf("%d calls, want one per proposer", n)
	}
	for id, a := range h.Adapters {
		for _, c := range a.Calls() {
			if len(c.Request.Tools) != 0 {
				t.Fatalf("%s's proposal offered tools", id)
			}
			if c.Request.MaxOutputTokens != 4096 {
				t.Fatalf("%s's output bound = %d, want proposer_max_output_tokens", id, c.Request.MaxOutputTokens)
			}
		}
	}
	if eventCount(h, journal.EventProposalsDispatched) != 1 || eventCount(h, journal.EventProposalsComplete) != 1 {
		t.Fatalf("events: dispatched %d, complete %d", eventCount(h, journal.EventProposalsDispatched), eventCount(h, journal.EventProposalsComplete))
	}
	if eventCount(h, journal.EventCritiquesDispatched) != 0 {
		t.Fatal("a critique was dispatched")
	}
}

// A failed, incomplete or late proposal ends the turn incomplete with
// proposal_failed:<id>, and no critique is dispatched.
func TestDeliberateProposalFailureEndsTurn(t *testing.T) {
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
				"author":   {enginetest.Reply("fine")},
				"reviewer": {step},
			})
			turnID := submitDeliberate(t, h)
			wantEnd(t, h, turnID, engine.StateIncomplete, "proposal_failed:reviewer")
			if eventCount(h, journal.EventProposalsComplete) != 0 || eventCount(h, journal.EventCritiquesDispatched) != 0 {
				t.Fatal("the turn went on after a failed proposal")
			}
			if n := len(phaseAttempts(t, h, turnID, "critique")); n != 0 {
				t.Fatalf("%d critique attempts", n)
			}
		})
	}
}

func TestDeliberateLateProposalEndsTurn(t *testing.T) {
	h := enginetest.New(t, deliberateRoster(""), map[string][]fake.Step{
		"author":   {enginetest.Reply("fine")},
		"reviewer": {{Kind: fake.StepKindHang}},
	})
	turnID := submitDeliberate(t, h)
	for deadline := time.Now().Add(10 * time.Second); len(h.Adapters["reviewer"].Calls()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the reviewer was never called")
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.Clock.Advance(24 * time.Hour)
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, "proposal_failed:") {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	if eventCount(h, journal.EventCritiquesDispatched) != 0 {
		t.Fatal("a critique was dispatched")
	}
}
