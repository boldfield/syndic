package engine_test

import (
	"bytes"
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

func TestFrozenHistoryContainsRealData(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first question with specific text",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}

	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)

	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "second question",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &reply1.ConversationID,
	}

	reply2, err := h.Engine.Submit(req2)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	// The second turn's history should contain the first turn's real input
	if reply2.ContextHash == "" {
		t.Fatal("second turn should have context_hash")
	}

	// Verify the history document was stored with real data
	var historyBytes []byte
	if err := h.DB.Conn().QueryRow(
		`SELECT history_bytes FROM turns WHERE id = ?`, reply2.TurnID,
	).Scan(&historyBytes); err != nil {
		t.Fatalf("query history_bytes: %v", err)
	}

	if len(historyBytes) == 0 {
		t.Fatal("history_bytes should not be empty")
	}

	// Parse the history document and verify it contains the real input
	historyDoc, err := history.Parse(historyBytes)
	if err != nil {
		t.Fatalf("parse history document: %v", err)
	}

	turns := historyDoc.Included()
	if len(turns) == 0 {
		t.Fatal("history should include at least one turn")
	}

	if !bytes.Contains(turns[0].Input, []byte("first question with specific text")) {
		t.Fatalf("history input should contain the actual input text, got: %s", turns[0].Input)
	}
}

func TestContextFromTurnFiltersHistory(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	// Create first turn
	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first input",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}
	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)

	// Create second turn
	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "second input",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &reply1.ConversationID,
	}
	reply2, err := h.Engine.Submit(req2)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	h.WaitTurnState(reply2.TurnID, engine.StateSoloComplete)
	turn2ID := reply2.TurnID

	// Create third turn with context_from_turn set to skip the first turn
	req3 := wire.SubmitRequest{
		SubmissionID:    "s3",
		Input:           "third input",
		Preset:          &[]string{"solo"}[0],
		Workspace:       workspace,
		ConversationID:  &reply1.ConversationID,
		ContextFromTurn: &turn2ID,
	}
	reply3, err := h.Engine.Submit(req3)
	if err != nil {
		t.Fatalf("third submit: %v", err)
	}

	// Verify history_bytes includes only turns starting from turn2
	var historyBytes []byte
	if err := h.DB.Conn().QueryRow(
		`SELECT history_bytes FROM turns WHERE id = ?`, reply3.TurnID,
	).Scan(&historyBytes); err != nil {
		t.Fatalf("query history_bytes: %v", err)
	}

	historyDoc, err := history.Parse(historyBytes)
	if err != nil {
		t.Fatalf("parse history document: %v", err)
	}

	turns := historyDoc.Included()
	if len(turns) != 1 {
		t.Fatalf("history with context_from_turn should have 1 turn, got %d", len(turns))
	}

	if turns[0].ID != turn2ID {
		t.Fatalf("history should include turn2, got %s", turns[0].ID)
	}

	// But the hash should still be non-empty
	if reply3.ContextHash == "" {
		t.Fatal("context_hash should be set even with context_from_turn")
	}
}

func TestTaintPersistsAcrossConversations(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	workspace := "/workspace/test"

	// Create first turn
	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first input",
		Preset:       &[]string{"solo"}[0],
		Workspace:    workspace,
	}
	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Wait for first turn to complete before marking it as tainted
	h.WaitTurnState(reply1.TurnID, engine.StateSoloComplete)

	// Mark the first turn as file_tainted
	if _, err := h.DB.Conn().Exec(`UPDATE turns SET file_tainted = 1 WHERE id = ?`, reply1.TurnID); err != nil {
		t.Fatalf("mark first turn as tainted: %v", err)
	}

	// Create second turn in the same conversation
	convID := reply1.ConversationID
	req2 := wire.SubmitRequest{
		SubmissionID:   "s2",
		Input:          "second input",
		Preset:         &[]string{"solo"}[0],
		Workspace:      workspace,
		ConversationID: &convID,
	}
	reply2, err := h.Engine.Submit(req2)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	// Verify second turn is also marked as tainted
	var fileTainted bool
	if err := h.DB.Conn().QueryRow(
		`SELECT file_tainted FROM turns WHERE id = ?`, reply2.TurnID,
	).Scan(&fileTainted); err != nil {
		t.Fatalf("query file_tainted: %v", err)
	}

	if !fileTainted {
		t.Fatal("second turn should be marked as tainted due to conversation history")
	}

	// Even with context_from_turn set to skip the tainted turn, the new turn should still be tainted
	h.WaitTurnState(reply2.TurnID, engine.StateSoloComplete)

	req3 := wire.SubmitRequest{
		SubmissionID:    "s3",
		Input:           "third input",
		Preset:          &[]string{"solo"}[0],
		Workspace:       workspace,
		ConversationID:  &convID,
		ContextFromTurn: &reply1.TurnID,
	}
	reply3, err := h.Engine.Submit(req3)
	if err != nil {
		t.Fatalf("third submit: %v", err)
	}

	// Verify third turn is also marked as tainted, even though context_from_turn skips the tainted turn
	fileTainted = false
	if err := h.DB.Conn().QueryRow(
		`SELECT file_tainted FROM turns WHERE id = ?`, reply3.TurnID,
	).Scan(&fileTainted); err != nil {
		t.Fatalf("query file_tainted for third turn: %v", err)
	}

	if !fileTainted {
		t.Fatal("third turn should be tainted even with context_from_turn, because taint survives trimming")
	}
}

// Add import for bytes package at the top of the file if needed
var _ = ""

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

	// Retrieve the stored history bytes for the second turn
	var historyBytes []byte
	if err := h.DB.Conn().QueryRow(
		`SELECT history_bytes FROM turns WHERE id = ?`, reply2.TurnID,
	).Scan(&historyBytes); err != nil {
		t.Fatalf("query history_bytes: %v", err)
	}

	// Parse the history document and verify the hash matches
	doc, err := history.Parse(historyBytes)
	if err != nil {
		t.Fatalf("parse history document: %v", err)
	}

	if doc.Hash() != hash {
		t.Fatalf("stored hash %q != computed hash of document %q", hash, doc.Hash())
	}

	// Verify the history includes the first turn with correct label
	turns := doc.Included()
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn in history, got %d", len(turns))
	}

	if turns[0].Label != "unreviewed (solo)" {
		t.Fatalf("expected label 'unreviewed (solo)', got %q", turns[0].Label)
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
