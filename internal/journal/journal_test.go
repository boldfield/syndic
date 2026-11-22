package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
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

func createTestConversation(t *testing.T, conn *sql.DB, id string) {
	t.Helper()
	_, err := conn.Exec(`INSERT INTO conversations (id, workspace) VALUES (?, NULL)`, id)
	if err != nil {
		t.Fatalf("failed to create test conversation: %v", err)
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
		{"valid mode solo", "INSERT INTO turns (id, run_id, mode, conversation_id) VALUES (?, ?, ?, ?)", []interface{}{"turn-1", runID, "solo", "conv-1"}, true},
		{"valid mode review", "INSERT INTO turns (id, run_id, mode, conversation_id) VALUES (?, ?, ?, ?)", []interface{}{"turn-2", runID, "review", "conv-2"}, true},
		{"invalid mode", "INSERT INTO turns (id, run_id, mode, conversation_id) VALUES (?, ?, ?, ?)", []interface{}{"turn-3", runID, "invalid", "conv-3"}, false},
		{"valid terminal state approved", "INSERT INTO turns (id, run_id, mode, terminal_state, conversation_id) VALUES (?, ?, ?, ?, ?)", []interface{}{"turn-4", runID, "solo", "approved", "conv-4"}, true},
		{"valid terminal state solo_complete", "INSERT INTO turns (id, run_id, mode, terminal_state, conversation_id) VALUES (?, ?, ?, ?, ?)", []interface{}{"turn-5", runID, "solo", "solo_complete", "conv-5"}, true},
		{"invalid terminal state", "INSERT INTO turns (id, run_id, mode, terminal_state, conversation_id) VALUES (?, ?, ?, ?, ?)", []interface{}{"turn-6", runID, "solo", "invalid", "conv-6"}, false},
	}

	// Create conversations for the tests
	for i := 1; i <= 6; i++ {
		createTestConversation(t, conn, fmt.Sprintf("conv-%d", i))
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
		hash  interface{}
	}{
		{"valid open", "open", true, nil},
		{"valid withdrawn", "withdrawn", true, "withdrawn_hash"},
		{"valid overridden", "overridden", true, nil},
		{"invalid state", "invalid", false, nil},
	}

	for _, tc := range blockStateTests {
		t.Run("block_state_"+tc.name, func(t *testing.T) {
			blockID := "block-state-" + tc.value
			_, err := conn.Exec(`
				INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
				VALUES (?, ?, 'hash', 'low', 'evidence', 'remedy', ?, ?)
			`, blockID, verdictID, tc.value, tc.hash)
			if tc.valid && err != nil {
				t.Errorf("expected valid insert to succeed, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected invalid insert to fail, but succeeded")
			}
		})
	}
}

