package journal_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestMigration004SchemaVersion(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}()

	conn := db.Conn()
	var version int
	err = conn.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}

	if version != 4 {
		t.Errorf("expected schema version 4, got %d", version)
	}
}

func TestMigration004RunsKindColumn(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Test that kind column exists and has correct default
	runID := "run-1"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	var kind string
	err = conn.QueryRow("SELECT kind FROM runs WHERE id = ?", runID).Scan(&kind)
	if err != nil {
		t.Fatalf("failed to query kind: %v", err)
	}
	if kind != "probe" {
		t.Errorf("expected default kind 'probe', got %q", kind)
	}
}

func TestMigration004RunsKindSchemaConstraint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Verify CHECK constraint exists in the schema
	var createSQL string
	err := conn.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'").Scan(&createSQL)
	if err != nil {
		t.Fatalf("failed to query runs table definition: %v", err)
	}

	if createSQL == "" {
		t.Fatal("failed to get runs table CREATE statement")
	}

	// Verify the CREATE TABLE contains the CHECK constraint
	if !contains(createSQL, "CHECK") || !contains(createSQL, "kind") {
		t.Errorf("runs table missing CHECK constraint on kind column.\nSchema: %s", createSQL)
	}
}

func TestMigration004RunsKindConstraint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	testCases := []struct {
		name  string
		kind  string
		valid bool
	}{
		{"valid probe", "probe", true},
		{"valid engine", "engine", true},
		{"invalid other", "other", false},
		{"invalid unknown", "unknown", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runID := "run-" + tc.kind
			_, err := conn.Exec(`
				INSERT INTO runs (id, manifest, manifest_hash, kind)
				VALUES (?, '{}', 'hash', ?)
			`, runID, tc.kind)
			if tc.valid && err != nil {
				t.Errorf("expected valid kind to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid kind to fail, but succeeded")
			}
		})
	}
}

func TestMigration004TurnsNewColumns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Verify new columns exist and have proper defaults
	var epoch int
	err := conn.QueryRow(`
		SELECT epoch FROM turns WHERE id = ?
	`, turnID).Scan(&epoch)
	if err != nil {
		t.Fatalf("failed to query turns columns: %v", err)
	}

	if epoch != 0 {
		t.Errorf("expected epoch default 0, got %d", epoch)
	}
}

func TestMigration004SubmissionIDUnique(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	submissionID := "submission-1"

	// First turn with submission_id should succeed
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode, submission_id)
		VALUES (?, ?, 'review', ?)
	`, "turn-1", runID, submissionID)
	if err != nil {
		t.Fatalf("failed to insert first turn with submission_id: %v", err)
	}

	// Second turn with same submission_id should fail
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode, submission_id)
		VALUES (?, ?, 'review', ?)
	`, "turn-2", runID, submissionID)
	if err == nil {
		t.Errorf("expected unique constraint violation, but insert succeeded")
	}

	// Turns with NULL submission_id should not trigger unique constraint
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode, submission_id)
		VALUES (?, ?, 'review', NULL)
	`, "turn-3", runID)
	if err != nil {
		t.Fatalf("failed to insert turn with NULL submission_id: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode, submission_id)
		VALUES (?, ?, 'review', NULL)
	`, "turn-4", runID)
	if err != nil {
		t.Fatalf("failed to insert second turn with NULL submission_id: %v", err)
	}
}

func TestMigration004AttemptsNewColumns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Create attempt and verify new columns have correct defaults
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	var retryCount int
	err = conn.QueryRow("SELECT retry_count FROM attempts WHERE id = ?", attemptID).Scan(&retryCount)
	if err != nil {
		t.Fatalf("failed to query retry_count: %v", err)
	}
	if retryCount != 0 {
		t.Errorf("expected retry_count default 0, got %d", retryCount)
	}
}

