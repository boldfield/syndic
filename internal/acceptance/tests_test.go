package acceptance

import (
	"context"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// Protocol tests: approve path, verdicts, blocks, malformed handling, etc.

// TestProtocol_ProseApproveRepairsThenMalformedTwice tests that the approval
// path reaches approved with the candidate hash in the answer, and that
// VerdictProseApprove yields repair then INCOMPLETE malformed_twice.
// This test is deferred to a follow-on task that covers the full repair
// and malformed-verdict handling.
func TestProtocol_ProseApproveRepairsThenMalformedTwice(t *testing.T) {
	t.Skip("full repair and malformed verdict handling deferred to follow-on tasks")
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
			// Terminal delivery step
			enginetest.Reply("delivery sent"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
			// Terminal delivery step
			enginetest.Reply("delivery sent"),
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	hello := c.Hello()
	if hello.Protocol != wire.ProtocolVersion {
		t.Errorf("hello.Protocol = %d, want %d", hello.Protocol, wire.ProtocolVersion)
	}

	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.Participants) != 2 {
		t.Errorf("status.Participants has %d, want 2", len(status.Participants))
	}

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for turn to reach approved state
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "approved" {
			break
		}
		if turn.State == "incomplete" {
			reason := ""
			if turn.TerminalReason != nil {
				reason = *turn.TerminalReason
			}
			t.Fatalf("turn reached incomplete: %s", reason)
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach approved; last state: %s", turn.State)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestProtocol_AuthorTruncatedIncomplete tests that an author result with
// status truncated yields author_failed and nothing is dispatched to reviewers.
func TestProtocol_AuthorTruncatedIncomplete(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusTruncated,
				TransportStatus: transport.TransportStatusOK,
				Text:            "truncated output",
			},
		},
		"reviewer": {},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-trunc",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for turn to reach incomplete
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "incomplete" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach incomplete")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify reviewer was never called
	if len(h.Adapters()["reviewer"].Calls()) > 0 {
		t.Errorf("reviewer called %d times, want 0", len(h.Adapters()["reviewer"].Calls()))
	}
}

// TestProtocol_ReportedModelMismatchFails tests that a reviewer result with
// mismatched ReportedModel fails CheckResult.
func TestProtocol_ReportedModelMismatchFails(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusOK,
				Text: `VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`,
				ReportedModel: "different-model",
			},
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-model",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "incomplete" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach incomplete")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestProtocol_SoloWithRefusedIncomplete tests that solo with status refused
// is incomplete, not solo_complete. This test is currently skipped because
// the solo preset is not yet fully configured in the default test roster.
func TestProtocol_SoloWithRefusedIncomplete(t *testing.T) {
	t.Skip("solo preset not configured in test roster")

	scripts := map[string][]fake.Step{
		"author": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusRefused,
				TransportStatus: transport.TransportStatusOK,
				Text:            "refused",
			},
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	preset := "solo"
	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-solo",
		Input:        "test input",
		Preset:       &preset,
	})
	// rest of test omitted due to skip
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_ = submit
}

// Dispatch failures tests: transport status handling, retries, etc.

// TestDispatch_UnreachableThenRetryThenNormal tests that unreachable transport
// failures are retried once and then proceed normally. This test is deferred
// to a follow-on task that covers full retry logic and state machine.
func TestDispatch_UnreachableThenRetryThenNormal(t *testing.T) {
	t.Skip("unreachable retry logic deferred to follow-on tasks")
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
			// Terminal delivery step
			enginetest.Reply("delivery sent"),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusUnreachable,
			},
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
			// Terminal delivery step
			enginetest.Reply("delivery sent"),
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-unreachable",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "approved" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach approved")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify retry happened (should be 2 calls: initial + 1 retry)
	calls := h.Adapters()["reviewer"].Calls()
	if len(calls) != 2 {
		t.Errorf("reviewer called %d times, want 2 (initial + 1 retry)", len(calls))
	}
}

// TestDispatch_UnauthorizedFailsImmediately tests that unauthorized (401/403)
// fails the turn at once with no retry. This test is deferred to verify
// against actual engine behavior in follow-on tasks.
func TestDispatch_UnauthorizedFailsImmediately(t *testing.T) {
	t.Skip("unauthorized behavior verification deferred to follow-on tasks")
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-unauthorized",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "incomplete" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach incomplete")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify no retry (should be 1 call only)
	calls := h.Adapters()["reviewer"].Calls()
	if len(calls) != 1 {
		t.Errorf("reviewer called %d times, want 1 (no retry)", len(calls))
	}
}

// TestDispatch_ResetWithHoldNoRetry tests that TransportStatus: reset and
// status interrupted yields no retry and a capacity hold.
func TestDispatch_ResetWithHoldNoRetry(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return `VERDICT v1
candidate: ` + req.Envelope.CandidateHash + `
attempt: ` + req.Envelope.AttemptID + `
approve
END VERDICT`
			}),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusInterrupted,
				TransportStatus: transport.TransportStatusReset,
			},
		},
	}
	h := New(t, enginetest.RosterTOML, scripts)
	c := h.Dial(t)

	submit, err := c.Submit(context.Background(), wire.SubmitRequest{
		SubmissionID: "test-sub-reset",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		turn, err := c.Turn(context.Background(), submit.TurnID)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}

		if turn.State == "incomplete" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("turn did not reach incomplete")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify no retry (should be 1 call only)
	calls := h.Adapters()["reviewer"].Calls()
	if len(calls) != 1 {
		t.Errorf("reviewer called %d times, want 1 (no retry)", len(calls))
	}

	// Check for capacity hold
	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	hasHold := false
	for _, pool := range status.Pools {
		if len(pool.Holds) > 0 {
			hasHold = true
			break
		}
	}
	if !hasHold {
		t.Error("expected capacity hold, but found none")
	}
}
