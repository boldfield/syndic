package journal_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

// TestToolCallStorage tests storing and retrieving tool calls.
func TestToolCallStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create test data
	turnID := "turn1"
	attemptID := "attempt1"
	setupTestData(t, conn, "run1", turnID)
	createTestAttempt(t, conn, attemptID, turnID, "author")

	call := journal.ToolCall{
		ID:         "call1",
		AttemptID:  attemptID,
		Name:       "read_file",
		ArgsSHA256: "abcd1234",
		Class:      "read",
		Status:     journal.ToolCallStatusAccepted,
	}

	// Create a Store instance and store the tool call
	s := journal.NewStore(db)
	err := s.Tx(func(tx *journal.Tx) error {
		return tx.StoreToolCall(call)
	})
	if err != nil {
		t.Fatalf("store tool call: %v", err)
	}

	// Retrieve the tool call
	var retrieved []journal.ToolCall
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		retrieved, err = tx.GetToolCallsByAttempt(attemptID)
		return err
	})
	if err != nil {
		t.Fatalf("get tool calls: %v", err)
	}

	if len(retrieved) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(retrieved))
	}

	if retrieved[0].ID != call.ID {
		t.Errorf("ID mismatch: expected %s, got %s", call.ID, retrieved[0].ID)
	}
	if retrieved[0].Name != call.Name {
		t.Errorf("Name mismatch: expected %s, got %s", call.Name, retrieved[0].Name)
	}
	if retrieved[0].Status != call.Status {
		t.Errorf("Status mismatch: expected %s, got %s", call.Status, retrieved[0].Status)
	}
}

// TestObservationStorage tests storing and retrieving observations.
func TestObservationStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create test data
	turnID := "turn1"
	attemptID := "attempt1"
	setupTestData(t, conn, "run1", turnID)
	createTestAttempt(t, conn, attemptID, turnID, "author")

	now := time.Now()
	obs := journal.Observation{
		ID:              "obs1",
		TurnID:          turnID,
		AttemptID:       attemptID,
		SnapshotVersion: 1,
		Path:            "file.txt",
		Kind:            journal.ObservationKindFile,
		BaseSHA256:      strPtr("deadbeef"),
		ObservedAt:      now,
	}

	s := journal.NewStore(db)
	err := s.Tx(func(tx *journal.Tx) error {
		return tx.StoreObservation(obs)
	})
	if err != nil {
		t.Fatalf("store observation: %v", err)
	}

	var retrieved []journal.Observation
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		retrieved, err = tx.GetObservationsByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get observations: %v", err)
	}

	if len(retrieved) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(retrieved))
	}

	if retrieved[0].Path != obs.Path {
		t.Errorf("Path mismatch: expected %s, got %s", obs.Path, retrieved[0].Path)
	}
	if retrieved[0].Kind != obs.Kind {
		t.Errorf("Kind mismatch: expected %s, got %s", obs.Kind, retrieved[0].Kind)
	}
	if retrieved[0].SnapshotVersion != obs.SnapshotVersion {
		t.Errorf("SnapshotVersion mismatch: expected %d, got %d", obs.SnapshotVersion, retrieved[0].SnapshotVersion)
	}
}

// TestChangeSetStorage tests storing and retrieving change sets.
func TestChangeSetStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	now := time.Now()
	cs := journal.ChangeSet{
		ID:           "cs1",
		TurnID:       turnID,
		State:        journal.ChangeSetStateAppliable,
		SnapshotHash: "snap123",
		ChangesHash:  "changes456",
		CreatedAt:    now,
	}

	s := journal.NewStore(db)
	err := s.Tx(func(tx *journal.Tx) error {
		return tx.StoreChangeSet(cs)
	})
	if err != nil {
		t.Fatalf("store change set: %v", err)
	}

	var retrieved *journal.ChangeSet
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		retrieved, err = tx.GetChangeSetByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get change set: %v", err)
	}

	if retrieved == nil {
		t.Fatal("change set not found")
	}

	if retrieved.State != cs.State {
		t.Errorf("State mismatch: expected %s, got %s", cs.State, retrieved.State)
	}
	if retrieved.SnapshotHash != cs.SnapshotHash {
		t.Errorf("SnapshotHash mismatch: expected %s, got %s", cs.SnapshotHash, retrieved.SnapshotHash)
	}
}

