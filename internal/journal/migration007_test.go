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

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)

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

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)

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

func TestSetLatestCandidateValidation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)

	// Create first candidate (round 0)
	cand1ID, err := store.SetLatestCandidate("t1", "content1", "hash1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	// Test: round 0 with supersedes should fail
	_, err = store.SetLatestCandidate("t1", "content", "hash", 0, "some_id")
	if err == nil {
		t.Fatal("expected error for round 0 with supersedes, got nil")
	}

	// Test: round > 0 without supersedes should fail
	_, err = store.SetLatestCandidate("t1", "content", "hash", 1, "")
	if err == nil {
		t.Fatal("expected error for round 1 without supersedes, got nil")
	}

	// Test: round > 0 with nonexistent supersedes should fail
	_, err = store.SetLatestCandidate("t1", "content", "hash", 1, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent supersedes, got nil")
	}

	// Test: round > 0 with supersedes from different turn should fail
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t2', 'r1', 'solo', 'c1')`)
	cand2ID, _ := store.SetLatestCandidate("t2", "content2", "hash2", 0, "")
	_, err = store.SetLatestCandidate("t1", "content", "hash", 1, cand2ID)
	if err == nil {
		t.Fatal("expected error for supersedes from different turn, got nil")
	}

	// Test: round > 0 with correct supersedes should succeed
	cand3ID, err := store.SetLatestCandidate("t1", "content3", "hash3", 1, cand1ID)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1 with correct supersedes: %v", err)
	}
	if cand3ID == "" {
		t.Fatal("expected candidate ID, got empty")
	}
}

func TestAtomicRoundCandidateRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id, max_rounds_in_effect, max_rounds_ceiling) VALUES ('t1', 'r1', 'solo', 'c1', 5, 5)`)

	cand1ID, _ := store.SetLatestCandidate("t1", "content1", "hash1", 0, "")

	var countBefore int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = ?`, "t1").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}

	var latestIDBefore string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestIDBefore); err != nil {
		t.Fatal(err)
	}

	var roundBefore int
	if err := conn.QueryRow(`SELECT round FROM candidates WHERE id = ?`, cand1ID).Scan(&roundBefore); err != nil {
		t.Fatal(err)
	}

	// Test that SetLatestCandidate fails during validation (before insert) and rolls back
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

	var roundAfter int
	if err := conn.QueryRow(`SELECT round FROM candidates WHERE id = ?`, cand1ID).Scan(&roundAfter); err != nil {
		t.Fatal(err)
	}

	if countAfter > countBefore {
		t.Errorf("candidate count changed: before=%d, after=%d (transaction should have rolled back)", countBefore, countAfter)
	}

	if latestIDAfter != latestIDBefore || latestIDAfter != cand1ID {
		t.Errorf("candidate_id changed: before=%q, after=%q, cand1=%q (should be unchanged)", latestIDBefore, latestIDAfter, cand1ID)
	}

	if roundAfter != roundBefore {
		t.Errorf("candidate round changed: before=%d, after=%d (should be unchanged)", roundBefore, roundAfter)
	}
}

