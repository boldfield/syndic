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

func TestSetLatestCandidateWithRound(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)

	candID1, err := store.SetLatestCandidate("t1", "content1", "hash1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	var round int
	var supersedes sql.NullString
	var latestID string
	if err := conn.QueryRow(`SELECT round, supersedes FROM candidates WHERE id = ?`, candID1).Scan(&round, &supersedes); err != nil {
		t.Fatal(err)
	}
	if round != 0 {
		t.Errorf("round = %d, want 0", round)
	}
	if supersedes.Valid {
		t.Errorf("supersedes should be NULL for round 0, got %q", supersedes.String)
	}

	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestID); err != nil {
		t.Fatal(err)
	}
	if latestID != candID1 {
		t.Errorf("candidate_id = %q, want %q", latestID, candID1)
	}

	candID2, err := store.SetLatestCandidate("t1", "content2", "hash2", 1, candID1)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1: %v", err)
	}

	if err := conn.QueryRow(`SELECT round, supersedes FROM candidates WHERE id = ?`, candID2).Scan(&round, &supersedes); err != nil {
		t.Fatal(err)
	}
	if round != 1 {
		t.Errorf("round = %d, want 1", round)
	}
	if !supersedes.Valid || supersedes.String != candID1 {
		t.Errorf("supersedes = %q, want %q", supersedes.String, candID1)
	}

	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestID); err != nil {
		t.Fatal(err)
	}
	if latestID != candID2 {
		t.Errorf("candidate_id after update = %q, want %q", latestID, candID2)
	}
}

func TestAtomicRoundCandidateRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)

	cand1ID, _ := store.SetLatestCandidate("t1", "content1", "hash1", 0, "")

	var countBefore int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = ?`, "t1").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}

	var latestIDBefore string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestIDBefore); err != nil {
		t.Fatal(err)
	}

	_, err := store.SetLatestCandidate("t1", "content2", "hash2", 1, "nonexistent_candidate")

	if err == nil {
		t.Fatal("expected error for nonexistent supersedes, got nil")
	}

	var countAfter int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = ?`, "t1").Scan(&countAfter); err != nil {
		t.Fatal(err)
	}

	var latestIDAfter string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestIDAfter); err != nil {
		t.Fatal(err)
	}

	if countAfter > countBefore {
		t.Errorf("candidate count changed: before=%d, after=%d (transaction should have rolled back)", countBefore, countAfter)
	}

	if latestIDAfter != latestIDBefore || latestIDAfter != cand1ID {
		t.Errorf("candidate_id changed: before=%q, after=%q, cand1=%q (should be unchanged)", latestIDBefore, latestIDAfter, cand1ID)
	}
}

func TestMigration007LegacyTurnBytesPreserved(t *testing.T) {
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

	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{}', 'h', 'engine')`)

	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t2', 'r1', 'review')`)

	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c1', 't1', 'old content 1', 'oldhash1')`)
	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c2', 't2', 'old content 2', 'oldhash2')`)

	mustExec(t, conn, `UPDATE turns SET candidate_id = 'c1' WHERE id = 't1'`)
	mustExec(t, conn, `UPDATE turns SET candidate_id = 'c2' WHERE id = 't2'`)

	mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, journal_seq)
		VALUES ('m1', 't1', 'author', 'user', '[]', 1, 'message content', 'msghash', 15, 1)`)

	var journalSeqBefore int64
	if err := conn.QueryRow(`SELECT MAX(journal_seq) FROM messages WHERE turn_id = 't1'`).Scan(&journalSeqBefore); err != nil {
		t.Fatal(err)
	}

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

	var candContent1 string
	if err := db.Conn().QueryRow(`SELECT content FROM candidates WHERE id = ?`, "c1").Scan(&candContent1); err != nil {
		t.Fatal(err)
	}
	if candContent1 != "old content 1" {
		t.Errorf("candidate content changed: got %q, want %q", candContent1, "old content 1")
	}

	var journalSeqAfter int64
	if err := db.Conn().QueryRow(`SELECT journal_seq FROM messages WHERE id = 'm1'`).Scan(&journalSeqAfter); err != nil {
		t.Fatal(err)
	}
	if journalSeqAfter != journalSeqBefore {
		t.Errorf("journal_seq changed: got %d, want %d", journalSeqAfter, journalSeqBefore)
	}

	var t1ConvID, t2ConvID string
	if err := db.Conn().QueryRow(`SELECT conversation_id FROM turns WHERE id = ?`, "t1").Scan(&t1ConvID); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT conversation_id FROM turns WHERE id = ?`, "t2").Scan(&t2ConvID); err != nil {
		t.Fatal(err)
	}
	if t1ConvID == t2ConvID {
		t.Errorf("legacy turns should have different conversations, both got %q", t1ConvID)
	}
}

func TestMigration007FreshAndUpgradeConverge(t *testing.T) {
	pathFresh := t.TempDir() + "/fresh.db"
	pathUpgrade := t.TempDir() + "/upgrade.db"

	dbFresh, err := journal.Open(pathFresh)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer func() {
		if err := dbFresh.Close(); err != nil {
			t.Logf("close fresh: %v", err)
		}
	}()

	conn, err := sql.Open("sqlite", pathUpgrade)
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
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{}', 'h', 'engine')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`)

	dbUpgrade, err := journal.Open(pathUpgrade)
	if err != nil {
		t.Fatalf("open upgrade: %v", err)
	}
	defer func() {
		if err := dbUpgrade.Close(); err != nil {
			t.Logf("close upgrade: %v", err)
		}
	}()

	freshCols := tableColumns(t, dbFresh.Conn(), "conversations")
	upgradeCols := tableColumns(t, dbUpgrade.Conn(), "conversations")
	for col := range freshCols {
		if !upgradeCols[col] {
			t.Errorf("upgrade missing column %s in conversations table", col)
		}
	}

	freshTurnCols := tableColumns(t, dbFresh.Conn(), "turns")
	upgradeTurnCols := tableColumns(t, dbUpgrade.Conn(), "turns")
	for col := range freshTurnCols {
		if !upgradeTurnCols[col] {
			t.Errorf("upgrade missing column %s in turns table", col)
		}
	}

	freshCandCols := tableColumns(t, dbFresh.Conn(), "candidates")
	upgradeCandCols := tableColumns(t, dbUpgrade.Conn(), "candidates")
	for col := range freshCandCols {
		if !upgradeCandCols[col] {
			t.Errorf("upgrade missing column %s in candidates table", col)
		}
	}
}
