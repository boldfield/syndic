package journal_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestOpenCreatesSchema(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	// First open should create the schema
	db1, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	defer func() {
		if err := db1.Close(); err != nil {
			t.Logf("failed to close db1: %v", err)
		}
	}()

	// Verify tables exist by checking if we can insert into them
	conn := db1.Conn()
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query tables: %v", err)
	}
	if count < 8 {
		t.Fatalf("expected at least 8 tables, got %d", count)
	}

	// Second open on same file should succeed and not change schema version
	db2, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer func() {
		if err := db2.Close(); err != nil {
			t.Logf("failed to close db2: %v", err)
		}
	}()

	var version1, version2 int
	err = conn.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version1)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}

	conn2 := db2.Conn()
	err = conn2.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version2)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}

	if version1 != version2 {
		t.Fatalf("schema version changed: %d -> %d", version1, version2)
	}
}

func TestEnumCheckConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create a test run first
	runID := "run-1"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Test turn mode enum
	testCases := []struct {
		name  string
		query string
		args  []interface{}
		valid bool
	}{
		{"valid mode solo", "INSERT INTO turns (id, run_id, mode) VALUES (?, ?, ?)", []interface{}{"turn-1", runID, "solo"}, true},
		{"valid mode review", "INSERT INTO turns (id, run_id, mode) VALUES (?, ?, ?)", []interface{}{"turn-2", runID, "review"}, true},
		{"invalid mode", "INSERT INTO turns (id, run_id, mode) VALUES (?, ?, ?)", []interface{}{"turn-3", runID, "invalid"}, false},
		{"valid terminal state approved", "INSERT INTO turns (id, run_id, mode, terminal_state) VALUES (?, ?, ?, ?)", []interface{}{"turn-4", runID, "solo", "approved"}, true},
		{"valid terminal state solo_complete", "INSERT INTO turns (id, run_id, mode, terminal_state) VALUES (?, ?, ?, ?)", []interface{}{"turn-5", runID, "solo", "solo_complete"}, true},
		{"invalid terminal state", "INSERT INTO turns (id, run_id, mode, terminal_state) VALUES (?, ?, ?, ?)", []interface{}{"turn-6", runID, "solo", "invalid"}, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := conn.Exec(tc.query, tc.args...)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}
}

func TestAttemptEnumCheckConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	testCases := []struct {
		name  string
		state string
		valid bool
	}{
		{"valid state pending", "pending", true},
		{"valid state completed", "completed", true},
		{"valid state unknown", "unknown", true},
		{"invalid state", "invalid", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			attemptID := "attempt-" + tc.state
			_, err := conn.Exec(`
				INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
				VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, ?)
			`, attemptID, turnID, tc.state)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Test status enum
	statusTestCases := []struct {
		name   string
		status interface{}
		valid  bool
	}{
		{"null status", nil, true},
		{"valid status complete", "complete", true},
		{"valid status truncated", "truncated", true},
		{"valid status refused", "refused", true},
		{"valid status interrupted", "interrupted", true},
		{"valid status error", "error", true},
		{"invalid status", "invalid", false},
	}

	for _, tc := range statusTestCases {
		t.Run(tc.name, func(t *testing.T) {
			attemptID := "attempt-status-" + t.Name()
			var query string
			var args []interface{}
			if tc.status == nil {
				query = `
					INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, status)
					VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed', NULL)
				`
				args = []interface{}{attemptID, turnID}
			} else {
				query = `
					INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, status)
					VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed', ?)
				`
				args = []interface{}{attemptID, turnID, tc.status}
			}
			_, err := conn.Exec(query, args...)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}
}

func TestImmutableTables(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Test messages immutability
	messageID := "msg-1"
	_, err := conn.Exec(`
		INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length)
		VALUES (?, ?, 'phase-1', 'sender-1', 'all', 1, 'test content', 'hash', 12)
	`, messageID, turnID)
	if err != nil {
		t.Fatalf("failed to insert message: %v", err)
	}

	// Try to update message
	_, err = conn.Exec("UPDATE messages SET content = ? WHERE id = ?", "new content", messageID)
	if err == nil {
		t.Errorf("expected UPDATE on messages to fail, but succeeded")
	}

	// Try to delete message
	_, err = conn.Exec("DELETE FROM messages WHERE id = ?", messageID)
	if err == nil {
		t.Errorf("expected DELETE on messages to fail, but succeeded")
	}

	// Test candidates immutability
	candidateID := "cand-1"
	_, err = conn.Exec(`
		INSERT INTO candidates (id, turn_id, content, content_hash)
		VALUES (?, ?, 'test candidate', 'hash')
	`, candidateID, turnID)
	if err != nil {
		t.Fatalf("failed to insert candidate: %v", err)
	}

	// Try to update candidate
	_, err = conn.Exec("UPDATE candidates SET content = ? WHERE id = ?", "new candidate", candidateID)
	if err == nil {
		t.Errorf("expected UPDATE on candidates to fail, but succeeded")
	}

	// Try to delete candidate
	_, err = conn.Exec("DELETE FROM candidates WHERE id = ?", candidateID)
	if err == nil {
		t.Errorf("expected DELETE on candidates to fail, but succeeded")
	}

	// Test events immutability
	_, err = conn.Exec(`
		INSERT INTO events (turn_id, event_type, data)
		VALUES (?, 'dispatch', 'event data')
	`, turnID)
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}

	// Try to update event
	_, err = conn.Exec("UPDATE events SET data = ? WHERE id = 1", "new data")
	if err == nil {
		t.Errorf("expected UPDATE on events to fail, but succeeded")
	}

	// Try to delete event
	_, err = conn.Exec("DELETE FROM events WHERE id = 1")
	if err == nil {
		t.Errorf("expected DELETE on events to fail, but succeeded")
	}
}

func TestRawOutputImmutability(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Create attempt with raw_output
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, raw_output)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed', 'first output')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	// Try to set raw_output again
	_, err = conn.Exec("UPDATE attempts SET raw_output = ? WHERE id = ?", "second output", attemptID)
	if err == nil {
		t.Errorf("expected UPDATE of raw_output to fail when already set, but succeeded")
	}

	// Verify original value is unchanged
	var output string
	err = conn.QueryRow("SELECT raw_output FROM attempts WHERE id = ?", attemptID).Scan(&output)
	if err != nil {
		t.Fatalf("failed to query raw_output: %v", err)
	}
	if output != "first output" {
		t.Errorf("raw_output was changed to %q", output)
	}
}

func TestRawOutputClearingPrevented(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Create attempt with raw_output
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, raw_output)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed', 'first output')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	// Try to clear raw_output
	_, err = conn.Exec("UPDATE attempts SET raw_output = NULL WHERE id = ?", attemptID)
	if err == nil {
		t.Errorf("expected UPDATE to clear raw_output to fail, but succeeded")
	}

	// Verify value is still set
	var output string
	err = conn.QueryRow("SELECT raw_output FROM attempts WHERE id = ?", attemptID).Scan(&output)
	if err != nil {
		t.Fatalf("failed to query raw_output: %v", err)
	}
	if output != "first output" {
		t.Errorf("raw_output was changed to %q", output)
	}
}

func TestManifestImmutability(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run
	runID := "run-1"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash1')
	`, runID)
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	// Try to update manifest
	_, err = conn.Exec("UPDATE runs SET manifest = ? WHERE id = ?", "{}", runID)
	if err == nil {
		t.Errorf("expected UPDATE of manifest to fail, but succeeded")
	}

	// Try to update manifest_hash
	_, err = conn.Exec("UPDATE runs SET manifest_hash = ? WHERE id = ?", "hash2", runID)
	if err == nil {
		t.Errorf("expected UPDATE of manifest_hash to fail, but succeeded")
	}
}

func TestForeignKeyConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Try to insert attempt for non-existent turn
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at)
		VALUES ('attempt-1', 'nonexistent-turn', 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP)
	`)
	if err == nil {
		t.Errorf("expected foreign key constraint to prevent insert, but succeeded")
	}
}

