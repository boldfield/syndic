package journal

import (
	"encoding/json"
	"sync"
	"testing"
)

// TestTxConcurrentSerializedCounter verifies that 20 goroutines each running a Tx
// that reads a shared counter (turns.epoch), increments it, and writes it back
// all serialize correctly through the mutex, ending with the counter at 20.
func TestTxConcurrentSerializedCounter(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	store := NewStore(db)

	// Create a run and turn to work with
	runID, _, err := store.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "solo", nil)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}

	// Set initial epoch to 0
	conn := db.conn
	if _, err := conn.Exec(`UPDATE turns SET epoch = 0 WHERE id = ?`, turnID); err != nil {
		t.Fatalf("failed to set initial epoch: %v", err)
	}

	// Run 20 goroutines that each read-modify-write the counter
	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	var mu sync.Mutex
	var errs []error

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := store.Tx(func(tx *Tx) error {
				// Read the current epoch value
				var currentEpoch int
				if err := tx.tx.QueryRow(`
					SELECT epoch FROM turns WHERE id = ?
				`, turnID).Scan(&currentEpoch); err != nil {
					return err
				}

				// Write it back incremented
				if _, err := tx.tx.Exec(`
					UPDATE turns SET epoch = ? WHERE id = ?
				`, currentEpoch+1, turnID); err != nil {
					return err
				}

				return nil
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Check for any errors
	if len(errs) > 0 {
		t.Fatalf("got %d errors during concurrent operations: %v", len(errs), errs)
	}

	// Verify the final epoch is 20
	var finalEpoch int
	if err := conn.QueryRow(`SELECT epoch FROM turns WHERE id = ?`, turnID).Scan(&finalEpoch); err != nil {
		t.Fatalf("failed to query final epoch: %v", err)
	}

	if finalEpoch != numGoroutines {
		t.Errorf("expected final epoch %d, got %d", numGoroutines, finalEpoch)
	}
}
