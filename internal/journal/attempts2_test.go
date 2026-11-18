package journal_test

import (
	"errors"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

// createTestRunAndTurn creates a test run and turn, returning their IDs.
func createTestRunAndTurn(t *testing.T, db *journal.DB, store *journal.Store) (string, string, error) {
	t.Helper()

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		// Create a run with SQL INSERT into the transaction
		var txErr error
		runID, _, txErr = tx.CreateRun([]byte("{}"))
		if txErr != nil {
			return txErr
		}

		// Create a turn
		turnID, txErr = tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}
		return nil
	})

	return runID, turnID, err
}

// TestBeginAttemptWithRoundTrip verifies BeginAttemptWith and GetAttempt round-trip all fields.
func TestBeginAttemptWithRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run, turn, and attempt in a single transaction
	var attemptID string
	var runID, turnID string
	deadline := time.Now().Add(1 * time.Hour)

	err := store.Tx(func(tx *journal.Tx) error {
		var txErr error
		runID, _, txErr = tx.CreateRun([]byte("{}"))
		if txErr != nil {
			return txErr
		}

		turnID, txErr = tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}

		// Use BeginAttemptWith
		params := journal.AttemptParams{
			TurnID:                turnID,
			Participant:           "participant1",
			Phase:                 "phase1",
			Epoch:                 42,
			Deadline:              &deadline,
			RepairOf:              "",
			Delimiter:             "delimiter-value",
			MaxOutputTokens:       2000,
			EstimatedPromptTokens: 1000,
		}

		attemptID, txErr = tx.BeginAttemptWith(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("failed to create run/turn/attempt: %v", err)
	}

	row, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}

	if row.ID != attemptID {
		t.Errorf("expected ID %s, got %s", attemptID, row.ID)
	}
	if row.TurnID != turnID {
		t.Errorf("expected TurnID %s, got %s", turnID, row.TurnID)
	}
	if row.Participant != "participant1" {
		t.Errorf("expected Participant participant1, got %s", row.Participant)
	}
	if row.Phase != "phase1" {
		t.Errorf("expected Phase phase1, got %s", row.Phase)
	}
	if row.Epoch != 42 {
		t.Errorf("expected Epoch 42, got %d", row.Epoch)
	}
	if row.State != "pending" {
		t.Errorf("expected State pending, got %s", row.State)
	}
	if row.RepairOf != nil {
		t.Errorf("expected RepairOf nil, got %v", row.RepairOf)
	}
	if row.Delimiter == nil || *row.Delimiter != "delimiter-value" {
		t.Errorf("expected Delimiter delimiter-value, got %v", row.Delimiter)
	}
	if row.MaxOutputTokens == nil || *row.MaxOutputTokens != 2000 {
		t.Errorf("expected MaxOutputTokens 2000, got %v", row.MaxOutputTokens)
	}
	if row.EstimatedPromptTokens == nil || *row.EstimatedPromptTokens != 1000 {
		t.Errorf("expected EstimatedPromptTokens 1000, got %v", row.EstimatedPromptTokens)
	}
	if row.Deadline == nil {
		t.Errorf("expected Deadline to be set")
	}
	// Deadline should round-trip correctly
	if row.Deadline != nil {
		diff := row.Deadline.Sub(deadline)
		if diff < -1*time.Second || diff > 1*time.Second {
			t.Errorf("expected Deadline close to %v, got %v (diff: %v)", deadline, row.Deadline, diff)
		}
	}
	if row.RetryCount == nil || *row.RetryCount != 0 {
		t.Errorf("expected RetryCount 0, got %v", row.RetryCount)
	}

	// Check dispatch rows exist
	var dispatchCount int
	err = db.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = ?`, attemptID).Scan(&dispatchCount)
	if err != nil {
		t.Fatalf("failed to query dispatch count: %v", err)
	}
	if dispatchCount != 1 {
		t.Errorf("expected 1 dispatch row, got %d", dispatchCount)
	}

	var dispatchIndex int
	err = db.Conn().QueryRow(`SELECT dispatch_index FROM attempt_dispatches WHERE attempt_id = ? ORDER BY dispatch_index`, attemptID).Scan(&dispatchIndex)
	if err != nil {
		t.Fatalf("failed to query dispatch index: %v", err)
	}
	if dispatchIndex != 0 {
		t.Errorf("expected dispatch_index 0, got %d", dispatchIndex)
	}
}

// TestBeginAttemptWithRepairOfRoundTrip verifies that non-empty RepairOf values round-trip correctly.
func TestBeginAttemptWithRepairOfRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID, turnID, firstAttemptID, secondAttemptID string
	deadline := time.Now().Add(1 * time.Hour)

	err := store.Tx(func(tx *journal.Tx) error {
		var txErr error
		runID, _, txErr = tx.CreateRun([]byte("{}"))
		if txErr != nil {
			return txErr
		}

		turnID, txErr = tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}

		// Create first attempt
		params := journal.AttemptParams{
			TurnID:                turnID,
			Participant:           "participant1",
			Phase:                 "phase1",
			Epoch:                 42,
			Deadline:              &deadline,
			RepairOf:              "",
			Delimiter:             "delimiter-value",
			MaxOutputTokens:       2000,
			EstimatedPromptTokens: 1000,
		}

		firstAttemptID, txErr = tx.BeginAttemptWith(params)
		if txErr != nil {
			return txErr
		}

		// Create second attempt with non-empty RepairOf pointing to first
		params.RepairOf = firstAttemptID
		secondAttemptID, txErr = tx.BeginAttemptWith(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("failed to create attempts: %v", err)
	}

	row, err := store.GetAttempt(secondAttemptID)
	if err != nil {
		t.Fatalf("failed to get second attempt: %v", err)
	}

	// Verify RepairOf is set and round-trips correctly
	if row.RepairOf == nil {
		t.Errorf("expected RepairOf to be set, got nil")
	} else if *row.RepairOf != firstAttemptID {
		t.Errorf("expected RepairOf %s, got %s", firstAttemptID, *row.RepairOf)
	}
}

// TestRecordRetryDispatch verifies retry dispatch recording and state enforcement.
func TestRecordRetryDispatch(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	// Record a retry dispatch
	retryCount, err := store.RecordRetryDispatch(attemptID)
	if err != nil {
		t.Fatalf("failed to record retry dispatch: %v", err)
	}

	if retryCount != 1 {
		t.Errorf("expected retry count 1, got %d", retryCount)
	}

	// Check dispatch rows
	var dispatchCount int
	err = conn.QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = ?`, attemptID).Scan(&dispatchCount)
	if err != nil {
		t.Fatalf("failed to query dispatch count: %v", err)
	}
	if dispatchCount != 2 {
		t.Errorf("expected 2 dispatch rows, got %d", dispatchCount)
	}

	// Verify dispatch indexes are 0 and 1
	rows, err := conn.Query(`SELECT dispatch_index FROM attempt_dispatches WHERE attempt_id = ? ORDER BY dispatch_index`, attemptID)
	if err != nil {
		t.Fatalf("failed to query dispatch indexes: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()

	var expectedIndexes []int
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			t.Fatalf("failed to scan dispatch index: %v", err)
		}
		expectedIndexes = append(expectedIndexes, idx)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(expectedIndexes) != 2 || expectedIndexes[0] != 0 || expectedIndexes[1] != 1 {
		t.Errorf("expected dispatch indexes [0, 1], got %v", expectedIndexes)
	}

	// Verify row via GetAttempt
	row, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}
	if row.RetryCount == nil || *row.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %v", row.RetryCount)
	}

	// Try to record on a completed attempt - should fail
	var tempTurnID string
	err = conn.QueryRow(`SELECT turn_id FROM attempts WHERE id = ?`, attemptID).Scan(&tempTurnID)
	if err != nil {
		t.Fatalf("failed to get turn id: %v", err)
	}

	// Complete the attempt
	err = store.CompleteAttempt(attemptID, "output", 1, "identity", "complete", "stop", "ok")
	if err != nil {
		t.Fatalf("failed to complete attempt: %v", err)
	}

	// Try to record retry on completed attempt - should error
	_, err = store.RecordRetryDispatch(attemptID)
	if err == nil {
		t.Errorf("expected error when recording retry on completed attempt")
	}
}