func TestFreshDBSchemaVersion4(t *testing.T) {
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
	if version != 10 {
		t.Errorf("expected schema version 10, got %d", version)
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

func TestUpgradePathV2ToV3(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	// Create v2 database manually
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	// Enable foreign keys
	_, err = conn.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	// Create schema_version table the way journal.go does
	_, err = conn.Exec(`
		CREATE TABLE schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("failed to create schema_version table: %v", err)
	}

	// Read and apply migrations 001 and 002
	migration001Content, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("failed to read migration 001: %v", err)
	}

	_, err = conn.Exec(string(migration001Content))
	if err != nil {
		t.Fatalf("failed to apply migration 001: %v", err)
	}

	migration002Content, err := os.ReadFile("migrations/002_events_nullable_turn_id.sql")
	if err != nil {
		t.Fatalf("failed to read migration 002: %v", err)
	}

	_, err = conn.Exec(string(migration002Content))
	if err != nil {
		t.Fatalf("failed to apply migration 002: %v", err)
	}

	// Record version 2 in schema_version
	_, err = conn.Exec("INSERT INTO schema_version (version) VALUES (1), (2)")
	if err != nil {
		t.Fatalf("failed to record schema versions: %v", err)
	}

	// Insert test run
	_, err = conn.Exec("INSERT INTO runs (id, manifest, manifest_hash) VALUES ('run-1', '{}', 'hash')")
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	// Insert test turn
	insertTurnFor(t, conn, "turn-1", "run-1", "review")

	// Insert test attempt and verdict
	_, err = conn.Exec("INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state) VALUES ('attempt-1', 'turn-1', 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed')")
	if err != nil {
		t.Fatalf("failed to insert attempt: %v", err)
	}

	_, err = conn.Exec("INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status) VALUES ('verdict-1', 'attempt-1', 'hash', 'valid')")
	if err != nil {
		t.Fatalf("failed to insert verdict: %v", err)
	}

	// Insert one open block
	_, err = conn.Exec("INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state) VALUES ('block-1', 'verdict-1', 'hash', 'low', 'evidence', 'remedy', 'open')")
	if err != nil {
		t.Fatalf("failed to insert block: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("failed to close connection: %v", err)
	}

	// Now open with journal.Open which applies migration 003
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

	// Check schema version is 3
	var version int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}
	if version != 10 {
		t.Errorf("expected schema version 10 after upgrade, got %d", version)
	}

	// Check that the block still exists with null withdrawn_against_hash and other fields unchanged
	var blockID string
	var state string
	var withdrawnHash interface{}
	var verdictID string
	var candidateHash string
	var severity string
	var evidence string
	var remedy string
	err = conn.QueryRow("SELECT id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash FROM blocks WHERE id = 'block-1'").Scan(&blockID, &verdictID, &candidateHash, &severity, &evidence, &remedy, &state, &withdrawnHash)
	if err != nil {
		t.Fatalf("failed to query block: %v", err)
	}
	if blockID != "block-1" {
		t.Errorf("expected block id 'block-1', got %q", blockID)
	}
	if verdictID != "verdict-1" {
		t.Errorf("expected verdict_id 'verdict-1', got %q", verdictID)
	}
	if candidateHash != "hash" {
		t.Errorf("expected candidate_hash 'hash', got %q", candidateHash)
	}
	if severity != "low" {
		t.Errorf("expected severity 'low', got %q", severity)
	}
	if evidence != "evidence" {
		t.Errorf("expected evidence 'evidence', got %q", evidence)
	}
	if remedy != "remedy" {
		t.Errorf("expected remedy 'remedy', got %q", remedy)
	}
	if state != "open" {
		t.Errorf("expected block state 'open', got %q", state)
	}
	if withdrawnHash != nil {
		t.Errorf("expected withdrawn_against_hash to be null, got %v", withdrawnHash)
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

	insertTurnFor(t, conn, "turn-1", "run-1", "review")

	_, err = conn.Exec("INSERT INTO events (event_type, data) VALUES ('probe_complete', 'probe data')")
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}

	// Get schema version, event count, and event details
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

	var eventID1 int64
	var eventType1, data1 string
	err = conn.QueryRow("SELECT id, event_type, data FROM events WHERE event_type = 'probe_complete'").Scan(&eventID1, &eventType1, &data1)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}

	// Capture sqlite_master entries for events table and its triggers before second open
	eventsMasterRows1, err := conn.Query("SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'events' OR (type = 'trigger' AND name LIKE 'events_%') ORDER BY name")
	if err != nil {
		t.Fatalf("failed to query sqlite_master: %v", err)
	}
	var master1Entries []map[string]string
	for eventsMasterRows1.Next() {
		var typ, name, sql sql.NullString
		if err := eventsMasterRows1.Scan(&typ, &name, &sql); err != nil {
			t.Fatalf("failed to scan sqlite_master row: %v", err)
		}
		master1Entries = append(master1Entries, map[string]string{
			"type": typ.String,
			"name": name.String,
			"sql":  sql.String,
		})
	}
	if err := eventsMasterRows1.Err(); err != nil {
		t.Fatalf("failed to iterate sqlite_master rows: %v", err)
	}
	if err := eventsMasterRows1.Close(); err != nil {
		t.Fatalf("failed to close sqlite_master rows: %v", err)
	}

	if err := db1.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

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

	var eventID2 int64
	var eventType2, data2 string
	err = conn.QueryRow("SELECT id, event_type, data FROM events WHERE event_type = 'probe_complete'").Scan(&eventID2, &eventType2, &data2)
	if err != nil {
		t.Fatalf("failed to query event after second open: %v", err)
	}

	// Capture sqlite_master entries after second open
	eventsMasterRows2, err := conn.Query("SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'events' OR (type = 'trigger' AND name LIKE 'events_%') ORDER BY name")
	if err != nil {
		t.Fatalf("failed to query sqlite_master after second open: %v", err)
	}
	var master2Entries []map[string]string
	for eventsMasterRows2.Next() {
		var typ, name, sql sql.NullString
		if err := eventsMasterRows2.Scan(&typ, &name, &sql); err != nil {
			t.Fatalf("failed to scan sqlite_master row: %v", err)
		}
		master2Entries = append(master2Entries, map[string]string{
			"type": typ.String,
			"name": name.String,
			"sql":  sql.String,
		})
	}
	if err := eventsMasterRows2.Err(); err != nil {
		t.Fatalf("failed to iterate sqlite_master rows: %v", err)
	}
	if err := eventsMasterRows2.Close(); err != nil {
		t.Fatalf("failed to close sqlite_master rows: %v", err)
	}

	if err := db2.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	if version1 != version2 {
		t.Errorf("schema version changed: %d -> %d", version1, version2)
	}
	if count1 != count2 {
		t.Errorf("event count changed: %d -> %d", count1, count2)
	}
	if eventID1 != eventID2 {
		t.Errorf("event id changed: %d -> %d", eventID1, eventID2)
	}
	if eventType1 != eventType2 {
		t.Errorf("event_type changed: %q -> %q", eventType1, eventType2)
	}
	if data1 != data2 {
		t.Errorf("event data changed: %q -> %q", data1, data2)
	}
	if len(master1Entries) != len(master2Entries) {
		t.Errorf("sqlite_master entries count changed: %d -> %d", len(master1Entries), len(master2Entries))
	}
	for i, entry1 := range master1Entries {
		if i >= len(master2Entries) {
			t.Errorf("sqlite_master entry %d missing after second open: %v", i, entry1)
			continue
		}
		entry2 := master2Entries[i]
		if entry1["type"] != entry2["type"] || entry1["name"] != entry2["name"] || entry1["sql"] != entry2["sql"] {
			t.Errorf("sqlite_master entry %d changed: %v -> %v", i, entry1, entry2)
		}
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

	// Create schema_version table
	_, err = conn.Exec(`
		CREATE TABLE schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("failed to create schema_version table: %v", err)
	}

	// Apply migration 001
	migration001Content, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("failed to read migration 001: %v", err)
	}

	_, err = conn.Exec(string(migration001Content))
	if err != nil {
		t.Fatalf("failed to apply migration 001: %v", err)
	}

	// Record version 1
	_, err = conn.Exec("INSERT INTO schema_version (version) VALUES (1)")
	if err != nil {
		t.Fatalf("failed to record schema version: %v", err)
	}

	// Insert test run and turn
	_, err = conn.Exec("INSERT INTO runs (id, manifest, manifest_hash) VALUES ('run-1', '{}', 'hash')")
	if err != nil {
		t.Fatalf("failed to insert run: %v", err)
	}

	insertTurnFor(t, conn, "turn-1", "run-1", "review")

	// Insert test event at v1
	_, err = conn.Exec("INSERT INTO events (turn_id, event_type, data) VALUES ('turn-1', 'test_event', 'event data')")
	if err != nil {
		t.Fatalf("failed to insert event: %v", err)
	}

	var eventID1 int64
	var eventType1, data1 string
	err = conn.QueryRow("SELECT id, event_type, data FROM events WHERE event_type = 'test_event'").Scan(&eventID1, &eventType1, &data1)
	if err != nil {
		t.Fatalf("failed to query event: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("failed to close connection: %v", err)
	}

	// Now open with journal.Open which applies migrations 002 and 003
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

	// Check schema version is 3
	var version int
	err = conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("failed to query schema version: %v", err)
	}
	if version != 10 {
		t.Errorf("expected schema version 10 after upgrade, got %d", version)
	}

	// Check that the event still exists with original data preserved
	var eventID2 int64
	var eventType2, data2 string
	var turnID interface{}
	err = conn.QueryRow("SELECT id, turn_id, event_type, data FROM events WHERE event_type = 'test_event'").Scan(&eventID2, &turnID, &eventType2, &data2)
	if err != nil {
		t.Fatalf("failed to query event after upgrade: %v", err)
	}
	if eventID2 != eventID1 {
		t.Errorf("expected event id %d, got %d", eventID1, eventID2)
	}
	if turnID != "turn-1" {
		t.Errorf("expected turn_id 'turn-1', got %v", turnID)
	}
	if eventType2 != eventType1 {
		t.Errorf("expected event_type %q, got %q", eventType1, eventType2)
	}
	if data2 != data1 {
		t.Errorf("expected data %q, got %q", data1, data2)
	}

	// Test immutability triggers still work after migration
	_, err = conn.Exec("UPDATE events SET data = ? WHERE id = ?", "new data", eventID2)
	if err == nil {
		t.Errorf("expected UPDATE on events to fail after migration, but succeeded")
	}

	_, err = conn.Exec("DELETE FROM events WHERE id = ?", eventID2)
	if err == nil {
		t.Errorf("expected DELETE on events to fail after migration, but succeeded")
	}
}

func TestWithdrawnAgainstHashConstraint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Insert attempt and verdict
	attemptID := "attempt-1"
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, 'participant-1', 'phase-1', 1, CURRENT_TIMESTAMP, 'completed')
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

	// Test 1: withdrawn block with null hash should fail
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-withdrawn-null', ?, 'hash', 'low', 'evidence', 'remedy', 'withdrawn', NULL)
	`, verdictID)
	if err == nil {
		t.Error("expected INSERT of withdrawn block with null hash to fail, but succeeded")
	}

	// Test 2: withdrawn block with hash should succeed
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-withdrawn-hash', ?, 'hash', 'low', 'evidence', 'remedy', 'withdrawn', 'withdrawn_hash')
	`, verdictID)
	if err != nil {
		t.Errorf("expected INSERT of withdrawn block with hash to succeed, got error: %v", err)
	}

	// Test 3: open block with null hash should succeed
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-open-null', ?, 'hash', 'low', 'evidence', 'remedy', 'open', NULL)
	`, verdictID)
	if err != nil {
		t.Errorf("expected INSERT of open block with null hash to succeed, got error: %v", err)
	}

	// Test 4: open block with hash should fail
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-open-hash', ?, 'hash', 'low', 'evidence', 'remedy', 'open', 'some_hash')
	`, verdictID)
	if err == nil {
		t.Error("expected INSERT of open block with hash to fail, but succeeded")
	}

	// Test 5: overridden block with null hash should succeed
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-overridden-null', ?, 'hash', 'low', 'evidence', 'remedy', 'overridden', NULL)
	`, verdictID)
	if err != nil {
		t.Errorf("expected INSERT of overridden block with null hash to succeed, got error: %v", err)
	}

	// Test 6: overridden block with hash should fail
	_, err = conn.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, withdrawn_against_hash)
		VALUES ('block-overridden-hash', ?, 'hash', 'low', 'evidence', 'remedy', 'overridden', 'some_hash')
	`, verdictID)
	if err == nil {
		t.Error("expected INSERT of overridden block with hash to fail, but succeeded")
	}

	// Test 7: UPDATE withdrawn block to have null hash should fail
	_, err = conn.Exec(`
		UPDATE blocks SET withdrawn_against_hash = NULL WHERE id = 'block-withdrawn-hash'
	`)
	if err == nil {
		t.Error("expected UPDATE of withdrawn block to null hash to fail, but succeeded")
	}

	// Test 8: UPDATE open block to have hash should fail
	_, err = conn.Exec(`
		UPDATE blocks SET withdrawn_against_hash = 'some_hash' WHERE id = 'block-open-null'
	`)
	if err == nil {
		t.Error("expected UPDATE of open block to have hash to fail, but succeeded")
	}

	// Test 9: UPDATE block to withdrawn without hash should fail
	_, err = conn.Exec(`
		UPDATE blocks SET state = 'withdrawn', withdrawn_against_hash = NULL WHERE id = 'block-open-null'
	`)
	if err == nil {
		t.Error("expected UPDATE block to withdrawn with null hash to fail, but succeeded")
	}

	// Test 10: UPDATE block to withdrawn with hash should succeed
	_, err = conn.Exec(`
		UPDATE blocks SET state = 'withdrawn', withdrawn_against_hash = 'new_hash' WHERE id = 'block-open-null'
	`)
	if err != nil {
		t.Errorf("expected UPDATE block to withdrawn with hash to succeed, got error: %v", err)
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

	_, err = conn.Exec(`INSERT INTO conversations (id) VALUES (?)`, "conv-"+turnID)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	_, err = conn.Exec(`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES (?, ?, 'review', ?)`, turnID, runID, "conv-"+turnID)
	if err != nil {
		t.Fatalf("failed to create turn: %v", err)
	}
}

