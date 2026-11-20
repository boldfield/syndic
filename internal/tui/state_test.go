package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestApplyTurnSubmitted(t *testing.T) {
	s := NewState()
	body := wire.EvTurnSubmitted{
		TurnID:          "turn-1",
		SubmissionID:    "sub-1",
		Preset:          "review",
		EstimatedTokens: 1000,
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  1,
		Kind: wire.EventKindTurnSubmitted,
		Body: bodyBytes,
	}

	s = apply(s, f)

	if s.CurrentTurn != "turn-1" {
		t.Errorf("CurrentTurn = %q, want turn-1", s.CurrentTurn)
	}
	if s.TurnState["turn-1"] != "submitted" {
		t.Errorf("TurnState[turn-1] = %q, want submitted", s.TurnState["turn-1"])
	}
	if s.LastSeq != 1 {
		t.Errorf("LastSeq = %d, want 1", s.LastSeq)
	}
	if _, ok := s.Turns["turn-1"]; !ok {
		t.Error("TurnInfo not created for turn-1")
	}
}

func TestApplyTurnState(t *testing.T) {
	s := NewState()
	s.TurnState["turn-1"] = "submitted"
	s.CurrentTurn = "turn-1"
	s.Turns["turn-1"] = TurnInfo{ID: "turn-1", State: "submitted"}

	reason := "all_approved"
	body := wire.EvTurnState{
		TurnID: "turn-1",
		State:  "approved",
		Reason: &reason,
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  2,
		Kind: wire.EventKindTurnState,
		Body: bodyBytes,
	}

	s = apply(s, f)

	if s.TurnState["turn-1"] != "approved" {
		t.Errorf("TurnState[turn-1] = %q, want approved", s.TurnState["turn-1"])
	}
	if s.CurrentTurn != "" {
		t.Errorf("CurrentTurn = %q, want empty after terminal state", s.CurrentTurn)
	}
	ti := s.Turns["turn-1"]
	if ti.TerminalReason == nil || *ti.TerminalReason != "all_approved" {
		t.Errorf("TerminalReason = %v, want all_approved", ti.TerminalReason)
	}
}

func TestApplyAttemptBegun(t *testing.T) {
	s := NewState()
	body := wire.EvAttemptBegun{
		AttemptID:             "attempt-1",
		Participant:           "author",
		Phase:                 "author_dispatch",
		Epoch:                 0,
		Deadline:              time.Now().Add(5 * time.Minute).Format(time.RFC3339),
		MaxOutputTokens:       4096,
		EstimatedPromptTokens: 500,
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  3,
		Kind: wire.EventKindAttemptBegun,
		Body: bodyBytes,
	}

	s = apply(s, f)

	ai, ok := s.Attempts["attempt-1"]
	if !ok {
		t.Fatal("Attempt not found")
	}
	if ai.Participant != "author" {
		t.Errorf("Participant = %q, want author", ai.Participant)
	}
	if ai.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", ai.Status)
	}
}

func TestApplyAttemptCompleted(t *testing.T) {
	s := NewState()
	s.Attempts["attempt-1"] = AttemptInfo{ID: "attempt-1", Status: "in_progress"}

	body := wire.EvAttemptCompleted{
		AttemptID:       "attempt-1",
		Status:          "complete",
		TransportStatus: "ok",
		Usage: &wire.ReportedUsage{
			PromptTokens:     ptr(int64(100)),
			CompletionTokens: ptr(int64(200)),
		},
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  4,
		Kind: wire.EventKindAttemptCompleted,
		Body: bodyBytes,
	}

	s = apply(s, f)

	ai := s.Attempts["attempt-1"]
	if ai.Status != "complete" {
		t.Errorf("Status = %q, want complete", ai.Status)
	}
	if ai.ReportedPrompt == nil || *ai.ReportedPrompt != 100 {
		t.Errorf("ReportedPrompt = %v, want 100", ai.ReportedPrompt)
	}
	if ai.ReportedCompletion == nil || *ai.ReportedCompletion != 200 {
		t.Errorf("ReportedCompletion = %v, want 200", ai.ReportedCompletion)
	}
}

func TestApplyIgnoresUnknownEventKind(t *testing.T) {
	s := NewState()
	s.LastSeq = 10
	f := wire.Frame{
		Seq:  11,
		Kind: "unknown_event_kind",
		Body: []byte("{}"),
	}

	s = apply(s, f)

	if s.LastSeq != 11 {
		t.Errorf("LastSeq not updated for unknown event")
	}
}

func TestApplyIgnoresUnknownMembers(t *testing.T) {
	s := NewState()
	// JSON with unknown field
	bodyBytes := []byte(`{"turn_id":"turn-1","extra_field":"should_be_ignored"}`)
	f := wire.Frame{
		Seq:  1,
		Kind: wire.EventKindTurnSubmitted,
		Body: bodyBytes,
	}

	// Should not panic even with unknown members
	s = apply(s, f)

	if s.CurrentTurn != "turn-1" {
		t.Errorf("CurrentTurn = %q, want turn-1", s.CurrentTurn)
	}
}

