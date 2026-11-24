package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestMigration013RebuildPreservesAttempts upgrades a populated version-12
// database holding a turn attempt and a probe attempt, and checks the rebuilt
// attempts table keeps every column and dependent and admits tool_calls.
func TestMigration013RebuildPreservesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 13 {
		t.Fatalf("migration files = %v, %v", files, err)
	}
	for i, f := range files[:12] {
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
		`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`,
		`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, retry_count, journal_seq, text)
			VALUES ('a1', 't1', 'p1', 'review', 1, '2026-01-01T00:00:00Z', 'pending', 2, 7, 'hello')`,
		`INSERT INTO attempts (id, participant, phase, epoch, dispatched_at, probe_run_id, probe_endpoint, probe_model, probe_pool, probe_group, probe_step)
			VALUES ('a2', 'p2', 'probe', 1, '2026-01-01T00:00:00Z', 'r1', 'ep', 'mod', 'pool', '[]', 'models')`,
		`INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at) VALUES ('a1', 0, '2026-01-01T00:00:00Z')`,
		`INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since)
			VALUES ('a1', 'default', 'e', 'm', '[]', '2026-01-01T00:00:00Z')`,
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
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = db.Close() }()
	c := db.Conn()

	var turnID, text string
	var retry, seq int
	if err := c.QueryRow(`SELECT turn_id, retry_count, journal_seq, text FROM attempts WHERE id = 'a1'`).Scan(&turnID, &retry, &seq, &text); err != nil {
		t.Fatal(err)
	}
	if turnID != "t1" || retry != 2 || seq != 7 || text != "hello" {
		t.Fatalf("migrated attempt = %q %d %d %q", turnID, retry, seq, text)
	}
	var run, ep, model, pool, group, step string
	if err := c.QueryRow(`SELECT probe_run_id, probe_endpoint, probe_model, probe_pool, probe_group, probe_step FROM attempts WHERE id = 'a2' AND turn_id IS NULL`).
		Scan(&run, &ep, &model, &pool, &group, &step); err != nil {
		t.Fatal(err)
	}
	if run != "r1" || ep != "ep" || model != "mod" || pool != "pool" || group != "[]" || step != "models" {
		t.Fatalf("probe attempt = %q %q %q %q %q %q", run, ep, model, pool, group, step)
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = 'a1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dispatches = %d, %v", n, err)
	}
	if err := c.QueryRow(`SELECT COUNT(*) FROM capacity_holds WHERE attempt_id = 'a1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("holds = %d, %v", n, err)
	}
	if err := c.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'attempts_journal_seq_unique'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index count = %d, %v", n, err)
	}
	if _, err := c.Exec(`UPDATE attempts SET raw_output = 'x' WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`UPDATE attempts SET raw_output = 'y' WHERE id = 'a1'`); err == nil {
		t.Fatal("raw_output rewrite succeeded; trigger lost")
	}
	if _, err := c.Exec(`UPDATE attempts SET status = 'tool_calls' WHERE id = 'a1'`); err != nil {
		t.Fatalf("tool_calls status rejected: %v", err)
	}
	if _, err := c.Exec(`UPDATE attempts SET status = 'bogus' WHERE id = 'a1'`); err == nil {
		t.Fatal("bogus status accepted")
	}
}