func TestStoreCreateRunHashCorrectness(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{"test": "data"}`)
	runID, hash, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if runID == "" {
		t.Errorf("expected non-empty runID")
	}

	sum := sha256.Sum256(manifest)
	expectedHash := hex.EncodeToString(sum[:])
	if hash != expectedHash {
		t.Errorf("hash mismatch: got %q, expected %q", hash, expectedHash)
	}
}

func TestStoreCreateRunInvalidJSON(t *testing.T) {
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

	store := journal.NewStore(db)

	_, _, err = store.CreateRun([]byte(`invalid json`))
	if err == nil {
		t.Errorf("expected error for invalid JSON, but got none")
	}
}

func TestStoreCreateRunSecondConnectionVisibility(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db1, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db1.Close(); err != nil {
			t.Logf("failed to close db1: %v", err)
		}
	}()

	store := journal.NewStore(db1)
	manifest := []byte(`{"test": "data"}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	db2, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open second connection: %v", err)
	}
	defer func() {
		if err := db2.Close(); err != nil {
			t.Logf("failed to close db2: %v", err)
		}
	}()

	conn := db2.Conn()
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM runs WHERE id = ?", runID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if count != 1 {
		t.Errorf("expected run to be visible from second connection, but count=%d", count)
	}
}

func TestStoreCompleteAttemptTwiceError(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = store.CompleteAttempt(attemptID, "output1", 0, "model-id", "complete", "stop", "success")
	if err != nil {
		t.Fatalf("first CompleteAttempt failed: %v", err)
	}

	err = store.CompleteAttempt(attemptID, "output2", 0, "model-id", "complete", "stop", "success")
	if err == nil {
		t.Errorf("expected second CompleteAttempt to fail, but got none")
	}

	attempt, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("GetAttempt failed: %v", err)
	}
	if attempt.RawOutput == nil || *attempt.RawOutput != "output1" {
		t.Errorf("expected first output to be unchanged, got %v", attempt.RawOutput)
	}
}

