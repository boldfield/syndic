package engine_test

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// egressRoster is RosterTOML with the reviewer moved to a LAN endpoint with
// remote execution, a solo preset whose author reads, and a review preset
// whose reviewer has only web and turns web with file content off. destinations
// names the endpoints in tools.file_egress_destinations.
func egressRoster(t *testing.T, destinations ...string) string {
	t.Helper()
	r := enginetest.RosterTOML
	edit := func(old, new string) {
		t.Helper()
		if !strings.Contains(r, old) {
			t.Fatalf("roster has no %q", old)
		}
		r = strings.Replace(r, old, new, 1)
	}
	edit("id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"", "id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"lan\"")
	edit("local_only = true", "local_only = false")
	edit("[presets.review]", `[[endpoints]]
name = "lan"
base_url = "http://192.168.1.20:11434/v1"
declared_location = "lan"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3", "refmodel"]
referee_model = "refmodel"
tokens_per_byte_max = 1.0

[presets.reader]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]

[presets.webreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
reviewer_tools = ["web"]
allow_web_with_read = false

[presets.review]`)
	if len(destinations) > 0 {
		r += "\n[tools]\nfile_egress_destinations = [\"" + strings.Join(destinations, `", "`) + "\"]\n"
	}
	return r
}

func newEgressHarness(t *testing.T, roster string, scripts map[string][]fake.Step) *enginetest.Harness {
	t.Helper()
	return enginetest.New(t, roster, scripts,
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithToolCalls("reviewer", transport.TriYes),
		enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes))
}

func presetIn(id, preset string, conv *string) wire.SubmitRequest {
	return wire.SubmitRequest{SubmissionID: id, Input: "q " + id, Preset: &preset, ConversationID: conv}
}

// taintedConversation runs a turn under the reading preset and then a
// tool-free solo turn after it, and returns the conversation and the
// tool-free turn.
func taintedConversation(t *testing.T, h *enginetest.Harness) (conv, toolFree string) {
	t.Helper()
	read := submitSolo(t, h, presetIn("read", "reader", nil))
	if !tainted(t, h, read.TurnID) {
		t.Fatal("a turn that ran with read is not tainted")
	}
	conv = read.ConversationID
	later := submitSolo(t, h, presetIn("later", "solo", &conv))
	if !tainted(t, h, later.TurnID) {
		t.Fatal("a tool-free turn after a tainted one is not tainted")
	}
	return conv, later.TurnID
}

func wantEgress(t *testing.T, err error, participant, route string) {
	t.Helper()
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Participant == nil || *we.Participant != participant || we.Route == nil || *we.Route != route {
		t.Fatalf("egress refusal = %+v, want participant %q route %q", we, participant, route)
	}
}

// A tool-free preset is accepted on any endpoint in an untainted
// conversation, and refused with route history, naming the participant
// outside the destinations, once the conversation is tainted, also when the
// tainted turn is trimmed out of view. The refusal writes nothing.
func TestEgressHistoryRouteRefusesTaintedConversation(t *testing.T) {
	h := newEgressHarness(t, egressRoster(t), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	conv, toolFree := taintedConversation(t, h)

	_, err := h.Engine.Submit(presetIn("whole", "review", &conv))
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)

	trim := presetIn("trim", "review", &conv)
	trim.ContextFromTurn = &toolFree
	_, err = h.Engine.Submit(trim)
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)
	if n := turnCount(t, h, conv); n != 2 {
		t.Fatalf("turns = %d after refusals, want 2", n)
	}
	for _, id := range []string{"whole", "trim"} {
		if _, found, err := h.Store.FindSubmission(id); err != nil || found {
			t.Fatalf("refused submission %s recorded: %v, %v", id, found, err)
		}
	}

	fresh, err := h.Engine.Submit(presetIn("fresh", "review", nil))
	if err != nil {
		t.Fatalf("tool-free preset in a fresh conversation: %v", err)
	}
	wantEnd(t, h, fresh.TurnID, engine.StateApproved, "all_approved")
	if tainted(t, h, fresh.TurnID) {
		t.Fatal("a tool-free turn in a fresh conversation is tainted")
	}
}

// With the reviewer's endpoint among the destinations, the same submit to the
// tainted conversation is accepted, and the turn carries the taint forward.
func TestEgressHistoryRouteAcceptsDestinations(t *testing.T) {
	h := newEgressHarness(t, egressRoster(t, "lan"), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	conv, toolFree := taintedConversation(t, h)
	trim := presetIn("trim", "review", &conv)
	trim.ContextFromTurn = &toolFree
	reply, err := h.Engine.Submit(trim)
	if err != nil {
		t.Fatalf("submit with every participant inside the destinations: %v", err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	if !tainted(t, h, reply.TurnID) {
		t.Fatal("trimming laundered the taint")
	}
}

// With web and file content turned off for the preset, its web-only reviewer
// is accepted in an untainted conversation and refused with route history in
// a tainted one, though the preset grants no read and every endpoint is an
// allowed destination.
func TestEgressWebWithFileOffRefusesTaintedConversation(t *testing.T) {
	h := newEgressHarness(t, egressRoster(t, "lan"), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	conv, toolFree := taintedConversation(t, h)

	trim := presetIn("web-tainted", "webreview", &conv)
	trim.ContextFromTurn = &toolFree
	_, err := h.Engine.Submit(trim)
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)

	reply, err := h.Engine.Submit(presetIn("web-fresh", "webreview", nil))
	if err != nil {
		t.Fatalf("web-only preset in an untainted conversation: %v", err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	if tainted(t, h, reply.TurnID) {
		t.Fatal("a web-only turn is tainted")
	}
}
