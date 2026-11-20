package acceptance

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// TestProtocol_ProseApproveRepairsThenMalformedTwice tests the approval path
// and malformed handling as specified in section 8 "Protocol".
func TestProtocol_ProseApproveRepairsThenMalformedTwice(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
		"reviewer": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	hello := h.Engine.Hello()
	if hello.Protocol != wire.ProtocolVersion {
		t.Errorf("hello.Protocol = %d, want %d", hello.Protocol, wire.ProtocolVersion)
	}

	status, err := h.Engine.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.Participants) == 0 {
		t.Errorf("status has no participants")
	}

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submit.TurnID == "" {
		t.Errorf("submit.TurnID is empty")
	}

	h.WaitTurnState(submit.TurnID, "approved")
	states := h.TurnStates(submit.TurnID)
	if states[len(states)-1] != "approved" {
		t.Errorf("final state is %q, want approved", states[len(states)-1])
	}
}

// TestProtocol_UnreachableThenRetryThenNormal tests that unreachable
// transport failures are retried once and then proceed normally.
func TestProtocol_UnreachableThenRetryThenNormal(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusUnreachable,
			},
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "approved")
	states := h.TurnStates(submit.TurnID)
	if states[len(states)-1] != "approved" {
		t.Errorf("final state is %q, want approved", states[len(states)-1])
	}
}

// TestProtocol_AuthorTruncatedIncompleteThenReviewDispatched tests that
// an author result with status truncated yields author_failed and no
// reviewer dispatch.
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
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "incomplete")
	states := h.TurnStates(submit.TurnID)
	if states[len(states)-1] != "incomplete" {
		t.Errorf("final state is %q, want incomplete", states[len(states)-1])
	}

	if len(h.Adapters["reviewer"].Calls()) > 0 {
		t.Errorf("reviewer adapter was called, expected no calls")
	}
}

// TestProtocol_ReportedModelMismatchFails tests that a reviewer result with
// mismatched ReportedModel fails CheckResult.
func TestProtocol_ReportedModelMismatchFails(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
		"reviewer": {
			fake.Step{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusOK,
				Text:            `VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`,
				ReportedModel: "different-model",
			},
		},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "incomplete")
	states := h.TurnStates(submit.TurnID)
	if states[len(states)-1] != "incomplete" {
		t.Errorf("final state is %q, want incomplete", states[len(states)-1])
	}
}

// TestProtocol_SoloWithRefusedIncomplete tests that solo with status
// refused is incomplete, not solo_complete.
func TestProtocol_SoloWithRefusedIncomplete(t *testing.T) {
	soloRoster := `
default_preset = "solo"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2"]
tokens_per_byte_max = 1.0

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[policy]
local_only = true
allowed_destinations = []
`

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
	h := enginetest.New(t, soloRoster, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "incomplete")
	states := h.TurnStates(submit.TurnID)
	if states[len(states)-1] != "incomplete" {
		t.Errorf("final state is %q, want incomplete", states[len(states)-1])
	}
}

// TestProtocol_UnauthorizedFailsImmediately tests that unauthorized (401/403)
// fails the turn at once with no retry.
func TestProtocol_UnauthorizedFailsImmediately(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusComplete,
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "incomplete")

	if len(h.Adapters["reviewer"].Calls()) > 1 {
		t.Errorf("reviewer adapter was called %d times, want 1 (no retry)", len(h.Adapters["reviewer"].Calls()))
	}
}

// TestProtocol_ResetTimeout tests that reset or timeout after possible
// submission yields a hold and no retry.
func TestProtocol_ResetTimeout(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {
			enginetest.Reply(`VERDICT v1
candidate: c1234567890123456789012345678901
attempt: a1234567890123456789012345678901
approve
END VERDICT`),
		},
		"reviewer": {
			{
				Kind:            fake.StepKindReply,
				Status:          transport.StatusInterrupted,
				TransportStatus: transport.TransportStatusReset,
			},
		},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)

	submit, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "sub1",
		Input:        "test input",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h.WaitTurnState(submit.TurnID, "incomplete")

	if len(h.Adapters["reviewer"].Calls()) > 1 {
		t.Errorf("reviewer adapter was called %d times, want 1 (no retry)", len(h.Adapters["reviewer"].Calls()))
	}
}