func TestStoreCompleteAttemptRejectsNonPending(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = store.MarkUnknown(attemptID)
	if err != nil {
		t.Fatalf("MarkUnknown failed: %v", err)
	}

	err = store.CompleteAttempt(attemptID, "output", 0, "model-id", "complete", "stop", "success")
	if err == nil {
		t.Errorf("expected CompleteAttempt on non-pending to fail, but got none")
	}
}

func TestStoreMarkUnknownRejectsNonPending(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = store.CompleteAttempt(attemptID, "output", 0, "model-id", "complete", "stop", "success")
	if err != nil {
		t.Fatalf("CompleteAttempt failed: %v", err)
	}

	err = store.MarkUnknown(attemptID)
	if err == nil {
		t.Errorf("expected MarkUnknown on non-pending to fail, but got none")
	}
}

func TestStoreAppendMessageHashAndSequence(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	content := []byte("test message")
	msgID1, hash1, len1, err := store.AppendMessage(turnID, "phase-1", "sender-1", []string{"all"}, content, nil)
	if err != nil {
		t.Fatalf("first AppendMessage failed: %v", err)
	}

	sum := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(sum[:])
	if hash1 != expectedHash {
		t.Errorf("hash mismatch: got %q, expected %q", hash1, expectedHash)
	}

	if len1 != len(content) {
		t.Errorf("length mismatch: got %d, expected %d", len1, len(content))
	}

	msgID2, hash2, len2, err := store.AppendMessage(turnID, "phase-1", "sender-2", []string{"all"}, content, nil)
	if err != nil {
		t.Fatalf("second AppendMessage failed: %v", err)
	}

	if msgID1 == msgID2 {
		t.Errorf("expected different message IDs for identical content from different senders")
	}

	if hash1 != hash2 {
		t.Errorf("expected identical content to have same hash, got %q and %q", hash1, hash2)
	}

	if len1 != len2 {
		t.Errorf("expected identical content to have same length, got %d and %d", len1, len2)
	}

	conn := db.Conn()
	var seq1, seq2 int
	err = conn.QueryRow("SELECT sequence FROM messages WHERE id = ?", msgID1).Scan(&seq1)
	if err != nil {
		t.Fatalf("failed to query sequence for message 1: %v", err)
	}
	err = conn.QueryRow("SELECT sequence FROM messages WHERE id = ?", msgID2).Scan(&seq2)
	if err != nil {
		t.Fatalf("failed to query sequence for message 2: %v", err)
	}

	if seq2 <= seq1 {
		t.Errorf("expected increasing sequences: %d, %d", seq1, seq2)
	}
}