// TestSetAttemptUsageGateCommentary verifies usage, gate, and commentary round-trip.
func TestSetAttemptUsageGateCommentary(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	// Initially, no usage set - should be nil
	row, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}
	if row.UsagePrompt != nil || row.UsageCompletion != nil {
		t.Errorf("expected nil usage fields before setting, got prompt=%v, completion=%v", row.UsagePrompt, row.UsageCompletion)
	}

	// Set usage
	err = store.SetAttemptUsage(attemptID, 500, 300)
	if err != nil {
		t.Fatalf("failed to set usage: %v", err)
	}

	row, err = store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}
	if row.UsagePrompt == nil || *row.UsagePrompt != 500 {
		t.Errorf("expected UsagePrompt 500, got %v", row.UsagePrompt)
	}
	if row.UsageCompletion == nil || *row.UsageCompletion != 300 {
		t.Errorf("expected UsageCompletion 300, got %v", row.UsageCompletion)
	}

	// Set gate error
	err = store.SetAttemptGate(attemptID, "gate_error_message")
	if err != nil {
		t.Fatalf("failed to set gate: %v", err)
	}

	row, err = store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}
	if row.GateError == nil || *row.GateError != "gate_error_message" {
		t.Errorf("expected GateError gate_error_message, got %v", row.GateError)
	}

	// Set commentary
	err = store.SetAttemptCommentary(attemptID, "some commentary")
	if err != nil {
		t.Fatalf("failed to set commentary: %v", err)
	}

	row, err = store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to get attempt: %v", err)
	}
	if row.Commentary == nil || *row.Commentary != "some commentary" {
		t.Errorf("expected Commentary some commentary, got %v", row.Commentary)
	}
}