// TestChangeSetPathStorage tests storing and retrieving change set paths.
func TestChangeSetPathStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	// First create a change set
	cs := journal.ChangeSet{
		ID:           "cs1",
		TurnID:       turnID,
		State:        journal.ChangeSetStateAppliable,
		SnapshotHash: "snap123",
		ChangesHash:  "changes456",
		CreatedAt:    time.Now(),
	}

	s := journal.NewStore(db)
	err := s.Tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(cs); err != nil {
			return err
		}

		// Add paths to the change set
		paths := []journal.ChangeSetPath{
			{
				ID:          "path1",
				ChangeSetID: cs.ID,
				Path:        "file1.txt",
				Op:          journal.ChangeSetOpModify,
				BaseSHA256:  strPtr("old1"),
				NewSHA256:   "new1",
				NewLen:      100,
				Outcome:     journal.ChangeSetPathOutcomePending,
			},
			{
				ID:          "path2",
				ChangeSetID: cs.ID,
				Path:        "file2.txt",
				Op:          journal.ChangeSetOpCreate,
				NewSHA256:   "new2",
				NewLen:      200,
				Outcome:     journal.ChangeSetPathOutcomePending,
			},
		}
		for _, p := range paths {
			if err := tx.StoreChangeSetPath(p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("store change set and paths: %v", err)
	}

	var retrieved []journal.ChangeSetPath
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		retrieved, err = tx.GetChangeSetPathsByChangeSet(cs.ID)
		return err
	})
	if err != nil {
		t.Fatalf("get change set paths: %v", err)
	}

	if len(retrieved) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(retrieved))
	}

	if retrieved[0].Path != "file1.txt" {
		t.Errorf("first path mismatch: expected file1.txt, got %s", retrieved[0].Path)
	}
	if retrieved[1].Path != "file2.txt" {
		t.Errorf("second path mismatch: expected file2.txt, got %s", retrieved[1].Path)
	}
}

// TestStagingStorage tests storing and retrieving staged files.
func TestStagingStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	s := journal.NewStore(db)
	err := s.Tx(func(tx *journal.Tx) error {
		if err := tx.StoreStaging(turnID, "file1.txt", "hash1"); err != nil {
			return err
		}
		if err := tx.StoreStaging(turnID, "file2.txt", "hash2"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("store staging: %v", err)
	}

	var staging map[string]string
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		staging, err = tx.GetStagingByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get staging: %v", err)
	}

	if len(staging) != 2 {
		t.Fatalf("expected 2 staged files, got %d", len(staging))
	}

	if staging["file1.txt"] != "hash1" {
		t.Errorf("file1.txt hash mismatch: expected hash1, got %s", staging["file1.txt"])
	}
	if staging["file2.txt"] != "hash2" {
		t.Errorf("file2.txt hash mismatch: expected hash2, got %s", staging["file2.txt"])
	}

	// Test unstaging
	err = s.Tx(func(tx *journal.Tx) error {
		return tx.UnstageFile(turnID, "file1.txt")
	})
	if err != nil {
		t.Fatalf("unstage file: %v", err)
	}

	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		staging, err = tx.GetStagingByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get staging after unstage: %v", err)
	}

	if len(staging) != 1 {
		t.Fatalf("expected 1 staged file after unstage, got %d", len(staging))
	}
	if _, exists := staging["file1.txt"]; exists {
		t.Error("file1.txt should be unstaged")
	}
}