func TestStoreRecoverPending(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"
	db1, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	store := journal.NewStore(db1)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = db1.Close()
	if err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	db2, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen database: %v", err)
	}
	defer func() {
		if err := db2.Close(); err != nil {
			t.Logf("failed to close db2: %v", err)
		}
	}()

	store2 := journal.NewStore(db2)

	pendingAttempts, err := store2.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending failed: %v", err)
	}

	if len(pendingAttempts) != 1 || pendingAttempts[0] != attemptID {
		t.Errorf("expected to recover attempt %q, got %v", attemptID, pendingAttempts)
	}

	err = store2.MarkUnknown(attemptID)
	if err != nil {
		t.Fatalf("MarkUnknown failed: %v", err)
	}

	pendingAttempts, err = store2.RecoverPending()
	if err != nil {
		t.Fatalf("second RecoverPending failed: %v", err)
	}

	if len(pendingAttempts) != 0 {
		t.Errorf("expected no pending attempts after MarkUnknown, got %v", pendingAttempts)
	}
}

func TestStoreSetTurnTerminalRejectsAlreadyTerminal(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	err = store.SetTurnTerminal(turnID, "approved")
	if err != nil {
		t.Fatalf("first SetTurnTerminal failed: %v", err)
	}

	err = store.SetTurnTerminal(turnID, "incomplete")
	if err == nil {
		t.Errorf("expected SetTurnTerminal on already-terminal to fail, but got none")
	}
}