func TestMigration004AttemptDispatches(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	// First dispatch should succeed
	_, err = conn.Exec(`
		INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at)
		VALUES (?, 0, CURRENT_TIMESTAMP)
	`, attemptID)
	if err != nil {
		t.Fatalf("failed to insert first dispatch: %v", err)
	}

	// Second dispatch with different index should succeed
	_, err = conn.Exec(`
		INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at)
		VALUES (?, 1, CURRENT_TIMESTAMP)
	`, attemptID)
	if err != nil {
		t.Fatalf("failed to insert second dispatch: %v", err)
	}

	// Duplicate (attempt_id, dispatch_index) should fail
	_, err = conn.Exec(`
		INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at)
		VALUES (?, 0, CURRENT_TIMESTAMP)
	`, attemptID)
	if err == nil {
		t.Errorf("expected unique constraint violation on (attempt_id, dispatch_index), but succeeded")
	}
}

func TestMigration004CapacityHoldsCheckConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	// Valid: both released_at and released_how null
	_, err = conn.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since)
		VALUES (?, 'pool1', 'endpoint1', 'model1', '{}', CURRENT_TIMESTAMP)
	`, attemptID)
	if err != nil {
		t.Fatalf("failed to insert valid capacity_hold: %v", err)
	}

	// Invalid: released_how set but released_at null
	attemptID2 := "attempt-2"
	_, err = conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID2, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt 2: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_how)
		VALUES (?, 'pool2', 'endpoint2', 'model2', '{}', CURRENT_TIMESTAMP, 'operator')
	`, attemptID2)
	if err == nil {
		t.Errorf("expected check constraint violation when released_how set but released_at null, but succeeded")
	}

	// Invalid: released_at set but released_how null
	attemptID3 := "attempt-3"
	_, err = conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID3, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt 3: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_at)
		VALUES (?, 'pool3', 'endpoint3', 'model3', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, attemptID3)
	if err == nil {
		t.Errorf("expected check constraint violation when released_at set but released_how null, but succeeded")
	}

	// Invalid: released_how is not in allowed values
	attemptID4 := "attempt-4"
	_, err = conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID4, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt 4: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_at, released_how)
		VALUES (?, 'pool4', 'endpoint4', 'model4', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'manual')
	`, attemptID4)
	if err == nil {
		t.Errorf("expected check constraint violation for invalid released_how value, but succeeded")
	}

	// Valid: released_how is in allowed values
	attemptID5 := "attempt-5"
	_, err = conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID5, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt 5: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_at, released_how)
		VALUES (?, 'pool5', 'endpoint5', 'model5', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'server_confirmed')
	`, attemptID5)
	if err != nil {
		t.Fatalf("failed to insert capacity_hold with valid released_how: %v", err)
	}
}

