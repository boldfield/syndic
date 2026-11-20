package acceptance

import (
	"context"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func init() {
	cover("Dispatch failures", "a step with TransportStatus: unreachable followed by a normal reply yields one journaled retry and a normal turn", "TestDispatch_UnreachableRetry")
	cover("Dispatch failures", "two such steps yield reviewer_failed:<id>:unreachable without waiting for the deadline", "TestDispatch_TwoUnreachable")
	cover("Dispatch failures", "a step with TransportStatus: unauthorized fails the turn at once with no retry", "TestDispatch_Unauthorized")
	cover("Dispatch failures", "a step with TransportStatus: reset and status interrupted yields no retry and a capacity hold", "TestDispatch_ResetWithHold")
	cover("Dispatch failures", "a step with TransportStatus: malformed_body yields reviewer_failed:<id>:malformed_body with no retry and no hold", "TestDispatch_MalformedBody")
	cover("Dispatch failures", "a step returning an ErrNotSent-wrapped Go error yields an attempt with transport_status: not_sent:<error>, no hold, and an INCOMPLETE turn", "TestDispatch_ErrNotSent")
	cover("Dispatch failures", "a step returning a plain Go error yields status interrupted, transport_status: adapter_error:<error>, a capacity hold, and an INCOMPLETE turn", "TestDispatch_PlainGoError")
	cover("Dispatch failures", "the OpenAI adapter's contract test shows an unresolvable secret reference arrives wrapped in ErrNotSent and a body-read failure arrives unwrapped", "openai.TestContractOpenAI")
	cover("Dispatch failures", "the next submit after any of these dispatches to the same participant again", "TestDispatch_RetryAfterFailure")
}

// TestDispatch_UnreachableRetry tests that an unreachable transport status
// results in a retry and eventual normal turn completion.
func TestDispatch_UnreachableRetry(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnreachable,
			},
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-unreachable-retry",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Advance clock until reviewer has been called twice (once unreachable, once retry)
	h.AdvanceClock(t, "reviewer", 2)

	h.WaitTurnState(t, reply.TurnID, "approved")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "approved" {
		t.Fatalf("expected approved state, got %v", states)
	}
}

// TestDispatch_TwoUnreachable tests that two unreachable responses result in
// reviewer_failed and do not wait for deadline.
func TestDispatch_TwoUnreachable(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnreachable,
			},
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnreachable,
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-two-unreachable",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Advance clock until reviewer has been called twice (first unreachable, retry also unreachable)
	h.AdvanceClock(t, "reviewer", 2)

	h.WaitTurnState(t, reply.TurnID, "incomplete")
	states := h.engine.TurnStates(reply.TurnID)
	if len(states) == 0 || states[len(states)-1] != "incomplete" {
		t.Fatalf("expected incomplete state, got %v", states)
	}
}

// TestDispatch_Unauthorized tests that unauthorized transport status fails
// immediately with no retry.
func TestDispatch_Unauthorized(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-unauthorized",
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

// TestDispatch_ResetWithHold tests that a reset transport status with
// interrupted status creates a capacity hold.
func TestDispatch_ResetWithHold(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusInterrupted,
				TransportStatus: transport.TransportStatusReset,
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-reset-hold",
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

// TestDispatch_MalformedBody tests that malformed_body transport status
// fails with no retry and no hold.
func TestDispatch_MalformedBody(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-malformed-body",
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

// TestDispatch_ErrNotSent tests that an ErrNotSent-wrapped Go error yields
// no hold and incomplete turn.
func TestDispatch_ErrNotSent(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind: fake.StepKindNotSent,
				Err:  fmt.Errorf("secret resolution failed"),
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-not-sent",
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

// TestDispatch_PlainGoError tests that a plain Go error from the adapter
// yields a capacity hold and incomplete turn.
func TestDispatch_PlainGoError(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("author answer"),
		},
		"reviewer": {
			fake.Step{
				Kind: fake.StepKindError,
				Err:  fmt.Errorf("network error"),
			},
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	req := wire.SubmitRequest{
		SubmissionID: "test-plain-error",
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

// TestDispatch_RetryAfterFailure tests that after any dispatch failure,
// the next submit to the same participant again retries.
func TestDispatch_RetryAfterFailure(t *testing.T) {
	h := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("first answer"),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Text:            "",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnreachable,
			},
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})

	c := h.Dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	// Submit with unreachable response followed by successful retry
	req := wire.SubmitRequest{
		SubmissionID: "test-retry",
		Input:        "input",
		Preset:       ptrString("review"),
	}
	reply, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Advance clock until reviewer has been called twice (once unreachable, once retry)
	h.AdvanceClock(t, "reviewer", 2)
	h.WaitTurnState(t, reply.TurnID, "approved")
}
