package journal_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestDispositionNamesItsRoundAndCandidate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)

	var second string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if second, err = tx.SetLatestCandidate(f.turn, "answer 2", "h1", 1, f.candidate); err != nil {
			return err
		}
		in := journal.DeliberationItemInput{TurnID: f.turn, Participant: "author", RosterPosition: 0, AttemptID: f.author, Content: []byte("disposition one")}
		if _, err := tx.RecordDisposition(in, 1, f.candidate); err != nil {
			return err
		}
		in.Content = []byte("disposition two")
		_, err = tx.RecordDisposition(in, 2, second)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	items, next, err := store.DeliberationItems(f.turn, "", 10)
	if err != nil || next != "" || len(items) != 2 {
		t.Fatalf("items = %+v, next %q, %v", items, next, err)
	}
	want := []struct {
		round     int
		candidate string
		body      string
	}{{1, f.candidate, "disposition one"}, {2, second, "disposition two"}}
	for i, w := range want {
		it := items[i]
		if it.Kind != journal.DeliberationDisposition || it.Round != w.round || it.CandidateID != w.candidate || it.Label != "" ||
			it.ContentSHA256 != shaBytes([]byte(w.body)) || it.ContentByteLen != int64(len(w.body)) || it.AttemptID != f.author {
			t.Errorf("item %d = %+v", i, it)
		}
		body, err := store.Content(it.ContentSHA256)
		if err != nil || string(body) != w.body {
			t.Errorf("content %d = %q, %v", i, body, err)
		}
	}

	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordDisposition(journal.DeliberationItemInput{TurnID: f.turn, Participant: "author", AttemptID: f.author, Content: []byte("x")}, 3, f.candidate)
		return err
	})
	if !errors.Is(err, journal.ErrDeliberationItemRecorded) {
		t.Errorf("second disposition for a candidate = %v", err)
	}
}

func TestProposalRecordedTwiceIsRefused(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	in := journal.DeliberationItemInput{TurnID: f.turn, Participant: "r1", RosterPosition: 1, AttemptID: f.step["r1"], Label: "proposal 2", Content: []byte("p")}
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordProposal(in); err != nil {
			return err
		}
		// A critique from the same participant is a different item.
		_, err := tx.RecordCritique(in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, rec := range map[string]func(*journal.Tx) error{
		"proposal": func(tx *journal.Tx) error { _, err := tx.RecordProposal(in); return err },
		"critique": func(tx *journal.Tx) error { _, err := tx.RecordCritique(in); return err },
	} {
		if err := store.Tx(rec); !errors.Is(err, journal.ErrDeliberationItemRecorded) {
			t.Errorf("second %s = %v", name, err)
		}
	}
	// The table's own constraints hold without the method's pre-check.
	if _, err := db.Conn().Exec(`INSERT INTO deliberation_items (turn_id, kind, participant, roster_position, attempt_id, label, content_sha256, content_byte_len, recorded_at)
		VALUES (?, 'proposal', 'r1', 1, ?, 'proposal 9', ?, 1, 'now')`, f.turn, f.step["r1"], shaBytes([]byte("p"))); err == nil {
		t.Error("table accepted a second proposal")
	}
	bad := in
	bad.Label = ""
	if err := store.Tx(func(tx *journal.Tx) error { _, err := tx.RecordProposal(bad); return err }); err == nil {
		t.Error("proposal without a label accepted")
	}
	if err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordDisposition(in, 1, f.candidate)
		return err
	}); err == nil {
		t.Error("disposition with a label accepted")
	}
	if _, err := db.Conn().Exec(`UPDATE deliberation_items SET label = 'x'`); err == nil {
		t.Error("items are not immutable")
	}
}

func TestDeliberationItemsOrderAndPaging(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	var second string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if second, err = tx.SetLatestCandidate(f.turn, "answer 2", "h1", 1, f.candidate); err != nil {
			return err
		}
		item := func(p string, pos int, label string) journal.DeliberationItemInput {
			return journal.DeliberationItemInput{TurnID: f.turn, Participant: p, RosterPosition: pos, AttemptID: f.author, Label: label, Content: []byte(p + label)}
		}
		// Recorded out of order on purpose.
		if _, err := tx.RecordDisposition(item("author", 0, ""), 2, second); err != nil {
			return err
		}
		if _, err := tx.RecordCritique(item("b", 1, "critique 2")); err != nil {
			return err
		}
		if _, err := tx.RecordCritique(item("a", 0, "critique 1")); err != nil {
			return err
		}
		if _, err := tx.RecordProposal(item("b", 1, "proposal 2")); err != nil {
			return err
		}
		if _, err := tx.RecordDisposition(item("author", 0, ""), 1, f.candidate); err != nil {
			return err
		}
		_, err = tx.RecordProposal(item("a", 0, "proposal 1"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"proposal 1", "proposal 2", "critique 1", "critique 2", "r1", "r2"}
	var got []string
	cursor, pages := "", 0
	for {
		items, next, err := store.DeliberationItems(f.turn, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, it := range items {
			if it.Kind == journal.DeliberationDisposition {
				got = append(got, map[int]string{1: "r1", 2: "r2"}[it.Round])
			} else {
				got = append(got, it.Label)
			}
		}
		if next == "" {
			break
		}
		if len(items) != 4 {
			t.Fatalf("short page %d before the last", len(items))
		}
		cursor = next
	}
	if pages != 2 || len(got) != len(wantOrder) {
		t.Fatalf("pages = %d, got %v", pages, got)
	}
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", got, wantOrder)
		}
	}
	if _, _, err := store.DeliberationItems(f.turn, "junk", 4); err == nil {
		t.Error("bad cursor accepted")
	}
	if _, _, err := store.DeliberationItems(f.turn, "", 0); err == nil {
		t.Error("limit 0 accepted")
	}
	if items, next, err := store.DeliberationItems("none", "", 4); err != nil || len(items) != 0 || next != "" {
		t.Errorf("unknown turn = %v, %q, %v", items, next, err)
	}
}

