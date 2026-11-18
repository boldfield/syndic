package enginetest

import (
	"testing"

	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const minimalRoster = `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[budgets]
calibration_safety          = 0.75
message_overhead_tokens     = 8
request_overhead_tokens     = 16
context_margin              = 0.10
author_max_output_tokens    = 4096
reviewer_max_output_tokens  = 2048
delivery_max_tokens         = 64
turn_max_estimated_tokens   = 250000

[engine]
stop_grace = "5s"

[pools.default]
concurrency = 1

[[endpoints]]
name                = "local"
base_url            = "http://localhost:8000/v1"
declared_location   = "lan"
declared_execution  = "local"
declared_billing    = "none"
network_dependency  = "none"
dialect             = "chat_completions_v1"
tokens_per_byte_max = 1.0

[[participants]]
id             = "author-1"
model          = "test-model"
route          = "api"
endpoint       = "local"
eligible_roles = ["author"]
pool           = "default"
context_tokens = 32768
bytes_per_token = 2.5

[[participants]]
id             = "reviewer-1"
model          = "test-model"
route          = "api"
endpoint       = "local"
eligible_roles = ["reviewer"]
pool           = "default"
context_tokens = 32768
bytes_per_token = 2.5

[presets.solo]
mode   = "solo"
author = "author-1"
required_reviewers = []

[presets.review]
mode   = "review"
author = "author-1"
required_reviewers = ["reviewer-1"]
`

func ptrStr(s string) *string {
	return &s
}

// TestStartWritesEngineRun verifies that Start writes an engine run.
func TestStartWritesEngineRun(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{
		"author-1": {
			{
				Kind:   fake.StepKindReply,
				Text:   "test response",
				Status: "complete",
			},
		},
	})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hello := h.Engine.Hello()
	if hello.RunID == "" {
		t.Error("RunID is empty")
	}
	if hello.ManifestHash == "" {
		t.Error("ManifestHash is empty")
	}
	if hello.RunFirstSeq == 0 {
		t.Error("RunFirstSeq is 0")
	}
}

// TestHelloReturnsEngineInfo verifies that Hello returns correct engine info.
func TestHelloReturnsEngineInfo(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hello := h.Engine.Hello()
	if hello.EngineVersion == "" {
		t.Error("EngineVersion is empty")
	}
	if hello.Protocol != wire.ProtocolVersion {
		t.Errorf("Protocol = %d, want %d", hello.Protocol, wire.ProtocolVersion)
	}
}

// TestStatusReturnsParticipants verifies that Status lists participants.
func TestStatusReturnsParticipants(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status, err := h.Engine.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(status.Participants) == 0 {
		t.Error("Participants is empty")
	}

	if len(status.Participants) > 0 {
		p := status.Participants[0]
		if p.ID == "" {
			t.Error("Participant ID is empty")
		}
	}
}

// TestSubmitCreatesTurn verifies that Submit creates a turn.
func TestSubmitCreatesTurn(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{
		"author-1": {
			{
				Kind:   fake.StepKindReply,
				Text:   "test response",
				Status: "complete",
			},
		},
	})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	reply, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "test-submission-1",
		Input:        "test input",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if reply.TurnID == "" {
		t.Error("TurnID is empty")
	}
	if reply.Preset != "solo" {
		t.Errorf("Preset = %q, want %q", reply.Preset, "solo")
	}
}

// TestSubmitIdempotent verifies that Submit is idempotent.
func TestSubmitIdempotent(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{
		"author-1": {
			{
				Kind:   fake.StepKindReply,
				Text:   "test response",
				Status: "complete",
			},
		},
	})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	reply1, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "test-submission-1",
		Input:        "test input",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("First Submit: %v", err)
	}

	reply2, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "test-submission-1",
		Input:        "test input",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("Second Submit: %v", err)
	}

	if reply1.TurnID != reply2.TurnID {
		t.Errorf("TurnID changed: %q -> %q", reply1.TurnID, reply2.TurnID)
	}
}

// TestSoloHappyPath verifies the solo turn happy path.
func TestSoloHappyPath(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{
		"author-1": {
			{
				Kind:   fake.StepKindReply,
				Text:   "test response",
				Status: "complete",
			},
		},
	})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	reply, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "test-submission-1",
		Input:        "test input",
		Preset:       ptrStr("solo"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for turn to complete
	if err := h.WaitTurnState(reply.TurnID, "solo_complete"); err != nil {
		t.Fatalf("WaitTurnState: %v", err)
	}

	// Verify events include the required sequence
	events := h.Events()
	eventKinds := []string{}
	for _, e := range events {
		eventKinds = append(eventKinds, e.Kind)
	}

	expectedSequence := []string{
		wire.EventKindTurnSubmitted,
		wire.EventKindTurnState,
		wire.EventKindMessageAppended,
		wire.EventKindAttemptBegun,
		wire.EventKindTurnState,
		wire.EventKindAttemptCompleted,
		wire.EventKindTurnState,
	}

	for i, expected := range expectedSequence {
		if i >= len(eventKinds) {
			t.Errorf("Event %d: missing (expected %s)", i, expected)
			break
		}
		if eventKinds[i] != expected {
			t.Errorf("Event %d: got %s, want %s", i, eventKinds[i], expected)
		}
	}
}

// TestSubmitPresetInvalid tests invalid preset rejection.
func TestSubmitPresetInvalid(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	if err := h.Engine.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "test-submission-1",
		Input:        "test input",
		Preset:       ptrStr("unknown-preset"),
	})
	if err == nil {
		t.Fatal("Submit succeeded, want error")
	}

	wireErr, ok := err.(*wire.Error)
	if !ok {
		t.Fatalf("Error type: got %T, want *wire.Error", err)
	}

	if wireErr.Code != wire.ErrorCodePresetInvalid {
		t.Errorf("ErrorCode = %q, want %q", wireErr.Code, wire.ErrorCodePresetInvalid)
	}
}

// TestCancelReturnsNotImplemented verifies Cancel returns ErrNotImplemented.
func TestCancelReturnsNotImplemented(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	err = h.Engine.Cancel("any-turn-id")
	if err != ErrNotImplemented {
		t.Errorf("Cancel error: got %v, want ErrNotImplemented", err)
	}
}

// TestOverrideReturnsNotImplemented verifies Override returns ErrNotImplemented.
func TestOverrideReturnsNotImplemented(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	err = h.Engine.Override("any-turn-id", "test reason")
	if err != ErrNotImplemented {
		t.Errorf("Override error: got %v, want ErrNotImplemented", err)
	}
}

// TestReleaseReturnsNotImplemented verifies Release returns ErrNotImplemented.
func TestReleaseReturnsNotImplemented(t *testing.T) {
	h, err := New(t, minimalRoster, map[string][]*fake.Step{})
	if err != nil {
		t.Fatalf("New harness: %v", err)
	}
	defer h.Cleanup()

	_, err = h.Engine.Release(wire.ReleaseRequest{})
	if err != ErrNotImplemented {
		t.Errorf("Release error: got %v, want ErrNotImplemented", err)
	}
}