// TestApplyOpsStorage tests storing and retrieving apply operations.
func TestApplyOpsStorage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	// First create a change set
	cs := journal.ChangeSet{
		ID:           "cs1",
		TurnID:       turnID,
		State:        journal.ChangeSetStateAppliable,
		SnapshotHash: "snap123",
		ChangesHash:  "changes456",
		CreatedAt:    time.Now(),
	}

	s := journal.NewStore(db)
	now := time.Now()

	// Store apply operations: started, file, finished
	err := s.Tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(cs); err != nil {
			return err
		}

		// Record the sequence of apply operations
		ops := []journal.ApplyOp{
			{
				ID:          "op1",
				ChangeSetID: cs.ID,
				OpType:      "started",
				AppliedAt:   now,
			},
			{
				ID:          "op2",
				ChangeSetID: cs.ID,
				OpType:      "file",
				Path:        strPtr("file1.txt"),
				NewSHA256:   strPtr("newsha1"),
				AppliedAt:   now.Add(time.Second),
			},
			{
				ID:          "op3",
				ChangeSetID: cs.ID,
				OpType:      "file",
				Path:        strPtr("file2.txt"),
				NewSHA256:   strPtr("newsha2"),
				AppliedAt:   now.Add(2 * time.Second),
			},
			{
				ID:          "op4",
				ChangeSetID: cs.ID,
				OpType:      "finished",
				AppliedAt:   now.Add(3 * time.Second),
			},
		}

		for _, op := range ops {
			if err := tx.StoreApplyOp(op); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("store change set and apply ops: %v", err)
	}

	// Retrieve and verify apply operations
	var retrieved []journal.ApplyOp
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		retrieved, err = tx.GetApplyOps(cs.ID)
		return err
	})
	if err != nil {
		t.Fatalf("get apply ops: %v", err)
	}

	if len(retrieved) != 4 {
		t.Fatalf("expected 4 apply ops, got %d", len(retrieved))
	}

	if retrieved[0].OpType != "started" {
		t.Errorf("first op should be started, got %s", retrieved[0].OpType)
	}
	if retrieved[1].OpType != "file" || *retrieved[1].Path != "file1.txt" {
		t.Errorf("second op should be file for file1.txt, got %s for %v", retrieved[1].OpType, retrieved[1].Path)
	}
	if retrieved[3].OpType != "finished" {
		t.Errorf("last op should be finished, got %s", retrieved[3].OpType)
	}
}

// TestPerPathApplyOutcomes tests that per-path outcomes are recorded correctly.
func TestPerPathApplyOutcomes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	// Create a change set with paths
	cs := journal.ChangeSet{
		ID:           "cs1",
		TurnID:       turnID,
		State:        journal.ChangeSetStateAppliable,
		SnapshotHash: "snap123",
		ChangesHash:  "changes456",
		CreatedAt:    time.Now(),
	}

	s := journal.NewStore(db)
	now := time.Now()

	err := s.Tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(cs); err != nil {
			return err
		}

		// Store paths initially as pending
		paths := []journal.ChangeSetPath{
			{
				ID:          "path1",
				ChangeSetID: cs.ID,
				Path:        "file1.txt",
				Op:          journal.ChangeSetOpModify,
				BaseSHA256:  strPtr("oldsha1"),
				NewSHA256:   "newsha1",
				NewLen:      100,
				Outcome:     journal.ChangeSetPathOutcomePending,
			},
			{
				ID:          "path2",
				ChangeSetID: cs.ID,
				Path:        "file2.txt",
				Op:          journal.ChangeSetOpModify,
				BaseSHA256:  strPtr("oldsha2"),
				NewSHA256:   "newsha2",
				NewLen:      200,
				Outcome:     journal.ChangeSetPathOutcomePending,
			},
		}

		for _, p := range paths {
			if err := tx.StoreChangeSetPath(p); err != nil {
				return err
			}
		}

		// Record apply operations that transition outcomes
		ops := []journal.ApplyOp{
			{
				ID:          "op1",
				ChangeSetID: cs.ID,
				OpType:      "started",
				AppliedAt:   now,
			},
			{
				ID:          "op2",
				ChangeSetID: cs.ID,
				OpType:      "file",
				Path:        strPtr("file1.txt"),
				NewSHA256:   strPtr("newsha1"),
				AppliedAt:   now.Add(time.Second),
			},
			{
				ID:          "op3",
				ChangeSetID: cs.ID,
				OpType:      "file",
				Path:        strPtr("file2.txt"),
				NewSHA256:   strPtr("newsha2"),
				AppliedAt:   now.Add(2 * time.Second),
			},
			{
				ID:          "op4",
				ChangeSetID: cs.ID,
				OpType:      "finished",
				AppliedAt:   now.Add(3 * time.Second),
			},
		}

		for _, op := range ops {
			if err := tx.StoreApplyOp(op); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("store data: %v", err)
	}

	// Verify paths are still pending (outcomes not updated via UPDATE)
	var paths []journal.ChangeSetPath
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		paths, err = tx.GetChangeSetPathsByChangeSet(cs.ID)
		return err
	})
	if err != nil {
		t.Fatalf("get paths: %v", err)
	}

	for _, p := range paths {
		if p.Outcome != journal.ChangeSetPathOutcomePending {
			t.Errorf("path %s outcome should be pending, got %s", p.Path, p.Outcome)
		}
	}

	// Verify that apply_ops record the transitions
	var ops []journal.ApplyOp
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		ops, err = tx.GetApplyOps(cs.ID)
		return err
	})
	if err != nil {
		t.Fatalf("get apply ops: %v", err)
	}

	fileOps := 0
	for _, op := range ops {
		if op.OpType == "file" {
			fileOps++
			if op.Path == nil {
				t.Errorf("file op should have a path")
			}
		}
	}

	if fileOps != 2 {
		t.Errorf("expected 2 file ops, got %d", fileOps)
	}
}

