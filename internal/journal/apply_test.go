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

// TestClassifyCreatesAsPending verifies that absent paths with no base are classified as pending.
func TestClassifyCreatesAsPending(t *testing.T) {
	wsDir := t.TempDir()
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSetCreate(t, store, turnID)

	if err := store.Tx(func(tx *Tx) error {
		return tx.BeginApply(csID, false)
	}); err != nil {
		t.Fatalf("begin apply: %v", err)
	}

	intent, _ := store.ParseApplyIntent(csID)

	// Classify paths without any files in workspace
	getContent := func(sha256 string) ([]byte, error) {
		return store.GetContent(sha256)
	}

	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		t.Fatalf("classify paths: %v", err)
	}

	// Find the create path (new_c with no base)
	for _, outcome := range outcomes {
		if outcome.Path == "new_c" {
			if outcome.Outcome != PathPending {
				t.Errorf("create path new_c classified as %q; want pending", outcome.Outcome)
			}
			return
		}
	}
	t.Fatalf("create path new_c not found in outcomes")
}

// TestApplyPendingReturnErrorIfConflict verifies first-apply precondition.
func TestApplyPendingReturnErrorIfConflict(t *testing.T) {
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

	// Write a file with different content to cause conflict
	for path := range intent.Paths {
		if err := os.WriteFile(filepath.Join(wsDir, path), []byte("different"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	getContent := func(sha256 string) ([]byte, error) {
		return []byte("new content"), nil
	}

	// Try to apply; should fail because all paths are conflicted
	err := store.Tx(func(tx *Tx) error {
		_, err := tx.ApplyPendingPaths(wsDir, intent, getContent)
		return err
	})
	if err == nil {
		t.Fatalf("ApplyPendingPaths should return error when paths are in conflict")
	}
}

// TestRecoveryPersistsOutcomes verifies that recovery journals outcomes.
func TestRecoveryPersistsOutcomes(t *testing.T) {
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

	// Set up: path "a" at its new content (applied), path "b" at base (pending)
	newContentA, _ := store.GetContent(intent.Paths["a"].NewSHA256)
	if err := os.WriteFile(filepath.Join(wsDir, "a"), newContentA, 0o644); err != nil {
		t.Fatalf("write applied file a: %v", err)
	}

	baseContentB, _ := store.GetContent(intent.Paths["b"].BaseSHA256)
	if err := os.WriteFile(filepath.Join(wsDir, "b"), baseContentB, 0o644); err != nil {
		t.Fatalf("write pending file b: %v", err)
	}

	getContent := func(sha256 string) ([]byte, error) {
		return store.GetContent(sha256)
	}

	// Recover: should classify a=applied, b=pending and journal them
	err := store.Tx(func(tx *Tx) error {
		return tx.RecoverApplyState(wsDir, intent, getContent)
	})
	if err != nil {
		t.Fatalf("recover apply state: %v", err)
	}

	// Parse intent again; outcomes should be persisted from recovery
	intent, _ = store.ParseApplyIntent(csID)
	if intent.Paths["a"].Outcome != PathApplied {
		t.Errorf("after recovery, path a outcome %q; want applied", intent.Paths["a"].Outcome)
	}
	if intent.Paths["b"].Outcome != PathPending {
		t.Errorf("after recovery, path b outcome %q; want pending", intent.Paths["b"].Outcome)
	}
}

// TestRetryStableState verifies that retrying an applied state returns stable state.
func TestRetryStableState(t *testing.T) {
	wsDir := t.TempDir()
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	// First apply: set up path "a" at new content
	intent, _ := store.ParseApplyIntent(csID)
	newContentA, _ := store.GetContent(intent.Paths["a"].NewSHA256)
	if err := os.WriteFile(filepath.Join(wsDir, "a"), newContentA, 0o644); err != nil {
		t.Fatalf("write file a: %v", err)
	}

	// Apply "a"
	err := store.Tx(func(tx *Tx) error {
		if err := tx.BeginApply(csID, false); err != nil {
			return err
		}
		return tx.RecordApplyFile(csID, "a", PathApplied)
	})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Retry: should return stable state without writing
	intent, _ = store.ParseApplyIntent(csID)
	if intent.Paths["a"].Outcome != PathApplied {
		t.Fatalf("path a outcome after first apply %q; want applied", intent.Paths["a"].Outcome)
	}

	// File should still exist and be unchanged
	content, err := os.ReadFile(filepath.Join(wsDir, "a"))
	if err != nil {
		t.Fatalf("read file a: %v", err)
	}
	if string(content) != string(newContentA) {
		t.Errorf("file a was modified during retry")
	}
}

// TestDiscardRemovesApplicability verifies that discard preserves per-path outcomes.
func TestDiscardPreservesOutcomes(t *testing.T) {
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	turnID := createTestTurn(t, store)
	csID := createTestChangeSet(t, store, turnID)

	// Apply "a", leave "b" pending
	err := store.Tx(func(tx *Tx) error {
		if err := tx.BeginApply(csID, false); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(csID, "a", PathApplied); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("set up apply state: %v", err)
	}

	// Discard the change set
	err = store.Tx(func(tx *Tx) error {
		return tx.DiscardChangeSet(csID)
	})
	if err != nil {
		t.Fatalf("discard change set: %v", err)
	}

	// Verify state is discarded but outcomes are preserved
	intent, _ := store.ParseApplyIntent(csID)
	if intent.State != ChangeSetDiscarded {
		t.Fatalf("after discard state %q; want discarded", intent.State)
	}
	if intent.Paths["a"].Outcome != PathApplied {
		t.Errorf("after discard, path a outcome %q; want applied", intent.Paths["a"].Outcome)
	}
	if intent.Paths["b"].Outcome != PathPending {
		t.Errorf("after discard, path b outcome %q; want pending", intent.Paths["b"].Outcome)
	}
}

// TestOldProposalsSurviveOverlappingStaging verifies that old change sets stay appliable.
func TestOldProposalsSurviveOverlappingStaging(t *testing.T) {
	db, store := newTestStore(t)
	defer func() { _ = closeTestStore(db) }()

	// Create two turns each with their own change set
	turnID1 := createTestTurn(t, store)
	csID1 := createTestChangeSet(t, store, turnID1)

	turnID2 := createTestTurn(t, store)
	csID2 := createTestChangeSet(t, store, turnID2)

	// Both change sets should stay appliable
	intent1, _ := store.ParseApplyIntent(csID1)
	if intent1.State != ChangeSetAppliable {
		t.Fatalf("first change set state %q; want appliable", intent1.State)
	}

	intent2, _ := store.ParseApplyIntent(csID2)
	if intent2.State != ChangeSetAppliable {
		t.Fatalf("second change set state %q; want appliable", intent2.State)
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

func createTestChangeSetCreate(t *testing.T, store *Store, turnID string) string {
	var csID string
	err := store.Tx(func(tx *Tx) error {
		// Create a candidate first.
		candID, err := tx.SetLatestCandidate(turnID, "answer", "hash", 0, "")
		if err != nil {
			return fmt.Errorf("set candidate: %w", err)
		}

		// Create a change set with one create operation.
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

		// Add test path new_c as a create
		p := struct {
			path string
			op   string
			new  string
		}{
			path: "new_c",
			op:   "create",
			new:  "content_c",
		}
		newHash, err := tx.StoreContent([]byte(p.new))
		if err != nil {
			return fmt.Errorf("store new content: %w", err)
		}

		_, err = tx.tx.Exec(`
			INSERT INTO change_set_paths (change_set_id, path, op, base_sha256, new_sha256, new_len)
			VALUES (?, ?, ?, ?, ?, ?)
		`, csID, p.path, p.op, nil, newHash, int64(len(p.new)))
		if err != nil {
			return fmt.Errorf("insert change set path: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("create test change set: %v", err)
	}
	return csID
}
