package journal_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestMigration007FreshVersion(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	var v int
	if err := db.Conn().QueryRow("SELECT MAX(version) FROM schema_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 7 {
		t.Errorf("version = %d, want 7", v)
	}
}

func TestMigration007UpgradeFromV6(t *testing.T) {
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
	for _, f := range []string{
		"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql",
		"004_engine.sql", "005_journal_seq.sql", "006_attempt_text.sql",
	} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, conn, string(b))
	}
	mustExec(t, conn, `INSERT INTO schema_version (version) VALUES (1), (2), (3), (4), (5), (6)`)

	// Seed some data
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{}', 'h', 'engine')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t2', 'r1', 'solo')`)

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

	// Verify conversations were created
	var n int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("conversations count = %d, want 2", n)
	}

	// Verify turns have conversation_id set
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM turns WHERE conversation_id IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("turns with conversation_id = %d, want 2", n)
	}

	// Verify conversations have null workspace
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM conversations WHERE workspace IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("conversations with null workspace = %d, want 2", n)
	}
}

func TestMigration007ConversationsTable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	cols := tableColumns(t, conn, "conversations")
	for _, col := range []string{"id", "created_at", "workspace"} {
		if !cols[col] {
			t.Errorf("conversations missing column %s", col)
		}
	}
}

func TestMigration007TurnsColumns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	cols := tableColumns(t, conn, "turns")
	for _, col := range []string{"conversation_id", "context_hash", "file_tainted", "max_rounds_in_effect", "max_rounds_ceiling"} {
		if !cols[col] {
			t.Errorf("turns missing column %s", col)
		}
	}
}

func TestMigration007CandidatesColumns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	cols := tableColumns(t, conn, "candidates")
	for _, col := range []string{"round", "supersedes"} {
		if !cols[col] {
			t.Errorf("candidates missing column %s", col)
		}
	}
}

func TestCreateConversation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	workspace := "/home/user"
	convID, err := store.CreateConversation(&workspace)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	var w sql.NullString
	if err := db.Conn().QueryRow(`SELECT workspace FROM conversations WHERE id = ?`, convID).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if !w.Valid || w.String != workspace {
		t.Errorf("workspace = %q, want %q", w.String, workspace)
	}
}

func TestCreateConversationNullWorkspace(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	convID, err := store.CreateConversation(nil)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	var w sql.NullString
	if err := db.Conn().QueryRow(`SELECT workspace FROM conversations WHERE id = ?`, convID).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if w.Valid {
		t.Errorf("workspace should be NULL, got %q", w.String)
	}
}

func TestSetTurnContextHash(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	hash := "abc123def456"

	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)
	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)

	if err := store.SetTurnContextHash("t1", hash); err != nil {
		t.Fatal(err)
	}

	var ch sql.NullString
	if err := conn.QueryRow(`SELECT context_hash FROM turns WHERE id = ?`, "t1").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	if !ch.Valid || ch.String != hash {
		t.Errorf("context_hash = %q, want %q", ch.String, hash)
	}
}

func TestSetTurnFileTainted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)
	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)

	if err := store.SetTurnFileTainted("t1", true); err != nil {
		t.Fatal(err)
	}

	var tainted bool
	if err := conn.QueryRow(`SELECT file_tainted FROM turns WHERE id = ?`, "t1").Scan(&tainted); err != nil {
		t.Fatal(err)
	}
	if !tainted {
		t.Errorf("file_tainted = %v, want true", tainted)
	}
}