func TestStoreQuarantineLateOutputPreservesTerminalState(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	err = store.SetTurnTerminal(turnID, "approved")
	if err != nil {
		t.Fatalf("SetTurnTerminal failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = store.QuarantineLateOutput(attemptID, "late output", 0, "model-id", "complete", "stop", "success")
	if err != nil {
		t.Fatalf("QuarantineLateOutput failed: %v", err)
	}

	conn := db.Conn()
	var terminalState sql.NullString
	err = conn.QueryRow("SELECT terminal_state FROM turns WHERE id = ?", turnID).Scan(&terminalState)
	if err != nil {
		t.Fatalf("failed to query terminal state: %v", err)
	}

	if !terminalState.Valid || terminalState.String != "approved" {
		t.Errorf("terminal state was changed, expected 'approved', got %v", terminalState)
	}
}

func TestStoreQuarantineLateOutputRejectsCompleted(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	err = store.CompleteAttempt(attemptID, "output", 0, "model-id", "complete", "stop", "success")
	if err != nil {
		t.Fatalf("CompleteAttempt failed: %v", err)
	}

	err = store.QuarantineLateOutput(attemptID, "late output", 0, "model-id", "complete", "stop", "success")
	if err == nil {
		t.Errorf("expected QuarantineLateOutput on completed attempt to fail, but got none")
	}
}

func TestStoreGetRunParsesCURRENT_TIMESTAMP(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	run, err := store.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	if run.CreatedAt.IsZero() {
		t.Errorf("expected non-zero created_at timestamp")
	}
}

func TestStoreGetAttemptParsesDates(t *testing.T) {
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

	store := journal.NewStore(db)

	manifest := []byte(`{}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "participant-1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	attempt, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("GetAttempt failed: %v", err)
	}

	if attempt.DispatchedAt.IsZero() {
		t.Errorf("expected non-zero dispatched_at timestamp")
	}
}

func TestStoreBeginAttemptVisibleFromSecondConnection(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db1, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db1.Close(); err != nil {
			t.Logf("failed to close db1: %v", err)
		}
	}()

	store := journal.NewStore(db1)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}
	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}
	attemptID, err := store.BeginAttempt(turnID, "p1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}

	db2, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open second connection: %v", err)
	}
	defer func() {
		if err := db2.Close(); err != nil {
			t.Logf("failed to close db2: %v", err)
		}
	}()

	var state string
	if err := db2.Conn().QueryRow("SELECT state FROM attempts WHERE id = ?", attemptID).Scan(&state); err != nil {
		t.Fatalf("attempt not visible from second connection: %v", err)
	}
	if state != "pending" {
		t.Errorf("expected state pending, got %q", state)
	}
}

func TestStoreQuarantineLateOutputOnUnknownAttempt(t *testing.T) {
	db, err := journal.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}()

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}
	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn failed: %v", err)
	}
	if err := store.SetTurnTerminal(turnID, "approved"); err != nil {
		t.Fatalf("SetTurnTerminal failed: %v", err)
	}
	attemptID, err := store.BeginAttempt(turnID, "p1", "phase-1", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt failed: %v", err)
	}
	if err := store.MarkUnknown(attemptID); err != nil {
		t.Fatalf("MarkUnknown failed: %v", err)
	}
	if err := store.QuarantineLateOutput(attemptID, "late", 0, "model", "complete", "stop", "ok"); err != nil {
		t.Fatalf("QuarantineLateOutput on unknown attempt failed: %v", err)
	}

	var quarantined bool
	var terminal sql.NullString
	if err := db.Conn().QueryRow("SELECT quarantined FROM attempts WHERE id = ?", attemptID).Scan(&quarantined); err != nil {
		t.Fatalf("query attempt: %v", err)
	}
	if !quarantined {
		t.Error("expected attempt to be quarantined")
	}
	if err := db.Conn().QueryRow("SELECT terminal_state FROM turns WHERE id = ?", turnID).Scan(&terminal); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if terminal.String != "approved" {
		t.Errorf("turn terminal state changed: %v", terminal)
	}
}

func TestStoreAppendEventWithoutTurn(t *testing.T) {
	db, err := journal.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}()

	store := journal.NewStore(db)
	if err := store.AppendEvent("startup", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	var n int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM events WHERE turn_id IS NULL AND event_type = 'startup'").Scan(&n); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 event, got %d", n)
	}
}

// insertTurnFor inserts a turn row on a database at any schema version. From
// migration 007 on a turn must reference a conversation, so one is created
// (conv-<turnID>) when the table exists; older schemas get a bare turn.
func insertTurnFor(t *testing.T, conn *sql.DB, turnID, runID, mode string) {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='conversations'`).Scan(&n); err != nil {
		t.Fatalf("probe conversations table: %v", err)
	}
	if n == 0 {
		mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES (?, ?, ?)`, turnID, runID, mode)
		return
	}
	mustExec(t, conn, `INSERT INTO conversations (id) VALUES (?)`, "conv-"+turnID)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES (?, ?, ?, ?)`, turnID, runID, mode, "conv-"+turnID)
}

func TestGetReviewerOpenBlockIDsAcrossRounds(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	// Setup: create run and turn
	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	// Create two attempts (representing different rounds)
	attempt1ID := "attempt-1"
	attempt2ID := "attempt-2"
	reviewerParticipant := "reviewer-1"

	mustExec(t, conn, `
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, ?, 'review', 1, CURRENT_TIMESTAMP, 'completed')
	`, attempt1ID, turnID, reviewerParticipant)

	mustExec(t, conn, `
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, ?, 'review', 2, CURRENT_TIMESTAMP, 'completed')
	`, attempt2ID, turnID, reviewerParticipant)

	// Create verdicts for both attempts with different candidate hashes
	candidateHashA := "hash-candidate-a"
	candidateHashB := "hash-candidate-b"
	verdict1ID := "verdict-1"
	verdict2ID := "verdict-2"

	mustExec(t, conn, `
		INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
		VALUES (?, ?, ?, 'valid')
	`, verdict1ID, attempt1ID, candidateHashA)

	mustExec(t, conn, `
		INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
		VALUES (?, ?, ?, 'valid')
	`, verdict2ID, attempt2ID, candidateHashB)

	// Create an open block from round 1 on candidate A
	blockID := "block-1"
	mustExec(t, conn, `
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id)
		VALUES (?, ?, ?, 'low', 'evidence', 'remedy', 'open', ?)
	`, blockID, verdict1ID, candidateHashA, blockID)

	// A withdrawn block on candidate A and a block owned by another
	// participant must both stay out of the reviewer's set.
	mustExec(t, conn, `
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id, withdrawn_against_hash)
		VALUES ('block-withdrawn', ?, ?, 'low', 'e', 'r', 'withdrawn', 'block-withdrawn', ?)
	`, verdict1ID, candidateHashA, candidateHashB)
	mustExec(t, conn, `
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES ('attempt-other', ?, 'reviewer-2', 'review', 1, CURRENT_TIMESTAMP, 'completed')
	`, turnID)
	mustExec(t, conn, `
		INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status)
		VALUES ('verdict-other', 'attempt-other', ?, 'valid')
	`, candidateHashA)
	mustExec(t, conn, `
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id)
		VALUES ('block-other', 'verdict-other', ?, 'low', 'e', 'r', 'open', 'block-other')
	`, candidateHashA)

	// Looked up through the real method while a revised candidate B is current.
	store := journal.NewStore(db)
	var blockIDs map[string]bool
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		blockIDs, err = tx.GetReviewerOpenBlockIDs(turnID, reviewerParticipant)
		return err
	}); err != nil {
		t.Fatalf("GetReviewerOpenBlockIDs: %v", err)
	}
	if len(blockIDs) != 1 || !blockIDs[blockID] {
		t.Errorf("open blocks = %v, want only %q", blockIDs, blockID)
	}
}
