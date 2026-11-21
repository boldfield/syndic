package journal_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
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

// TestAtomicRoundCandidateRollback forces a failure after SetLatestCandidate has
// succeeded (candidate row inserted, turns.candidate_id updated) and asserts the
// whole transaction rolls back.
func TestAtomicRoundCandidateRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id, max_rounds_in_effect, max_rounds_ceiling) VALUES ('t1', 'r1', 'solo', 'c1', 5, 5)`)

	cand0, err := store.SetLatestCandidate("t1", "content0", "hash0", 0, "")
	if err != nil {
		t.Fatal(err)
	}

	errInjected := errors.New("injected failure after SetLatestCandidate")
	var inserted string
	err = store.Tx(func(tx *journal.Tx) error {
		id, err := tx.SetLatestCandidate("t1", "content1", "hash1", 1, cand0)
		if err != nil {
			return err
		}
		inserted = id
		return errInjected
	})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Tx error = %v, want injected failure", err)
	}
	if inserted == "" {
		t.Fatal("SetLatestCandidate did not succeed before the injected failure")
	}

	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE id = ?`, inserted).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("inserted candidate survived rollback")
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = 't1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("candidate count = %d, want 1", n)
	}
	var latest string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = 't1'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if latest != cand0 {
		t.Errorf("candidate_id = %q, want unchanged %q", latest, cand0)
	}
	var round int
	if err := conn.QueryRow(`SELECT round FROM candidates WHERE id = ?`, latest).Scan(&round); err != nil {
		t.Fatal(err)
	}
	if round != 0 {
		t.Errorf("latest round = %d, want 0", round)
	}

	// The same revision commits when the transaction succeeds.
	cand1, err := store.SetLatestCandidate("t1", "content1", "hash1", 1, cand0)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = 't1'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if latest != cand1 {
		t.Errorf("candidate_id = %q, want %q after commit", latest, cand1)
	}
}