// TestOpenHoldUnreleasedHolds verifies hold opening and unreleased holds querying.
func TestOpenHoldUnreleasedHolds(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	hold := journal.Hold{
		AttemptID:    attemptID,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group: []journal.GroupMember{
			{EndpointName: "endpoint1", Model: "model1"},
			{EndpointName: "endpoint2", Model: "model2"},
		},
		HeldSince: time.Now().UTC(),
	}

	err = store.OpenHold(hold)
	if err != nil {
		t.Fatalf("failed to open hold: %v", err)
	}

	// Query unreleased holds
	holds, err := store.UnreleasedHolds()
	if err != nil {
		t.Fatalf("failed to query unreleased holds: %v", err)
	}

	if len(holds) != 1 {
		t.Errorf("expected 1 hold, got %d", len(holds))
	}

	retrievedHold := holds[0]
	if retrievedHold.AttemptID != attemptID {
		t.Errorf("expected AttemptID %s, got %s", attemptID, retrievedHold.AttemptID)
	}
	if retrievedHold.Pool != "pool1" {
		t.Errorf("expected Pool pool1, got %s", retrievedHold.Pool)
	}
	if len(retrievedHold.Group) != 2 {
		t.Errorf("expected 2 group members, got %d", len(retrievedHold.Group))
	}
	if retrievedHold.Group[0].EndpointName != "endpoint1" || retrievedHold.Group[0].Model != "model1" {
		t.Errorf("expected first member endpoint1/model1, got %s/%s", retrievedHold.Group[0].EndpointName, retrievedHold.Group[0].Model)
	}
}

// TestHoldGroupJSONEquality verifies that identical groups with different ordering serialize the same.
func TestHoldGroupJSONEquality(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	// Create two attempts to test with
	attemptID1, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt 1: %v", err)
	}

	attemptID2, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p2",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt 2: %v", err)
	}

	// Create holds with same members in different order
	group1 := []journal.GroupMember{
		{EndpointName: "endpoint1", Model: "model1"},
		{EndpointName: "endpoint2", Model: "model2"},
	}

	group2 := []journal.GroupMember{
		{EndpointName: "endpoint2", Model: "model2"},
		{EndpointName: "endpoint1", Model: "model1"},
	}

	hold1 := journal.Hold{
		AttemptID:    attemptID1,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group:        group1,
		HeldSince:    time.Now().UTC(),
	}

	hold2 := journal.Hold{
		AttemptID:    attemptID2,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group:        group2,
		HeldSince:    time.Now().UTC().Add(1 * time.Millisecond),
	}

	err = store.OpenHold(hold1)
	if err != nil {
		t.Fatalf("failed to open hold1: %v", err)
	}

	err = store.OpenHold(hold2)
	if err != nil {
		t.Fatalf("failed to open hold2: %v", err)
	}

	// Read the group_json from both directly
	var groupJSON1, groupJSON2 string
	err = db.Conn().QueryRow(`SELECT group_json FROM capacity_holds WHERE attempt_id = ?`, attemptID1).Scan(&groupJSON1)
	if err != nil {
		t.Fatalf("failed to query group_json1: %v", err)
	}

	err = db.Conn().QueryRow(`SELECT group_json FROM capacity_holds WHERE attempt_id = ?`, attemptID2).Scan(&groupJSON2)
	if err != nil {
		t.Fatalf("failed to query group_json2: %v", err)
	}

	if groupJSON1 != groupJSON2 {
		t.Errorf("expected identical group_json, got:\n  %s\n  %s", groupJSON1, groupJSON2)
	}
}

