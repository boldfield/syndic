package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

var testTime = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

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
		Phase:                 "author",
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

// TestMergeStatusPopulatesParticipants verifies that MergeStatus fills participants
// from the status reply.
func TestMergeStatusPopulatesParticipants(t *testing.T) {
	s := NewState()
	status := &wire.StatusReply{
		RunID:                "run-1",
		RosterHash:           "hash123",
		RosterFileChanged:    true,
		DeadlinesProvisional: true,
		DefaultPreset:        "review",
		Participants: []wire.Participant{
			{
				ID:                  "author",
				Model:               "model1",
				ReportedModel:       "reported-model1",
				Identity:            "unknown",
				State:               "idle",
				Pool:                "pool1",
				EligibleRoles:       []string{"author", "reviewer"},
				BytesPerTokenSource: "probe",
				ContextSource:       "probe",
				DeclaredBilling:     "flat",
			},
		},
	}

	s = MergeStatus(s, status)

	if _, ok := s.Participants["author"]; !ok {
		t.Fatal("author not in participants")
	}
	p := s.Participants["author"]
	if p.State != "idle" || p.ReportedModel != "reported-model1" {
		t.Errorf("participant data mismatch: state=%s, reported=%s", p.State, p.ReportedModel)
	}
	if p.DeclaredBilling != "flat" {
		t.Errorf("declared billing = %q, want flat", p.DeclaredBilling)
	}
	if s.Run.RosterHash != "hash123" {
		t.Errorf("run info not merged: hash=%s", s.Run.RosterHash)
	}
}

// TestMergeTurnPopulatesAuthorAndReviewers verifies that MergeTurn fills author
// and reviewers from attempt records.
func TestMergeTurnPopulatesAuthorAndReviewers(t *testing.T) {
	s := NewState()
	s.Participants["author"] = ParticipantInfo{ID: "author", State: "running"}
	s.Participants["reviewer1"] = ParticipantInfo{ID: "reviewer1", State: "queued"}

	turn := &wire.TurnReply{
		State:  "approved",
		Preset: "review",
		Attempts: []wire.AttemptRecord{
			{ID: "a1", Participant: "author", Phase: "author", Status: "complete", TransportStatus: "ok"},
			{ID: "a2", Participant: "reviewer1", Phase: "review", Status: "complete", TransportStatus: "ok"},
		},
	}

	s = MergeTurn(s, "turn-1", turn, testTime)

	ti := s.Turns["turn-1"]
	if ti.Author != "author" {
		t.Errorf("author = %q, want author", ti.Author)
	}
	if len(ti.Reviewers) != 1 || ti.Reviewers[0] != "reviewer1" {
		t.Errorf("reviewers = %v, want [reviewer1]", ti.Reviewers)
	}
	if ti.ReviewerStates["reviewer1"] != "queued" {
		t.Errorf("reviewer state = %s, want queued", ti.ReviewerStates["reviewer1"])
	}
}

// TestApplyPureOnParticipantState verifies apply doesn't mutate input maps.
func TestApplyPureOnParticipantState(t *testing.T) {
	s := NewState()
	s.Participants["p1"] = ParticipantInfo{ID: "p1", State: "idle"}
	s.CurrentTurn = "turn-1"
	s.Turns["turn-1"] = TurnInfo{ID: "turn-1", Author: "p1", ReviewerStates: make(map[string]string)}

	body := wire.EvParticipantState{Participant: "p1", State: "running"}
	bodyBytes, _ := json.Marshal(body)
	f := wire.Frame{Seq: 1, Kind: wire.EventKindParticipantState, Body: bodyBytes}

	s1 := apply(s, f)

	// Original should be unchanged (no mutations)
	if s.Participants["p1"].State != "idle" {
		t.Error("original state was mutated")
	}
	if s1.Participants["p1"].State != "running" {
		t.Error("new state not updated")
	}
	// Check that the author state was updated in the turn
	if s1.Turns["turn-1"].AuthorState != "running" {
		t.Error("author state not updated in turn")
	}
}