// TestSetLatestCandidateTurnUpdateFailureRollsBack makes the turns UPDATE (the
// second write) fail after the candidate INSERT succeeded.
func TestSetLatestCandidateTurnUpdateFailureRollsBack(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('c1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)
	cand0, err := store.SetLatestCandidate("t1", "content0", "hash0", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `CREATE TRIGGER fail_candidate_update BEFORE UPDATE OF candidate_id ON turns
		BEGIN SELECT RAISE(ABORT, 'injected turn update failure'); END`)

	if _, err := store.SetLatestCandidate("t1", "content1", "hash1", 1, cand0); err == nil {
		t.Fatal("expected turn update failure")
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM candidates WHERE turn_id = 't1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("candidate count = %d, want 1 (insert must roll back)", n)
	}
	var latest string
	if err := conn.QueryRow(`SELECT candidate_id FROM turns WHERE id = 't1'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if latest != cand0 {
		t.Errorf("candidate_id = %q, want %q", latest, cand0)
	}
}

// buildV6DB creates a database at schema version 6 from the deployed migrations
// 001-006 and returns its path.
func buildV6DB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := t.TempDir() + "/v6.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	for i, f := range []string{
		"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql",
		"004_engine.sql", "005_journal_seq.sql", "006_attempt_text.sql",
	} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, conn, string(b))
		mustExec(t, conn, `INSERT INTO schema_version (version) VALUES (?)`, i+1)
	}
	return path, conn
}

// dumpTable returns every row of table restricted to cols, rendered with the
// storage class of each value so byte-for-byte differences are visible.
func dumpTable(t *testing.T, conn *sql.DB, table string, cols []string) []string {
	t.Helper()
	rows, err := conn.Query(fmt.Sprintf(`SELECT %s FROM %s ORDER BY rowid`, strings.Join(cols, ", "), table))
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts = append(parts, fmt.Sprintf("%s=%T:%v", cols[i], v, v))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func columnNames(t *testing.T, conn *sql.DB, table string) []string {
	t.Helper()
	rows, err := conn.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	return cols
}

var v6Tables = []string{
	"runs", "turns", "messages", "attempts", "candidates", "verdicts", "blocks",
	"events", "attempt_dispatches", "capacity_holds", "journal_counter",
}

// TestMigration007LegacyTurnBytesPreserved seeds populated v6 rows in every
// table, upgrades, and asserts every pre-existing column (including each
// journal_seq) is identical afterwards and each turn got its own conversation.
func TestMigration007LegacyTurnBytesPreserved(t *testing.T) {
	path, conn := buildV6DB(t)

	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{"a":1}', 'mh', 'engine')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, whole_turn_deadline, terminal_state, submission_id, submission_hash, epoch, terminal_reason, phase_at_cancel, attempt_deadline_ms, turn_deadline_ms, preset, quick_profile, candidate_id, budget_json)
		VALUES ('t1', 'r1', 'review', '2026-01-02T03:04:05Z', 'approved', 'sub-1', 'subhash-1', 3, 'ok', 'author', 1000, 5000, 'standard', 'quick', 'c1', '{"max":7,"x":[1,2]}')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, submission_id, epoch) VALUES ('t2', 'r1', 'solo', 'sub-2', 1)`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t3', 'r1', 'solo')`)
	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c1', 't1', 'candidate bytes é', 'chash1')`)
	mustExec(t, conn, `INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('c2', 't2', '', 'chash2')`)
	mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, parent_ids, journal_seq)
		VALUES ('m2', 't1', 'author', 'a', '["b"]', 2, 'second', 'h2', 6, '["m1"]', 5)`)
	mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, journal_seq)
		VALUES ('m1', 't1', 'author', 'a', '["b"]', 1, 'first', 'h1', 5, 2)`)
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status, cancellation_state, quarantined, raw_output, scrub_count, observed_identity, native_finish, transport_status, gate_error, commentary, delimiter, max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion, retry_count, journal_seq, text)
		VALUES ('a1', 't1', 'p1', 'author', 3, '2026-01-02T03:04:06Z', '2026-01-02T03:05:00Z', 'completed', 'complete', 'none', 1, 'raw out', 2, 'ident', 'stop', '200', 'gerr', 'comm', 'DELIM', 100, 50, 40, 60, 2, 3, 'attempt text')`)
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, repair_of, journal_seq) VALUES ('a2', 't2', 'p2', 'review', 1, 'now', 'a1', 9)`)
	mustExec(t, conn, `INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at) VALUES ('a1', 0, 'd0'), ('a1', 1, 'd1')`)
	mustExec(t, conn, `INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since, released_at, released_how, released_reason)
		VALUES ('a1', 'pool', 'ep', 'm', '{}', 'hs', 'ra', 'operator', 'why')`)
	mustExec(t, conn, `INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, reason, block_id, severity, evidence, remedy, validation_status)
		VALUES ('v1', 'a1', 'chash1', 'block', 'r', 'b1', 'high', 'ev', 'rem', 'valid')`)
	mustExec(t, conn, `INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id)
		VALUES ('b1', 'v1', 'chash1', 'high', 'ev', 'rem', 'open', 'rb')`)
	mustExec(t, conn, `INSERT INTO events (turn_id, event_type, timestamp, data) VALUES ('t1', 'e1', '2026-01-02T03:04:05Z', '{"k":"v"}'), ('t2', 'e2', '2026-01-02T03:04:07Z', NULL), (NULL, 'e3', '2026-01-02T03:04:08Z', 'x')`)
	mustExec(t, conn, `UPDATE journal_counter SET next = 10`)

	before := map[string][]string{}
	cols := map[string][]string{}
	for _, tbl := range v6Tables {
		cols[tbl] = columnNames(t, conn, tbl)
		before[tbl] = dumpTable(t, conn, tbl, cols[tbl])
		if len(before[tbl]) == 0 {
			t.Fatalf("seed left %s empty", tbl)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	up := db.Conn()

	for _, tbl := range v6Tables {
		after := dumpTable(t, up, tbl, cols[tbl])
		if !reflect.DeepEqual(before[tbl], after) {
			t.Errorf("table %s changed across upgrade:\nbefore: %q\nafter:  %q", tbl, before[tbl], after)
		}
	}

	// One null-workspace conversation per legacy turn, mapped 1:1.
	var nConv int
	if err := up.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&nConv); err != nil {
		t.Fatal(err)
	}
	if nConv != 3 {
		t.Errorf("conversations = %d, want 3", nConv)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		var convID string
		var workspace sql.NullString
		var turnsOnConv int
		if err := up.QueryRow(`SELECT t.conversation_id, c.workspace, (SELECT COUNT(*) FROM turns WHERE conversation_id = c.id)
			FROM turns t JOIN conversations c ON c.id = t.conversation_id WHERE t.id = ?`, id).Scan(&convID, &workspace, &turnsOnConv); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if convID != "conv-"+id {
			t.Errorf("%s conversation_id = %q, want conv-%s", id, convID, id)
		}
		if workspace.Valid {
			t.Errorf("%s workspace = %q, want NULL", id, workspace.String)
		}
		if turnsOnConv != 1 {
			t.Errorf("conversation for %s holds %d turns, want 1", id, turnsOnConv)
		}
	}

	// Legacy candidates read as round 0 with no supersedes; context fields default.
	var round int
	var supersedes sql.NullString
	if err := up.QueryRow(`SELECT round, supersedes FROM candidates WHERE id = 'c1'`).Scan(&round, &supersedes); err != nil {
		t.Fatal(err)
	}
	if round != 0 || supersedes.Valid {
		t.Errorf("legacy candidate round/supersedes = %d/%v, want 0/NULL", round, supersedes)
	}

	// The migrated turn reads back through the store.
	store := journal.NewStore(db)
	turn, err := store.GetTurn("t1")
	if err != nil {
		t.Fatalf("GetTurn on migrated turn: %v", err)
	}
	if turn.ConversationID != "conv-t1" || turn.SubmissionID != "sub-1" || turn.TerminalState != "approved" {
		t.Errorf("GetTurn = %+v", turn)
	}

	// The journal counter is untouched by the upgrade.
	var next int
	if err := up.QueryRow(`SELECT next FROM journal_counter`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 10 {
		t.Errorf("journal_counter.next = %d, want 10", next)
	}
}

// schemaSnapshot returns a normalized, comparable view of the whole schema.
func schemaSnapshot(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	snap := map[string]string{}
	rows, err := conn.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, tbl, sqlText string
		if err := rows.Scan(&typ, &name, &tbl, &sqlText); err != nil {
			t.Fatal(err)
		}
		snap["master:"+typ+":"+name] = tbl + "|" + strings.NewReplacer("( ", "(", " )", ")").Replace(strings.Join(strings.Fields(sqlText), " "))
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	for _, tbl := range tables {
		for _, pragma := range []string{"table_info", "index_list", "foreign_key_list"} {
			prows, err := conn.Query(fmt.Sprintf(`PRAGMA %s(%s)`, pragma, tbl))
			if err != nil {
				t.Fatal(err)
			}
			pcols, _ := prows.Columns()
			var lines []string
			for prows.Next() {
				vals := make([]interface{}, len(pcols))
				ptrs := make([]interface{}, len(pcols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := prows.Scan(ptrs...); err != nil {
					t.Fatal(err)
				}
				var parts []string
				for i, v := range vals {
					if pragma == "index_list" && pcols[i] == "seq" {
						continue // ordering of creation, not definition
					}
					parts = append(parts, fmt.Sprintf("%s=%v", pcols[i], v))
				}
				lines = append(lines, strings.Join(parts, ","))
			}
			if err := prows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = prows.Close()
			sort.Strings(lines)
			snap["pragma:"+pragma+":"+tbl] = strings.Join(lines, "\n")
		}
	}
	return snap
}

// TestMigration007FreshAndUpgradeConverge compares the complete schema of a
// fresh database against a v6 database upgraded by Open, in both directions.
func TestMigration007FreshAndUpgradeConverge(t *testing.T) {
	fresh, cleanup := openTestDB(t)
	defer cleanup()

	path, conn := buildV6DB(t)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()

	a := schemaSnapshot(t, fresh.Conn())
	b := schemaSnapshot(t, upgraded.Conn())
	for k, v := range a {
		got, ok := b[k]
		if !ok {
			t.Errorf("upgraded database lacks %s", k)
		} else if got != v {
			t.Errorf("%s differs:\nfresh:    %s\nupgraded: %s", k, v, got)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("fresh database lacks %s", k)
		}
	}
	if len(a) < 20 {
		t.Errorf("snapshot suspiciously small: %d entries", len(a))
	}
}

// TestTurnConversationIntegrity covers NULL, orphan and delete of a referenced row.
func TestTurnConversationIntegrity(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'h')`)
	mustExec(t, conn, `INSERT INTO conversations (id) VALUES ('c1')`)

	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode) VALUES ('tn', 'r1', 'solo')`); err == nil {
		t.Error("turn with NULL conversation_id accepted")
	}
	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('to', 'r1', 'solo', 'nope')`); err == nil {
		t.Error("turn with orphan conversation_id accepted")
	}
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`)
	if _, err := conn.Exec(`UPDATE turns SET conversation_id = NULL WHERE id = 't1'`); err == nil {
		t.Error("UPDATE to NULL conversation_id accepted")
	}
	if _, err := conn.Exec(`UPDATE turns SET conversation_id = 'nope' WHERE id = 't1'`); err == nil {
		t.Error("UPDATE to orphan conversation_id accepted")
	}
	if _, err := conn.Exec(`DELETE FROM conversations WHERE id = 'c1'`); err == nil {
		t.Error("delete of referenced conversation accepted")
	}
}
