package engine_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/wire"
)

func TestSubmitFrozenHistoryAndContextHash(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question 1",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	if reply.ContextHash != "" {
		t.Fatalf("first turn context_hash should be empty, got %q", reply.ContextHash)
	}

	var contextHash sql.NullString
	if err := h.DB.Conn().QueryRow(
		`SELECT context_hash FROM turns WHERE id = ?`, reply.TurnID,
	).Scan(&contextHash); err != nil {
		t.Fatalf("query first turn context_hash: %v", err)
	}

	if contextHash.Valid {
		t.Fatalf("first turn stored context_hash should be NULL, got %q", contextHash.String)
	}

	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)

	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "question 2",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &reply.ConversationID,
	}

	reply2, err := h.Engine.Submit(req2)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	if reply2.ContextHash == "" {
		t.Fatalf("second turn context_hash should not be empty")
	}

	if reply2.ConversationID != reply.ConversationID {
		t.Fatalf("conversation id should not change")
	}

	if err := h.DB.Conn().QueryRow(
		`SELECT context_hash FROM turns WHERE id = ?`, reply2.TurnID,
	).Scan(&contextHash); err != nil {
		t.Fatalf("query second turn context_hash: %v", err)
	}

	if !contextHash.Valid || contextHash.String != reply2.ContextHash {
		t.Fatalf("stored context_hash %v != reply %q", contextHash, reply2.ContextHash)
	}
}

func TestSubmitWorkspaceMismatchRefused(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace1 := "/workspace/a"
	workspace2 := "/workspace/b"

	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace1,
	}

	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)

	convID := reply1.ConversationID
	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "question 2",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace2,
		ConversationID: &convID,
	}

	_, err = h.Engine.Submit(req2)
	we := wantRefusal(t, err, wire.ErrorCodeWorkspaceMismatch)
	if we == nil {
		t.Fatal("expected workspace_mismatch refusal")
	}
}

func TestBusyIsGlobalAcrossConversations(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	release := h.Hold("author")
	workspace := "/workspace/test"

	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question 1",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	h.WaitTurnState(reply1.TurnID, engine.StateAuthorDispatched)

	req2 := wire.SubmitRequest{
		SubmissionID: "s2",
		Input:        "question 2",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	_, err = h.Engine.Submit(req2)
	we := wantRefusal(t, err, wire.ErrorCodeBusy)
	if we == nil || we.TurnID == nil || *we.TurnID != reply1.TurnID {
		t.Fatalf("expected busy refusal for first turn, got %+v", we)
	}

	release()
	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)
}

// TODO: TestContextFromTurnFiltersHistory - implement after debugging budget issue with ContextFromTurn

func TestMaxRoundsValidation(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
		MaxRounds:    &[]int{10}[0],
	}

	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeMaxRounds)
	if we == nil || we.Ceiling == nil {
		t.Fatalf("expected max_rounds refusal with ceiling, got %+v", we)
	}
	if *we.Ceiling != 5 {
		t.Fatalf("ceiling should be 5, got %d", *we.Ceiling)
	}

	negReq := wire.SubmitRequest{
		SubmissionID: "s2",
		Input:        "question",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
		MaxRounds:    &[]int{-1}[0],
	}

	_, err = h.Engine.Submit(negReq)
	we = wantRefusal(t, err, wire.ErrorCodeMaxRounds)
	if we == nil {
		t.Fatalf("expected max_rounds refusal for negative value")
	}
}

func TestUnknownConversationRefused(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	unknownID := "nonexistent-conv-id"
	req := wire.SubmitRequest{
		SubmissionID:   "s1",
		Input:          "question",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &unknownID,
	}

	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeBadRequest)
	if we == nil || we.Detail == nil || *we.Detail != "unknown_conversation" {
		t.Fatalf("expected unknown_conversation refusal, got %+v", we)
	}
}

func TestHistoryDocumentFormat(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first question",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)

	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "second question",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &reply.ConversationID,
	}

	reply2, err := h.Engine.Submit(req2)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	hash := reply2.ContextHash

	doc := history.New()
	turns, err := h.Store.GetConversationTurns(reply.ConversationID)
	if err != nil {
		t.Fatalf("get conversation turns: %v", err)
	}

	for _, turn := range turns {
		if turn.TerminalState == nil {
			continue
		}
		histTurn := history.Turn{
			ID:     turn.ID,
			State:  *turn.TerminalState,
			Input:  []byte{},
			Answer: []byte{},
			Label:  "reviewed",
		}
		if err := doc.AddTurn(histTurn); err != nil {
			t.Fatalf("add turn: %v", err)
		}
	}

	expectedHash := doc.Hash()
	if hash != expectedHash {
		t.Fatalf("stored hash %q != computed hash %q", hash, expectedHash)
	}
}

func TestSubmissionHashIncludesConversationAndWorkspace(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	reply1, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)

	convID := reply1.ConversationID

	conflictReq := wire.SubmitRequest{
		SubmissionID:   "s1",
		Input:          "question",
		Preset:         &[]string{"solo"}[0],
		Workspace:      "/different/workspace",
		ConversationID: &convID,
	}

	_, err = h.Engine.Submit(conflictReq)
	we := wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
	if we == nil {
		t.Fatalf("expected submission conflict when workspace differs")
	}

	conflictReq2 := wire.SubmitRequest{
		SubmissionID:   "s1",
		Input:          "different question",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &convID,
	}

	_, err = h.Engine.Submit(conflictReq2)
	we = wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
	if we == nil {
		t.Fatalf("expected submission conflict when input differs")
	}
}
