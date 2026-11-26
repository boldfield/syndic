package observation

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

type fixture struct {
	j      *journal.Store
	turn   string
	author string
}

func newFixture(tb testing.TB) fixture {
	tb.Helper()
	db, err := journal.Open(filepath.Join(tb.TempDir(), "j.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	f := fixture{j: journal.NewStore(db)}
	err = f.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		f.author, err = tx.BeginAttempt(f.turn, "author", "author", 1, nil, "")
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	return f
}

func heldLocks(j *journal.Store) int {
	turnLocks.mu.Lock()
	defer turnLocks.mu.Unlock()
	n := 0
	for k := range turnLocks.m {
		if k.j == j {
			n++
		}
	}
	return n
}

func TestConcurrentFirstTouchAcrossStoresReadsOnce(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	n := 8
	if testing.Short() {
		n = 4
	}
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := NewStore(f.j).Observe(f.turn, f.author, "a.txt", func() (string, []byte, error) {
				reads.Add(1)
				time.Sleep(5 * time.Millisecond)
				return journal.ObservationFile, []byte("A"), nil
			})
			if err != nil {
				t.Error(err)
			}
			ids[i] = o.ID
		}()
	}
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("live reads = %d, want 1", reads.Load())
	}
	for _, id := range ids {
		if id != ids[0] || id == "" {
			t.Fatalf("ids differ: %v", ids)
		}
	}
	if f.count(t) != 1 {
		t.Fatalf("records = %d", f.count(t))
	}
}

func fileReader(n *atomic.Int32, body string) Reader {
	return func() (string, []byte, error) {
		n.Add(1)
		return journal.ObservationFile, []byte(body), nil
	}
}

func (f fixture) count(t *testing.T) int {
	t.Helper()
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	return len(obs)
}

func TestConcurrentFirstTouchReadsOnce(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var reads atomic.Int32
	n := 16
	if testing.Short() {
		n = 4
	}
	got := make([]journal.Observation, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s.Observe(f.turn, f.author, "a.txt", func() (string, []byte, error) {
				reads.Add(1)
				time.Sleep(5 * time.Millisecond)
				return journal.ObservationFile, []byte("A"), nil
			})
			if err != nil {
				t.Error(err)
			}
			got[i] = o
		}()
	}
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("live reads = %d, want 1", reads.Load())
	}
	for _, o := range got {
		if o.ID != got[0].ID || o.ID == "" {
			t.Fatalf("observations differ: %+v vs %+v", o, got[0])
		}
	}
	if f.count(t) != 1 {
		t.Fatalf("records = %d", f.count(t))
	}
	if n := heldLocks(f.j); n != 0 {
		t.Fatalf("turn locks retained: %d", n)
	}
}