func TestDeliberationPhaseHashAndEvents(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		mode, phase, err := tx.DeliberationPhase(f.turn)
		if err != nil || mode != "" || phase != "" {
			t.Errorf("plain turn = %q %q, %v", mode, phase, err)
		}
		if h, err := tx.CandidateDeliberationHash(f.candidate); err != nil || h != "" {
			t.Errorf("plain candidate hash = %q, %v", h, err)
		}
		if err := tx.SetDeliberationPhase(f.turn, "bogus"); err == nil {
			t.Error("bogus phase accepted")
		}
		for _, p := range []string{journal.PhasePropose, journal.PhaseCritique, journal.PhaseIntegrate, journal.PhaseReview} {
			if err := tx.SetDeliberationPhase(f.turn, p); err != nil {
				return err
			}
		}
		if mode, phase, err = tx.DeliberationPhase(f.turn); err != nil || mode != journal.ModeDeliberate || phase != journal.PhaseReview {
			t.Errorf("turn = %q %q, %v", mode, phase, err)
		}
		if err := tx.SetCandidateDeliberationHash(f.candidate, "dh"); err != nil {
			return err
		}
		if err := tx.SetCandidateDeliberationHash(f.candidate, "dh2"); err == nil {
			t.Error("deliberation hash set twice")
		}
		if h, err := tx.CandidateDeliberationHash(f.candidate); err != nil || h != "dh" {
			t.Errorf("hash = %q, %v", h, err)
		}
		if err := tx.EmitDeliberationEvent(f.turn, "tool_call"); err == nil {
			t.Error("foreign event accepted")
		}
		for _, k := range []string{journal.EventProposalsDispatched, journal.EventProposalsComplete, journal.EventCritiquesDispatched,
			journal.EventCritiquesComplete, journal.EventIntegrationDispatched} {
			if err := tx.EmitDeliberationEvent(f.turn, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Candidates stay immutable otherwise.
	if _, err := db.Conn().Exec(`UPDATE candidates SET content = 'x' WHERE id = ?`, f.candidate); err == nil {
		t.Error("candidate content updated")
	}
	evs, err := store.EventsSince(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		if e.TurnID == f.turn && string(e.Data) == `{"turn_id":"`+f.turn+`"}` {
			seen[e.Kind] = true
		}
	}
	for _, k := range []string{"proposals_dispatched", "proposals_complete", "critiques_dispatched", "critiques_complete", "integration_dispatched"} {
		if !seen[k] {
			t.Errorf("event %s missing", k)
		}
	}
}

// TestMigration018UpgradeKeepsRows seeds a version-17 database, upgrades it
// and checks every row survives and an old turn reads back with null mode
// fields.
func TestMigration018UpgradeKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/0*.sql")
	if err != nil || len(files) < 18 {
		t.Fatalf("migration files = %v, %v", files, err)
	}
	sort.Strings(files)
	for i, f := range files[:17] {
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
		`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{}', 'mh')`,
		`INSERT INTO conversations (id, workspace) VALUES ('c1', '/ws')`,
		`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'review', 'c1')`,
		`INSERT INTO candidates (id, turn_id, content, content_hash) VALUES ('k1', 't1', 'answer', 'h0')`,
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
	var mode, phase, hash sql.NullString
	var runID, tmode, content string
	if err := c.QueryRow(`SELECT run_id, mode, deliberation_mode, deliberation_phase FROM turns WHERE id = 't1'`).Scan(&runID, &tmode, &mode, &phase); err != nil {
		t.Fatal(err)
	}
	if runID != "r1" || tmode != "review" || mode.Valid || phase.Valid {
		t.Errorf("turn = %s %s %v %v", runID, tmode, mode, phase)
	}
	if err := c.QueryRow(`SELECT content, deliberation_hash FROM candidates WHERE id = 'k1'`).Scan(&content, &hash); err != nil || content != "answer" || hash.Valid {
		t.Errorf("candidate = %q %v, %v", content, hash, err)
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM deliberation_items`).Scan(&n); err != nil || n != 0 {
		t.Errorf("items = %d, %v", n, err)
	}
	if _, err := c.Exec(`UPDATE turns SET deliberation_phase = 'bogus' WHERE id = 't1'`); err == nil {
		t.Error("bogus phase accepted by the table")
	}
}
