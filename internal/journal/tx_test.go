package journal_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestTxErrorRollback verifies that if fn returns an error, the transaction is rolled back
// and neither the turn nor the attempt are persisted.
func TestTxErrorRollback(t *testing.T) {
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

	// Use Tx to try to create a turn and attempt, then return an error
	testErr := fmt.Errorf("test error")
	returnedErr := store.Tx(func(tx *journal.Tx) error {
		_, txErr := tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}
		// Now return an error without creating an attempt
		return testErr
	})

	// Verify the error is returned unwrapped
	if !errors.Is(returnedErr, testErr) {
		t.Errorf("expected error %v, got %v", testErr, returnedErr)
	}

	// Verify that no turn was created
	var turnCount int
	err = conn.QueryRow("SELECT COUNT(*) FROM turns").Scan(&turnCount)
	if err != nil {
		t.Fatalf("failed to query turns: %v", err)
	}
	if turnCount != 0 {
		t.Errorf("expected 0 turns after error rollback, got %d", turnCount)
	}
}

// TestTxErrorRollbackWithAttempt verifies that creating a turn and attempt, then returning
// an error, leaves neither row persisted.
func TestTxErrorRollbackWithAttempt(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run first
	runID := "test-run-2"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use Tx to create a turn and attempt, then return an error
	testErr := fmt.Errorf("after creating attempt")
	returnedErr := store.Tx(func(tx *journal.Tx) error {
		turnID, txErr := tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}
		_, txErr = tx.BeginAttempt(turnID, "participant1", "phase1", 1, nil, "")
		if txErr != nil {
			return txErr
		}
		return testErr
	})

	if !errors.Is(returnedErr, testErr) {
		t.Errorf("expected error %v, got %v", testErr, returnedErr)
	}

	// Verify that neither turn nor attempt were created
	var turnCount int
	err = conn.QueryRow("SELECT COUNT(*) FROM turns").Scan(&turnCount)
	if err != nil {
		t.Fatalf("failed to query turns: %v", err)
	}
	if turnCount != 0 {
		t.Errorf("expected 0 turns after error rollback, got %d", turnCount)
	}

	var attemptCount int
	err = conn.QueryRow("SELECT COUNT(*) FROM attempts").Scan(&attemptCount)
	if err != nil {
		t.Fatalf("failed to query attempts: %v", err)
	}
	if attemptCount != 0 {
		t.Errorf("expected 0 attempts after error rollback, got %d", attemptCount)
	}
}

// TestTxPanicRollback verifies that if fn panics, the transaction is rolled back,
// no rows are persisted, and the panic is re-raised.
func TestTxPanicRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a run first
	runID := "test-run-3"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use Tx to create a turn, then panic
	panicMsg := "intentional panic for testing"
	didPanic := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				didPanic = true
				if r != panicMsg {
					t.Errorf("expected panic %q, got %q", panicMsg, r)
				}
			}
		}()

		_ = store.Tx(func(tx *journal.Tx) error {
			_, err := tx.CreateTurn(runID, "solo", nil)
			if err != nil {
				return err
			}
			panic(panicMsg)
		})
	}()

	if !didPanic {
		t.Error("expected panic to be re-raised, but it was not")
	}

	// Verify that no turn was created
	var turnCount int
	err = conn.QueryRow("SELECT COUNT(*) FROM turns").Scan(&turnCount)
	if err != nil {
		t.Fatalf("failed to query turns: %v", err)
	}
	if turnCount != 0 {
		t.Errorf("expected 0 turns after panic rollback, got %d", turnCount)
	}
}

// TestTxCommitVisibility verifies that a successful Tx commits all changes and they are
// visible through the Store afterwards.
func TestTxCommitVisibility(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run first
	runID, _, err := store.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use Tx to create a turn, attempt, and event
	var turnID, attemptID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}

		attemptID, txErr = tx.BeginAttempt(turnID, "participant1", "phase1", 1, nil, "")
		if txErr != nil {
			return txErr
		}

		return tx.AppendEvent("test_event", json.RawMessage(`{"key":"value"}`))
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Verify the turn can be read back through the Store
	retrievedRun, err := store.GetRun(runID)
	if err != nil {
		t.Fatalf("failed to retrieve run: %v", err)
	}
	if retrievedRun.ID != runID {
		t.Errorf("expected run %q, got %q", runID, retrievedRun.ID)
	}

	// Verify the turn created inside Tx is visible by turn ID
	conn := db.Conn()
	var retrievedTurnID string
	err = conn.QueryRow("SELECT id FROM turns WHERE id = ?", turnID).Scan(&retrievedTurnID)
	if err != nil {
		t.Fatalf("failed to retrieve turn: %v", err)
	}
	if retrievedTurnID != turnID {
		t.Errorf("expected turn %q, got %q", turnID, retrievedTurnID)
	}

	// Verify the attempt can be read back through the Store
	retrievedAttempt, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to retrieve attempt: %v", err)
	}
	if retrievedAttempt.ID != attemptID {
		t.Errorf("expected attempt %q, got %q", attemptID, retrievedAttempt.ID)
	}
	if retrievedAttempt.TurnID != turnID {
		t.Errorf("expected attempt turn_id %q, got %q", turnID, retrievedAttempt.TurnID)
	}

	// Verify the event was created
	var eventCount int
	err = conn.QueryRow("SELECT COUNT(*) FROM events WHERE event_type = 'test_event'").Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("expected 1 event, got %d", eventCount)
	}
}

// TestTxErrorsIsPreserved verifies that an error returned by fn satisfies errors.Is
// against the original error at the call site.
func TestTxErrorsIsPreserved(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run first
	runID, _, err := store.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use Tx to return a sentinel error
	testErr := errors.New("test custom error")

	returnedErr := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}
		return testErr
	})

	// Verify the error can be checked with errors.Is
	if !errors.Is(returnedErr, testErr) {
		t.Fatalf("expected error to match testErr, got %v", returnedErr)
	}
}

// TestTxCommitWithMultipleOperations verifies that multiple Tx operations are all committed
// atomically when fn succeeds.
func TestTxCommitWithMultipleOperations(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a run first
	runID, _, err := store.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Use Tx to create multiple related entities
	var turnID, attemptID, messageID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateTurn(runID, "solo", nil)
		if txErr != nil {
			return txErr
		}

		attemptID, txErr = tx.BeginAttempt(turnID, "participant1", "phase1", 1, nil, "")
		if txErr != nil {
			return txErr
		}

		messageID, _, _, txErr = tx.AppendMessage(turnID, "user", "user", []string{"assistant"}, []byte("test message"), nil)
		if txErr != nil {
			return txErr
		}

		return tx.AppendEvent("multi_op_test", json.RawMessage(`{}`))
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Verify all entities exist and are consistent
	attempt, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("failed to retrieve attempt: %v", err)
	}
	if attempt.TurnID != turnID {
		t.Errorf("attempt's turn_id mismatch: expected %q, got %q", turnID, attempt.TurnID)
	}

	conn := db.Conn()
	var msgTurnID string
	err = conn.QueryRow("SELECT turn_id FROM messages WHERE id = ?", messageID).Scan(&msgTurnID)
	if err != nil {
		t.Fatalf("failed to query message: %v", err)
	}
	if msgTurnID != turnID {
		t.Errorf("message's turn_id mismatch: expected %q, got %q", turnID, msgTurnID)
	}
}