func TestForeignKeyEnforcedOnNewConnection(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	conn := db.Conn()

	// Set MaxIdleConns to 0 to force a new connection on the next query
	conn.SetMaxIdleConns(0)

	// Try to insert attempt for non-existent turn on a new connection
	_, err = conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at)
		VALUES ('attempt-1', 'nonexistent-turn', 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP)
	`)
	if err == nil {
		t.Errorf("expected foreign key constraint to prevent insert on new connection, but succeeded")
	}
}

func TestNullableFields(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Insert attempt and verify nullable fields
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP)
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	// Verify nullable fields are NULL
	var (
		status            sql.NullString
		cancellationState string
		quarantined       bool
	)
	err = conn.QueryRow(`
		SELECT status, cancellation_state, quarantined FROM attempts WHERE id = ?
	`, attemptID).Scan(&status, &cancellationState, &quarantined)
	if err != nil {
		t.Fatalf("failed to query attempt: %v", err)
	}

	if status.Valid {
		t.Errorf("expected status to be NULL, got %q", status.String)
	}
	if cancellationState != "none" {
		t.Errorf("expected cancellation_state to be 'none', got %q", cancellationState)
	}
	if quarantined {
		t.Errorf("expected quarantined to be false")
	}
}

func TestMissingEnumCheckConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Test attempts.cancellation_state enum
	cancellationStateTests := []struct {
		name  string
		value string
		valid bool
	}{
		{"valid none", "none", true},
		{"valid requested", "requested", true},
		{"valid confirmed", "confirmed", true},
		{"valid unknown", "unknown", true},
		{"invalid state", "invalid", false},
	}

	for _, tc := range cancellationStateTests {
		t.Run("cancellation_state_"+tc.name, func(t *testing.T) {
			attemptID := "attempt-cancel-" + tc.value
			_, err := conn.Exec(`
				INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, cancellation_state)
				VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'pending', ?)
			`, attemptID, turnID, tc.value)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Create an attempt for verdict tests
	attemptID := "attempt-for-verdicts"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed')
	`, attemptID, turnID)
	if err != nil {
		t.Fatalf("failed to insert attempt for verdict tests: %v", err)
	}

	// Test verdicts.parsed_verdict enum
	parsedVerdictTests := []struct {
		name  string
		value interface{}
		valid bool
	}{
		{"null parsed_verdict", nil, true},
		{"valid approve", "approve", true},
		{"valid block", "block", true},
		{"invalid verdict", "invalid", false},
	}

	for _, tc := range parsedVerdictTests {
		t.Run("parsed_verdict_"+tc.name, func(t *testing.T) {
			verdictID := "verdict-" + tc.name
			var query string
			var args []interface{}
			if tc.value == nil {
				query = `
					INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, validation_status)
					VALUES (?, ?, 'hash', NULL, 'valid')
				`
				args = []interface{}{verdictID, attemptID}
			} else {
				query = `
					INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, validation_status)
					VALUES (?, ?, 'hash', ?, 'valid')
				`
				args = []interface{}{verdictID, attemptID, tc.value}
			}
			_, err := conn.Exec(query, args...)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Test verdicts.validation_status enum
	validationStatusTests := []struct {
		name  string
		value string
		valid bool
	}{
		{"valid valid", "valid", true},
		{"valid rejected", "rejected", true},
		{"valid malformed", "malformed", true},
		{"valid revision_request", "revision_request", true},
		{"invalid status", "invalid", false},
	}

	for _, tc := range validationStatusTests {
		t.Run("validation_status_"+tc.name, func(t *testing.T) {
			verdictID := "verdict-val-" + tc.value
			_, err := conn.Exec(`
				INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
				VALUES (?, ?, 'hash', ?)
			`, verdictID, attemptID, tc.value)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Test verdicts.severity enum (nullable)
	verdictSeverityTests := []struct {
		name  string
		value interface{}
		valid bool
	}{
		{"null severity", nil, true},
		{"valid low", "low", true},
		{"valid medium", "medium", true},
		{"valid high", "high", true},
		{"invalid severity", "invalid", false},
	}

	for _, tc := range verdictSeverityTests {
		t.Run("verdict_severity_"+tc.name, func(t *testing.T) {
			verdictID := "verdict-sev-" + tc.name
			var query string
			var args []interface{}
			if tc.value == nil {
				query = `
					INSERT INTO verdicts (id, attempt_id, candidate_hash, severity, validation_status)
					VALUES (?, ?, 'hash', NULL, 'valid')
				`
				args = []interface{}{verdictID, attemptID}
			} else {
				query = `
					INSERT INTO verdicts (id, attempt_id, candidate_hash, severity, validation_status)
					VALUES (?, ?, 'hash', ?, 'valid')
				`
				args = []interface{}{verdictID, attemptID, tc.value}
			}
			_, err := conn.Exec(query, args...)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Create a verdict for block tests
	verdictID := "verdict-for-blocks"
	_, err = conn.Exec(`
		INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
		VALUES (?, ?, 'hash', 'valid')
	`, verdictID, attemptID)
	if err != nil {
		t.Fatalf("failed to insert verdict for block tests: %v", err)
	}

	// Test blocks.severity enum
	blockSeverityTests := []struct {
		name  string
		value string
		valid bool
	}{
		{"valid low", "low", true},
		{"valid medium", "medium", true},
		{"valid high", "high", true},
		{"invalid severity", "invalid", false},
	}

	for _, tc := range blockSeverityTests {
		t.Run("block_severity_"+tc.name, func(t *testing.T) {
			blockID := "block-sev-" + tc.value
			_, err := conn.Exec(`
				INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state)
				VALUES (?, ?, 'hash', ?, 'evidence', 'remedy', 'open')
			`, blockID, verdictID, tc.value)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}

	// Test blocks.state enum
	blockStateTests := []struct {
		name  string
		value string
		valid bool
	}{
		{"valid open", "open", true},
		{"valid withdrawn", "withdrawn", true},
		{"valid overridden", "overridden", true},
		{"invalid state", "invalid", false},
	}

	for _, tc := range blockStateTests {
		t.Run("block_state_"+tc.name, func(t *testing.T) {
			blockID := "block-state-" + tc.value
			_, err := conn.Exec(`
				INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state)
				VALUES (?, ?, 'hash', 'low', 'evidence', 'remedy', ?)
			`, blockID, verdictID, tc.value)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}
}

func TestFreshDBSchemaVersion2(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	conn := db.Conn()
	var version int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}
	if version != 2 {
		t.Errorf("expected schema version 2, got %d", version)
	}
}

func TestNullTurnIDInsert(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Insert event with NULL turn_id
	_, err := conn.Exec(`
		INSERT INTO events (event_type, data)
		VALUES ('probe_complete', 'probe data')
	`)
	if err != nil {
		t.Errorf("expected NULL turn_id insert to succeed, got error: %v", err)
	}

	// Verify the event was inserted
	var eventType string
	err = conn.QueryRow("SELECT event_type FROM events WHERE turn_id IS NULL").Scan(&eventType)
	if err != nil {
		t.Errorf("failed to query inserted event: %v", err)
	}
	if eventType != "probe_complete" {
		t.Errorf("expected event_type 'probe_complete', got %q", eventType)
	}
}

func TestForeignKeyConstraintWithNullableTurnID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Try to insert event with invalid turn_id
	_, err := conn.Exec(`
		INSERT INTO events (turn_id, event_type, data)
		VALUES ('nonexistent-turn', 'some_event', 'data')
	`)
	if err == nil {
		t.Errorf("expected foreign key constraint to prevent insert, but succeeded")
	}

	// Verify event was not inserted
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM events WHERE turn_id = 'nonexistent-turn'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no events with invalid turn_id, got %d", count)
	}
}

func TestUpgradePathV1ToV2(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	// Create v1 database manually
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	// Enable foreign keys
	_, err = conn.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	// Apply migration 001
	migration001 := `
	-- Turn table with mode and terminal_state enums
	CREATE TABLE turns (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		mode TEXT NOT NULL CHECK(mode IN ('solo', 'review')),
		terminal_state TEXT CHECK(terminal_state IN ('approved', 'solo_complete', NULL)),
		FOREIGN KEY (run_id) REFERENCES runs(id)
	);

	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		turn_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		data TEXT,
		FOREIGN KEY (turn_id) REFERENCES turns(id)
	);

	CREATE TRIGGER events_update_prevented BEFORE UPDATE ON events
	BEGIN
		SELECT RAISE(ABORT, 'events table is immutable');
	END;

	CREATE TRIGGER events_delete_prevented BEFORE DELETE ON events
	BEGIN
		SELECT RAISE(ABORT, 'events table is immutable');
	END;

	CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		manifest TEXT NOT NULL,
		manifest_hash TEXT NOT NULL
	);

	CREATE TABLE schema_version (
		version INTEGER PRIMARY KEY
	);

	INSERT INTO schema_version (version) VALUES (1);
	`

	_, err = conn.Exec(migration001)
	if err != nil {
		t.Fatalf("failed to apply migration 001: %v", err)
	}

	// Insert test run
	_, err = conn.Exec("INSERT INTO runs (id, manifest, manifest_hash) VALUES ('run-1', '{}', 'hash')")
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	// Insert test turn
	_, err = conn.Exec("INSERT INTO turns (id, run_id, mode) VALUES ('turn-1', 'run-1', 'review')")
	if err != nil {
		t.Fatalf("failed to insert turn: %v", err)
	}

	// Insert two events
	_, err = conn.Exec("INSERT INTO events (turn_id, event_type, data) VALUES ('turn-1', 'event1', 'data1')")
	if err != nil {
		t.Fatalf("failed to insert event 1: %v", err)
	}

	var event1ID int64
	err = conn.QueryRow("SELECT id FROM events WHERE event_type = 'event1'").Scan(&event1ID)
	if err != nil {
		t.Fatalf("failed to get event1 id: %v", err)
	}

	_, err = conn.Exec("INSERT INTO events (turn_id, event_type, data) VALUES ('turn-1', 'event2', 'data2')")
	if err != nil {
		t.Fatalf("failed to insert event 2: %v", err)
	}

	var event2ID int64
	err = conn.QueryRow("SELECT id FROM events WHERE event_type = 'event2'").Scan(&event2ID)
	if err != nil {
		t.Fatalf("failed to get event2 id: %v", err)
	}

	conn.Close()

	// Now open with journal.Open which applies migrations
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database with journal: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	conn = db.Conn()

	// Check schema version is 2
	var version int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}
	if version != 2 {
		t.Errorf("expected schema version 2 after upgrade, got %d", version)
	}

	// Check events still exist with same IDs, event types, and data
	var id int64
	var eventType, data string
	err = conn.QueryRow("SELECT id, event_type, data FROM events WHERE event_type = 'event1'").Scan(&id, &eventType, &data)
	if err != nil {
		t.Fatalf("failed to query event1: %v", err)
	}
	if id != event1ID {
		t.Errorf("event1 id changed from %d to %d", event1ID, id)
	}
	if eventType != "event1" {
		t.Errorf("expected event_type 'event1', got %q", eventType)
	}
	if data != "data1" {
		t.Errorf("expected data 'data1', got %q", data)
	}

	err = conn.QueryRow("SELECT id, event_type, data FROM events WHERE event_type = 'event2'").Scan(&id, &eventType, &data)
	if err != nil {
		t.Fatalf("failed to query event2: %v", err)
	}
	if id != event2ID {
		t.Errorf("event2 id changed from %d to %d", event2ID, id)
	}
	if eventType != "event2" {
		t.Errorf("expected event_type 'event2', got %q", eventType)
	}
	if data != "data2" {
		t.Errorf("expected data 'data2', got %q", data)
	}
}

func TestEventsImmutabilityAfterMigration(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Insert event with NULL turn_id
	_, err := conn.Exec(`
		INSERT INTO events (event_type, data)
		VALUES ('probe_complete', 'probe data')
	`)
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}

	var eventID int64
	err = conn.QueryRow("SELECT id FROM events WHERE turn_id IS NULL").Scan(&eventID)
	if err != nil {
		t.Fatalf("failed to get event id: %v", err)
	}

	// Try to update event
	_, err = conn.Exec("UPDATE events SET data = ? WHERE id = ?", "new data", eventID)
	if err == nil {
		t.Errorf("expected UPDATE on events to fail, but succeeded")
	}

	// Try to delete event
	_, err = conn.Exec("DELETE FROM events WHERE id = ?", eventID)
	if err == nil {
		t.Errorf("expected DELETE on events to fail, but succeeded")
	}

	// Verify data is unchanged
	var data string
	err = conn.QueryRow("SELECT data FROM events WHERE id = ?", eventID).Scan(&data)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}
	if data != "probe data" {
		t.Errorf("expected data 'probe data', got %q", data)
	}
}

func TestOpenOnMigratedDBNoChange(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	// First open creates schema
	db1, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}

	conn := db1.Conn()

	// Insert test data
	_, err = conn.Exec("INSERT INTO runs (id, manifest, manifest_hash) VALUES ('run-1', '{}', 'hash')")
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	_, err = conn.Exec("INSERT INTO turns (id, run_id, mode) VALUES ('turn-1', 'run-1', 'review')")
	if err != nil {
		t.Fatalf("failed to insert turn: %v", err)
	}

	_, err = conn.Exec("INSERT INTO events (event_type, data) VALUES ('probe_complete', 'probe data')")
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}

	// Get schema version and event count
	var version1 int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version1)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}

	var count1 int
	err = conn.QueryRow("SELECT COUNT(*) FROM events").Scan(&count1)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}

	db1.Close()

	// Second open should not change anything
	db2, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}

	conn = db2.Conn()

	var version2 int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version2)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}

	var count2 int
	err = conn.QueryRow("SELECT COUNT(*) FROM events").Scan(&count2)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}

	db2.Close()

	if version1 != version2 {
		t.Errorf("schema version changed: %d -> %d", version1, version2)
	}
	if count1 != count2 {
		t.Errorf("event count changed: %d -> %d", count1, count2)
	}
}

// openTestDB opens a database in a temporary directory for testing
func openTestDB(t *testing.T) (*journal.DB, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	return db, func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}
}

// setupTestData creates a run and turn for testing
func setupTestData(t *testing.T, conn *sql.DB, runID, turnID string) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, '{}', 'hash')
	`, runID)
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode)
		VALUES (?, ?, 'review')
	`, turnID, runID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}
}
