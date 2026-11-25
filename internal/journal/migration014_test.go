package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestMigration014 verifies that migration 014 creates the tools schema correctly.
func TestMigration014(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Create schema_version table and apply migrations through 013
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 14 {
		t.Fatalf("migration files = %v, %v (need at least 14 migrations)", files, err)
	}

	// Apply migrations 001-014
	for i, f := range files[:14] {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(content)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
		if _, err := conn.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}

	// Now verify the tables exist
	tables := []string{"tool_calls", "tool_results", "observations", "change_sets", "change_set_paths", "apply_ops", "verdict_tool_results", "staging"}
	for _, table := range tables {
		var exists int
		err := conn.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists)
		if err != nil || exists != 1 {
			t.Errorf("table %s not created", table)
		}
	}

	// Verify tool_calls columns
	toolCallsCols := tableColumns(t, conn, "tool_calls")
	requiredToolCallsCols := []string{"id", "attempt_id", "name", "args_sha256", "class", "status", "result_sha256", "result_byte_len", "observed_identity", "error_message"}
	for _, col := range requiredToolCallsCols {
		if !toolCallsCols[col] {
			t.Errorf("tool_calls.%s not found", col)
		}
	}

	// Verify tool_results columns
	toolResultsCols := tableColumns(t, conn, "tool_results")
	requiredToolResultsCols := []string{"id", "tool_call_id", "status", "result_sha256", "result_byte_len", "error_message", "recorded_at"}
	for _, col := range requiredToolResultsCols {
		if !toolResultsCols[col] {
			t.Errorf("tool_results.%s not found", col)
		}
	}

	// Verify observations columns
	observationsCols := tableColumns(t, conn, "observations")
	requiredObsCols := []string{"id", "turn_id", "attempt_id", "snapshot_version", "path", "kind", "base_sha256", "entries_sha256", "observed_at"}
	for _, col := range requiredObsCols {
		if !observationsCols[col] {
			t.Errorf("observations.%s not found", col)
		}
	}

	// Verify change_sets columns
	changeSetsCols := tableColumns(t, conn, "change_sets")
	requiredChangeSetsCols := []string{"id", "turn_id", "state", "snapshot_hash", "changes_hash", "created_at"}
	for _, col := range requiredChangeSetsCols {
		if !changeSetsCols[col] {
			t.Errorf("change_sets.%s not found", col)
		}
	}

	// Verify change_set_paths columns
	changeSetPathsCols := tableColumns(t, conn, "change_set_paths")
	requiredPathCols := []string{"id", "change_set_id", "path", "op", "base_sha256", "new_sha256", "new_len", "outcome"}
	for _, col := range requiredPathCols {
		if !changeSetPathsCols[col] {
			t.Errorf("change_set_paths.%s not found", col)
		}
	}

	// Verify apply_ops columns
	applyOpsCols := tableColumns(t, conn, "apply_ops")
	requiredApplyOpsCols := []string{"id", "change_set_id", "op_type", "path", "new_sha256", "applied_at"}
	for _, col := range requiredApplyOpsCols {
		if !applyOpsCols[col] {
			t.Errorf("apply_ops.%s not found", col)
		}
	}

	// Verify verdict_tool_results columns
	verdictToolResultsCols := tableColumns(t, conn, "verdict_tool_results")
	requiredVerdictToolCols := []string{"id", "verdict_id", "tool_call_id"}
	for _, col := range requiredVerdictToolCols {
		if !verdictToolResultsCols[col] {
			t.Errorf("verdict_tool_results.%s not found", col)
		}
	}

	// Verify staging columns
	stagingCols := tableColumns(t, conn, "staging")
	requiredStagingCols := []string{"id", "turn_id", "path", "new_content_hash", "staged_at"}
	for _, col := range requiredStagingCols {
		if !stagingCols[col] {
			t.Errorf("staging.%s not found", col)
		}
	}
}

// TestMigration014UpgradeFromV13 tests that data at v13 is retained after upgrading to v14.
func TestMigration014UpgradeFromV13(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Create schema_version table
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	// Apply migrations 001-013
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 13 {
		t.Fatalf("migration files = %v, %v (need at least 13 migrations)", files, err)
	}

	for i, f := range files[:13] {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(content)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
		if _, err := conn.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}

	// Insert D data at v13
	runID := "run-upgrade-test"
	turnID := "turn-1"
	attemptID := "attempt-1"

	if _, err := conn.Exec(`INSERT INTO runs (id, model, messages_sent, tokens_in, tokens_out, frozen_at) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, "opus", 1, 100, 200, nil); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, turn_number, state, assigned_to, epoch, submitted_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		turnID, runID, 1, "completed", "participant1", 1, nil); err != nil {
		t.Fatalf("insert turn: %v", err)
	}

	if _, err := conn.Exec(`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		attemptID, turnID, "participant1", "author", 1, nil, "completed"); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}

	// Now apply migration 014
	content, err := os.ReadFile("migrations/014_tools.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(content)); err != nil {
		t.Fatalf("apply migration 014: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO schema_version (version) VALUES (14)`); err != nil {
		t.Fatal(err)
	}

	// Verify the D data still exists
	var count int
	if err := conn.QueryRow("SELECT COUNT(*) FROM runs WHERE id = ?", runID).Scan(&count); err != nil {
		t.Fatalf("query runs: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 run after upgrade, got %d", count)
	}

	if err := conn.QueryRow("SELECT COUNT(*) FROM turns WHERE id = ?", turnID).Scan(&count); err != nil {
		t.Fatalf("query turns: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 turn after upgrade, got %d", count)
	}

	if err := conn.QueryRow("SELECT COUNT(*) FROM attempts WHERE id = ?", attemptID).Scan(&count); err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 attempt after upgrade, got %d", count)
	}

	// Verify schema version is 14
	var version int
	if err := conn.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Errorf("expected schema version 14 after upgrade, got %d", version)
	}
}
