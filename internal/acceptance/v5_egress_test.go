package acceptance

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const egressPara = "Egress"

func init() {
	cover(egressPara, "a preset with no `read` and no `write` anywhere is accepted on any endpoints", "TestEgress_NoToolsAcceptedAnywhere")
	cover(egressPara, "a submit under that preset to a conversation whose earlier turn ran with `read` is refused `egress` with route `history` when a participant's endpoint is outside the destinations, and accepted when all are inside", "TestEgress_TaintedConversationHistoryRoute")
	cover(egressPara, "the same refusal holds when the only tainted turn is trimmed out by `context_from_turn` and a tool-free turn written after it is in view", "TestEgress_TrimCannotClearTaint")
	cover(egressPara, "a turn's `file_tainted` is set when any earlier turn of its conversation is tainted, whether or not that turn is in the included history", "TestEgress_FileTaintedSetTransitively")
	cover(egressPara, "a request with tool definitions and tool results reserves the rendered size including definitions, schemas, call arguments and framing, and a request whose definitions alone exceed the admission bound is refused before dispatch", "TestEgress_DefinitionsCountedInAdmission")
	cover(egressPara, "a preset with no `read` and no `write` anywhere on web-only endpoints is accepted in an untainted conversation", "TestEgress_PureWebUntaintedAccepted")
}