func TestMigration004BlocksReviewerBlockID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Create a verdict for the block
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	verdictID := "verdict-1"
	_, err = conn.Exec(`
		INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
		VALUES (?, ?, 'hash', 'valid')
	`, verdictID, attemptID)
	if err != nil {
		t.Fatalf("failed to insert verdict: %v", err)
	}

	// Create a block with reviewer_block_id
	blockID := "block-1"
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state)
		VALUES (?, ?, 'hash', 'high', 'evidence', 'remedy', 'open')
	`, blockID, verdictID)
	if err != nil {
		t.Fatalf("failed to insert block: %v", err)
	}

	// Verify reviewer_block_id column exists
	var nullableID sql.NullString
	err = conn.QueryRow("SELECT reviewer_block_id FROM blocks WHERE id = ?", blockID).Scan(&nullableID)
	if err != nil {
		t.Fatalf("failed to query reviewer_block_id: %v", err)
	}
}

func TestMigration004V3UpgradePreservesRuns(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	// Create a schema at version 3
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("failed to close conn: %v", err)
		}
	}()

	// Create schema_version table and set to 3
	_, err = conn.Exec(`
		CREATE TABLE schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO schema_version (version) VALUES (1);
		INSERT INTO schema_version (version) VALUES (2);
		INSERT INTO schema_version (version) VALUES (3);
	`)
	if err != nil {
		t.Fatalf("failed to create schema_version: %v", err)
	}

	// Create minimal v3 schema
	_, err = conn.Exec(`
		CREATE TABLE runs (
			id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			manifest TEXT NOT NULL,
			manifest_hash TEXT NOT NULL
		);
		CREATE TABLE turns (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			mode TEXT NOT NULL CHECK (mode IN ('solo', 'review')),
			whole_turn_deadline TEXT,
			terminal_state TEXT CHECK (
				terminal_state IS NULL OR
				terminal_state IN ('approved', 'solo_complete', 'incomplete', 'overridden', 'cancelled')
			),
			FOREIGN KEY (run_id) REFERENCES runs(id)
		);
		CREATE TABLE attempts (
			id TEXT PRIMARY KEY,
			turn_id TEXT NOT NULL,
			participant TEXT NOT NULL,
			phase TEXT NOT NULL,
			epoch INTEGER NOT NULL,
			dispatched_at TEXT NOT NULL,
			deadline TEXT,
			state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'completed', 'unknown')),
			status TEXT CHECK (status IS NULL OR status IN ('complete', 'truncated', 'refused', 'interrupted', 'error')),
			cancellation_state TEXT NOT NULL DEFAULT 'none' CHECK (cancellation_state IN ('none', 'requested', 'confirmed', 'unknown')),
			quarantined BOOLEAN NOT NULL DEFAULT 0,
			raw_output TEXT,
			scrub_count INTEGER,
			observed_identity TEXT,
			native_finish TEXT,
			transport_status TEXT,
			repair_of TEXT,
			FOREIGN KEY (turn_id) REFERENCES turns(id),
			FOREIGN KEY (repair_of) REFERENCES attempts(id)
		);
		CREATE TABLE verdicts (
			id TEXT PRIMARY KEY,
			attempt_id TEXT NOT NULL,
			candidate_hash TEXT NOT NULL,
			parsed_verdict TEXT CHECK (parsed_verdict IS NULL OR parsed_verdict IN ('approve', 'block')),
			reason TEXT,
			block_id TEXT,
			severity TEXT CHECK (severity IS NULL OR severity IN ('low', 'medium', 'high')),
			evidence TEXT,
			remedy TEXT,
			validation_status TEXT NOT NULL CHECK (validation_status IN ('valid', 'rejected', 'malformed', 'revision_request')),
			FOREIGN KEY (attempt_id) REFERENCES attempts(id)
		);
		CREATE TABLE blocks (
			id TEXT PRIMARY KEY,
			verdict_id TEXT NOT NULL,
			candidate_hash TEXT NOT NULL,
			severity TEXT NOT NULL CHECK (severity IN ('low', 'medium', 'high')),
			evidence TEXT NOT NULL,
			remedy TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('open', 'withdrawn', 'overridden')),
			FOREIGN KEY (verdict_id) REFERENCES verdicts(id)
		);
		ALTER TABLE blocks ADD COLUMN withdrawn_against_hash TEXT;
		CREATE TABLE candidates (
			id TEXT PRIMARY KEY,
			turn_id TEXT NOT NULL,
			content TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			FOREIGN KEY (turn_id) REFERENCES turns(id)
		);
		CREATE TABLE messages (
			id TEXT PRIMARY KEY,
			turn_id TEXT NOT NULL,
			phase TEXT NOT NULL,
			sender TEXT NOT NULL,
			recipient_set TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			content TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			byte_length INTEGER NOT NULL,
			parent_ids TEXT,
			FOREIGN KEY (turn_id) REFERENCES turns(id)
		);
		CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			turn_id TEXT,
			event_type TEXT NOT NULL,
			timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			data TEXT,
			FOREIGN KEY (turn_id) REFERENCES turns(id)
		);
	`)
	if err != nil {
		t.Fatalf("failed to create v3 schema: %v", err)
	}

	// Insert a probe run in v3
	_, err = conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES ('run-1', '{}', 'hash')
	`)
	if err != nil {
		t.Fatalf("failed to insert probe run: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Logf("failed to close v3 connection: %v", err)
	}

	// Now open with the full journal.Open to apply migration 004
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database with migration: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}()

	// Verify the probe run now has kind = 'probe'
	var kind string
	err = db.Conn().QueryRow("SELECT kind FROM runs WHERE id = ?", "run-1").Scan(&kind)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if kind != "probe" {
		t.Errorf("expected existing run to have kind 'probe', got %q", kind)
	}

	// Verify schema_version is 4
	var version int
	err = db.Conn().QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}
	if version != 4 {
		t.Errorf("expected schema version 4, got %d", version)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
