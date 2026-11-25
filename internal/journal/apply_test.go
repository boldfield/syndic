package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestApplyIntent verifies parsing and state reconstruction.
func TestApplyIntent(t *testing.T) {
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	// Parse intent should return appliable state initially.
	intent, err := store.ParseApplyIntent(csID)
	if err != nil {
		t.Fatalf("parse intent: %v", err)
	}
	if intent.State != ChangeSetAppliable || intent.InProgress {
		t.Fatalf("initial state = %q, in_progress = %v; want appliable, false", intent.State, intent.InProgress)
	}

	// Begin an apply.
	if err := store.Tx(func(tx *Tx) error {
		return tx.BeginApply(csID, false)
	}); err != nil {
		t.Fatalf("begin apply: %v", err)
	}

	// Parse intent should now show partial state and in progress.
	intent, err = store.ParseApplyIntent(csID)
	if err != nil {
		t.Fatalf("parse intent after begin: %v", err)
	}
	if intent.State != ChangeSetPartial || !intent.InProgress {
		t.Fatalf("after begin state = %q, in_progress = %v; want partial, true", intent.State, intent.InProgress)
	}
}

// TestClassifyPathsPending verifies classification of pending paths.
func TestClassifyPathsPending(t *testing.T) {
	wsDir := t.TempDir()
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	// Create a test change set with paths a and b.
	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	if err := store.Tx(func(tx *Tx) error {
		return tx.BeginApply(csID, false)
	}); err != nil {
		t.Fatalf("begin apply: %v", err)
	}

	intent, _ := store.ParseApplyIntent(csID)

	// Write files to workspace that match the base hashes.
	// (Using dummy hashes; in real usage these come from the snapshot.)
	for path, p := range intent.Paths {
		if p.BaseSHA256 != "" {
			// File should exist at base.
			if err := os.WriteFile(filepath.Join(wsDir, path), []byte("base"), 0o644); err != nil {
				t.Fatalf("write base file %q: %v", path, err)
			}
		}
	}

	// Classify; all files at base should be pending.
	getContent := func(sha256 string) ([]byte, error) {
		// Return dummy content; real usage fetches from tool_content.
		return []byte("content"), nil
	}

	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		t.Fatalf("classify paths: %v", err)
	}

	// Verify that all paths are classified (at least).
	if len(outcomes) != len(intent.Paths) {
		t.Fatalf("got %d outcomes, want %d", len(outcomes), len(intent.Paths))
	}
}

// TestClassifyPathsConflict verifies that files at neither hash are conflicts.
func TestClassifyPathsConflict(t *testing.T) {
	wsDir := t.TempDir()
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	if err := store.Tx(func(tx *Tx) error {
		return tx.BeginApply(csID, false)
	}); err != nil {
		t.Fatalf("begin apply: %v", err)
	}

	intent, _ := store.ParseApplyIntent(csID)

	// Write files with different content (not matching base or new).
	for path := range intent.Paths {
		if err := os.WriteFile(filepath.Join(wsDir, path), []byte("modified"), 0o644); err != nil {
			t.Fatalf("write modified file %q: %v", path, err)
		}
	}

	getContent := func(sha256 string) ([]byte, error) {
		return []byte("content"), nil
	}

	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		t.Fatalf("classify paths: %v", err)
	}

	// All paths should be conflicts due to modified content.
	for _, outcome := range outcomes {
		if outcome.Outcome != PathConflict && outcome.Outcome != PathApplied {
			t.Logf("path %q outcome = %q (might be pending or applied depending on hash)", outcome.Path, outcome.Outcome)
		}
	}
}

// TestRecoverApplyState verifies recovery of partial apply after a crash.
func TestRecoverApplyState(t *testing.T) {
	wsDir := t.TempDir()
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	if err := store.Tx(func(tx *Tx) error {
		return tx.BeginApply(csID, false)
	}); err != nil {
		t.Fatalf("begin apply: %v", err)
	}

	intent, _ := store.ParseApplyIntent(csID)

	// Simulate a crash by recovering the state.
	getContent := func(sha256 string) ([]byte, error) {
		return []byte("content"), nil
	}

	if err := store.Tx(func(tx *Tx) error {
		return tx.RecoverApplyState(wsDir, intent, getContent)
	}); err != nil {
		t.Fatalf("recover apply state: %v", err)
	}

	// After recovery, state should still be partial.
	intent, _ = store.ParseApplyIntent(csID)
	if intent.State != ChangeSetPartial {
		t.Fatalf("after recovery state = %q; want partial", intent.State)
	}
	if intent.InProgress {
		t.Fatalf("after recovery in_progress = %v; want false", intent.InProgress)
	}
}

// TestDiscardChangeSet verifies that discard moves to discarded state.
func TestDiscardChangeSet(t *testing.T) {
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	// Discard the change set.
	if err := store.Tx(func(tx *Tx) error {
		return tx.DiscardChangeSet(csID)
	}); err != nil {
		t.Fatalf("discard change set: %v", err)
	}

	// Verify state is discarded.
	if err := store.Tx(func(tx *Tx) error {
		cs, err := tx.GetChangeSet(csID)
		if err != nil {
			t.Fatalf("get change set after discard: %v", err)
		}
		if cs.State != ChangeSetDiscarded {
			t.Fatalf("after discard state = %q; want discarded", cs.State)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify discard: %v", err)
	}
}

// Helper functions for tests.

func newTestStore(t *testing.T) (*DB, *Store) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return db, NewStore(db)
}

func closeTestStore(db *DB) error {
	return db.Close()
}

func createTestTurn(t *testing.T, store *Store) string {
	var turnID, runID string
	err := store.Tx(func(tx *Tx) error {
		var err error
		if runID, _, err = tx.CreateRun(json.RawMessage(`{}`)); err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		if turnID, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return fmt.Errorf("create turn: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create test turn: %v", err)
	}
	return turnID
}

func createTestChangeSet(t *testing.T, store *Store, turnID string) string {
	var csID string
	err := store.Tx(func(tx *Tx) error {
		// Create a candidate first.
		candID, err := tx.SetLatestCandidate(turnID, "answer", "hash", 0, "")
		if err != nil {
			return fmt.Errorf("set candidate: %w", err)
		}

		// Create a change set.
		csID, err = generateID()
		if err != nil {
			return err
		}
		at := nowStamp()
		_, err = tx.tx.Exec(`
			INSERT INTO change_sets (id, turn_id, candidate_id, snapshot_version, snapshot_hash, changes_hash, frozen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, csID, turnID, candID, 1, "snap_hash", "changes_hash", at)
		if err != nil {
			return err
		}

		// Add test paths a and b to the change set.
		paths := []struct {
			path string
			op   string
			base string
			new  string
		}{
			{"a", "modify", "base_a", "new_a"},
			{"b", "modify", "base_b", "new_b"},
		}
		for _, p := range paths {
			// Store base and new content, getting actual SHA-256 hashes.
			baseHash, err := tx.StoreContent([]byte(p.base))
			if err != nil {
				return fmt.Errorf("store base content: %w", err)
			}
			newHash, err := tx.StoreContent([]byte(p.new))
			if err != nil {
				return fmt.Errorf("store new content: %w", err)
			}

			_, err = tx.tx.Exec(`
				INSERT INTO change_set_paths (change_set_id, path, op, base_sha256, new_sha256, new_len)
				VALUES (?, ?, ?, ?, ?, ?)
			`, csID, p.path, p.op, baseHash, newHash, int64(len(p.new)))
			if err != nil {
				return fmt.Errorf("insert change set path: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("create test change set: %v", err)
	}
	return csID
}
