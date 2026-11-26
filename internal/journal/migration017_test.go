package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestMigration017UpgradeRetainsToolCalls seeds a version-16 database with a
// tool call and its result, upgrades it through journal.Open, and checks that
// both are unchanged, that class exec is now admitted and an unknown class is
// not, and that the rebuilt table keeps its append-only triggers.
func TestMigration017UpgradeRetainsToolCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 17 {
		t.Fatalf("migration files = %v, %v", files, err)
	}
	sort.Strings(files)
	for i, f := range files[:16] {
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
	const sum = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" // SHA-256 of "{}"
	for _, stmt := range []string{
		`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'mh')`,
		`INSERT INTO conversations (id, workspace) VALUES ('c1', '/ws')`,
		`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'review', 'c1')`,
		`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, status)
			VALUES ('a1', 't1', 'author', 'author', 1, '2026-01-01T00:00:00Z', 'completed', 'tool_calls')`,
		`INSERT INTO tool_content (sha256, byte_len, body) VALUES ('` + sum + `', 2, X'7b7d')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc1', 'a1', 't1', 'author', 0, 'call_0', 'read_file', 'read', '` + sum + `', '2026-01-01T00:00:01Z')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc2', 'a1', 't1', 'author', 1, 'call_1', 'zzz', 'none', '` + sum + `', '2026-01-01T00:00:01Z')`,
		`INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, journal_seq, recorded_at)
			VALUES ('tr1', 'tc1', 'executed', '` + sum + `', 2, 9, '2026-01-01T00:00:02Z')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const insert = `INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
		VALUES ('tc0', 'a1', 't1', 'author', 5, 'call_5', 'bash', 'exec', '` + sum + `', 'now')`
	if _, err := conn.Exec(insert); err == nil {
		t.Fatal("version 16 admitted class exec")
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
	var version int
	if err := c.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("version = %d, %v; want 17", version, err)
	}
	rows, err := c.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign key violations after upgrade")
	}
	_ = rows.Close()

	for id, want := range map[string]string{"tc1": "read", "tc2": "none"} {
		var class, name, recordedAt, args string
		var idx int
		if err := c.QueryRow(`SELECT class, name, recorded_at, args_sha256, call_index FROM tool_calls WHERE id = ?`, id).
			Scan(&class, &name, &recordedAt, &args, &idx); err != nil {
			t.Fatal(err)
		}
		if class != want || recordedAt != "2026-01-01T00:00:01Z" || args != sum {
			t.Errorf("%s = %s %s %s %d", id, class, name, recordedAt, idx)
		}
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM tool_results WHERE id = 'tr1' AND tool_call_id = 'tc1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tool result lost: %d, %v", n, err)
	}
	if _, err := c.Exec(insert); err != nil {
		t.Fatalf("class exec refused after upgrade: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE tool_calls SET name = 'x' WHERE id = 'tc1'`,
		`DELETE FROM tool_calls WHERE id = 'tc1'`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'a1', 't1', 'author', 9, 'call_9', 'x', 'shell', '` + sum + `', 'now')`,
	} {
		if _, err := c.Exec(stmt); err == nil {
			t.Errorf("accepted: %s", stmt)
		}
	}
}