func TestRepeatedReadIgnoresLiveEdits(t *testing.T) {
	f := newFixture(t)
	var reads atomic.Int32
	s := NewStore(f.j)
	first, err := s.Observe(f.turn, f.author, "a.txt", fileReader(&reads, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	// Same Store, then a fresh Store on the same journal: the live tree has
	// changed but the record is served.
	for _, st := range []*Store{s, NewStore(f.j)} {
		again, err := st.Observe(f.turn, f.author, "a.txt", fileReader(&reads, "v2"))
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || again.ContentSHA256 != first.ContentSHA256 {
			t.Fatalf("got %+v, want %+v", again, first)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("live reads = %d, want 1", reads.Load())
	}
}

func TestObservedAbsenceRemainsAbsent(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var reads atomic.Int32
	first, err := s.Observe(f.turn, f.author, "new.go", func() (string, []byte, error) {
		reads.Add(1)
		return journal.ObservationAbsent, nil, nil
	})
	if err != nil || first.Kind != journal.ObservationAbsent {
		t.Fatalf("first = %+v, %v", first, err)
	}
	again, err := s.Observe(f.turn, f.author, "new.go", fileReader(&reads, "now exists"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != journal.ObservationAbsent || again.ID != first.ID || reads.Load() != 1 {
		t.Fatalf("again = %+v, reads %d", again, reads.Load())
	}
}

func TestObservationBoundToAttemptAndVersion(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var reads atomic.Int32
	var second string
	if err := f.j.Tx(func(tx *journal.Tx) (err error) {
		second, err = tx.BeginAttempt(f.turn, "author", "author", 1, nil, "")
		return
	}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Observe(f.turn, f.author, "a", fileReader(&reads, "A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Observe(f.turn, second, "b", fileReader(&reads, "B"))
	if err != nil {
		t.Fatal(err)
	}
	if a.AttemptID != f.author || a.SnapshotVersion != 1 || b.AttemptID != second || b.SnapshotVersion != 2 {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
	// A later touch from another attempt keeps the original binding.
	c, err := s.Observe(f.turn, second, "a", fileReader(&reads, "A2"))
	if err != nil || c.AttemptID != f.author {
		t.Fatalf("c = %+v, %v", c, err)
	}
}

func TestFailedObserveRecordsNothingAndRetries(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var reads atomic.Int32
	boom := errors.New("boom")

	if _, err := s.Observe(f.turn, f.author, "p", func() (string, []byte, error) { return "", nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("reader error = %v", err)
	}
	if _, err := s.Observe(f.turn, f.author, "p", func() (string, []byte, error) {
		return journal.ObservationAbsent, []byte("x"), nil
	}); err == nil {
		t.Fatal("absence with content accepted")
	}
	if _, err := s.Observe(f.turn, f.author, "p", func() (string, []byte, error) { return "z", nil, nil }); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// The real cause is reported, not a misleading not-observed error.
	_, err := s.Observe(f.turn, "no-such-attempt", "p", fileReader(&reads, "A"))
	if err == nil || errors.Is(err, journal.ErrNotObserved) || errors.Is(err, journal.ErrAlreadyObserved) {
		t.Fatalf("unknown attempt error = %v", err)
	}
	if f.count(t) != 0 {
		t.Fatalf("records = %d after failures", f.count(t))
	}
	o, err := s.Observe(f.turn, f.author, "p", fileReader(&reads, ""))
	if err != nil || o.Kind != journal.ObservationFile {
		t.Fatalf("retry = %+v, %v", o, err)
	}
	if n := heldLocks(f.j); n != 0 {
		t.Fatalf("turn locks retained: %d", n)
	}
}

func TestEmptyFileAcceptedWithNilContent(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	o, err := s.Observe(f.turn, f.author, "empty", func() (string, []byte, error) {
		return journal.ObservationFile, nil, nil
	})
	if err != nil || o.ContentSHA256 == "" {
		t.Fatalf("o = %+v, %v", o, err)
	}
}

func TestReaderCanTouchJournal(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	done := make(chan error, 1)
	go func() {
		_, err := s.Observe(f.turn, f.author, "a", func() (string, []byte, error) {
			if _, err := f.j.Observations(f.turn); err != nil {
				return "", nil, err
			}
			return journal.ObservationAbsent, nil, nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Observe deadlocked with a reader that touches the journal")
	}
}

func TestOtherTurnsDoNotSerialize(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var turn2, att2 string
	if err := f.j.Tx(func(tx *journal.Tx) (err error) {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if turn2, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		att2, err = tx.BeginAttempt(turn2, "author", "author", 1, nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = s.Observe(f.turn, f.author, "a", func() (string, []byte, error) {
			close(inside)
			<-release
			return journal.ObservationAbsent, nil, nil
		})
	}()
	<-inside
	done := make(chan error, 1)
	go func() {
		var n atomic.Int32
		_, err := s.Observe(turn2, att2, "a", fileReader(&n, "x"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second turn blocked behind first turn's lock")
	}
	close(release)
}

func BenchmarkObserve(b *testing.B) {
	f := newFixture(b)
	s := NewStore(f.j)
	var n atomic.Int32
	r := fileReader(&n, "x")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Observe(f.turn, f.author, "p", r); err != nil {
			b.Fatal(err)
		}
	}
}

// End-to-end test: Known file retained in later parent view.
// When a file is observed, then its parent directory is listed without it,
// the file must appear in the listing with its known type.
func TestObserve_KnownFileRetainedInLaterParentView(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)

	// First, observe that "work/a.txt" exists
	fileObs, err := s.Observe(f.turn, f.author, "work/a.txt", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("content"), nil
	})
	if err != nil {
		t.Fatalf("observe file: %v", err)
	}
	if fileObs.Kind != journal.ObservationFile {
		t.Fatalf("expected file observation, got %s", fileObs.Kind)
	}

	// Now list the parent directory without "a.txt"
	// (simulating live directory missing the file)
	entries := []Entry{
		{Type: "f", Name: "b.txt"},
	}
	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	listingObs, err := s.Observe(f.turn, f.author, "work", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	})
	if err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Parse the recorded listing by fetching content through journal
	var listingEntries []Entry
	err = f.j.Tx(func(tx *journal.Tx) error {
		content, err := tx.Content(listingObs.ContentSHA256)
		if err != nil {
			return err
		}
		listingEntries, err = ParseEntries(content)
		return err
	})
	if err != nil {
		t.Fatalf("get content: %v", err)
	}

	// The reconciled listing should include both b.txt and a.txt
	if len(listingEntries) != 2 {
		t.Fatalf("got %d entries, want 2 (should include both a.txt and b.txt)", len(listingEntries))
	}

	names := make(map[string]string)
	for _, e := range listingEntries {
		names[e.Name] = e.Type
	}

	if names["a.txt"] != "f" {
		t.Fatalf("a.txt not in listing or wrong type: %v", names)
	}
	if names["b.txt"] != "f" {
		t.Fatalf("b.txt not in listing or wrong type: %v", names)
	}
}

// End-to-end test: Cached listing excludes later live file through Observe.
func TestObserve_CachedListingExcludesUnlistedFile(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing without "new.txt"
	entries := []Entry{
		{Type: "f", Name: "existing.txt"},
	}
	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	_, err = s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	})
	if err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "dir/new.txt" (not in listing).
	// Should synthesize an absence without calling the reader.
	readerCalled := false
	obs, err := s.Observe(f.turn, f.author, "dir/new.txt", func() (string, []byte, error) {
		readerCalled = true
		t.Fatal("reader should not be called for unlisted file")
		return "", nil, nil
	})

	// Should succeed and return an absence
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.Kind != journal.ObservationAbsent {
		t.Fatalf("expected absence for unlisted file, got kind=%s", obs.Kind)
	}
	if readerCalled {
		t.Fatal("reader was called for unlisted file")
	}

	// Verify an absence was recorded for "dir/new.txt"
	allObs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range allObs {
		if o.Path == "dir/new.txt" {
			if o.Kind != journal.ObservationAbsent {
				t.Fatalf("expected absence for dir/new.txt, got kind=%s", o.Kind)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no observation recorded for dir/new.txt")
	}
}

// End-to-end test: Deleted listed file triggers snapshot_conflict through Observe.
func TestObserve_DeletedListedFileConflict(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing with "file.txt"
	entries := []Entry{
		{Type: "f", Name: "file.txt"},
	}
	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	_, err = s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	})
	if err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "dir/file.txt" as absent - should fail with snapshot_conflict
	_, err = s.Observe(f.turn, f.author, "dir/file.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	})

	if err == nil {
		t.Fatal("expected snapshot_conflict for deleted listed file, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Verify "dir/file.txt" was not recorded
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == "dir/file.txt" {
			t.Fatalf("dir/file.txt should not be recorded, but got: %+v", o)
		}
	}
}

// End-to-end test: Type-changed listed file triggers snapshot_conflict through Observe.
func TestObserve_TypeChangedListedFileConflict(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing with "target" as a directory
	entries := []Entry{
		{Type: "d", Name: "target"},
	}
	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	_, err = s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	})
	if err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "dir/target" as a file (type changed from d to f) - should fail with snapshot_conflict
	_, err = s.Observe(f.turn, f.author, "dir/target", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("file content"), nil
	})

	if err == nil {
		t.Fatal("expected snapshot_conflict for type-changed file, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Verify "dir/target" was not recorded
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == "dir/target" {
			t.Fatalf("dir/target should not be recorded, but got: %+v", o)
		}
	}
}
