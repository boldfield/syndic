package engine_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func reviewerToolCall(name string, argsJSON string) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            "",
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls: []transport.ToolCall{
			{
				ID:       "tc1",
				Type:     "function",
				Function: transport.ToolCallFunction{Name: name, Arguments: argsJSON},
			},
		},
	}
}

// TestReviewerToolCallContinuation verifies a reviewer's tool call and
// result continuation: tool call → result → next attempt → verdict.
func TestReviewerToolCallContinuation(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	roster := withPresetTools(t, enginetest.RosterTOML, "review", nil, []string{tools.GroupRead})
	h := enginetest.New(t, roster,
		map[string][]fake.Step{
			"author": {
				enginetest.Reply(authorText),
			},
			"reviewer": {
				reviewerToolCall(tools.NameReadFile, `{"path":"main.go"}`),
				enginetest.Reply("APPROVE\n"),
			},
		},
		enginetest.WithToolCalls("reviewer", transport.TriYes),
	)
	sr, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        testInput,
		Workspace:    "/work",
		Preset:       ptrStr("review"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wantEnd(t, h, sr.TurnID, engine.StateApproved, "all_approved")
}

// TestReviewerMalformedVerdictRepairCarriesOwnResults verifies that repair
// after a malformed verdict carries the reviewer's own tool results and
// no tool definitions.
func TestReviewerMalformedVerdictRepairCarriesOwnResults(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	roster := withPresetTools(t, enginetest.RosterTOML, "review", nil, []string{tools.GroupRead})
	h := enginetest.New(t, roster,
		map[string][]fake.Step{
			"author": {enginetest.Reply(authorText)},
			"reviewer": {
				reviewerToolCall(tools.NameReadFile, `{"path":"main.go"}`),
				enginetest.Reply("malformed verdict\n"),
				enginetest.Reply("APPROVE\n"), // repair attempt
			},
		},
		enginetest.WithToolCalls("reviewer", transport.TriYes),
	)
	sr, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        testInput,
		Workspace:    "/work",
		Preset:       ptrStr("review"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wantEnd(t, h, sr.TurnID, engine.StateApproved, "all_approved")
}
