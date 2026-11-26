package observation

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func listing(tb testing.TB, entries ...Entry) []byte {
	tb.Helper()
	b, err := FrameEntries(entries)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func listReader(tb testing.TB, n *atomic.Int32, entries ...Entry) Reader {
	b := listing(tb, entries...)
	return func() (string, []byte, error) {
		n.Add(1)
		return journal.ObservationListing, b, nil
	}
}

func absentReader(n *atomic.Int32) Reader {
	return func() (string, []byte, error) {
		n.Add(1)
		return journal.ObservationAbsent, nil, nil
	}
}

func (f fixture) observe(tb testing.TB, path string, r Reader) (journal.Observation, error) {
	tb.Helper()
	return NewStore(f.j).Observe(f.turn, f.author, path, r)
}

func (f fixture) mustObserve(tb testing.TB, path string, r Reader) journal.Observation {
	tb.Helper()
	o, err := f.observe(tb, path, r)
	if err != nil {
		tb.Fatalf("observe %q: %v", path, err)
	}
	return o
}

func (f fixture) entries(tb testing.TB, o journal.Observation) []Entry {
	tb.Helper()
	body, err := f.j.Content(o.ContentSHA256)
	if err != nil {
		tb.Fatal(err)
	}
	e, err := ParseEntries(body)
	if err != nil {
		tb.Fatal(err)
	}
	return e
}

func names(es []Entry) string {
	var parts []string
	for _, e := range es {
		parts = append(parts, e.Type+":"+e.Name)
	}
	return strings.Join(parts, ",")
}

func (f fixture) recorded(tb testing.TB, path string) bool {
	tb.Helper()
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		tb.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == path {
			return true
		}
	}
	return false
}

