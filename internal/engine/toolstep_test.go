package engine_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const toolStepText = "calling a tool"

func toolStep() fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            toolStepText,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls: []transport.ToolCall{{
			ID: "call_1", Type: "function",
			Function: transport.ToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`},
		}},
	}
}

func attemptText(t *testing.T, h *enginetest.Harness, turnID, attemptID string) string {
	t.Helper()
	texts, err := h.Store.AttemptTexts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return texts[attemptID]
}

func TestSoloToolStepEndsUnsupported(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {toolStep()}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "tool_step:unsupported")
	if got := attemptText(t, h, reply.TurnID, attemptsOf(h, reply.TurnID, "author")[0]); got != toolStepText {
		t.Fatalf("journaled text = %q", got)
	}
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewAuthorToolStepFreezesNothing(t *testing.T) {
	scripts := reviewScripts(approveStep())
	scripts["author"] = []fake.Step{toolStep()}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "tool_step:unsupported")
	if got := attemptText(t, h, turnID, attemptsOf(h, turnID, "author")[0]); got != toolStepText {
		t.Fatalf("journaled text = %q", got)
	}
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
	if n := len(attemptsOf(h, turnID, "reviewer")); n != 0 {
		t.Fatalf("%d reviewer attempts begun", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewerToolStepIsNotParsed(t *testing.T) {
	// The tool step's text is a well-formed approve verdict; it must not count.
	step := toolStep()
	step.Build = func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	}
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(step))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "tool_step:unsupported")
	if rows := verdictRows(t, h, turnID); len(rows) != 0 {
		t.Fatalf("verdicts = %+v", rows)
	}
	if got := attemptText(t, h, turnID, attemptsOf(h, turnID, "reviewer")[0]); got == "" {
		t.Fatal("reviewer tool-step text not journaled")
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}
