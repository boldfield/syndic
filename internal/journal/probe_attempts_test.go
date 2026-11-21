package journal_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// TestMigration010RebuildPreservesAttempts upgrades a populated version-9
// database and checks the rebuilt attempts table keeps its rows and their
// dependents, and now accepts a probe attempt that belongs to a run.
func TestMigration010RebuildPreservesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/00[1-9]_*.sql")
	if err != nil || len(files) != 9 {
		t.Fatalf("migration files = %v, %v", files, err)
	}
	for i, f := range files {
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
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = 'a1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dispatches = %d, %v", n, err)
	}
	if err := c.QueryRow(`SELECT COUNT(*) FROM capacity_holds WHERE attempt_id = 'a1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("holds = %d, %v", n, err)
	}
	// The raw-output trigger survived the rebuild.
	if _, err := c.Exec(`UPDATE attempts SET raw_output = 'x' WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`UPDATE attempts SET raw_output = 'y' WHERE id = 'a1'`); err == nil {
		t.Fatal("raw_output rewrite succeeded; trigger lost")
	}
	// An attempt belongs to a turn or to a probe run, never neither.
	if _, err := c.Exec(`INSERT INTO attempts (id, participant, phase, epoch, dispatched_at) VALUES ('a2', 'p', 'probe', 1, 'now')`); err == nil {
		t.Fatal("attempt with neither turn nor probe run accepted")
	}
	if _, err := c.Exec(`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, probe_run_id) VALUES ('a3', 't1', 'p', 'probe', 1, 'now', 'r1')`); err == nil {
		t.Fatal("attempt with both turn and probe run accepted")
	}
}

func TestProbeAttemptRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	group := []journal.GroupMember{{EndpointName: "e", Model: "z"}, {EndpointName: "e", Model: "a"}}
	var runID, attemptID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if runID, err = tx.AllocateEngineRun(); err != nil {
			return err
		}
		attemptID, err = tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID: runID, Participant: "p1", Endpoint: "e", Model: "a", Pool: "pool", Group: group, Step: "models",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var pending []string
	var pa journal.ProbeAttempt
	var found bool
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		if pending, err = tx.RecoverPending(); err != nil {
			return err
		}
		pa, found, err = tx.GetProbeAttempt(attemptID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != attemptID {
		t.Fatalf("pending = %v", pending)
	}
	if !found || pa.RunID != runID || pa.Participant != "p1" || pa.Endpoint != "e" || pa.Model != "a" || pa.Pool != "pool" || pa.Step != "models" {
		t.Fatalf("probe attempt = %+v found=%v", pa, found)
	}
	if len(pa.Group) != 2 || pa.Group[0].Model != "a" || pa.Group[1].Model != "z" {
		t.Fatalf("group = %+v, want sorted", pa.Group)
	}
	row, err := store.GetAttempt(attemptID)
	if err != nil {
		t.Fatalf("GetAttempt of a probe attempt: %v", err)
	}
	if row.TurnID != "" || row.Phase != "probe" || row.State != "pending" {
		t.Fatalf("row = %+v", row)
	}

	if err := store.Tx(func(tx *journal.Tx) error {
		return tx.CompleteProbeAttempt(attemptID, "complete", "ok")
	}); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetAttempt(attemptID)
	if err != nil || row.State != "completed" {
		t.Fatalf("after complete: %+v, %v", row, err)
	}

	// A turn's attempt is not a probe attempt.
	_, turnID, err := createTestRunAndTurn(t, db, store)
	if err != nil {
		t.Fatal(err)
	}
	var turnAttempt string
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		turnAttempt, err = tx.BeginAttempt(turnID, "p1", "review", 1, nil, "")
		if err != nil {
			return err
		}
		_, found, err = tx.GetProbeAttempt(turnAttempt)
		return err
	}); err != nil || found {
		t.Fatalf("turn attempt reported as probe attempt: found=%v err=%v", found, err)
	}
}
