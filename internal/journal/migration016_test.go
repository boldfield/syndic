package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestMigration016UpgradeRetainsToolCalls seeds a version-15 database with a
// tool call and its result, upgrades it through journal.Open, and checks that
// both survive, that class none is now admitted and the earlier classes still
// are, and that the rebuilt table keeps its uniqueness constraints, foreign
// keys and append-only triggers.
func TestMigration016UpgradeRetainsToolCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 16 {
		t.Fatalf("migration files = %v, %v", files, err)
	}
	sort.Strings(files)
	for i, f := range files[:15] {
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
		`INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, journal_seq, recorded_at)
			VALUES ('tr1', 'tc1', 'executed', '` + sum + `', 2, 9, '2026-01-01T00:00:02Z')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Version 15 refuses class none, which is what the migration changes.
	if _, err := conn.Exec(`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
		VALUES ('tc0', 'a1', 't1', 'author', 5, 'call_5', 'x', 'none', '` + sum + `', 'now')`); err == nil {
		t.Fatal("version 15 admitted class none")
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
	if err := c.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 19 {
		t.Fatalf("version = %d, %v; want 19", version, err)
	}
	var fk int
	if err := c.QueryRow(`PRAGMA foreign_key_check`).Scan(&fk); err == nil {
		t.Fatal("foreign key violations after upgrade")
	}

	store := journal.NewStore(db)
	calls, err := store.ToolCallsByTurn("t1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].ID != "tc1" || calls[0].Class != "read" || calls[0].Status != journal.ToolCallExecuted ||
		calls[0].Result == nil || calls[0].Result.ID != "tr1" {
		t.Fatalf("calls = %+v", calls)
	}
	err = store.Tx(func(tx *journal.Tx) error {
		for i, c := range []struct{ name, class string }{{"run_shell", journal.ToolClassNone}, {"", journal.ToolClassNone}, {"web_fetch", "web"}, {"write_file", "write"}} {
			if _, err := tx.RecordToolCall("a1", i+1, "call_"+string(rune('1'+i)), c.name, c.class, []byte(`{}`)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("record after upgrade: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE tool_calls SET name = 'x' WHERE id = 'tc1'`,
		`DELETE FROM tool_calls WHERE id = 'tc1'`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'a1', 't1', 'author', 9, 'call_9', 'x', 'shell', '` + sum + `', 'now')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'a1', 't1', 'author', 0, 'call_9', 'x', 'read', '` + sum + `', 'now')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'a1', 't1', 'author', 9, 'call_0', 'x', 'read', '` + sum + `', 'now')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'nope', 't1', 'author', 9, 'call_9', 'x', 'read', '` + sum + `', 'now')`,
		`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
			VALUES ('tc9', 'a1', 't1', 'author', 9, 'call_9', 'x', 'read', 'nope', 'now')`,
	} {
		if _, err := c.Exec(stmt); err == nil {
			t.Errorf("accepted: %s", stmt)
		}
	}
}