// TestReleaseHold verifies hold release and error conditions.
func TestReleaseHold(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	hold := journal.Hold{
		AttemptID:    attemptID,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group: []journal.GroupMember{
			{EndpointName: "endpoint1", Model: "model1"},
		},
		HeldSince: time.Now().UTC(),
	}

	err = store.OpenHold(hold)
	if err != nil {
		t.Fatalf("failed to open hold: %v", err)
	}

	// Release the hold
	err = store.ReleaseHold(attemptID, "server_confirmed", "confirmed by server")
	if err != nil {
		t.Fatalf("failed to release hold: %v", err)
	}

	// Should not be in unreleased holds anymore
	holds, err := store.UnreleasedHolds()
	if err != nil {
		t.Fatalf("failed to query unreleased holds: %v", err)
	}
	if len(holds) != 0 {
		t.Errorf("expected 0 unreleased holds after release, got %d", len(holds))
	}

	// Verify released_how and released_reason are persisted
	var releasedHow, releasedReason string
	err = db.Conn().QueryRow(`SELECT released_how, released_reason FROM capacity_holds WHERE attempt_id = ?`, attemptID).Scan(&releasedHow, &releasedReason)
	if err != nil {
		t.Fatalf("failed to query released hold: %v", err)
	}
	if releasedHow != "server_confirmed" {
		t.Errorf("expected released_how server_confirmed, got %s", releasedHow)
	}
	if releasedReason != "confirmed by server" {
		t.Errorf("expected released_reason 'confirmed by server', got %s", releasedReason)
	}

	// Try to release again - should error with ErrNoSuchHold
	err = store.ReleaseHold(attemptID, "server_confirmed", "confirmed by server")
	if !errors.Is(err, journal.ErrNoSuchHold) {
		t.Errorf("expected ErrNoSuchHold on second release, got %v", err)
	}

	// Try to release a non-existent hold - should error with ErrNoSuchHold
	err = store.ReleaseHold("nonexistent-attempt", "server_confirmed", "confirmed by server")
	if !errors.Is(err, journal.ErrNoSuchHold) {
		t.Errorf("expected ErrNoSuchHold on nonexistent hold, got %v", err)
	}

	// Try to release with invalid how - should error
	attemptID2, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p2",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt 2: %v", err)
	}

	hold2 := journal.Hold{
		AttemptID:    attemptID2,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group: []journal.GroupMember{
			{EndpointName: "endpoint1", Model: "model1"},
		},
		HeldSince: time.Now().UTC(),
	}

	err = store.OpenHold(hold2)
	if err != nil {
		t.Fatalf("failed to open hold2: %v", err)
	}

	err = store.ReleaseHold(attemptID2, "invalid_how", "some reason")
	if err == nil {
		t.Errorf("expected error for invalid how method")
	}
}

// TestOpenHoldTxRollback verifies that a transaction error leaves no hold.
func TestOpenHoldTxRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	// Use Tx to open a hold then return an error
	testErr := errors.New("test error")
	returnedErr := store.Tx(func(tx *journal.Tx) error {
		hold := journal.Hold{
			AttemptID:    attemptID,
			Pool:         "pool1",
			EndpointName: "endpoint1",
			Model:        "model1",
			Group: []journal.GroupMember{
				{EndpointName: "endpoint1", Model: "model1"},
			},
			HeldSince: time.Now().UTC(),
		}
		if err := tx.OpenHold(hold); err != nil {
			return err
		}
		return testErr
	})

	if !errors.Is(returnedErr, testErr) {
		t.Errorf("expected test error, got %v", returnedErr)
	}

	// Verify that no hold was created
	holds, err := store.UnreleasedHolds()
	if err != nil {
		t.Fatalf("failed to query unreleased holds: %v", err)
	}
	if len(holds) != 0 {
		t.Errorf("expected 0 holds after Tx rollback, got %d", len(holds))
	}
}

