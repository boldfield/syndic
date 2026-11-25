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
	tables := []string{"tool_calls", "observations", "change_sets", "change_set_paths", "apply_ops", "verdict_tool_results", "staging"}
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
