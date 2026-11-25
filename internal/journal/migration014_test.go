package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
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
	tables := []string{"tool_content", "tool_calls", "tool_results", "observations", "change_sets", "change_set_paths", "apply_ops", "verdict_tool_results", "staging"}
	for _, table := range tables {
		var exists int
		err := conn.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists)
		if err != nil || exists != 1 {
			t.Errorf("table %s not created", table)
		}
	}

	// Verify tool_calls columns
	toolCallsCols := tableColumns(t, conn, "tool_calls")
	requiredToolCallsCols := []string{"id", "attempt_id", "name", "args_sha256", "class", "observed_identity"}
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
	requiredChangeSetsCols := []string{"id", "turn_id", "snapshot_hash", "changes_hash", "created_at"}
	for _, col := range requiredChangeSetsCols {
		if !changeSetsCols[col] {
			t.Errorf("change_sets.%s not found", col)
		}
	}

	// Verify change_set_paths columns
	changeSetPathsCols := tableColumns(t, conn, "change_set_paths")
	requiredPathCols := []string{"id", "change_set_id", "path", "op", "base_sha256", "new_sha256", "new_len"}
	for _, col := range requiredPathCols {
		if !changeSetPathsCols[col] {
			t.Errorf("change_set_paths.%s not found", col)
		}
	}

	// Verify apply_ops columns
	applyOpsCols := tableColumns(t, conn, "apply_ops")
	requiredApplyOpsCols := []string{"id", "change_set_id", "op_type", "path", "new_sha256", "outcome", "final_state", "applied_at"}
	for _, col := range requiredApplyOpsCols {
		if !applyOpsCols[col] {
			t.Errorf("apply_ops.%s not found", col)
		}
	}

	// Verify verdict_tool_results columns
	verdictToolResultsCols := tableColumns(t, conn, "verdict_tool_results")
	requiredVerdictToolCols := []string{"id", "verdict_id", "tool_result_id"}
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

// TestMigration014UpgradeFromV13 seeds a populated version-13 database and
// upgrades it through journal.Open, the real migration runner, then checks the
// v13 data survived and the new tables work against it.
func TestMigration014UpgradeFromV13(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v13.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 14 {
		t.Fatalf("migration files = %v, %v", files, err)
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
	for _, stmt := range []string{
		`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h1')`,
		`INSERT INTO conversations (id) VALUES ('c1')`,
		`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'review', 'c1')`,
		`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, text)
			VALUES ('a1', 't1', 'p1', 'author', 1, '2026-01-01T00:00:00Z', 'completed', 'hello')`,
		`INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, validation_status) VALUES ('v1', 'a1', 'ch', 'approve', 'valid')`,
		`INSERT INTO review_content (ref, body) VALUES ('commentary:a1', 'frozen commentary')`,
		`INSERT INTO events (turn_id, event_type, data) VALUES ('t1', 'old_event', '{}')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("upgrade via Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	c := db.Conn()

	var version int
	if err := c.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != len(files) {
		t.Fatalf("schema version = %d, %v; want %d", version, err, len(files))
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM runs WHERE id = 'r1'`,
		`SELECT COUNT(*) FROM turns WHERE id = 't1'`,
		`SELECT COUNT(*) FROM attempts WHERE id = 'a1' AND text = 'hello'`,
		`SELECT COUNT(*) FROM verdicts WHERE id = 'v1'`,
		`SELECT COUNT(*) FROM review_content WHERE ref = 'commentary:a1'`,
		`SELECT COUNT(*) FROM events WHERE event_type = 'old_event'`,
	} {
		var n int
		if err := c.QueryRow(q).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s = %d, %v; want 1", q, n, err)
		}
	}

	// The upgraded database accepts tool records tied to the old rows.
	s := journal.NewStore(db)
	err = s.Tx(func(tx *journal.Tx) error {
		sum, err := tx.PutToolContent([]byte("args"))
		if err != nil {
			return err
		}
		if err := tx.StoreToolCall(journal.ToolCall{ID: "tc1", AttemptID: "a1", Name: "read_file", ArgsSHA256: sum}); err != nil {
			return err
		}
		return tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "tc1", Status: journal.ToolCallStatusExecuted, RecordedAt: time.Now()})
	})
	if err != nil {
		t.Fatalf("tool records on upgraded db: %v", err)
	}
}