func TestApplyBudgetEvents(t *testing.T) {
	s := NewState()

	// Budget event
	body1 := wire.EvBudget{
		TurnID:    "turn-1",
		Budget:    "turn_max_estimated_tokens",
		Estimated: ptr(int64(50000)),
		Limit:     100000,
	}
	bodyBytes1, _ := json.Marshal(body1)
	f1 := wire.Frame{Seq: 1, Kind: wire.EventKindBudget, Body: bodyBytes1}
	s = apply(s, f1)

	if s.LastBudgetNotice == nil || s.LastBudgetNotice.Kind != "turn_max_estimated_tokens" {
		t.Errorf("Budget event not applied correctly")
	}

	// Budget estimate exceeded
	body2 := wire.EvBudgetEstimateExceeded{
		TurnID:    "turn-1",
		AttemptID: "attempt-1",
		Estimated: 500,
		Reported:  600,
	}
	bodyBytes2, _ := json.Marshal(body2)
	f2 := wire.Frame{Seq: 2, Kind: wire.EventKindBudgetEstimateExceeded, Body: bodyBytes2}
	s = apply(s, f2)

	if s.LastBudgetExceeded == nil || s.LastBudgetExceeded.Reported != 600 {
		t.Errorf("Budget estimate exceeded not applied correctly")
	}

	// Budget exhausted
	body3 := wire.EvBudgetExhausted{
		TurnID:  "turn-1",
		Charged: 100000,
		Limit:   100000,
	}
	bodyBytes3, _ := json.Marshal(body3)
	f3 := wire.Frame{Seq: 3, Kind: wire.EventKindBudgetExhausted, Body: bodyBytes3}
	s = apply(s, f3)

	if s.LastBudgetExhausted == nil || s.LastBudgetExhausted.Charged != 100000 {
		t.Errorf("Budget exhausted not applied correctly")
	}
}

func TestApplyBlockOpened(t *testing.T) {
	s := NewState()

	body := wire.EvBlockOpened{
		BlockID:  "block-1",
		Severity: "error",
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  1,
		Kind: wire.EventKindBlockOpened,
		Body: bodyBytes,
	}

	s = apply(s, f)

	bi, ok := s.Blocks["block-1"]
	if !ok {
		t.Fatal("Block not found")
	}
	if bi.Severity != "error" || bi.State != "open" {
		t.Errorf("Block = %+v, want severity:error state:open", bi)
	}
}

func TestApplyMessageAppended(t *testing.T) {
	s := NewState()

	body := wire.EvMessageAppended{
		MsgID:   "msg-1",
		TurnID:  "turn-1",
		Phase:   "author_dispatch",
		Sender:  "author",
		SHA256:  "abc123",
		ByteLen: 1000,
	}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{
		Seq:  1,
		Kind: wire.EventKindMessageAppended,
		Body: bodyBytes,
	}

	s = apply(s, f)

	mi, ok := s.Messages["msg-1"]
	if !ok {
		t.Fatal("Message not found")
	}
	if mi.TurnID != "turn-1" || mi.Sender != "author" {
		t.Errorf("Message = %+v", mi)
	}
}

func TestApplySequenceOfEventsFromRealTurn(t *testing.T) {
	s := NewState()

	// Simulate a complete turn: submit -> attempt_begun -> attempt_completed -> turn_state

	// 1. Turn submitted
	submit := wire.EvTurnSubmitted{
		TurnID:          "turn-1",
		SubmissionID:    "sub-1",
		Preset:          "review",
		EstimatedTokens: 1000,
	}
	submitBytes, _ := json.Marshal(submit)
	s = apply(s, wire.Frame{Seq: 1, Kind: wire.EventKindTurnSubmitted, Body: submitBytes})

	if s.CurrentTurn != "turn-1" {
		t.Fatalf("After submit: CurrentTurn = %q, want turn-1", s.CurrentTurn)
	}

	// 2. Attempt begun
	attemptBegun := wire.EvAttemptBegun{
		AttemptID:             "attempt-1",
		Participant:           "author",
		Phase:                 "author_dispatch",
		Epoch:                 0,
		Deadline:              time.Now().Add(5 * time.Minute).Format(time.RFC3339),
		MaxOutputTokens:       4096,
		EstimatedPromptTokens: 500,
	}
	attemptBegunBytes, _ := json.Marshal(attemptBegun)
	s = apply(s, wire.Frame{Seq: 2, Kind: wire.EventKindAttemptBegun, Body: attemptBegunBytes})

	if _, ok := s.Attempts["attempt-1"]; !ok {
		t.Fatalf("After attempt_begun: attempt not found")
	}

	// 3. Attempt completed
	attemptCompleted := wire.EvAttemptCompleted{
		AttemptID:       "attempt-1",
		Status:          "complete",
		TransportStatus: "ok",
		Usage: &wire.ReportedUsage{
			PromptTokens:     ptr(int64(100)),
			CompletionTokens: ptr(int64(200)),
		},
	}
	attemptCompletedBytes, _ := json.Marshal(attemptCompleted)
	s = apply(s, wire.Frame{Seq: 3, Kind: wire.EventKindAttemptCompleted, Body: attemptCompletedBytes})

	if s.Attempts["attempt-1"].Status != "complete" {
		t.Fatalf("After attempt_completed: status = %q", s.Attempts["attempt-1"].Status)
	}

	// 4. Turn terminal
	reason := "all_approved"
	turnState := wire.EvTurnState{
		TurnID: "turn-1",
		State:  "approved",
		Reason: &reason,
	}
	turnStateBytes, _ := json.Marshal(turnState)
	s = apply(s, wire.Frame{Seq: 4, Kind: wire.EventKindTurnState, Body: turnStateBytes})

	if s.TurnState["turn-1"] != "approved" {
		t.Errorf("After turn_state: state = %q, want approved", s.TurnState["turn-1"])
	}
	if s.CurrentTurn != "" {
		t.Errorf("After turn terminal: CurrentTurn = %q, want empty", s.CurrentTurn)
	}
	if s.LastSeq != 4 {
		t.Errorf("LastSeq = %d, want 4", s.LastSeq)
	}
}

func ptr[T any](v T) *T {
	return &v
}