// TestOpenBlockFor verifies block opening with namespaced IDs and reviewer block ID storage.
func TestOpenBlockFor(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	// Create two attempts and verdicts with blocks
	attemptID1, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt 1: %v", err)
	}

	attemptID2, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p2",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt 2: %v", err)
	}

	// Create verdicts
	verdictID1, err := store.RecordVerdict(attemptID1, "hash1", "block", "reason1", "valid")
	if err != nil {
		t.Fatalf("failed to record verdict 1: %v", err)
	}

	verdictID2, err := store.RecordVerdict(attemptID2, "hash2", "block", "reason2", "valid")
	if err != nil {
		t.Fatalf("failed to record verdict 2: %v", err)
	}

	// Open blocks with same reviewer block ID but different verdicts
	blockID1, err := store.OpenBlockFor(verdictID1, "b1", "candidate-hash-1", "low", "evidence1", "remedy1")
	if err != nil {
		t.Fatalf("failed to open block 1: %v", err)
	}

	blockID2, err := store.OpenBlockFor(verdictID2, "b1", "candidate-hash-2", "medium", "evidence2", "remedy2")
	if err != nil {
		t.Fatalf("failed to open block 2: %v", err)
	}

	// Verify block IDs are different (namespaced)
	if blockID1 == blockID2 {
		t.Errorf("expected different block IDs, got both %s", blockID1)
	}

	// Verify block IDs contain verdict ID and reviewer block ID
	expectedBlockID1 := verdictID1 + ":b1"
	expectedBlockID2 := verdictID2 + ":b1"
	if blockID1 != expectedBlockID1 {
		t.Errorf("expected block ID1 %s, got %s", expectedBlockID1, blockID1)
	}
	if blockID2 != expectedBlockID2 {
		t.Errorf("expected block ID2 %s, got %s", expectedBlockID2, blockID2)
	}

	// Retrieve blocks using OpenBlocks
	blocks, err := store.OpenBlocks(turnID)
	if err != nil {
		t.Fatalf("failed to query open blocks: %v", err)
	}

	// Find our two blocks
	var block1, block2 *journal.Block
	for i := range blocks {
		switch blocks[i].ID {
		case blockID1:
			block1 = &blocks[i]
		case blockID2:
			block2 = &blocks[i]
		}
	}

	if block1 == nil || block2 == nil {
		t.Fatalf("failed to find created blocks")
	}

	// Verify ReviewerBlockID is stored correctly
	if block1.ReviewerBlockID != "b1" {
		t.Errorf("expected block1 ReviewerBlockID b1, got %s", block1.ReviewerBlockID)
	}
	if block2.ReviewerBlockID != "b1" {
		t.Errorf("expected block2 ReviewerBlockID b1, got %s", block2.ReviewerBlockID)
	}
}

// TestOpenHoldEmptyGroup verifies that opening a hold with empty group errors.
func TestOpenHoldEmptyGroup(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	// Try to open a hold with empty group
	hold := journal.Hold{
		AttemptID:    attemptID,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group:        []journal.GroupMember{},
		HeldSince:    time.Now().UTC(),
	}

	err = store.OpenHold(hold)
	if err == nil {
		t.Errorf("expected error when opening hold with empty group")
	}
}

// TestOpenHoldDuplicateAttempt verifies that opening a second hold for same attempt errors.
func TestOpenHoldDuplicateAttempt(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run and turn
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}

	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{
		TurnID:      turnID,
		Participant: "p1",
		Phase:       "phase1",
		Epoch:       1,
	})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}

	hold := journal.Hold{
		AttemptID:    attemptID,
		Pool:         "pool1",
		EndpointName: "endpoint1",
		Model:        "model1",
		Group: []journal.GroupMember{
			{EndpointName: "endpoint1", Model: "model1"},
		},
		HeldSince: time.Now().UTC(),
	}

	// Open first hold
	err = store.OpenHold(hold)
	if err != nil {
		t.Fatalf("failed to open first hold: %v", err)
	}

	// Try to open second hold for same attempt
	err = store.OpenHold(hold)
	if err == nil {
		t.Errorf("expected error when opening second hold for same attempt")
	}
}

func TestSetAttemptEstimate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatalf("failed to create run/turn: %v", err)
	}
	attemptID, err := store.BeginAttemptWith(journal.AttemptParams{TurnID: turnID, Participant: "p1", Phase: "phase1", Epoch: 1})
	if err != nil {
		t.Fatalf("failed to begin attempt: %v", err)
	}
	if err := store.Tx(func(tx *journal.Tx) error { return tx.SetAttemptEstimate(attemptID, 123) }); err != nil {
		t.Fatalf("SetAttemptEstimate: %v", err)
	}
	row, err := store.GetAttempt(attemptID)
	if err != nil || row.EstimatedPromptTokens == nil || *row.EstimatedPromptTokens != 123 {
		t.Fatalf("attempt = %+v, err %v", row, err)
	}
}
