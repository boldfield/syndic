package journal_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestEmitConsecutiveSeq verifies that consecutive Emit calls in the same transaction
// return consecutive, increasing Seq values.
func TestEmitConsecutiveSeq(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run first
	runID := "test-run-1"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Create a turn for FK constraint
	turnID := "test-turn-1"
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	var seq1, seq2 int64
	err = store.Tx(func(tx *journal.Tx) error {
		e1, err := tx.Emit(turnID, "event1", json.RawMessage(`{"data":"test1"}`))
		if err != nil {
			return err
		}
		seq1 = e1.Seq

		e2, err := tx.Emit(turnID, "event2", json.RawMessage(`{"data":"test2"}`))
		if err != nil {
			return err
		}
		seq2 = e2.Seq
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Verify consecutive and increasing
	if seq2 != seq1+1 {
		t.Errorf("expected seq2=%d to be seq1+1=%d", seq2, seq1+1)
	}

	// Verify MaxSeq returns the second value
	maxSeq, err := store.MaxSeq()
	if err != nil {
		t.Fatalf("MaxSeq failed: %v", err)
	}
	if maxSeq != seq2 {
		t.Errorf("expected MaxSeq=%d, got %d", seq2, maxSeq)
	}
}

// TestEmitRollbackDoesNotReuse verifies that when a Tx containing Emit returns an error,
// the sequence number is not reused in the next transaction.
func TestEmitRollbackDoesNotReuse(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run and turn
	runID := "test-run-2"
	turnID := "test-turn-2"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// First transaction: Emit, then return an error (rollback)
	var seq1 int64
	testErr := fmt.Errorf("test error")
	err = store.Tx(func(tx *journal.Tx) error {
		e, err := tx.Emit(turnID, "event1", json.RawMessage(`{"data":"test1"}`))
		if err != nil {
			return err
		}
		seq1 = e.Seq
		// Return error to trigger rollback
		return testErr
	})
	if !errors.Is(err, testErr) {
		t.Fatalf("expected error %v, got %v", testErr, err)
	}

	// Verify no row was inserted
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after rollback, got %d", count)
	}

	// Second transaction: Emit again
	var seq2 int64
	err = store.Tx(func(tx *journal.Tx) error {
		e, err := tx.Emit(turnID, "event2", json.RawMessage(`{"data":"test2"}`))
		if err != nil {
			return err
		}
		seq2 = e.Seq
		return nil
	})
	if err != nil {
		t.Fatalf("second Tx failed: %v", err)
	}

	// Verify the second seq is greater than the first (never reused)
	if seq2 <= seq1 {
		t.Errorf("expected seq2=%d > seq1=%d (sequence numbers must never be reused)", seq2, seq1)
	}

	// Verify exactly one row in the database with seq2
	err = conn.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after second Tx, got %d", count)
	}

	var rowSeq int64
	err = conn.QueryRow("SELECT id FROM events").Scan(&rowSeq)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}
	if rowSeq != seq2 {
		t.Errorf("expected row seq=%d, got %d", seq2, rowSeq)
	}
}