// TestProgressLineWithReviewersInOrder verifies reviewers are shown in order,
// not random map iteration.
func TestProgressLineWithReviewersInOrder(t *testing.T) {
	s := NewState()
	s.Status = &wire.StatusReply{DefaultPreset: "review"}
	s.CurrentTurn = "turn-1"
	s.Turns["turn-1"] = TurnInfo{
		ID:          "turn-1",
		State:       "submitted",
		Preset:      "review",
		Author:      "author",
		AuthorState: "running",
		Reviewers:   []string{"r1", "r2", "r3"},
		ReviewerStates: map[string]string{
			"r1": "queued",
			"r2": "running",
			"r3": "idle",
		},
	}

	strip := progressLine(s, "turn-1", testTime, false)
	spin := spinnerAt(testTime)
	if !contains(strip, "○ r1") || !contains(strip, spin+" r2") || !contains(strip, "○ r3") {
		t.Errorf("strip = %s", strip)
	}
	// Verify order: r1 should come before r2 should come before r3
	idx1 := indexOf(strip, "r1")
	idx2 := indexOf(strip, "r2")
	idx3 := indexOf(strip, "r3")
	if idx1 < 0 || idx2 < 0 || idx3 < 0 || idx1 >= idx2 || idx2 >= idx3 {
		t.Errorf("reviewers not in order: r1@%d, r2@%d, r3@%d", idx1, idx2, idx3)
	}
}

// TestRenderStripTruncatesWithEllipsis verifies the strip truncates at terminal
// display width with ellipsis.
func TestRenderStripTruncatesWithEllipsis(t *testing.T) {
	s := NewState()
	s.Status = &wire.StatusReply{DefaultPreset: "review"}
	s.CurrentTurn = "turn-1"
	s.Turns["turn-1"] = TurnInfo{
		ID:        "turn-1",
		State:     "submitted",
		Preset:    "review",
		Author:    "author",
		Reviewers: []string{"r1", "r2", "r3", "r4", "r5"},
		ReviewerStates: map[string]string{
			"r1": "running", "r2": "running", "r3": "running", "r4": "running", "r5": "running",
		},
	}

	strip := renderStrip(s, 15, testTime, false)
	displayWidth := runewidth.StringWidth(strip)
	if displayWidth > 15 {
		t.Errorf("strip display width = %d, want <= 15", displayWidth)
	}
	if strip != "review · submi…" {
		t.Errorf("strip = %q, want the turn state shortened with an ellipsis", strip)
	}
	if strings.Count(strip, "\n") > 0 {
		t.Errorf("strip should be a single line, got: %s", strip)
	}
}

// TestRenderBannersShowsReconnecting verifies the reconnecting banner appears.
func TestRenderBannersShowsReconnecting(t *testing.T) {
	s := NewState()
	s.Status = &wire.StatusReply{DeadlinesProvisional: true}
	s.ConnState = ConnStateReconnecting

	banners := renderBanners(s)
	if len(banners) < 2 {
		t.Fatalf("expected at least 2 banners, got %d: %v", len(banners), banners)
	}
	hasProvisional := false
	hasReconnecting := false
	for _, b := range banners {
		if b == "deadlines: provisional" {
			hasProvisional = true
		}
		if b == "engine: reconnecting" {
			hasReconnecting = true
		}
	}
	if !hasProvisional {
		t.Error("provisional banner missing")
	}
	if !hasReconnecting {
		t.Error("reconnecting banner missing")
	}
}

// TestRenderBannersShowsRosterChanged verifies the roster changed banner.
func TestRenderBannersShowsRosterChanged(t *testing.T) {
	s := NewState()
	s.Status = &wire.StatusReply{RosterFileChanged: true}
	s.ConnState = ConnStateConnected

	banners := renderBanners(s)
	found := false
	for _, b := range banners {
		if strings.Contains(b, "roster file changed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("roster changed banner not found in %v", banners)
	}
}

func ptr[T any](v T) *T {
	return &v
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func indexOf(s, substr string) int {
	return strings.Index(s, substr)
}

func TestMergeTurnStaleIncompleteReplyKeepsOverride(t *testing.T) {
	s := State{Turns: map[string]TurnInfo{"t1": {ID: "t1", State: "overridden"}}}
	s = MergeTurn(s, "t1", &wire.TurnReply{State: "incomplete", Mode: "review", Preset: "review"}, testTime)
	if got := s.Turns["t1"].State; got != "overridden" {
		t.Fatalf("state = %q, want overridden", got)
	}
}