func (f fixture) conflictEvents(tb testing.TB) []string {
	tb.Helper()
	var out []string
	evs, err := f.j.EventsSince(0, 100000)
	for _, e := range evs {
		if e.TurnID == f.turn && e.Kind == "snapshot_conflict" {
			out = append(out, string(e.Data))
		}
	}
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

func wantConflict(tb testing.TB, f fixture, path string, err error, typ string) {
	tb.Helper()
	var ce *SnapshotConflictError
	if !errors.Is(err, ErrSnapshotConflict) || !errors.As(err, &ce) || ce.Path != path || ce.Type != typ {
		tb.Fatalf("err = %v, want snapshot_conflict %s %q", err, typ, path)
	}
	if f.recorded(tb, path) {
		tb.Fatalf("%q was recorded despite the conflict", path)
	}
	evs := f.conflictEvents(tb)
	if len(evs) == 0 || !strings.Contains(evs[len(evs)-1], `"conflict":"`+typ+`"`) {
		tb.Fatalf("conflict not journaled: %v", evs)
	}
}

func TestListedNameMissingServedAbsentWithoutLiveRead(t *testing.T) {
	for _, tc := range []struct{ dir, child string }{{"dir", "dir/new"}, {Root, "new.txt"}} {
		t.Run(tc.dir, func(t *testing.T) {
			f := newFixture(t)
			var reads atomic.Int32
			f.mustObserve(t, tc.dir, listReader(t, &reads, Entry{"f", "a"}))
			reads.Store(0)
			o := f.mustObserve(t, tc.child, fileReader(&reads, "live"))
			if o.Kind != journal.ObservationAbsent || reads.Load() != 0 {
				t.Fatalf("kind %q, reads %d; want absent from the record", o.Kind, reads.Load())
			}
			if again := f.mustObserve(t, tc.child, fileReader(&reads, "live")); again.ID != o.ID {
				t.Fatal("repeat read not served from the record")
			}
		})
	}
}

func TestUnlistedAncestorHidesDescendants(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	f.mustObserve(t, "dir", listReader(t, &reads, Entry{"f", "x"}))
	reads.Store(0)
	if o := f.mustObserve(t, "dir/sub/y", fileReader(&reads, "live")); o.Kind != journal.ObservationAbsent || reads.Load() != 0 {
		t.Fatalf("deep unlisted: kind %q reads %d", o.Kind, reads.Load())
	}
	// A listed file is not a directory either.
	if o := f.mustObserve(t, "dir/x/z", fileReader(&reads, "live")); o.Kind != journal.ObservationAbsent {
		t.Fatalf("under a listed file: kind %q", o.Kind)
	}
}

func TestRecordedAbsenceCoversSubtree(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	f.mustObserve(t, "gone", absentReader(&reads))
	reads.Store(0)
	if o := f.mustObserve(t, "gone/z", fileReader(&reads, "live")); o.Kind != journal.ObservationAbsent || reads.Load() != 0 {
		t.Fatalf("kind %q reads %d", o.Kind, reads.Load())
	}
	if o := f.mustObserve(t, "gone", listReader(t, &reads)); o.Kind != journal.ObservationAbsent {
		t.Fatal("listing of an absent path must come from the record")
	}
}

func TestRecordedAbsenceRemovedFromLaterListing(t *testing.T) {
	for _, dir := range []string{"dir", Root} {
		t.Run(dir, func(t *testing.T) {
			f := newFixture(t)
			var reads atomic.Int32
			child := "ghost"
			if dir != Root {
				child = dir + "/ghost"
			}
			f.mustObserve(t, child, absentReader(&reads))
			o := f.mustObserve(t, dir, listReader(t, &reads, Entry{"f", "ghost"}, Entry{"f", "keep"}))
			if got := names(f.entries(t, o)); got != "f:keep" {
				t.Fatalf("entries %q", got)
			}
		})
	}
}

func TestKnownFileAndDirRetainedInLaterListing(t *testing.T) {
	for _, dir := range []string{"p", Root} {
		t.Run(dir, func(t *testing.T) {
			f := newFixture(t)
			var reads atomic.Int32
			join := func(n string) string {
				if dir == Root {
					return n
				}
				return dir + "/" + n
			}
			f.mustObserve(t, join("file"), fileReader(&reads, "x"))
			f.mustObserve(t, join("sub"), listReader(t, &reads))
			f.mustObserve(t, join("deep/er/f"), fileReader(&reads, "y"))
			// The live tree has lost them or changed their type.
			o := f.mustObserve(t, dir, listReader(t, &reads, Entry{"d", "file"}, Entry{"f", "other"}))
			if got := names(f.entries(t, o)); got != "d:deep,f:file,f:other,d:sub" {
				t.Fatalf("entries %q", got)
			}
		})
	}
}

func TestDeletedListedFileConflicts(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	f.mustObserve(t, "dir", listReader(t, &reads, Entry{"f", "a"}))
	_, err := f.observe(t, "dir/a", absentReader(&reads))
	wantConflict(t, f, "dir/a", err, ConflictDeleted)
	// Nothing recorded, so the path is free to retry once the file is back.
	if o := f.mustObserve(t, "dir/a", fileReader(&reads, "back")); o.Kind != journal.ObservationFile {
		t.Fatalf("retry kind %q", o.Kind)
	}
}

func TestTypeChangedListedEntryConflicts(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	f.mustObserve(t, Root, listReader(t, &reads, Entry{"f", "a"}, Entry{"d", "d"}))
	_, err := f.observe(t, "a", listReader(t, &reads))
	wantConflict(t, f, "a", err, ConflictTypeChanged)
	_, err = f.observe(t, "d", fileReader(&reads, "x"))
	wantConflict(t, f, "d", err, ConflictTypeChanged)
}

func TestKnownDescendantForcesDirectory(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	f.mustObserve(t, "p/q/r", fileReader(&reads, "x"))
	_, err := f.observe(t, "p", absentReader(&reads))
	wantConflict(t, f, "p", err, ConflictDeleted)
	_, err = f.observe(t, "p/q", fileReader(&reads, "x"))
	wantConflict(t, f, "p/q", err, ConflictTypeChanged)
}

func TestConflictWithBadAttemptIsNotJournaledOrReportedAsConflict(t *testing.T) {
	f := newFixture(t)
	var other string
	err := f.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		turn, err := tx.CreateTurn(run, "review", nil)
		if err != nil {
			return err
		}
		other, err = tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	f.mustObserve(t, "dir", listReader(t, &reads, Entry{"f", "a"}))
	for name, attempt := range map[string]string{"nonexistent": "no-such-attempt", "cross-turn": other} {
		t.Run(name, func(t *testing.T) {
			_, err := NewStore(f.j).Observe(f.turn, attempt, "dir/a", absentReader(&reads))
			if err == nil || errors.Is(err, ErrSnapshotConflict) {
				t.Fatalf("err = %v, want a journaling failure that is not snapshot_conflict", err)
			}
			if f.recorded(t, "dir/a") {
				t.Fatal("dir/a recorded")
			}
			if evs := f.conflictEvents(t); len(evs) != 0 {
				t.Fatalf("conflict event written for a bad attempt: %v", evs)
			}
		})
	}
}

func TestParseEntriesStrict(t *testing.T) {
	want := []Entry{{"f", "a\nb"}, {"d", ""}, {"f", "z"}}
	got, err := ParseEntries(listing(t, want...))
	if err != nil || names(got) != "d:,f:a\nb,f:z" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"f -1\nx\n", "f +1\nx\n", "f 01\nx\n", "f 1x\nx\n", "f 1\nxy\n", "f 5\nx\n", "f 1\nx", "f", "f  1\nx\n"} {
		if _, err := ParseEntries([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