// TestEventsSinceWithAppendEvent verifies that EventsSince reads rows written by
// AppendEvent without error and returns them with an empty TurnID when turn_id is null.
func TestEventsSinceWithAppendEvent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run
	runID := "test-run-3"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use AppendEvent (existing method) to write a row with null turn_id
	err = store.AppendEvent("legacy_event", json.RawMessage(`{"legacy":"data"}`))
	if err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}

	// Create a turn for Emit
	turnID := "test-turn-3"
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// Use Emit twice
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.Emit(turnID, "event1", json.RawMessage(`{"data":"test1"}`))
		if err != nil {
			return err
		}
		_, err = tx.Emit(turnID, "event2", json.RawMessage(`{"data":"test2"}`))
		return err
	})
	if err != nil {
		t.Fatalf("Tx with two Emits failed: %v", err)
	}

	// Query all events using EventsSince(0, 10)
	events, err := store.EventsSince(0, 10)
	if err != nil {
		t.Fatalf("EventsSince failed: %v", err)
	}

	// Should have exactly 3 events in order
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}

	// First event (from AppendEvent) should have empty TurnID
	if events[0].TurnID != "" {
		t.Errorf("expected first event TurnID to be empty, got %q", events[0].TurnID)
	}
	if events[0].Kind != "legacy_event" {
		t.Errorf("expected first event kind to be 'legacy_event', got %q", events[0].Kind)
	}

	// Second and third events should have the turnID
	if events[1].TurnID != turnID {
		t.Errorf("expected second event TurnID=%q, got %q", turnID, events[1].TurnID)
	}
	if events[2].TurnID != turnID {
		t.Errorf("expected third event TurnID=%q, got %q", turnID, events[2].TurnID)
	}

	// Verify seq is in ascending order
	if events[0].Seq >= events[1].Seq || events[1].Seq >= events[2].Seq {
		t.Errorf("expected seq in ascending order: %d, %d, %d", events[0].Seq, events[1].Seq, events[2].Seq)
	}
}

// TestEventsSincePaging verifies that EventsSince pages correctly and each call
// with the last returned Seq visits every event exactly once.
func TestEventsSincePaging(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run and turn
	runID := "test-run-4"
	turnID := "test-turn-4"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// Create 5 events
	err = store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < 5; i++ {
			_, err := tx.Emit(turnID, fmt.Sprintf("event%d", i), json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Page through with limit=2
	var allSeqs []int64
	var lastSeq int64

	for {
		events, err := store.EventsSince(lastSeq, 2)
		if err != nil {
			t.Fatalf("EventsSince failed: %v", err)
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			allSeqs = append(allSeqs, e.Seq)
		}
		lastSeq = events[len(events)-1].Seq
	}

	// Should have visited all 5 events
	if len(allSeqs) != 5 {
		t.Errorf("expected to visit 5 events, visited %d", len(allSeqs))
	}

	// All seqs should be unique (no duplicates)
	seen := make(map[int64]bool)
	for _, seq := range allSeqs {
		if seen[seq] {
			t.Errorf("seq %d visited more than once", seq)
		}
		seen[seq] = true
	}

	// All seqs should be in ascending order
	for i := 1; i < len(allSeqs); i++ {
		if allSeqs[i] <= allSeqs[i-1] {
			t.Errorf("seq not in ascending order at index %d: %d vs %d", i, allSeqs[i-1], allSeqs[i])
		}
	}
}

// TestEmitWithoutTurnID verifies that Emit with an empty turnID stores null in the database.
func TestEmitWithoutTurnID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Emit with empty turnID
	var seq int64
	err := store.Tx(func(tx *journal.Tx) error {
		e, err := tx.Emit("", "event_no_turn", json.RawMessage(`{"data":"test"}`))
		if err != nil {
			return err
		}
		seq = e.Seq
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Query directly to verify turn_id is null
	conn := db.Conn()
	var turnIDPtr *string
	err = conn.QueryRow("SELECT turn_id FROM events WHERE id = ?", seq).Scan(&turnIDPtr)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}
	if turnIDPtr != nil {
		t.Errorf("expected turn_id to be null, got %q", *turnIDPtr)
	}

	// EventsSince should return empty TurnID
	events, err := store.EventsSince(0, 10)
	if err != nil {
		t.Fatalf("EventsSince failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].TurnID != "" {
		t.Errorf("expected empty TurnID, got %q", events[0].TurnID)
	}
}

// TestEmitForeignKeyError verifies that Emit with a turn id that does not exist fails.
func TestEmitForeignKeyError(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Emit with nonexistent turnID
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.Emit("nonexistent-turn", "event", json.RawMessage(`{"data":"test"}`))
		return err
	})
	if err == nil {
		t.Fatal("expected Emit to fail with foreign key error")
	}
}

// TestEmitInvalidJSON verifies that Emit with invalid JSON payload fails.
func TestEmitInvalidJSON(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run and turn
	runID := "test-run-5"
	turnID := "test-turn-5"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// Emit with invalid JSON
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.Emit(turnID, "event", json.RawMessage(`{invalid json}`))
		return err
	})
	if err == nil {
		t.Fatal("expected Emit to fail with invalid JSON error")
	}
}