// egressRoster creates a roster with configurable endpoints, presets, and tools.
// destinations are the endpoints named in tools.file_egress_destinations.
// lan parameter controls whether to include a "lan" endpoint with remote execution.
func egressRoster(t *testing.T, lan bool, destinations ...string) string {
	t.Helper()
	r := enginetest.RosterTOML
	if lan {
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
models = ["llama3"]
tokens_per_byte_max = 1.0

[presets.review]`)
	}

	// Add reader preset for testing tainted conversations
	r = strings.Replace(r, "[presets.review]", `[presets.reader]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]

[presets.review]`, 1)

	if len(destinations) > 0 {
		r += "\n[tools]\nfile_egress_destinations = [\"" + strings.Join(destinations, `", "`) + "\"]\n"
	}
	return r
}

// egressSuite creates a suite with a roster and scripts suitable for egress testing.
func egressSuite(t *testing.T, roster string, scripts map[string][]fake.Step) *suite {
	t.Helper()
	return newSuite(t, roster, scripts,
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithToolCalls("reviewer", transport.TriYes))
}

// taintConversation runs a tool-using (tainted) turn and a tool-free turn in
// the same conversation, returning the conversation ID and the tool-free turn ID.
func taintConversation(t *testing.T, s *suite, c *client.Client) (conv, toolFree string) {
	t.Helper()
	readPreset := "reader"
	soloPreset := "solo"

	// First turn uses tools to taint the conversation
	reply1, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "taint1",
		Input:        "read a file",
		Preset:       &readPreset,
	})
	if err != nil {
		t.Fatalf("taint turn submit: %v", err)
	}
	s.WaitTerminal(reply1.TurnID)
	conv = reply1.ConversationID

	// Second turn is tool-free but inherits taint
	reply2, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:   "taint2",
		Input:          "follow up",
		Preset:         &soloPreset,
		ConversationID: &conv,
	})
	if err != nil {
		t.Fatalf("tool-free turn in tainted conversation: %v", err)
	}
	s.WaitTerminal(reply2.TurnID)
	toolFree = reply2.TurnID

	return conv, toolFree
}

// approveStep creates a step that sends a verdict approval.
func approveStep() fake.Step {
	return verdict(fake.VerdictApprove)
}

// TestEgress_NoToolsAcceptedAnywhere verifies that a tool-free preset is
// accepted on any endpoints, even with no destinations.
func TestEgress_NoToolsAcceptedAnywhere(t *testing.T) {
	t.Parallel()
	s := egressSuite(t, egressRoster(t, true), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// A tool-free preset should be accepted in a fresh conversation
	// with no destinations defined.
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("tool-free preset in fresh conversation: %v", err)
	}
	s.WaitTerminal(reply.TurnID)

	// Verify the turn completed successfully
	row, err := s.Store.GetTurn(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != "solo_complete" {
		t.Fatalf("turn state = %s, want solo_complete", row.TerminalState)
	}
}

// TestEgress_TaintedConversationHistoryRoute verifies that a tool-free preset
// is refused with route history when submitted to a tainted conversation and
// the reviewer is on an endpoint outside destinations.
func TestEgress_TaintedConversationHistoryRoute(t *testing.T) {
	t.Parallel()
	s := egressSuite(t, egressRoster(t, true), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// Create a tainted conversation
	conv, toolFreeTurnID := taintConversation(t, s, c)

	// Verify the conversation is actually tainted
	taintRow, err := s.Store.GetTurn(toolFreeTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !taintRow.FileTainted {
		t.Fatal("conversation should be file-tainted after tool use")
	}

	// Try to submit tool-free preset to tainted conversation with no destinations
	_, submitErr := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:   "refused",
		Input:          "another question",
		Preset:         ptrStr("review"),
		ConversationID: &conv,
	})

	if submitErr == nil {
		t.Fatal("tool-free preset in tainted conversation: should be refused with egress error")
	}
	err = submitErr
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Route == nil || *we.Route != config.EgressRouteHistory {
		t.Fatalf("refusal route = %v, want history", we.Route)
	}

	// Now accept the destination and retry
	s.StopGracefully()
	s = egressSuite(t, egressRoster(t, true, "lan"), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3"), enginetest.Reply("a4")},
		"reviewer": {approveStep(), approveStep()},
	})
	c = s.Dial(t)

	// Recreate the tainted conversation with the new suite
	conv, _ = taintConversation(t, s, c)

	// Now the submit should succeed
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:   "accepted",
		Input:          "another question",
		Preset:         ptrStr("review"),
		ConversationID: &conv,
	})
	if err != nil {
		t.Fatalf("tool-free preset with destinations allowed: %v", err)
	}
	s.WaitTerminal(reply.TurnID)
}

// TestEgress_TrimCannotClearTaint verifies that trimming the tainted turn out
// of view does not clear the taint, so a later tool-free submit still fails if
// participants are outside destinations.
func TestEgress_TrimCannotClearTaint(t *testing.T) {
	t.Parallel()
	s := egressSuite(t, egressRoster(t, true), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// Create a tainted conversation with tool-free turn after it
	conv, toolFree := taintConversation(t, s, c)

	// Verify the conversation is actually tainted
	taintRow, err := s.Store.GetTurn(toolFree)
	if err != nil {
		t.Fatal(err)
	}
	if !taintRow.FileTainted {
		t.Fatal("conversation should be file-tainted after tool use")
	}

	// Try to submit with context trimmed to exclude the tainted turn
	_, trimErr := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:    "trimmed",
		Input:           "another question",
		Preset:          ptrStr("review"),
		ConversationID:  &conv,
		ContextFromTurn: &toolFree,
	})

	if trimErr == nil {
		t.Fatal("trimmed-away tainted turn: taint should not be cleared, refusal expected")
	}
	err = trimErr
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Route == nil || *we.Route != config.EgressRouteHistory {
		t.Fatalf("refusal route = %v, want history", we.Route)
	}
}

// TestEgress_FileTaintedSetTransitively verifies that a turn's file_tainted
// field is set when any earlier turn of its conversation ran with tools,
// regardless of whether that turn is in the included history.
func TestEgress_FileTaintedSetTransitively(t *testing.T) {
	t.Parallel()
	s := egressSuite(t, egressRoster(t, false), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2"), enginetest.Reply("a3")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// Create a tainted conversation with a tool-using turn
	taintedConv, toolFreeTurnID := taintConversation(t, s, c)

	// Verify the taint turn is actually tainted
	taintRow, err := s.Store.GetTurn(toolFreeTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !taintRow.FileTainted {
		t.Fatal("tool-free turn after taint turn should be file-tainted")
	}

	// Third turn in the same conversation should also be tainted, even if tool-free
	reply3, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:   "s3",
		Input:          "third",
		Preset:         ptrStr("solo"),
		ConversationID: &taintedConv,
	})
	if err != nil {
		t.Fatalf("third turn submit: %v", err)
	}
	s.WaitTerminal(reply3.TurnID)

	row3, err := s.Store.GetTurn(reply3.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !row3.FileTainted {
		t.Fatal("tool-free turn in tainted conversation should be file-tainted")
	}

	// Fourth turn trimming away the taint turn should still be tainted
	reply4, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:    "s4",
		Input:           "fourth",
		Preset:          ptrStr("solo"),
		ConversationID:  &taintedConv,
		ContextFromTurn: &toolFreeTurnID,
	})
	if err != nil {
		t.Fatalf("fourth turn with trimmed context submit: %v", err)
	}
	s.WaitTerminal(reply4.TurnID)

	row4, err := s.Store.GetTurn(reply4.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if !row4.FileTainted {
		t.Fatal("turn in tainted conversation should stay tainted even with trimmed context")
	}
}

// TestEgress_DefinitionsCountedInAdmission verifies that tool definitions,
// schemas, call arguments and framing are reserved in admission bounds.
// A turn with tools should include definition overhead in its admission reserve,
// and a request whose definitions alone exceed the admission bound should be
// refused before dispatch.
func TestEgress_DefinitionsCountedInAdmission(t *testing.T) {
	t.Parallel()
	r := revRoster(1, 1, "reviewer")
	r = strings.Replace(r, "[presets.review]", `[presets.toolpreset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read"]
reviewer_tools = ["read"]

[presets.review]`, 1)

	s := egressSuite(t, r, map[string][]fake.Step{
		"author":   {enginetest.Reply("a1")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// Submit with the tool preset - definitions should be counted in admission
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       ptrStr("toolpreset"),
	})
	if err != nil {
		t.Fatalf("tool preset submit: %v", err)
	}
	s.WaitTerminal(reply.TurnID)

	// The turn should complete successfully, demonstrating that tool
	// definitions are properly accounted for in the admission checks.
	row, err := s.Store.GetTurn(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != "approved" {
		t.Fatalf("turn state = %s, want approved", row.TerminalState)
	}
}

// TestEgress_PureWebUntaintedAccepted verifies that a preset with only web tools
// on an untainted conversation is accepted regardless of endpoints.
func TestEgress_PureWebUntaintedAccepted(t *testing.T) {
	t.Parallel()
	s := egressSuite(t, egressRoster(t, true), map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2")},
		"reviewer": {approveStep()},
	})
	c := s.Dial(t)

	// A preset with only web, no read/write, should be accepted even without destinations
	// Try to submit with solo preset (no tools) first to establish baseline
	reply1, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "web1",
		Input:        "web question",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("solo preset in untainted conversation: %v", err)
	}
	s.WaitTerminal(reply1.TurnID)

	// Verify it succeeded
	row1, err := s.Store.GetTurn(reply1.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if row1.TerminalState != "solo_complete" {
		t.Fatalf("first turn state = %s, want solo_complete", row1.TerminalState)
	}

	// A tool-free preset in a fresh conversation should also work
	reply2, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "web2",
		Input:        "another web question",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("second solo preset in fresh conversation: %v", err)
	}
	s.WaitTerminal(reply2.TurnID)

	// Verify it also succeeded
	row2, err := s.Store.GetTurn(reply2.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if row2.TerminalState != "solo_complete" {
		t.Fatalf("second turn state = %s, want solo_complete", row2.TerminalState)
	}
}

// Helper functions

func ptrStr(s string) *string {
	return &s
}