// TestAtomicRoundCandidateRollbackPostInsert tests true transactional rollback when
// failure happens after candidate is inserted but before turns.candidate_id is updated.
func TestAtomicRoundCandidateRollbackPostInsert(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id, max_rounds_in_effect) VALUES ('t1', 'r1', 'solo', 'c1', 0)`)

	cand1ID, _ := store.SetLatestCandidate("t1", "content1", "hash1", 0, "")

	var countBefore int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = ?`, "t1").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}

	var latestIDBefore string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestIDBefore); err != nil {
		t.Fatal(err)
	}

	// Test: attempting round 1 when max_rounds_in_effect is 0 should fail during candidate creation
	// (zero revisions allowed, so no round 1 possible)
	_, err := store.SetLatestCandidate("t1", "content2", "hash2", 1, cand1ID)

	if err == nil {
		t.Fatal("expected error for round exceeding max_rounds_in_effect, got nil")
	}

	var countAfter int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = ?`, "t1").Scan(&countAfter); err != nil {
		t.Fatal(err)
	}

	var latestIDAfter string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, "t1").Scan(&latestIDAfter); err != nil {
		t.Fatal(err)
	}

	if countAfter != countBefore {
		t.Errorf("candidate count changed: before=%d, after=%d (should be unchanged)", countBefore, countAfter)
	}

	if latestIDAfter != latestIDBefore {
		t.Errorf("candidate_id changed: before=%q, after=%q (should be unchanged)", latestIDBefore, latestIDAfter)
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

	// Seed v6 turns with full data
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, submission_id, submission_hash, epoch, whole_turn_deadline, attempt_deadline_ms, turn_deadline_ms, budget_json, terminal_state, terminal_reason)
		VALUES ('t1', 'r1', 'solo', 'sub1', 'subhash1', 1, '2026-11-21T20:00:00Z', 300000, 600000, '{}', 'approved', NULL)`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, submission_id, submission_hash, epoch, whole_turn_deadline, attempt_deadline_ms, turn_deadline_ms, budget_json, terminal_state, terminal_reason)
		VALUES ('t2', 'r1', 'review', 'sub2', 'subhash2', 2, '2026-11-21T21:00:00Z', 300000, 600000, '{"tokens": 10000}', 'incomplete', 'budget:turn')`)

	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c1', 't1', 'old content 1', 'oldhash1')`)
	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c2', 't2', 'old content 2', 'oldhash2')`)

	mustExec(t, conn, `UPDATE turns SET candidate_id = 'c1' WHERE id = 't1'`)
	mustExec(t, conn, `UPDATE turns SET candidate_id = 'c2' WHERE id = 't2'`)

	mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, journal_seq)
		VALUES ('m1', 't1', 'author', 'user', '[]', 1, 'message content', 'msghash', 15, 1)`)
	mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, journal_seq)
		VALUES ('m2', 't2', 'reviewers', 'assistant', '["r1"]', 2, 'review message', 'revhash', 14, 2)`)

	mustExec(t, conn, `INSERT INTO events (turn_id, event_type, data) VALUES ('t1', 'test_event', '{"event": "test"}')`)
	mustExec(t, conn, `INSERT INTO events (turn_id, event_type, data) VALUES ('t2', 'review_event', '{"event": "review"}')`)

	// Capture data before migration
	var t1Before struct {
		ID, RunID, Mode, SubmissionID, SubmissionHash, BudgetJSON, TerminalState, TerminalReason sql.NullString
		Epoch, AttemptDeadlineMS, TurnDeadlineMS                                                 sql.NullInt64
	}
	var t2Before struct {
		ID, RunID, Mode, SubmissionID, SubmissionHash, BudgetJSON, TerminalState, TerminalReason sql.NullString
		Epoch, AttemptDeadlineMS, TurnDeadlineMS                                                 sql.NullInt64
	}

	if err := conn.QueryRow(`SELECT id, run_id, mode, submission_id, submission_hash, epoch, attempt_deadline_ms, turn_deadline_ms, budget_json, terminal_state, terminal_reason FROM turns WHERE id = 't1'`).
		Scan(&t1Before.ID, &t1Before.RunID, &t1Before.Mode, &t1Before.SubmissionID, &t1Before.SubmissionHash, &t1Before.Epoch, &t1Before.AttemptDeadlineMS, &t1Before.TurnDeadlineMS, &t1Before.BudgetJSON, &t1Before.TerminalState, &t1Before.TerminalReason); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT id, run_id, mode, submission_id, submission_hash, epoch, attempt_deadline_ms, turn_deadline_ms, budget_json, terminal_state, terminal_reason FROM turns WHERE id = 't2'`).
		Scan(&t2Before.ID, &t2Before.RunID, &t2Before.Mode, &t2Before.SubmissionID, &t2Before.SubmissionHash, &t2Before.Epoch, &t2Before.AttemptDeadlineMS, &t2Before.TurnDeadlineMS, &t2Before.BudgetJSON, &t2Before.TerminalState, &t2Before.TerminalReason); err != nil {
		t.Fatal(err)
	}

	var journalSeqs []int64
	rows, err := conn.Query(`SELECT journal_seq FROM messages WHERE turn_id IN ('t1', 't2') ORDER BY journal_seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		journalSeqs = append(journalSeqs, seq)
	}
	if err := rows.Close(); err != nil {
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

	// Verify candidate content unchanged
	var candContent1 string
	if err := db.Conn().QueryRow(`SELECT content FROM candidates WHERE id = ?`, "c1").Scan(&candContent1); err != nil {
		t.Fatal(err)
	}
	if candContent1 != "old content 1" {
		t.Errorf("candidate content changed: got %q, want %q", candContent1, "old content 1")
	}

	// Verify candidate content for t2
	var candContent2 string
	if err := db.Conn().QueryRow(`SELECT content FROM candidates WHERE id = ?`, "c2").Scan(&candContent2); err != nil {
		t.Fatal(err)
	}
	if candContent2 != "old content 2" {
		t.Errorf("candidate content changed: got %q, want %q", candContent2, "old content 2")
	}

	// Verify journal sequences unchanged
	var journalSeqsAfter []int64
	rows, err = db.Conn().Query(`SELECT journal_seq FROM messages WHERE turn_id IN ('t1', 't2') ORDER BY journal_seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		journalSeqsAfter = append(journalSeqsAfter, seq)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(journalSeqs) != len(journalSeqsAfter) {
		t.Errorf("journal_seq count changed: before=%d, after=%d", len(journalSeqs), len(journalSeqsAfter))
	}
	for i, seq := range journalSeqs {
		if i >= len(journalSeqsAfter) || seq != journalSeqsAfter[i] {
			t.Errorf("journal_seq[%d] changed: before=%d, after=%v", i, seq, journalSeqsAfter)
			break
		}
	}

	// Verify turns got separate conversations
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

	// Verify conversation mappings and workspaces
	if !checkConversationMapping(t, db.Conn(), "t1", t1ConvID, "conv-t1", nil) {
		t.Error("t1 conversation mapping incorrect")
	}
	if !checkConversationMapping(t, db.Conn(), "t2", t2ConvID, "conv-t2", nil) {
		t.Error("t2 conversation mapping incorrect")
	}

	// Verify byte-identical turn fields before and after (excluding budget_json which may differ in representation)
	t1After := struct {
		ID, RunID, Mode, SubmissionID, SubmissionHash, TerminalState, TerminalReason sql.NullString
		Epoch, AttemptDeadlineMS, TurnDeadlineMS                                     sql.NullInt64
	}{}
	if err := db.Conn().QueryRow(`SELECT id, run_id, mode, submission_id, submission_hash, epoch, attempt_deadline_ms, turn_deadline_ms, terminal_state, terminal_reason FROM turns WHERE id = 't1'`).
		Scan(&t1After.ID, &t1After.RunID, &t1After.Mode, &t1After.SubmissionID, &t1After.SubmissionHash, &t1After.Epoch, &t1After.AttemptDeadlineMS, &t1After.TurnDeadlineMS, &t1After.TerminalState, &t1After.TerminalReason); err != nil {
		t.Fatal(err)
	}

	// Compare all fields
	if t1Before.ID.String != t1After.ID.String {
		t.Errorf("t1.id changed: before=%q, after=%q", t1Before.ID.String, t1After.ID.String)
	}
	if t1Before.RunID.String != t1After.RunID.String {
		t.Errorf("t1.run_id changed: before=%q, after=%q", t1Before.RunID.String, t1After.RunID.String)
	}
	if t1Before.Mode.String != t1After.Mode.String {
		t.Errorf("t1.mode changed: before=%q, after=%q", t1Before.Mode.String, t1After.Mode.String)
	}
	if t1Before.SubmissionID.String != t1After.SubmissionID.String || t1Before.SubmissionID.Valid != t1After.SubmissionID.Valid {
		t.Errorf("t1.submission_id changed: before=%q (valid=%v), after=%q (valid=%v)", t1Before.SubmissionID.String, t1Before.SubmissionID.Valid, t1After.SubmissionID.String, t1After.SubmissionID.Valid)
	}
	if t1Before.SubmissionHash.String != t1After.SubmissionHash.String || t1Before.SubmissionHash.Valid != t1After.SubmissionHash.Valid {
		t.Errorf("t1.submission_hash changed: before=%q (valid=%v), after=%q (valid=%v)", t1Before.SubmissionHash.String, t1Before.SubmissionHash.Valid, t1After.SubmissionHash.String, t1After.SubmissionHash.Valid)
	}
	if t1Before.Epoch.Int64 != t1After.Epoch.Int64 || t1Before.Epoch.Valid != t1After.Epoch.Valid {
		t.Errorf("t1.epoch changed: before=%d (valid=%v), after=%d (valid=%v)", t1Before.Epoch.Int64, t1Before.Epoch.Valid, t1After.Epoch.Int64, t1After.Epoch.Valid)
	}
	if t1Before.AttemptDeadlineMS.Int64 != t1After.AttemptDeadlineMS.Int64 || t1Before.AttemptDeadlineMS.Valid != t1After.AttemptDeadlineMS.Valid {
		t.Errorf("t1.attempt_deadline_ms changed: before=%d (valid=%v), after=%d (valid=%v)", t1Before.AttemptDeadlineMS.Int64, t1Before.AttemptDeadlineMS.Valid, t1After.AttemptDeadlineMS.Int64, t1After.AttemptDeadlineMS.Valid)
	}
	if t1Before.TurnDeadlineMS.Int64 != t1After.TurnDeadlineMS.Int64 || t1Before.TurnDeadlineMS.Valid != t1After.TurnDeadlineMS.Valid {
		t.Errorf("t1.turn_deadline_ms changed: before=%d (valid=%v), after=%d (valid=%v)", t1Before.TurnDeadlineMS.Int64, t1Before.TurnDeadlineMS.Valid, t1After.TurnDeadlineMS.Int64, t1After.TurnDeadlineMS.Valid)
	}
	if t1Before.TerminalState.String != t1After.TerminalState.String || t1Before.TerminalState.Valid != t1After.TerminalState.Valid {
		t.Errorf("t1.terminal_state changed: before=%q (valid=%v), after=%q (valid=%v)", t1Before.TerminalState.String, t1Before.TerminalState.Valid, t1After.TerminalState.String, t1After.TerminalState.Valid)
	}
	if t1Before.TerminalReason.String != t1After.TerminalReason.String || t1Before.TerminalReason.Valid != t1After.TerminalReason.Valid {
		t.Errorf("t1.terminal_reason changed: before=%q (valid=%v), after=%q (valid=%v)", t1Before.TerminalReason.String, t1Before.TerminalReason.Valid, t1After.TerminalReason.String, t1After.TerminalReason.Valid)
	}

	// Verify GetTurn works on migrated turn
	turn1, err := journal.NewStore(db).GetTurn("t1")
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}
	if turn1.ConversationID != t1ConvID {
		t.Errorf("GetTurn conversation_id mismatch: got %q, want %q", turn1.ConversationID, t1ConvID)
	}
	if turn1.SubmissionID != "sub1" {
		t.Errorf("GetTurn submission_id changed: got %q, want %q", turn1.SubmissionID, "sub1")
	}
	if turn1.TerminalState != "approved" {
		t.Errorf("GetTurn terminal_state changed: got %q, want %q", turn1.TerminalState, "approved")
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

	// Compare full schema for conversations table
	compareTableSchema(t, dbFresh.Conn(), dbUpgrade.Conn(), "conversations")
	compareTableSchema(t, dbFresh.Conn(), dbUpgrade.Conn(), "turns")
	compareTableSchema(t, dbFresh.Conn(), dbUpgrade.Conn(), "candidates")

	// Also check in reverse direction
	compareTableSchema(t, dbUpgrade.Conn(), dbFresh.Conn(), "conversations")
	compareTableSchema(t, dbUpgrade.Conn(), dbFresh.Conn(), "turns")
	compareTableSchema(t, dbUpgrade.Conn(), dbFresh.Conn(), "candidates")
}

func checkConversationMapping(t *testing.T, conn *sql.DB, turnID, actualConvID, expectedConvPrefix string, expectedWorkspace *string) bool {
	t.Helper()

	// Verify conversation ID format
	if actualConvID != expectedConvPrefix {
		t.Errorf("Turn %s: conversation_id = %q, want %q", turnID, actualConvID, expectedConvPrefix)
		return false
	}

	// Verify workspace
	var workspace sql.NullString
	if err := conn.QueryRow(`SELECT workspace FROM conversations WHERE id = ?`, actualConvID).Scan(&workspace); err != nil {
		t.Fatalf("Query conversation workspace: %v", err)
	}

	if expectedWorkspace == nil {
		if workspace.Valid {
			t.Errorf("Turn %s: conversation workspace = %q, want NULL", turnID, workspace.String)
			return false
		}
	} else {
		if !workspace.Valid || workspace.String != *expectedWorkspace {
			t.Errorf("Turn %s: conversation workspace = %q, want %q", turnID, workspace.String, *expectedWorkspace)
			return false
		}
	}

	return true
}

func compareTableSchema(t *testing.T, expected *sql.DB, actual *sql.DB, table string) {
	t.Helper()

	// Compare column info with full PRAGMA table_info details
	expectedCols := getTableInfoFull(t, expected, table)
	actualCols := getTableInfoFull(t, actual, table)

	if len(expectedCols) != len(actualCols) {
		t.Errorf("%s: column count differs: expected %d, got %d", table, len(expectedCols), len(actualCols))
	}

	for i, col := range expectedCols {
		if i >= len(actualCols) {
			t.Errorf("%s: missing column at index %d: %v", table, i, col)
			continue
		}
		if col != actualCols[i] {
			t.Errorf("%s: column %d differs: expected %q, got %q", table, i, col, actualCols[i])
		}
	}

	// Compare indexes for the table
	expectedIndexes := getIndexes(t, expected, table)
	actualIndexes := getIndexes(t, actual, table)

	if len(expectedIndexes) != len(actualIndexes) {
		t.Errorf("%s: index count differs: expected %d, got %d", table, len(expectedIndexes), len(actualIndexes))
	}

	for i, idx := range expectedIndexes {
		if i >= len(actualIndexes) {
			t.Errorf("%s: missing index at position %d: %v", table, i, idx)
			continue
		}
		if idx != actualIndexes[i] {
			t.Errorf("%s: index %d differs: expected %q, got %q", table, i, idx, actualIndexes[i])
		}
	}

	// Compare triggers for the table
	expectedTriggers := getTriggers(t, expected, table)
	actualTriggers := getTriggers(t, actual, table)

	if len(expectedTriggers) != len(actualTriggers) {
		t.Errorf("%s: trigger count differs: expected %d, got %d", table, len(expectedTriggers), len(actualTriggers))
	}

	for i, trg := range expectedTriggers {
		if i >= len(actualTriggers) {
			t.Errorf("%s: missing trigger at position %d: %v", table, i, trg)
			continue
		}
		if trg != actualTriggers[i] {
			t.Errorf("%s: trigger %d differs: expected %q, got %q", table, i, trg, actualTriggers[i])
		}
	}
}

func getTableInfoFull(t *testing.T, conn *sql.DB, table string) []string {
	t.Helper()
	query := `PRAGMA table_info(` + table + `)`
	rows, err := conn.Query(query)
	if err != nil {
		t.Fatalf("pragma table_info %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var info []string
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		// Include notnull constraint in the comparison
		notNullStr := ""
		if notnull != 0 {
			notNullStr = " NOT NULL"
		}
		info = append(info, name+":"+typ+notNullStr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return info
}

func getIndexes(t *testing.T, conn *sql.DB, table string) []string {
	t.Helper()
	query := `SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? ORDER BY name`
	rows, err := conn.Query(query, table)
	if err != nil {
		t.Fatalf("get indexes for %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var indexes []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		// Skip auto-generated SQLite indexes
		if !isAutoIndex(name) {
			indexes = append(indexes, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return indexes
}

func getTriggers(t *testing.T, conn *sql.DB, table string) []string {
	t.Helper()
	query := `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name=? ORDER BY name`
	rows, err := conn.Query(query, table)
	if err != nil {
		t.Fatalf("get triggers for %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var triggers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return triggers
}

func isAutoIndex(name string) bool {
	// SQLite generates automat ic indexes with this pattern
	return len(name) > 7 && name[:7] == "sqlite_"
}
