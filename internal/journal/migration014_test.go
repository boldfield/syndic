package journal_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestMigration014UpgradeRetainsData seeds a version-13 database with
// milestone-D records (a run, conversation, turn, attempt, candidate, verdict,
// block, review record with frozen content, history document and events),
// upgrades it through journal.Open, and checks that every seeded row survives,
// the version is 14, and the new tables accept records against the old rows.
func TestMigration014UpgradeRetainsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
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
		`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{"k":1}', 'mh')`,
		`INSERT INTO conversations (id, workspace) VALUES ('c1', '/ws')`,
		`INSERT INTO turns (id, run_id, mode, conversation_id, context_hash, file_tainted) VALUES ('t1', 'r1', 'review', 'c1', 'ctx', 1)`,
		`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, status, journal_seq, text)
			VALUES ('a1', 't1', 'author', 'author', 1, '2026-01-01T00:00:00Z', 'completed', 'tool_calls', 7, 'hello')`,
		`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, journal_seq)
			VALUES ('a2', 't1', 'r1', 'review', 1, '2026-01-01T00:00:01Z', 'completed', 8)`,
		`INSERT INTO candidates (id, turn_id, content, content_hash, round) VALUES ('cand1', 't1', 'answer', 'ch', 0)`,
		`UPDATE turns SET candidate_id = 'cand1' WHERE id = 't1'`,
		`INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, validation_status) VALUES ('v1', 'a2', 'ch', 'block', 'valid')`,
		`INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id)
			VALUES ('b1', 'v1', 'ch', 'low', 'ev', 'rem', 'open', 'B1')`,
		`INSERT INTO review_records (verdict_id, turn_id, round, roster_index, participant, attempt_id, candidate_hash, kind, record, record_sha256, record_len)
			VALUES ('v1', 't1', 0, 0, 'r1', 'a2', 'ch', 'block', X'7B7D', 'rs', 2)`,
		`INSERT INTO review_content (ref, body) VALUES ('evidence:b1', X'6576')`,
		`INSERT INTO history_documents (hash, document) VALUES ('hd', X'646F63')`,
		`INSERT INTO events (id, turn_id, event_type, data) VALUES (1, 't1', 'turn_opened', '{}')`,
		`INSERT INTO events (id, event_type, data) VALUES (2, 'engine_started', '{"x":1}')`,
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

	var version int
	if err := c.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 20 {
		t.Fatalf("version = %d, %v; want 20", version, err)
	}

	var ws, ctx string
	var tainted bool
	var candidateID string
	if err := c.QueryRow(`SELECT cv.workspace, t.context_hash, t.file_tainted, t.candidate_id FROM turns t JOIN conversations cv ON cv.id = t.conversation_id WHERE t.id = 't1'`).
		Scan(&ws, &ctx, &tainted, &candidateID); err != nil {
		t.Fatal(err)
	}
	if ws != "/ws" || ctx != "ctx" || !tainted || candidateID != "cand1" {
		t.Fatalf("turn = %q %q %v %q", ws, ctx, tainted, candidateID)
	}
	var status, text string
	var seq int
	if err := c.QueryRow(`SELECT status, text, journal_seq FROM attempts WHERE id = 'a1'`).Scan(&status, &text, &seq); err != nil {
		t.Fatal(err)
	}
	if status != "tool_calls" || text != "hello" || seq != 7 {
		t.Fatalf("attempt = %q %q %d", status, text, seq)
	}
	var parsed, blockState, reviewerBlock string
	var verdictSeq sql.NullInt64
	if err := c.QueryRow(`SELECT v.parsed_verdict, v.journal_seq, b.state, b.reviewer_block_id FROM verdicts v JOIN blocks b ON b.verdict_id = v.id WHERE v.id = 'v1'`).
		Scan(&parsed, &verdictSeq, &blockState, &reviewerBlock); err != nil {
		t.Fatal(err)
	}
	if parsed != "block" || blockState != "open" || reviewerBlock != "B1" || verdictSeq.Valid {
		t.Fatalf("verdict = %q %v %q %q", parsed, verdictSeq, blockState, reviewerBlock)
	}
	var record, frozen, doc []byte
	if err := c.QueryRow(`SELECT record FROM review_records WHERE verdict_id = 'v1'`).Scan(&record); err != nil || string(record) != "{}" {
		t.Fatalf("review record = %q, %v", record, err)
	}
	if err := c.QueryRow(`SELECT body FROM review_content WHERE ref = 'evidence:b1'`).Scan(&frozen); err != nil || string(frozen) != "ev" {
		t.Fatalf("review content = %q, %v", frozen, err)
	}
	if err := c.QueryRow(`SELECT document FROM history_documents WHERE hash = 'hd'`).Scan(&doc); err != nil || string(doc) != "doc" {
		t.Fatalf("history document = %q, %v", doc, err)
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("events = %d, %v", n, err)
	}
	var cand string
	if err := c.QueryRow(`SELECT content FROM candidates WHERE id = 'cand1'`).Scan(&cand); err != nil || cand != "answer" {
		t.Fatalf("candidate = %q, %v", cand, err)
	}

	// The new tables reference the retained rows.
	store := journal.NewStore(db)
	err = store.Tx(func(tx *journal.Tx) error {
		call, err := tx.RecordToolCall("a1", 0, "call_0", "read_file", "read", []byte(`{"path":"a"}`))
		if err != nil {
			return err
		}
		if _, err := tx.RecordObservation("t1", "a1", "a", journal.ObservationFile, []byte("A")); err != nil {
			return err
		}
		if _, err := tx.StageWrite("t1", "a", []byte("B")); err != nil {
			return err
		}
		if _, err := tx.FreezeChangeSet("t1", "cand1", "snap", "changes"); err != nil {
			return err
		}
		if _, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("A"), ""); err != nil {
			return err
		}
		// The pre-upgrade verdict has no journal sequence, so it takes no
		// evidence; a verdict recorded after the upgrade does.
		if err := tx.RecordVerdictEvidence("v1", 1, nil, nil); !errors.Is(err, journal.ErrEvidenceScope) {
			return fmt.Errorf("evidence for pre-upgrade verdict: %v", err)
		}
		v, err := tx.RecordVerdict("a2", "ch", "approve", "", "valid")
		if err != nil {
			return err
		}
		return tx.RecordVerdictEvidence(v, 1, nil, nil)
	})
	if err != nil {
		t.Fatalf("records against upgraded rows: %v", err)
	}
	var newVerdictSeq sql.NullInt64
	if err := c.QueryRow(`SELECT journal_seq FROM verdicts WHERE id <> 'v1'`).Scan(&newVerdictSeq); err != nil || !newVerdictSeq.Valid {
		t.Fatalf("post-upgrade verdict journal_seq = %v, %v", newVerdictSeq, err)
	}
	for _, table := range []string{"tool_content", "tool_calls", "tool_results", "observations", "staging", "change_sets", "change_set_paths", "apply_ops", "verdict_evidence", "verdict_tool_results", "verdict_observations"} {
		if err := c.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s: count %d, %v", table, n, err)
		}
	}
	cs, err := store.ChangeSetForTurn("t1")
	if err != nil || cs.CandidateID != "cand1" || len(cs.Paths) != 1 || cs.State != journal.ChangeSetAppliable {
		t.Fatalf("change set = %+v, %v", cs, err)
	}
	// Events written before the upgrade keep their sequence; new ones follow.
	events, err := store.EventsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 7 || events[0].Kind != "turn_opened" || events[1].Kind != "engine_started" || events[2].Kind != "tool_call_accepted" {
		t.Fatalf("events = %+v", events)
	}
}
