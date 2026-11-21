package journal_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestFinalizeEngineRunManifestGuards(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	if _, err := store.FinalizeEngineRunManifest("no-such-run", json.RawMessage(`{}`)); err == nil {
		t.Fatal("finalize of an unknown run succeeded")
	}

	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.FinalizeEngineRunManifest(runID, json.RawMessage(`{"a":1}`))
	if err != nil || hash == "" {
		t.Fatalf("first finalize = %q, %v", hash, err)
	}
	if _, err := store.FinalizeEngineRunManifest(runID, json.RawMessage(`{"a":2}`)); err == nil {
		t.Fatal("second finalize succeeded")
	}
	// Direct writes are refused by the trigger as well.
	if _, err := db.Conn().Exec(`UPDATE runs SET manifest = '{}' WHERE id = ?`, runID); err == nil {
		t.Fatal("manifest rewrite after finalize succeeded")
	}

	var stored string
	if err := db.Conn().QueryRow(`SELECT manifest FROM runs WHERE id = ?`, runID).Scan(&stored); err != nil || stored != `{"a":1}` {
		t.Fatalf("manifest = %q, %v", stored, err)
	}
}

func TestGetRunWhileProbing(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "probing" || run.Manifest != nil || run.ManifestHash != "" {
		t.Fatalf("probing run = %+v", run)
	}
}

func TestRunOpenedSeqWithZeroProbes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID string
	var opened journal.Event
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if runID, err = tx.AllocateEngineRun(); err != nil {
			return err
		}
		opened, err = tx.Emit("", "run_opened", json.RawMessage(fmt.Sprintf(`{"run_id":%q,"roster_hash":"h"}`, runID)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Finalizing with no probe event in between leaves run_opened first.
	if err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.FinalizeEngineRunManifest(runID, json.RawMessage(`{}`)); err != nil {
			return err
		}
		_, err := tx.Emit("", "engine_started", json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, runID)))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	seq, found, err := store.RunOpenedSeq(runID)
	if err != nil || !found || seq != opened.Seq {
		t.Fatalf("RunOpenedSeq = %d, %v, %v; want %d", seq, found, err, opened.Seq)
	}
	if _, found, err := store.RunOpenedSeq("other"); err != nil || found {
		t.Fatalf("RunOpenedSeq(other) found = %v, err %v", found, err)
	}
}

func TestAbandonProbingEngineRuns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	stale, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatal(err)
	}
	done, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeEngineRunManifest(done, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	var ids []string
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		ids, err = tx.AbandonProbingEngineRuns()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != stale {
		t.Fatalf("abandoned %v, want [%s]", ids, stale)
	}
	for id, want := range map[string]string{stale: "abandoned", done: "started"} {
		run, err := store.GetRun(id)
		if err != nil || run.State != want {
			t.Fatalf("run %s = %+v, %v; want state %s", id, run, err, want)
		}
	}
	// An abandoned run can no longer be finalized.
	if _, err := store.FinalizeEngineRunManifest(stale, json.RawMessage(`{}`)); err == nil {
		t.Fatal("abandoned run was finalized")
	}
}

// TestMigration009RebuildPreservesRuns upgrades a populated version-8
// database and checks the rebuilt runs table keeps its rows, its kind
// default, the immutability trigger and the foreign key from turns.
func TestMigration009RebuildPreservesRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("migrations/00[1-8]_*.sql")
	if err != nil || len(files) != 8 {
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
	if _, err := conn.Exec(`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r1', '{"x":1}', 'h1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO conversations (id) VALUES ('c1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t1', 'r1', 'solo', 'c1')`); err != nil {
		t.Fatal(err)
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

	var kind, state, manifest, hash string
	if err := c.QueryRow(`SELECT kind, state, manifest, manifest_hash FROM runs WHERE id = 'r1'`).Scan(&kind, &state, &manifest, &hash); err != nil {
		t.Fatal(err)
	}
	if kind != "probe" || state != "started" || manifest != `{"x":1}` || hash != "h1" {
		t.Fatalf("migrated row = %q %q %q %q", kind, state, manifest, hash)
	}
	// The kind default survives: an insert that omits it is a probe run.
	if _, err := c.Exec(`INSERT INTO runs (id, manifest, manifest_hash) VALUES ('r2', '{}', 'h2')`); err != nil {
		t.Fatalf("insert without kind: %v", err)
	}
	if err := c.QueryRow(`SELECT kind FROM runs WHERE id = 'r2'`).Scan(&kind); err != nil || kind != "probe" {
		t.Fatalf("default kind = %q, %v", kind, err)
	}
	// Existing manifests stay immutable.
	if _, err := c.Exec(`UPDATE runs SET manifest_hash = 'x' WHERE id = 'r1'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("manifest_hash update err = %v, want immutable", err)
	}
	// The foreign key from turns is enforced again after the migration.
	if _, err := c.Exec(`INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('t2', 'missing', 'solo', 'c1')`); err == nil {
		t.Fatal("turn referencing a missing run was accepted")
	}
	var turns int
	if err := c.QueryRow(`SELECT COUNT(*) FROM turns WHERE run_id = 'r1'`).Scan(&turns); err != nil || turns != 1 {
		t.Fatalf("turns = %d, %v", turns, err)
	}
	// A started run cannot carry a NULL manifest.
	if _, err := c.Exec(`INSERT INTO runs (id, manifest, manifest_hash, state) VALUES ('r3', NULL, NULL, 'started')`); err == nil {
		t.Fatal("started run without manifest was accepted")
	}
}
