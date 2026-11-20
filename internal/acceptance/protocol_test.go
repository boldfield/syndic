package acceptance

import (
	"context"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func init() {
	cover("Protocol", "approve path reaches approved with the candidate hash in the answer", "TestProtocol_ApproveWithHash")
	cover("Protocol", "VerdictProseApprove yields repair then INCOMPLETE malformed_twice", "TestProtocol_ProseApproveTwice")
	cover("Protocol", "VerdictWrongHash yields rejected and INCOMPLETE with no repair", "TestProtocol_WrongHash")
	cover("Protocol", "VerdictApproveWithChanges yields revision_request, INCOMPLETE, no repair", "TestProtocol_ApproveWithChanges")
	cover("Protocol", "VerdictTwoEnvelopes is malformed", "TestProtocol_TwoEnvelopes")
	cover("Protocol", "VerdictBlockWithEndMarker parses to the first delimiter and the block opens", "TestProtocol_BlockWithEndMarker")
	cover("Protocol", "a block ends INCOMPLETE with the block open", "TestProtocol_BlockOpensIncomplete")
	cover("Protocol", "an author result with status truncated yields author_failed:truncated and nothing is dispatched to reviewers", "TestProtocol_AuthorTruncated")
	cover("Protocol", "a reviewer result with mismatched ReportedModel fails CheckResult and is never parsed", "TestProtocol_ReviewerModelMismatch")
	cover("Protocol", "solo with status refused is incomplete, not solo_complete", "TestProtocol_SoloRefused")
}

// TestProtocol_ApproveWithHash tests that an approve verdict reaches the approved
// state with the candidate hash in the answer.
func TestProtocol_ApproveWithHash(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				// Verdict approve with the actual candidate hash and attempt ID from the request
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-submit-1",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "approved")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "approved" {
		t.Fatalf("expected approved state, got %v", states)
	}
}

func ptrString(s string) *string { return &s }

// TestProtocol_ProseApproveTwice tests that a prose approve yields repair
// and then a second malformed verdict yields incomplete malformed_twice.
func TestProtocol_ProseApproveTwice(t *testing.T) {
	callCount := 0
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				callCount++
				if callCount == 1 {
					return fake.VerdictProseApprove()
				}
				return "invalid json"
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-prose-twice",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_WrongHash tests that a wrong-hash verdict yields rejected
// and incomplete with no repair.
func TestProtocol_WrongHash(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				// Return a verdict with a different candidate hash
				return fake.VerdictWrongHash(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-wrong-hash",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_ApproveWithChanges tests that an approve-with-changes verdict
// yields revision_request and incomplete.
func TestProtocol_ApproveWithChanges(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApproveWithChanges(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-changes",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_TwoEnvelopes tests that a two-envelope verdict is malformed.
func TestProtocol_TwoEnvelopes(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictTwoEnvelopes(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-two-envelopes",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_BlockWithEndMarker tests that a block with an end marker
// parses to the first delimiter and opens a block.
func TestProtocol_BlockWithEndMarker(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictBlockWithEndMarker(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-block-end-marker",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_BlockOpensIncomplete tests that a block ends a turn incomplete
// with the block still open.
func TestProtocol_BlockOpensIncomplete(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "test", "fix it", "blocked")
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-block-opens",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_AuthorTruncated tests that a truncated author result yields
// author_failed:truncated and nothing is dispatched to reviewers.
func TestProtocol_AuthorTruncated(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "truncated answer",
				Status:          transport.StatusTruncated,
				TransportStatus: transport.TransportStatusOK,
			},
		},
		"reviewer": {}, // Should not be called
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-truncated",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_ReviewerModelMismatch tests that a reviewer result with
// mismatched ReportedModel fails CheckResult and is never parsed.
func TestProtocol_ReviewerModelMismatch(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            fake.VerdictApprove("hash", "attempt"),
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusOK,
				ReportedModel:   "different-model", // Mismatch
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-model-mismatch",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestProtocol_SoloRefused tests that solo mode with a refused status yields
// incomplete rather than solo_complete.
func TestProtocol_SoloRefused(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "refused",
				Status:          transport.StatusRefused,
				TransportStatus: transport.TransportStatusOK,
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-solo-refused",
		Input:        "input",
		Preset:       ptrString("solo"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}