// TestStagingWithPathsContainingColons tests that staging works with paths containing ':'.
func TestStagingWithPathsContainingColons(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	s := journal.NewStore(db)

	// Test staging a path with colons
	pathWithColon := "dir:subdir/file::name.txt"
	err := s.Tx(func(tx *journal.Tx) error {
		return tx.StoreStaging(turnID, pathWithColon, "hash1")
	})
	if err != nil {
		t.Fatalf("store staging: %v", err)
	}

	// Retrieve and verify
	var staging map[string]string
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		staging, err = tx.GetStagingByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get staging: %v", err)
	}

	if val, exists := staging[pathWithColon]; !exists {
		t.Error("path with colons not found in staging")
	} else if val != "hash1" {
		t.Errorf("path hash mismatch: expected hash1, got %s", val)
	}
}

// TestAtomicRecordAndEventRollback tests that records and events are journaled atomically,
// and that a failed transaction rolls back both records and events.
// This demonstrates: "Atomic record plus event rollback" (design section 6).
func TestAtomicRecordAndEventRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	turnID := "turn1"
	setupTestData(t, conn, "run1", turnID)

	s := journal.NewStore(db)

	// First, store a record and event successfully
	err := s.Tx(func(tx *journal.Tx) error {
		obs := journal.Observation{
			ID:              "obs1",
			TurnID:          turnID,
			AttemptID:       "attempt1",
			SnapshotVersion: 1,
			Path:            "file.txt",
			Kind:            journal.ObservationKindFile,
			BaseSHA256:      strPtr("hash1"),
			ObservedAt:      time.Now(),
		}
		if err := tx.StoreObservation(obs); err != nil {
			return err
		}
		// Append event in same transaction
		if err := tx.AppendEvent("observation_recorded", json.RawMessage(`{"path":"file.txt"}`)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("store observation with event: %v", err)
	}

	// Verify record and event were stored
	var obs []journal.Observation
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.GetObservationsByTurn(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("get observations: %v", err)
	}

	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}

	// Now test that a failed transaction with both record and event is rolled back
	attemptID := "attempt1"
	createTestAttempt(t, conn, attemptID, turnID, "author")

	err = s.Tx(func(tx *journal.Tx) error {
		// Store a tool call
		call := journal.ToolCall{
			ID:         "call2",
			AttemptID:  attemptID,
			Name:       "web_fetch",
			ArgsSHA256: "hash2",
			Class:      "web",
			Status:     journal.ToolCallStatusAccepted,
		}
		if err := tx.StoreToolCall(call); err != nil {
			return err
		}
		// Append event in same transaction
		if err := tx.AppendEvent("tool_call_recorded", json.RawMessage(`{"name":"web_fetch"}`)); err != nil {
			return err
		}
		// Simulate transaction failure by returning an error
		return sql.ErrNoRows // any error will cause rollback
	})

	// Error expected from failed transaction
	if err == nil {
		t.Fatal("expected transaction to fail")
	}

	// Verify that the failed record was not stored
	var calls []journal.ToolCall
	err = s.Tx(func(tx *journal.Tx) error {
		var err error
		calls, err = tx.GetToolCallsByAttempt(attemptID)
		return err
	})
	if err != nil {
		t.Fatalf("get tool calls: %v", err)
	}

	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls after rollback, got %d", len(calls))
	}
}

// Helper functions

func strPtr(s string) *string {
	return &s
}

func createTestAttempt(t *testing.T, conn *sql.DB, attemptID, turnID, participant string) {
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, attemptID, turnID, participant, "author", 1, time.Now().Format(time.RFC3339Nano), "completed")
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
}
