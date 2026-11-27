package journal_test

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func mustExec(t *testing.T, conn *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := conn.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func seed004(t *testing.T, conn *sql.DB) {
	t.Helper()
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	insertTurnFor(t, conn, "t1", "r1", "solo")
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES ('a1', 't1', 'p', 'review', 0, 'now')`)
}

func TestMigration004FreshVersion(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	var v int
	if err := db.Conn().QueryRow("SELECT MAX(version) FROM schema_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 22 {
		t.Errorf("version = %d, want 22", v)
	}
}

func TestMigration004UpgradeFromV3(t *testing.T) {
	path := t.TempDir() + "/test.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	mustExec(t, conn, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	for _, f := range []string{"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql"} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, conn, string(b))
	}
	mustExec(t, conn, `INSERT INTO schema_version (version) VALUES (1), (2), (3)`)
	seed004(t, conn)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	var kind string
	if err := db.Conn().QueryRow(`SELECT kind FROM runs WHERE id = 'r1'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "probe" {
		t.Errorf("kind = %q, want probe", kind)
	}
	var n int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM turns WHERE epoch = 0`).Scan(&n); err != nil || n != 1 {
		t.Errorf("turns epoch default: n=%d err=%v", n, err)
	}
}

func TestMigration004RunsKindCheck(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('e', '{}', 'h', 'engine')`)
	if _, err := conn.Exec(`INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('o', '{}', 'h', 'other')`); err == nil {
		t.Error("kind 'other' was accepted")
	}
	var sqlText string
	if err := conn.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'runs'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "CHECK (kind IN ('probe', 'engine'))") {
		t.Errorf("runs DDL lacks the kind CHECK: %s", sqlText)
	}
}

func TestMigration004SubmissionIDUnique(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO conversations (id) VALUES ('c')`)
	ins := `INSERT INTO turns (id, run_id, mode, submission_id, conversation_id) VALUES (?, 'r1', 'solo', ?, 'c')`
	mustExec(t, conn, ins, "t1", "s1")
	if _, err := conn.Exec(ins, "t2", "s1"); err == nil {
		t.Error("duplicate submission_id accepted")
	}
	mustExec(t, conn, ins, "t3", nil)
	mustExec(t, conn, ins, "t4", nil)
}

func tableColumns(t *testing.T, conn *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Logf("close rows: %v", err)
		}
	}()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestMigration004Columns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	want := map[string][]string{
		"runs": {"kind"},
		"turns": {"submission_id", "submission_hash", "epoch", "terminal_reason", "phase_at_cancel",
			"attempt_deadline_ms", "turn_deadline_ms", "preset", "quick_profile", "candidate_id", "budget_json"},
		"attempts": {"gate_error", "commentary", "delimiter", "max_output_tokens", "estimated_prompt_tokens",
			"usage_prompt", "usage_completion", "retry_count"},
		"blocks":             {"reviewer_block_id"},
		"attempt_dispatches": {"attempt_id", "dispatch_index", "dispatched_at"},
		"capacity_holds": {"attempt_id", "pool", "endpoint_name", "model", "group_json", "held_since",
			"released_at", "released_how", "released_reason"},
	}
	for table, cols := range want {
		have := tableColumns(t, db.Conn(), table)
		for _, c := range cols {
			if !have[c] {
				t.Errorf("%s missing column %s", table, c)
			}
		}
	}
	if tableColumns(t, db.Conn(), "config_snapshot")["id"] {
		t.Error("config_snapshot must not exist")
	}
}

func TestMigration004CapacityHolds(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	seed004(t, conn)
	ins := `INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_at, released_how)
		VALUES ('a1', 'p', 'e', 'm', '{}', 'now', ?, ?)`
	if _, err := conn.Exec(ins, "now", "manual"); err == nil {
		t.Error("released_how 'manual' accepted")
	}
	if _, err := conn.Exec(ins, "now", nil); err == nil {
		t.Error("released_at without released_how accepted")
	}
	if _, err := conn.Exec(ins, nil, "operator"); err == nil {
		t.Error("released_how without released_at accepted")
	}
	mustExec(t, conn, ins, nil, nil)
	mustExec(t, conn, `UPDATE capacity_holds SET released_at = 'now', released_how = 'late_result' WHERE attempt_id = 'a1'`)
}

func TestMigration004AttemptDispatchesPK(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	seed004(t, conn)
	ins := `INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at) VALUES ('a1', ?, 'now')`
	mustExec(t, conn, ins, 0)
	if _, err := conn.Exec(ins, 0); err == nil {
		t.Error("duplicate (attempt_id, dispatch_index) accepted")
	}
	mustExec(t, conn, ins, 1)
	if _, err := conn.Exec(`INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at) VALUES ('nope', 0, 'now')`); err == nil {
		t.Error("dangling attempt_id accepted")
	}
}
