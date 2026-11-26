package journal_test

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// applyFixture freezes a change set over a (modify), b (modify) and n (create).
func applyFixture(t *testing.T, store *journal.Store) (csID, turn string, hashes map[string][2]string) {
	t.Helper()
	f := buildToolsTurn(t, store)
	hashes = map[string][2]string{}
	err := store.Tx(func(tx *journal.Tx) error {
		for _, p := range []string{"a", "b"} {
			if _, err := tx.RecordObservation(f.turn, f.author, p, journal.ObservationFile, []byte("base "+p)); err != nil {
				return err
			}
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "n", journal.ObservationAbsent, nil); err != nil {
			return err
		}
		for _, p := range []string{"a", "b", "n"} {
			if _, err := tx.StageWrite(f.turn, p, []byte("new "+p)); err != nil {
				return err
			}
		}
		cs, err := tx.FreezeChangeSet(f.turn, f.candidate, "s", "c")
		if err != nil {
			return err
		}
		csID = cs.ID
		for _, p := range cs.Paths {
			hashes[p.Path] = [2]string{p.BaseSHA256, p.NewSHA256}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return csID, f.turn, hashes
}

func outcomes(t *testing.T, store *journal.Store, csID string) (string, map[string]string) {
	t.Helper()
	cs, err := store.GetChangeSet(csID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range cs.Paths {
		out[p.Path] = p.Outcome
	}
	return cs.State, out
}

func TestClassifyPaths(t *testing.T) {
	paths := []journal.ChangeSetPath{
		{Path: "m", Op: "modify", BaseSHA256: "b", NewSHA256: "n"},
		{Path: "c", Op: "create", NewSHA256: "n"},
	}
	cases := []struct {
		live map[string]journal.LivePath
		want [2]string
	}{
		{map[string]journal.LivePath{"m": {true, "b"}}, [2]string{journal.PathPending, journal.PathPending}},
		{map[string]journal.LivePath{"m": {true, "n"}, "c": {true, "n"}}, [2]string{journal.PathApplied, journal.PathApplied}},
		{map[string]journal.LivePath{"m": {true, "x"}, "c": {true, "x"}}, [2]string{journal.PathConflict, journal.PathConflict}},
		{map[string]journal.LivePath{}, [2]string{journal.PathConflict, journal.PathPending}},
	}
	for i, c := range cases {
		got := journal.ClassifyPaths(paths, c.live)
		if got[0].Outcome != c.want[0] || got[1].Outcome != c.want[1] {
			t.Errorf("case %d: got %+v, want %v", i, got, c.want)
		}
	}
}

func TestPrepareApplyBaseChangedWritesNothing(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	csID, _, h := applyFixture(t, store)
	live := map[string]journal.LivePath{"a": {true, h["a"][0]}, "b": {true, "other"}, "n": {true, "squatter"}}
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.PrepareApply(csID, live, false)
		return err
	})
	var bc *journal.BaseChangedError
	if !errors.Is(err, journal.ErrBaseChanged) || !errors.As(err, &bc) || len(bc.Paths) != 2 || bc.Paths[0] != "b" || bc.Paths[1] != "n" {
		t.Fatalf("err = %v", err)
	}
	if s, _ := outcomes(t, store, csID); s != journal.ChangeSetAppliable {
		t.Fatalf("state = %s", s)
	}
	if ops, _ := store.ApplyOps(csID); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestAppliedRetryAndPartialDiscard(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	csID, _, h := applyFixture(t, store)
	live := map[string]journal.LivePath{"a": {true, h["a"][0]}, "b": {true, h["b"][0]}} // n absent: pending
	var plan journal.ApplyPlan
	if err := store.Tx(func(tx *journal.Tx) (err error) { plan, err = tx.PrepareApply(csID, live, false); return }); err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 3 || plan.State != journal.ChangeSetPartial {
		t.Fatalf("plan = %+v", plan)
	}
	if err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.RecordApplyFile(csID, "a", journal.PathApplied); err != nil {
			return err
		}
		return tx.DiscardChangeSet(csID)
	}); err != nil {
		t.Fatal(err)
	}
	s, o := outcomes(t, store, csID)
	if s != journal.ChangeSetDiscarded || o["a"] != journal.PathApplied || o["b"] != journal.PathPending {
		t.Fatalf("after discard = %s %v", s, o)
	}

	// Applied retry on a second change set is stable and journals nothing.
	csID2, _, h2 := applyFixture(t, store)
	done := map[string]journal.LivePath{"a": {true, h2["a"][1]}, "b": {true, h2["b"][1]}, "n": {true, h2["n"][1]}}
	if err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.PrepareApply(csID2, map[string]journal.LivePath{"a": {true, h2["a"][0]}, "b": {true, h2["b"][0]}}, false); err != nil {
			return err
		}
		for _, p := range []string{"a", "b", "n"} {
			if err := tx.RecordApplyFile(csID2, p, journal.PathApplied); err != nil {
				return err
			}
		}
		_, err := tx.FinishApply(csID2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ApplyOps(csID2)
	for i := 0; i < 2; i++ {
		if err := store.Tx(func(tx *journal.Tx) (err error) { plan, err = tx.PrepareApply(csID2, done, false); return }); err != nil {
			t.Fatal(err)
		}
		if plan.State != journal.ChangeSetApplied || len(plan.Pending) != 0 {
			t.Fatalf("retry plan = %+v", plan)
		}
	}
	if after, _ := store.ApplyOps(csID2); len(after) != len(before) {
		t.Fatalf("retry journaled: %d -> %d", len(before), len(after))
	}
}

func TestRecoverApplyPersistsPerPathStates(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	csID, _, h := applyFixture(t, store)
	if err := store.Tx(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); err != nil {
		t.Fatal(err)
	}
	live := map[string]journal.LivePath{"a": {true, h["a"][1]}, "b": {true, h["b"][0]}} // a done, b pending, n absent
	cs, err := store.RecoverApply(csID, live)
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != journal.ChangeSetPartial {
		t.Fatalf("state = %s", cs.State)
	}
	s, o := outcomes(t, store, csID)
	if s != journal.ChangeSetPartial || o["a"] != journal.PathApplied || o["b"] != journal.PathPending || o["n"] != journal.PathPending {
		t.Fatalf("durable = %s %v", s, o)
	}
	// Recovering again needs a new started op: refused, nothing changes.
	if _, err := store.RecoverApply(csID, live); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("second recover = %v", err)
	}
}

func TestRecoverApplyRollsBackAtomically(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	csID, _, h := applyFixture(t, store)
	// No apply in progress: the file records are refused, so nothing is kept.
	live := map[string]journal.LivePath{"a": {true, h["a"][1]}}
	if _, err := store.RecoverApply(csID, live); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("err = %v", err)
	}
	if ops, _ := store.ApplyOps(csID); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}

	// Failure after the file records, before apply_recovered, undoes them.
	if err := store.Tx(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ApplyOps(csID)
	sentinel := errors.New("boom")
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecoverApply(csID, live); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if after, _ := store.ApplyOps(csID); len(after) != len(before) {
		t.Fatalf("ops not rolled back: %d -> %d", len(before), len(after))
	}
}

func TestOldChangeSetSurvivesOverlappingStaging(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	csID, _, h := applyFixture(t, store)
	_, _, _ = applyFixture(t, store) // a later turn stages the same paths
	live := map[string]journal.LivePath{"a": {true, h["a"][0]}, "b": {true, h["b"][0]}}
	if err := store.Tx(func(tx *journal.Tx) error { _, err := tx.PrepareApply(csID, live, false); return err }); err != nil {
		t.Fatal(err)
	}
	if s, _ := outcomes(t, store, csID); s != journal.ChangeSetPartial {
		t.Fatalf("state = %s", s)
	}
}
