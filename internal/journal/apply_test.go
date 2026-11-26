package journal_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// applyEdit is one staged path: base "" means absent.
type applyEdit struct{ path, base, next string }

// applyFixture freezes a change set over edits, writing the base files into a
// fresh workspace first.
func applyFixture(t *testing.T, store *journal.Store, edits ...applyEdit) (ws, csID string) {
	t.Helper()
	ws = filepath.Join(t.TempDir(), "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		for _, e := range edits {
			if e.base == "" {
				if _, err := tx.RecordObservation(f.turn, f.author, e.path, journal.ObservationAbsent, nil); err != nil {
					return err
				}
			} else {
				full := filepath.Join(ws, e.path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(full, []byte(e.base), 0o640); err != nil {
					return err
				}
				if _, err := tx.RecordObservation(f.turn, f.author, e.path, journal.ObservationFile, []byte(e.base)); err != nil {
					return err
				}
			}
			if _, err := tx.StageWrite(f.turn, e.path, []byte(e.next)); err != nil {
				return err
			}
		}
		cs, err := tx.FreezeChangeSet(f.turn, f.candidate, "snap", "chg")
		csID = cs.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws, csID
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func outcomes(paths []journal.ChangeSetPath) map[string]string {
	m := map[string]string{}
	for _, p := range paths {
		m[p.Path] = p.Outcome
	}
	return m
}

func newApplyStore(t *testing.T) *journal.Store {
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	return journal.NewStore(db)
}

func TestApplyModifyAndCreateThenStableRetry(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store,
		applyEdit{"a.txt", "old a", "new a"},
		applyEdit{"sub/dir/c.txt", "", "created"})
	res, err := store.ApplyChangeSet(ws, id, false)
	if err != nil {
		t.Fatal(err)
	}
	o := outcomes(res.Paths)
	if res.State != journal.ChangeSetApplied || o["a.txt"] != "applied" || o["sub/dir/c.txt"] != "applied" {
		t.Fatalf("result = %+v", res)
	}
	if readFile(t, filepath.Join(ws, "a.txt")) != "new a" || readFile(t, filepath.Join(ws, "sub/dir/c.txt")) != "created" {
		t.Fatal("content not written")
	}
	if fi, _ := os.Stat(filepath.Join(ws, "a.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if ents, _ := os.ReadDir(ws); len(ents) != 2 {
		t.Fatalf("stray files in workspace: %v", ents)
	}
	opsBefore, _ := store.ApplyOps(id)

	// Retry of an applied change set returns the stable state, writes and
	// journals nothing, even if the files have since changed.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("later edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := store.ApplyChangeSet(ws, id, false)
	if err != nil || again.State != journal.ChangeSetApplied {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if readFile(t, filepath.Join(ws, "a.txt")) != "later edit" {
		t.Fatal("retry wrote")
	}
	if opsAfter, _ := store.ApplyOps(id); len(opsAfter) != len(opsBefore) {
		t.Fatalf("retry journaled: %d -> %d", len(opsBefore), len(opsAfter))
	}
}

func TestApplyFirstApplyBaseChangedWritesNothing(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	if err := os.WriteFile(filepath.Join(ws, "b"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyChangeSet(ws, id, false)
	var bc *journal.BaseChangedError
	if !errors.Is(err, journal.ErrBaseChanged) || !errors.As(err, &bc) || len(bc.Paths) != 1 || bc.Paths[0].Path != "b" {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, filepath.Join(ws, "a")) != "base a" {
		t.Fatal("a was written")
	}
	cs, _ := store.GetChangeSet(id)
	if cs.State != journal.ChangeSetAppliable {
		t.Fatalf("state = %s", cs.State)
	}
	if ops, _ := store.ApplyOps(id); len(ops) != 0 {
		t.Fatalf("journaled %+v", ops)
	}
	// An already-applied path on a first apply is also base_changed.
	if err := os.WriteFile(filepath.Join(ws, "b"), []byte("base b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a"), []byte("new a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyChangeSet(ws, id, false); !errors.Is(err, journal.ErrBaseChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyWorkspaceMissing(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "x", "y"})
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyChangeSet(ws, id, false); !errors.Is(err, journal.ErrWorkspaceMissing) {
		t.Fatalf("err = %v", err)
	}
	if ops, _ := store.ApplyOps(id); len(ops) != 0 {
		t.Fatal("journaled")
	}
}

// A crash after the rename and before apply_file leaves the apply unfinished;
// recovery persists what it finds without writing, and a retry completes.
func TestApplyCrashRecoverThenRetry(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	crash := errors.New("crash")
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "renamed" && path == "a" {
			return crash
		}
		return nil
	})
	_, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if !errors.Is(err, crash) {
		t.Fatalf("err = %v", err)
	}
	cs, _ := store.GetChangeSet(id)
	if cs.State != journal.ChangeSetPartial || outcomes(cs.Paths)["a"] != journal.PathPending {
		t.Fatalf("after crash = %+v", cs)
	}
	rec, err := store.RecoverApply(ws, id)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rec.Paths); rec.State != journal.ChangeSetPartial || o["a"] != "applied" || o["b"] != "pending" {
		t.Fatalf("recovered = %+v", rec)
	}
	if readFile(t, filepath.Join(ws, "b")) != "base b" {
		t.Fatal("recovery wrote")
	}
	// Persisted, not only returned.
	if got, _ := store.GetChangeSet(id); outcomes(got.Paths)["a"] != "applied" {
		t.Fatalf("not persisted: %+v", got)
	}
	// Recovery with nothing unfinished is a no-op.
	n1, _ := store.ApplyOps(id)
	if _, err := store.RecoverApply(ws, id); err != nil {
		t.Fatal(err)
	}
	if n2, _ := store.ApplyOps(id); len(n2) != len(n1) {
		t.Fatal("second recovery journaled")
	}
	res, err := store.ApplyChangeSet(ws, id, false)
	if err != nil || res.State != journal.ChangeSetApplied {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	if readFile(t, filepath.Join(ws, "a")) != "new a" || readFile(t, filepath.Join(ws, "b")) != "new b" {
		t.Fatal("retry content")
	}
}

// A conflict is reported by name after pending paths are written; discard
// then keeps the applied path recorded.
func TestApplyPartialConflictRetryAndDiscard(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "write" && path == "b" {
			return os.WriteFile(filepath.Join(ws, "b"), []byte("edited meanwhile"), 0o644)
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(res.Paths); res.State != journal.ChangeSetConflict || o["a"] != "applied" || o["b"] != "conflict" {
		t.Fatalf("result = %+v", res)
	}
	if readFile(t, filepath.Join(ws, "b")) != "edited meanwhile" {
		t.Fatal("conflicting target overwritten")
	}
	if ents, _ := os.ReadDir(ws); len(ents) != 2 {
		t.Fatalf("temporary file left: %v", ents)
	}

	// Retry after restoring b: a is skipped, b is written.
	if err := os.WriteFile(filepath.Join(ws, "b"), []byte("base b"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = store.ApplyChangeSet(ws, id, false)
	if err != nil || res.State != journal.ChangeSetApplied || readFile(t, filepath.Join(ws, "b")) != "new b" {
		t.Fatalf("retry = %+v, %v", res, err)
	}

	// Partial discard preserves applied paths.
	ws2, id2 := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	restore = journal.SetApplyHook(func(stage, path string) error {
		if stage == "write" && path == "b" {
			return errors.New("crash")
		}
		return nil
	})
	_, _ = store.ApplyChangeSet(ws2, id2, false)
	restore()
	if _, err := store.RecoverApply(ws2, id2); err != nil {
		t.Fatal(err)
	}
	if err := store.Tx(func(tx *journal.Tx) error { return tx.DiscardChangeSet(id2) }); err != nil {
		t.Fatal(err)
	}
	cs, _ := store.GetChangeSet(id2)
	if o := outcomes(cs.Paths); cs.State != journal.ChangeSetDiscarded || o["a"] != journal.PathApplied || o["b"] != journal.PathPending {
		t.Fatalf("discarded = %+v", cs)
	}
	if readFile(t, filepath.Join(ws2, "a")) != "new a" {
		t.Fatal("discard deleted applied file")
	}
	if _, err := store.ApplyChangeSet(ws2, id2, false); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("apply discarded err = %v", err)
	}
}

func TestApplyCreateNeverReplaces(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"new.txt", "", "mine"})
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "write" {
			return os.WriteFile(filepath.Join(ws, path), []byte("theirs"), 0o644)
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if err != nil || res.State != journal.ChangeSetConflict || outcomes(res.Paths)["new.txt"] != "conflict" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, filepath.Join(ws, "new.txt")) != "theirs" {
		t.Fatal("create replaced an existing file")
	}
	if ents, _ := os.ReadDir(ws); len(ents) != 1 {
		t.Fatalf("temporary file left: %v", ents)
	}
}

func TestApplyConfinement(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store,
		applyEdit{"link/x", "", "escaped"},
		applyEdit{"../outside", "", "escaped"},
		applyEdit{"ok", "", "fine"})
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyChangeSet(ws, id, false)
	var bc *journal.BaseChangedError
	if !errors.As(err, &bc) || len(bc.Paths) != 2 {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("escaped into %v", ents)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ws), "outside")); err == nil {
		t.Fatal("wrote through ..")
	}

	// Swapped for a link between classification and effect: refused at open.
	ws2, id2 := applyFixture(t, store, applyEdit{"d/f", "", "x"}, applyEdit{"ok", "", "y"})
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "write" && path == "d/f" {
			return os.Symlink(outside, filepath.Join(ws2, "d"))
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws2, id2, false)
	restore()
	if o := outcomes(res.Paths); err != nil || o["d/f"] != "conflict" || o["ok"] != "applied" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("escaped into %v", ents)
	}
}

func TestApplyFollowsRelocatedRootAndRefusesSpecialFile(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	moved := ws + "-moved"
	restore := journal.SetApplyHook(func(stage, _ string) error {
		if stage == "started" {
			return os.Rename(ws, moved)
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if err != nil || res.State != journal.ChangeSetApplied {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, filepath.Join(moved, "a")) != "new a" {
		t.Fatal("moved root not followed")
	}
	if _, err := os.Stat(ws); err == nil {
		t.Fatal("wrote at the old path")
	}

	// A FIFO at the target is not a regular file: base_changed, no blocking.
	ws2, id2 := applyFixture(t, store, applyEdit{"f", "x", "y"})
	if err := os.Remove(filepath.Join(ws2, "f")); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(filepath.Join(ws2, "f")); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	if _, err = store.ApplyChangeSet(ws2, id2, false); !errors.Is(err, journal.ErrBaseChanged) {
		t.Fatalf("err = %v", err)
	}
}

// Failure between record operations rolls back atomically.
func TestApplyRecordsRollBackAtomically(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "x", "y"}, applyEdit{"b", "", "z"})
	boom := errors.New("boom")
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.BeginApply(id, false); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(id, "a", journal.PathApplied); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if ops, _ := store.ApplyOps(id); len(ops) != 0 {
		t.Fatalf("partial records survived: %+v", ops)
	}
	cs, _ := store.GetChangeSet(id)
	if cs.State != journal.ChangeSetAppliable || outcomes(cs.Paths)["a"] != journal.PathPending {
		t.Fatalf("state = %+v", cs)
	}
	// A clean apply still works afterwards.
	if res, err := store.ApplyChangeSet(ws, id, false); err != nil || res.State != journal.ChangeSetApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

// An old change set survives a later one staging the same path and fails its
// base check once the other is applied.
func TestApplyOldProposalSurvivesOverlappingStaging(t *testing.T) {
	store := newApplyStore(t)
	ws, first := applyFixture(t, store, applyEdit{"a", "base", "first"})
	f := buildToolsTurn(t, store)
	var second string
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordObservation(f.turn, f.author, "a", journal.ObservationFile, []byte("base")); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "a", []byte("second")); err != nil {
			return err
		}
		cs, err := tx.FreezeChangeSet(f.turn, f.candidate, "snap2", "chg2")
		second = cs.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs, err := store.GetChangeSet(first); err != nil || cs.State != journal.ChangeSetAppliable {
		t.Fatalf("first = %+v, %v", cs, err)
	}
	if res, err := store.ApplyChangeSet(ws, second, false); err != nil || res.State != journal.ChangeSetApplied {
		t.Fatalf("second = %+v, %v", res, err)
	}
	if _, err = store.ApplyChangeSet(ws, first, false); !errors.Is(err, journal.ErrBaseChanged) {
		t.Fatalf("first err = %v", err)
	}
	cs, err := store.GetChangeSet(first)
	if err != nil || cs.State != journal.ChangeSetAppliable || readFile(t, filepath.Join(ws, "a")) != "second" {
		t.Fatalf("first kept = %+v, %v", cs, err)
	}
}

// A created file takes the process umask, not a fixed world-writable mode.
func TestApplyCreatedFileHonoursUmask(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"new.txt", "", "hello"}, applyEdit{"m.txt", "old", "new"})
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	if _, err := store.ApplyChangeSet(ws, id, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(ws, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("created mode = %v, want -rw-r--r--", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(ws, "m.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("modified mode = %v", fi.Mode().Perm())
	}
}

// The applied state is read before the workspace is opened.
func TestApplyAppliedRetryWithWorkspaceGone(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "x", "y"})
	if _, err := store.ApplyChangeSet(ws, id, false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	res, err := store.ApplyChangeSet(ws, id, false)
	if err != nil || res.State != journal.ChangeSetApplied {
		t.Fatalf("retry = %+v, %v", res, err)
	}
}

// Recovery records every fresh classification, including a path that was
// recorded as conflict and is back at its base.
func TestApplyRecoverRecordsConflictToPending(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	if err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.BeginApply(id, false); err != nil {
			return err
		}
		return tx.RecordApplyFile(id, "a", journal.PathConflict)
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.RecoverApply(ws, id)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomes(rec.Paths); rec.State != journal.ChangeSetPartial || o["a"] != journal.PathPending || o["b"] != journal.PathPending {
		t.Fatalf("recovered = %+v", rec)
	}
	if got, _ := store.GetChangeSet(id); outcomes(got.Paths)["a"] != journal.PathPending {
		t.Fatalf("not persisted: %+v", got)
	}
}

// Atomic-save replacement (new inode) between temp write and re-check is
// detected as conflict and the editor's file is preserved. This is the race
// that the pre-rename re-check protects against.
func TestApplyAtomicSaveReplacementDetectedAsConflict(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"a", "base a", "new a"}, applyEdit{"b", "base b", "new b"})
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "temp" && path == "b" {
			// Simulate an editor's atomic save: write a sibling file and rename
			// it over the target, creating a new inode.
			sibPath := filepath.Join(ws, "b.tmp-atomic")
			if err := os.WriteFile(sibPath, []byte("editor's atomic save"), 0o644); err != nil {
				return err
			}
			return os.Rename(sibPath, filepath.Join(ws, "b"))
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	o := outcomes(res.Paths)
	if res.State != journal.ChangeSetConflict || o["a"] != "applied" || o["b"] != "conflict" {
		t.Fatalf("result = %+v", res)
	}
	// Editor's atomic-save content is preserved.
	if readFile(t, filepath.Join(ws, "b")) != "editor's atomic save" {
		t.Fatal("editor's atomic save was overwritten")
	}
	// No temp file left behind.
	if ents, _ := os.ReadDir(ws); len(ents) != 2 {
		t.Fatalf("temporary file left: %v", ents)
	}
}

// Concurrent creation at the temp stage is detected as conflict and the
// existing file is left untouched. This exercises linkat's EEXIST rejection.
func TestApplyConcurrentCreateAtTempStageDetectedAsConflict(t *testing.T) {
	store := newApplyStore(t)
	ws, id := applyFixture(t, store, applyEdit{"new", "", "created by us"})
	restore := journal.SetApplyHook(func(stage, path string) error {
		if stage == "temp" && path == "new" {
			// A concurrent create: write a file at the target name before our
			// linkat attempt.
			return os.WriteFile(filepath.Join(ws, "new"), []byte("created by other"), 0o644)
		}
		return nil
	})
	res, err := store.ApplyChangeSet(ws, id, false)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	o := outcomes(res.Paths)
	if res.State != journal.ChangeSetConflict || o["new"] != "conflict" {
		t.Fatalf("result = %+v", res)
	}
	// The other process's file is left untouched.
	if readFile(t, filepath.Join(ws, "new")) != "created by other" {
		t.Fatal("concurrent create was overwritten")
	}
	// No temp file left behind.
	if ents, _ := os.ReadDir(ws); len(ents) != 1 {
		t.Fatalf("temporary file left: %v", ents)
	}
}
