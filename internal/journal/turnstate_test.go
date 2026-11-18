package journal_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func setupTurnstateTestData(t *testing.T, conn *sql.DB) (runID, turnID, attemptID string) {
	t.Helper()
	runID = "test-run-" + t.Name()
	turnID = "test-turn-" + t.Name()
	attemptID = "test-attempt-" + t.Name()

	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES (?, '{}', 'hash')`, runID)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES (?, ?, 'review')`, turnID, runID)
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES (?, ?, 'p1', 'author', 0, '2026-11-18T10:00:00Z')`, attemptID, turnID)
	return runID, turnID, attemptID
}

func TestEndTurnFreshTurnToApproved(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "approved", "test reason")
	})
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	var state, reason sql.NullString
	err = conn.QueryRow(`SELECT terminal_state, terminal_reason FROM turns WHERE id = ?`, turnID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "approved" {
		t.Errorf("terminal_state = %v, want 'approved'", state)
	}
	if !reason.Valid || reason.String != "test reason" {
		t.Errorf("terminal_reason = %v, want 'test reason'", reason)
	}
}

func TestEndTurnFreshTurnToSoloComplete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "solo_complete", "completed")
	})
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "solo_complete" {
		t.Errorf("terminal_state = %v, want 'solo_complete'", state)
	}
}

func TestEndTurnFreshTurnToIncomplete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "incomplete", "needs review")
	})
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "incomplete" {
		t.Errorf("terminal_state = %v, want 'incomplete'", state)
	}
}

func TestEndTurnFreshTurnToCancelled(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "cancelled", "user cancelled")
	})
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "cancelled" {
		t.Errorf("terminal_state = %v, want 'cancelled'", state)
	}
}

func TestEndTurnSecondEndTurnErrors(t *testing.T) {
	tests := []struct {
		name          string
		firstState    string
		firstReason   string
		secondState   string
		secondReason  string
		needCandidate bool
	}{
		{
			name:         "approved to incomplete",
			firstState:   "approved",
			firstReason:  "first",
			secondState:  "incomplete",
			secondReason: "second",
		},
		{
			name:         "solo_complete to approved",
			firstState:   "solo_complete",
			firstReason:  "done",
			secondState:  "approved",
			secondReason: "try again",
		},
		{
			name:         "incomplete to cancelled",
			firstState:   "incomplete",
			firstReason:  "needs review",
			secondState:  "cancelled",
			secondReason: "user cancelled",
		},
		{
			name:         "cancelled to approved",
			firstState:   "cancelled",
			firstReason:  "user cancel",
			secondState:  "approved",
			secondReason: "override",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, cleanup := openTestDB(t)
			defer cleanup()
			conn := db.Conn()
			_, turnID, _ := setupTurnstateTestData(t, conn)

			store := journal.NewStore(db)

			// First EndTurn - should succeed
			err := store.Tx(func(tx *journal.Tx) error {
				return tx.EndTurn(turnID, tt.firstState, tt.firstReason)
			})
			if err != nil {
				t.Fatalf("first EndTurn failed: %v", err)
			}

			// For all cases, second EndTurn should error
			err = store.Tx(func(tx *journal.Tx) error {
				return tx.EndTurn(turnID, tt.secondState, tt.secondReason)
			})
			if err == nil {
				t.Error("second EndTurn should have errored")
			}

			// Verify that terminal_state and terminal_reason remain from first call
			var state, reason sql.NullString
			err = conn.QueryRow(`SELECT terminal_state, terminal_reason FROM turns WHERE id = ?`, turnID).Scan(&state, &reason)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Valid || state.String != tt.firstState {
				t.Errorf("after failed EndTurn, terminal_state = %v, want %v", state, tt.firstState)
			}
			if !reason.Valid || reason.String != tt.firstReason {
				t.Errorf("after failed EndTurn, terminal_reason = %v, want %v", reason, tt.firstReason)
			}
		})
	}
}

func TestEndTurnOnAlreadyOverriddenErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)

	// Reach overridden state via incomplete -> FreezeCandidate -> EndTurn(overridden)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}
		_, err := tx.FreezeCandidate(turnID, "content", "hash123")
		if err != nil {
			return err
		}
		return tx.EndTurn(turnID, "overridden", "override")
	})
	if err != nil {
		t.Fatalf("reaching overridden state failed: %v", err)
	}

	// Now call EndTurn again on the already-overridden turn
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "approved", "try again")
	})
	if err == nil {
		t.Error("EndTurn on already-overridden turn should have errored")
	}

	// Verify that terminal_state and terminal_reason remain unchanged
	var state, reason sql.NullString
	err = conn.QueryRow(`SELECT terminal_state, terminal_reason FROM turns WHERE id = ?`, turnID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "overridden" {
		t.Errorf("after failed EndTurn on overridden, terminal_state = %v, want 'overridden'", state)
	}
	if !reason.Valid || reason.String != "override" {
		t.Errorf("after failed EndTurn on overridden, terminal_reason = %v, want 'override'", reason)
	}
}

func TestEndTurnIncompleteToOverriddenWithCandidate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}
		candidateID, err := tx.FreezeCandidate(turnID, "content", "hash123")
		if err != nil {
			return err
		}
		if candidateID == "" {
			t.Fatal("candidateID is empty")
		}
		return tx.EndTurn(turnID, "overridden", "override")
	})
	if err != nil {
		t.Fatalf("EndTurn transition failed: %v", err)
	}

	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "overridden" {
		t.Errorf("terminal_state = %v, want 'overridden'", state)
	}
}

func TestEndTurnIncompleteToOverriddenWithoutCandidateErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}
		return tx.EndTurn(turnID, "overridden", "override")
	})
	if err == nil {
		t.Error("EndTurn to overridden without candidate should have errored")
	}
}

func TestEndTurnCancelledToOverriddenErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "cancelled", "user cancel"); err != nil {
			return err
		}
		return tx.EndTurn(turnID, "overridden", "override")
	})
	if err == nil {
		t.Error("cancelled to overridden should have errored")
	}
}

func TestEndTurnEmptyReasonErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID, "approved", "")
	})
	if err == nil {
		t.Error("EndTurn with empty reason should have errored")
	}
}

func TestOverrideTurnIncompleteWithTwoBlocks(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)

	// Create a second attempt on the same turn for the verdicts/blocks
	attemptID2 := "test-attempt-2-" + t.Name()
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES (?, ?, 'p2', 'review', 0, '2026-11-18T10:00:00Z')`, attemptID2, turnID)

	var verdictID1, verdictID2, blockID1, blockID2 string

	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}
		candidateID, err := tx.FreezeCandidate(turnID, "content", "hash123")
		if err != nil || candidateID == "" {
			return err
		}

		// Create verdicts with parsed_verdict = "block" on the second attempt
		verdictID1, err = tx.RecordVerdict(attemptID2, "hash123", "block", "reason1", "valid")
		if err != nil {
			return err
		}

		verdictID2, err = tx.RecordVerdict(attemptID2, "hash123", "block", "reason2", "valid")
		if err != nil {
			return err
		}

		blockID1 = "v1:b1"
		if err := tx.OpenBlock(verdictID1, blockID1, "hash123", "high", "evidence1", "remedy1"); err != nil {
			return err
		}

		blockID2 = "v2:b2"
		if err := tx.OpenBlock(verdictID2, blockID2, "hash123", "low", "evidence2", "remedy2"); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		return tx.OverrideTurn(turnID, "test override")
	})
	if err != nil {
		t.Fatalf("OverrideTurn failed: %v", err)
	}

	// Check turn is overridden with correct terminal_reason
	var state, reason sql.NullString
	err = conn.QueryRow(`SELECT terminal_state, terminal_reason FROM turns WHERE id = ?`, turnID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "overridden" {
		t.Errorf("terminal_state = %v, want 'overridden'", state)
	}
	if !reason.Valid || reason.String != "override" {
		t.Errorf("terminal_reason = %v, want 'override'", reason)
	}

	// Check both blocks are overridden
	var blockStates []string
	rows, err := conn.Query(`SELECT state FROM blocks WHERE id IN (?, ?)`, blockID1, blockID2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		blockStates = append(blockStates, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(blockStates) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(blockStates))
	}
	for _, s := range blockStates {
		if s != "overridden" {
			t.Errorf("block state = %v, want 'overridden'", s)
		}
	}

	// Check override event exists and is tied to the turn
	var eventType string
	var eventData []byte
	var eventTurnID sql.NullString
	err = conn.QueryRow(`SELECT event_type, data, turn_id FROM events WHERE event_type = 'override'`).Scan(&eventType, &eventData, &eventTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if eventType != "override" {
		t.Errorf("event_type = %v, want 'override'", eventType)
	}
	if !eventTurnID.Valid || eventTurnID.String != turnID {
		t.Errorf("event turn_id = %v, want %v", eventTurnID, turnID)
	}

	var eventPayload map[string]string
	if err := json.Unmarshal(eventData, &eventPayload); err != nil {
		t.Fatalf("failed to unmarshal event data: %v", err)
	}
	if eventPayload["turn_id"] != turnID {
		t.Errorf("event payload turn_id = %v, want %v", eventPayload["turn_id"], turnID)
	}
	if eventPayload["reason"] != "test override" {
		t.Errorf("event payload reason = %v, want 'test override'", eventPayload["reason"])
	}
}

func TestOverrideTurnWithoutCandidateRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)

	// Create a second attempt and open blocks
	attemptID2 := "test-attempt-2-" + t.Name()
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES (?, ?, 'p2', 'review', 0, '2026-11-18T10:00:00Z')`, attemptID2, turnID)

	var verdictID1, blockID1 string

	// Set up the turn with blocks but no candidate (first transaction - should succeed)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}

		// Create verdict and open block
		vid, err := tx.RecordVerdict(attemptID2, "hash123", "block", "reason1", "valid")
		if err != nil {
			return err
		}
		verdictID1 = vid

		blockID1 = "v1:b1"
		if err := tx.OpenBlock(verdictID1, blockID1, "hash123", "high", "evidence1", "remedy1"); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Verify setup succeeded: turn is incomplete, block is open
	var setupState sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&setupState)
	if err != nil {
		t.Fatal(err)
	}
	if !setupState.Valid || setupState.String != "incomplete" {
		t.Errorf("after setup, terminal_state = %v, want 'incomplete'", setupState)
	}

	// Now try to override without candidate (separate transaction - should fail and not change anything)
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.OverrideTurn(turnID, "test override")
	})
	if err == nil {
		t.Error("OverrideTurn without candidate should have errored")
	}

	// Verify the OverrideTurn call didn't change anything: turn should still be incomplete
	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Valid || state.String != "incomplete" {
		t.Errorf("after failed OverrideTurn, terminal_state = %v, want 'incomplete'", state)
	}

	// Verify block state is still open (unchanged)
	var blockState string
	err = conn.QueryRow(`SELECT state FROM blocks WHERE id = ?`, blockID1).Scan(&blockState)
	if err != nil {
		t.Fatal(err)
	}
	if blockState != "open" {
		t.Errorf("after failed OverrideTurn, block state = %v, want 'open'", blockState)
	}

	// No override event should exist
	var count int
	err = conn.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type = 'override'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("after failed OverrideTurn, found %d override events, want 0", count)
	}
}

func TestBumpEpochIncrementsCorrectly(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	var epoch1, epoch2 int

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		e, err := tx.BumpEpoch(turnID)
		if err != nil {
			return err
		}
		epoch1 = e
		return nil
	})
	if err != nil {
		t.Fatalf("first BumpEpoch failed: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		e, err := tx.BumpEpoch(turnID)
		if err != nil {
			return err
		}
		epoch2 = e
		return nil
	})
	if err != nil {
		t.Fatalf("second BumpEpoch failed: %v", err)
	}

	if epoch1 != 1 {
		t.Errorf("first BumpEpoch returned %d, want 1", epoch1)
	}
	if epoch2 != 2 {
		t.Errorf("second BumpEpoch returned %d, want 2", epoch2)
	}
}

func TestAcceptResultMatchingEpochAccepts(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, attemptID := setupTurnstateTestData(t, conn)

	out := journal.AttemptOutput{
		RawScrubbed:     "output",
		ScrubCount:      1,
		Identity:        "test-id",
		Status:          "complete",
		NativeFinish:    "complete",
		TransportStatus: "ok",
	}

	var accepted bool
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		epoch, err := tx.BumpEpoch(turnID)
		if err != nil {
			return err
		}
		var accErr error
		accepted, accErr = tx.AcceptResult(attemptID, epoch, "", out)
		return accErr
	})
	if err != nil {
		t.Fatalf("AcceptResult failed: %v", err)
	}

	if !accepted {
		t.Error("AcceptResult returned false, want true")
	}

	// Verify attempt is completed and not quarantined
	var status string
	var quarantined int
	err = conn.QueryRow(`SELECT status, quarantined FROM attempts WHERE id = ?`, attemptID).Scan(&status, &quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Errorf("attempt status = %v, want 'complete'", status)
	}
	if quarantined != 0 {
		t.Errorf("attempt should not be quarantined, got quarantined = %v", quarantined)
	}
}

func TestAcceptResultStaleEpochQuarantines(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, attemptID := setupTurnstateTestData(t, conn)

	out := journal.AttemptOutput{
		RawScrubbed:     "output",
		ScrubCount:      1,
		Identity:        "test-id",
		Status:          "complete",
		NativeFinish:    "complete",
		TransportStatus: "ok",
	}

	var accepted bool
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.BumpEpoch(turnID); err != nil {
			return err
		}
		if _, err := tx.BumpEpoch(turnID); err != nil {
			return err
		}
		var accErr error
		accepted, accErr = tx.AcceptResult(attemptID, 1, "", out)
		return accErr
	})
	if err != nil {
		t.Fatalf("AcceptResult failed: %v", err)
	}

	if accepted {
		t.Error("AcceptResult returned true, want false")
	}

	// Verify attempt is quarantined
	var quarantined int
	err = conn.QueryRow(`SELECT quarantined FROM attempts WHERE id = ?`, attemptID).Scan(&quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined != 1 {
		t.Errorf("attempt should be quarantined, got quarantined = %v", quarantined)
	}

	// Verify turn state is unchanged
	var state sql.NullString
	err = conn.QueryRow(`SELECT terminal_state FROM turns WHERE id = ?`, turnID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if state.Valid {
		t.Errorf("turn should not have terminal_state, got %v", state)
	}
}

func TestAcceptResultIncompleteTerminalState(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, attemptID1 := setupTurnstateTestData(t, conn)

	// Create a second attempt for the matching state test
	attemptID2 := "test-attempt-2-" + t.Name()
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES (?, ?, 'p1', 'author', 0, '2026-11-18T10:00:00Z')`, attemptID2, turnID)

	out := journal.AttemptOutput{
		RawScrubbed:     "output",
		ScrubCount:      1,
		Identity:        "test-id",
		Status:          "complete",
		NativeFinish:    "complete",
		TransportStatus: "ok",
	}

	var accepted bool
	store := journal.NewStore(db)

	// Test 1: expectedTerminalState empty should fail on incomplete turn
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.EndTurn(turnID, "incomplete", "needs review"); err != nil {
			return err
		}
		epoch, err := tx.BumpEpoch(turnID)
		if err != nil {
			return err
		}
		var accErr error
		accepted, accErr = tx.AcceptResult(attemptID1, epoch, "", out)
		return accErr
	})
	if err != nil {
		t.Fatalf("AcceptResult test 1 failed: %v", err)
	}

	if accepted {
		t.Error("AcceptResult with empty expectedTerminalState on incomplete turn returned true, want false")
	}

	// Verify attemptID1 is quarantined
	var attemptID1Quarantined int
	err = conn.QueryRow(`SELECT quarantined FROM attempts WHERE id = ?`, attemptID1).Scan(&attemptID1Quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if attemptID1Quarantined != 1 {
		t.Errorf("attemptID1 should be quarantined, got quarantined = %v", attemptID1Quarantined)
	}

	// Test 2: with matching terminal state
	err = store.Tx(func(tx *journal.Tx) error {
		epoch, err := tx.BumpEpoch(turnID)
		if err != nil {
			return err
		}
		var accErr error
		accepted, accErr = tx.AcceptResult(attemptID2, epoch, "incomplete", out)
		return accErr
	})
	if err != nil {
		t.Fatalf("AcceptResult test 2 failed: %v", err)
	}

	if !accepted {
		t.Error("AcceptResult with matching terminal state returned false, want true")
	}

	// Verify attemptID2 is completed and not quarantined
	var attemptID2Status string
	var attemptID2Quarantined int
	err = conn.QueryRow(`SELECT status, quarantined FROM attempts WHERE id = ?`, attemptID2).Scan(&attemptID2Status, &attemptID2Quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if attemptID2Status != "complete" {
		t.Errorf("attemptID2 status = %v, want 'complete'", attemptID2Status)
	}
	if attemptID2Quarantined != 0 {
		t.Errorf("attemptID2 should not be quarantined, got quarantined = %v", attemptID2Quarantined)
	}
}

func TestFreezeCandidateSets(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	var candidateID string
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		cid, err := tx.FreezeCandidate(turnID, "content", "hash123")
		if err != nil {
			return err
		}
		candidateID = cid
		return nil
	})
	if err != nil {
		t.Fatalf("FreezeCandidate failed: %v", err)
	}

	if candidateID == "" {
		t.Error("candidateID is empty")
	}

	var storedCandidateID sql.NullString
	err = conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, turnID).Scan(&storedCandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if !storedCandidateID.Valid || storedCandidateID.String != candidateID {
		t.Errorf("candidate_id = %v, want %v", storedCandidateID, candidateID)
	}
}

func TestFreezeCandidateSecondCallErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	_, turnID, _ := setupTurnstateTestData(t, conn)

	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.FreezeCandidate(turnID, "content", "hash123"); err != nil {
			return err
		}
		_, err := tx.FreezeCandidate(turnID, "content2", "hash456")
		return err
	})
	if err == nil {
		t.Error("second FreezeCandidate should have errored")
	}
}