// TestEventAtAndDataRoundTrip verifies that Event At and Data round-trip exactly.
func TestEventAtAndDataRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run and turn
	runID := "test-run-6"
	turnID := "test-turn-6"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'solo')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// Emit an event
	payload := json.RawMessage(`{"key":"value","number":42,"array":[1,2,3]}`)
	var emittedEvent journal.Event
	err = store.Tx(func(tx *journal.Tx) error {
		e, err := tx.Emit(turnID, "test_event", payload)
		if err != nil {
			return err
		}
		emittedEvent = e
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Read back via EventsSince
	events, err := store.EventsSince(0, 10)
	if err != nil {
		t.Fatalf("EventsSince failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	readEvent := events[0]

	// Verify At matches
	if !emittedEvent.At.Equal(readEvent.At) {
		t.Errorf("At mismatch: emitted=%q, read=%q", emittedEvent.At, readEvent.At)
	}

	// Verify Data matches byte-for-byte
	if string(emittedEvent.Data) != string(readEvent.Data) {
		t.Errorf("Data mismatch: emitted=%s, read=%s", emittedEvent.Data, readEvent.Data)
	}
}

// TestEventsSinceLimitValidation verifies that limit < 1 is an error.
func TestEventsSinceLimitValidation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Test limit = 0
	_, err := store.EventsSince(0, 0)
	if err == nil {
		t.Fatal("expected EventsSince with limit=0 to fail")
	}

	// Test limit = -1
	_, err = store.EventsSince(0, -1)
	if err == nil {
		t.Fatal("expected EventsSince with limit=-1 to fail")
	}
}

// TestMaxSeqEmptyTable verifies that MaxSeq returns 0 for an empty events table.
func TestMaxSeqEmptyTable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	maxSeq, err := store.MaxSeq()
	if err != nil {
		t.Fatalf("MaxSeq failed: %v", err)
	}
	if maxSeq != 0 {
		t.Errorf("expected MaxSeq=0 for empty table, got %d", maxSeq)
	}
}

func TestLatestTurnStateEvent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()
	if _, err := conn.Exec(`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r', '{}', 'h')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2"} {
		if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode) VALUES (?, 'r', 'solo')`, id); err != nil {
			t.Fatal(err)
		}
	}

	emit := func(turnID, kind, state string) {
		t.Helper()
		err := store.Tx(func(tx *journal.Tx) error {
			_, err := tx.Emit(turnID, kind, json.RawMessage(fmt.Sprintf(`{"state":%q}`, state)))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	ev, err := store.LatestTurnStateEvent("t1")
	if err != nil || ev != nil {
		t.Fatalf("no events: got %+v, %v; want nil, nil", ev, err)
	}

	emit("t1", "turn_state", "submitted")
	emit("t2", "turn_state", "submitted")
	emit("t1", "turn_state", "author_dispatched")
	emit("t1", "attempt_begun", "ignored")
	emit("t2", "turn_state", "approved")
	emit("t1", "turn_state", "candidate_frozen")
	emit("t2", "turn_state", "ignored_later")
	emit("t1", "attempt_begun", "ignored")

	ev, err = store.LatestTurnStateEvent("t1")
	if err != nil || ev == nil {
		t.Fatalf("t1: got %+v, %v", ev, err)
	}
	if ev.Kind != "turn_state" || ev.TurnID != "t1" || string(ev.Data) != `{"state":"candidate_frozen"}` {
		t.Fatalf("t1 latest = %+v (%s)", ev, ev.Data)
	}

	if ev, err = store.LatestTurnStateEvent("unknown"); err != nil || ev != nil {
		t.Fatalf("unknown turn: got %+v, %v; want nil, nil", ev, err)
	}
}
