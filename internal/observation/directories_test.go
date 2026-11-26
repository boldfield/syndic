package observation

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

type dirFixture struct {
	j      *journal.Store
	turn   string
	author string
}

func newDirFixture(tb testing.TB) dirFixture {
	tb.Helper()
	db, err := journal.Open(filepath.Join(tb.TempDir(), "j.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	f := dirFixture{j: journal.NewStore(db)}
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

func TestParseEntries(t *testing.T) {
	entries := []Entry{
		{Type: "f", Name: "file.txt"},
		{Type: "d", Name: "subdir"},
		{Type: "l", Name: "link"},
	}

	// Frame the entries (this sorts them by name)
	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	// Parse them back (they come out sorted by name: file.txt, link, subdir)
	parsed, err := ParseEntries(frameData)
	if err != nil {
		t.Fatalf("ParseEntries: %v", err)
	}

	if len(parsed) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(parsed), len(entries))
	}
	expectedOrder := []Entry{
		{Type: "f", Name: "file.txt"},
		{Type: "l", Name: "link"},
		{Type: "d", Name: "subdir"},
	}
	for i, e := range parsed {
		if e.Type != expectedOrder[i].Type || e.Name != expectedOrder[i].Name {
			t.Fatalf("entry %d: got %+v, want %+v", i, e, expectedOrder[i])
		}
	}
}

func TestParseEntriesWithSpecialCharacters(t *testing.T) {
	entries := []Entry{
		{Type: "f", Name: "file\nwith\nnewlines"},
		{Type: "d", Name: "dir with spaces"},
		{Type: "f", Name: "binary\x00\x01\x02"},
	}

	frameData, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	parsed, err := ParseEntries(frameData)
	if err != nil {
		t.Fatalf("ParseEntries: %v", err)
	}

	if len(parsed) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(parsed), len(entries))
	}
	// After sorting by name: binary..., dir with spaces, file\nwith\nnewlines
	expectedOrder := []Entry{
		{Type: "f", Name: "binary\x00\x01\x02"},
		{Type: "d", Name: "dir with spaces"},
		{Type: "f", Name: "file\nwith\nnewlines"},
	}
	for i, e := range parsed {
		if e.Type != expectedOrder[i].Type || e.Name != expectedOrder[i].Name {
			t.Fatalf("entry %d: got %+v, want %+v", i, e, expectedOrder[i])
		}
	}
}

func TestReconcileDirectoryObservation_RemovesAbsentEntries(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, observe that "dir/deleted.txt" is absent
	if _, err := s.Observe(f.turn, f.author, "dir/deleted.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	}); err != nil {
		t.Fatalf("first observe (absent): %v", err)
	}

	// Now reconcile a directory listing that includes "deleted.txt"
	entries := []Entry{
		{Type: "f", Name: "file.txt"},
		{Type: "f", Name: "deleted.txt"},
	}

	reconciled, err := ReconcileDirectoryObservation(f.turn, "dir", entries, f.j)
	if err != nil {
		t.Fatalf("ReconcileDirectoryObservation: %v", err)
	}

	if len(reconciled) != 1 {
		t.Fatalf("got %d entries, want 1", len(reconciled))
	}
	if reconciled[0].Name != "file.txt" {
		t.Fatalf("got entry %s, want file.txt", reconciled[0].Name)
	}
}

func TestReconcileDirectoryObservation_PreservesOtherEntries(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Observe that "dir/absent.txt" is absent
	if _, err := s.Observe(f.turn, f.author, "dir/absent.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	}); err != nil {
		t.Fatalf("observe absent: %v", err)
	}

	// Reconcile a listing without the absent file
	entries := []Entry{
		{Type: "f", Name: "a.txt"},
		{Type: "d", Name: "b"},
		{Type: "f", Name: "c.txt"},
	}

	reconciled, err := ReconcileDirectoryObservation(f.turn, "dir", entries, f.j)
	if err != nil {
		t.Fatalf("ReconcileDirectoryObservation: %v", err)
	}

	if len(reconciled) != 3 {
		t.Fatalf("got %d entries, want 3", len(reconciled))
	}
}

func TestReconcileDirectoryObservation_IgnoresAbsenceFromOtherDirs(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Observe that "otherdir/file.txt" is absent
	if _, err := s.Observe(f.turn, f.author, "otherdir/file.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	}); err != nil {
		t.Fatalf("observe absent in other dir: %v", err)
	}

	// Reconcile a listing in a different directory
	entries := []Entry{
		{Type: "f", Name: "file.txt"},
	}

	reconciled, err := ReconcileDirectoryObservation(f.turn, "thisdir", entries, f.j)
	if err != nil {
		t.Fatalf("ReconcileDirectoryObservation: %v", err)
	}

	// The entry should be preserved since the absence was in a different directory
	if len(reconciled) != 1 || reconciled[0].Name != "file.txt" {
		t.Fatalf("got %+v, want [file.txt]", reconciled)
	}
}

func TestCheckFileConflict_DetectsTypeChange(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing where "target" is a directory
	dirEntries := []Entry{
		{Type: "d", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now check if reading that path as a file (type "f") creates a conflict
	err = CheckFileConflict(f.turn, "dir/target", "f", f.j)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("got error %v, want ErrSnapshotConflict", err)
	}
}

func TestCheckFileConflict_AllowsMatchingType(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Record a directory listing where "target" is a file
	dirEntries := []Entry{
		{Type: "f", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Check if reading that path as a file (type "f") succeeds
	err = CheckFileConflict(f.turn, "dir/target", "f", f.j)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckFileConflict_UnlistedPathsConflict(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Record a directory listing with one entry
	dirEntries := []Entry{
		{Type: "f", Name: "listed.txt"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Check if reading a path that wasn't in the listing conflicts
	// (must conflict because it's not in the cached listing)
	err = CheckFileConflict(f.turn, "dir/unlisted.txt", "f", f.j)
	if err == nil {
		t.Fatal("expected snapshot_conflict for unlisted path, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}
}

func TestCheckFileConflict_IgnoresOtherDirectories(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Record a directory listing in "otherdir"
	dirEntries := []Entry{
		{Type: "d", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "otherdir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe otherdir: %v", err)
	}

	// Check if reading "thisdir/target" as a file succeeds
	// (no conflict because the listing was in a different directory)
	err = CheckFileConflict(f.turn, "thisdir/target", "f", f.j)
	if err != nil {
		t.Fatalf("unexpected error for different directory: %v", err)
	}
}

func TestReconcileAndCheckRoundTrip(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// Scenario: file A is present, file B was absent
	if _, err := s.Observe(f.turn, f.author, "work/a.txt", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("content A"), nil
	}); err != nil {
		t.Fatalf("observe a.txt: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "work/b.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	}); err != nil {
		t.Fatalf("observe b.txt absent: %v", err)
	}

	// Now list the directory with both A and B
	entries := []Entry{
		{Type: "f", Name: "a.txt"},
		{Type: "f", Name: "b.txt"},
	}

	// Reconciliation should remove B (because it was observed as absent)
	reconciled, err := ReconcileDirectoryObservation(f.turn, "work", entries, f.j)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(reconciled) != 1 || reconciled[0].Name != "a.txt" {
		t.Fatalf("reconciled entries: got %+v, want [a.txt]", reconciled)
	}

	// Record the reconciled listing
	frameData, err := FrameEntries(reconciled)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "work", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe work directory: %v", err)
	}

	// Check if reading a.txt (type f) succeeds - it's in the listing with matching type
	if err := CheckFileConflict(f.turn, "work/a.txt", "f", f.j); err != nil {
		t.Fatalf("check a.txt: %v", err)
	}

	// Check if reading a.txt (type d) fails - it's in the listing as "f" but we're checking as "d"
	if err := CheckFileConflict(f.turn, "work/a.txt", "d", f.j); err == nil {
		t.Fatal("expected conflict for a.txt type change from f to d")
	}
}

func TestFrameEntries(t *testing.T) {
	entries := []Entry{
		{Type: "f", Name: "z.txt"},
		{Type: "d", Name: "a_dir"},
		{Type: "f", Name: "m.txt"},
	}

	data, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	// Should be sorted by name
	parsed, err := ParseEntries(data)
	if err != nil {
		t.Fatalf("ParseEntries: %v", err)
	}

	expected := []string{"a_dir", "m.txt", "z.txt"}
	for i, name := range expected {
		if parsed[i].Name != name {
			t.Fatalf("entry %d: got %s, want %s", i, parsed[i].Name, name)
		}
	}
}

func TestEntriesHash(t *testing.T) {
	entries := []Entry{
		{Type: "f", Name: "file.txt"},
		{Type: "d", Name: "dir"},
	}

	hash1, err := EntriesHash(entries)
	if err != nil {
		t.Fatalf("EntriesHash first call: %v", err)
	}

	// Same entries in different order should produce the same hash
	entries2 := []Entry{
		{Type: "d", Name: "dir"},
		{Type: "f", Name: "file.txt"},
	}

	hash2, err := EntriesHash(entries2)
	if err != nil {
		t.Fatalf("EntriesHash second call: %v", err)
	}

	if hash1 != hash2 {
		t.Fatalf("hashes differ for same entries: %s vs %s", hash1, hash2)
	}

	// Verify it's a valid SHA256 hex string
	if len(hash1) != 64 {
		t.Fatalf("hash length: got %d, want 64", len(hash1))
	}
	if _, err := hex.DecodeString(hash1); err != nil {
		t.Fatalf("hash not valid hex: %v", err)
	}
}

// Test: Known file retained in later parent view.
// When a file is observed, then its parent directory is listed,
// the file must appear in the listing even if the live directory doesn't have it.
func TestReconcileDirectoryObservation_RetainsKnownFiles(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, observe that "work/a.txt" exists
	if _, err := s.Observe(f.turn, f.author, "work/a.txt", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("content"), nil
	}); err != nil {
		t.Fatalf("observe a.txt: %v", err)
	}

	// Now reconcile a directory listing that does not include "a.txt"
	// (simulating live directory missing the file)
	entries := []Entry{
		{Type: "f", Name: "b.txt"},
	}

	reconciled, err := ReconcileDirectoryObservation(f.turn, "work", entries, f.j)
	if err != nil {
		t.Fatalf("ReconcileDirectoryObservation: %v", err)
	}

	// The reconciled listing should include both b.txt and a.txt
	if len(reconciled) != 2 {
		t.Fatalf("got %d entries, want 2", len(reconciled))
	}
	names := make(map[string]bool)
	for _, e := range reconciled {
		names[e.Name] = true
	}
	if !names["a.txt"] || !names["b.txt"] {
		t.Fatalf("reconciled entries missing a.txt or b.txt: %+v", reconciled)
	}
}

// Test: Deleted file conflict detection.
// When a file is listed in a directory, and later we try to read it but it's deleted,
// CheckFileConflict should detect it.
func TestCheckFileConflict_Deleted(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing where "target" is a file
	dirEntries := []Entry{
		{Type: "f", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now check if reading that path when it's deleted creates a conflict
	// (empty string means the file is not found)
	err = CheckFileConflict(f.turn, "dir/target", "", f.j)
	if err == nil {
		t.Fatal("expected conflict error for deleted file, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}
}

// Test: Type-changed file conflict detection.
// When a file is listed as "f" in a directory, but later we try to read it as "d",
// we should get a snapshot_conflict error.
func TestObserveFileConflict_TypeChanged(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing where "target" is a directory
	dirEntries := []Entry{
		{Type: "d", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe it as a file (type changed from d to f)
	_, err = s.Observe(f.turn, f.author, "dir/target", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("content"), nil
	})
	// Should get a snapshot_conflict error
	if err == nil {
		t.Fatal("expected snapshot_conflict error for type change, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}
}

// Test: Root-level file (parent is "").
func TestReconcileDirectoryObservation_RootLevel(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, observe that a root-level file is absent
	if _, err := s.Observe(f.turn, f.author, "root.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	}); err != nil {
		t.Fatalf("observe absence: %v", err)
	}

	// Now reconcile a root directory listing that includes "root.txt"
	entries := []Entry{
		{Type: "f", Name: "root.txt"},
		{Type: "d", Name: "dir"},
	}

	reconciled, err := ReconcileDirectoryObservation(f.turn, "", entries, f.j)
	if err != nil {
		t.Fatalf("ReconcileDirectoryObservation: %v", err)
	}

	// The reconciled listing should exclude "root.txt" (which is marked absent)
	if len(reconciled) != 1 {
		t.Fatalf("got %d entries, want 1", len(reconciled))
	}
	if reconciled[0].Name != "dir" {
		t.Fatalf("got entry %s, want dir", reconciled[0].Name)
	}
}

// Test: Cached listing excludes later live file.
// When a directory is listed without a file, and later a file appears in the live tree
// with that name, reading it should return snapshot_conflict (cached listing behavior).
func TestCheckFileConflict_UnlistedFile(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing without "unlisted.txt"
	dirEntries := []Entry{
		{Type: "f", Name: "listed.txt"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now check if reading "unlisted.txt" (which is not in the listing) conflicts
	// It should conflict because it was not in the cached listing but exists in live tree
	err = CheckFileConflict(f.turn, "dir/unlisted.txt", "f", f.j)
	if err == nil {
		t.Fatal("expected snapshot_conflict error for unlisted file, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}
}

func BenchmarkParseEntries(b *testing.B) {
	entries := []Entry{}
	for i := 0; i < 100; i++ {
		entries = append(entries, Entry{
			Type: "f",
			Name: fmt.Sprintf("file%d.txt", i),
		})
	}

	frameData, err := FrameEntries(entries)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseEntries(frameData); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReconcileDirectoryObservation(b *testing.B) {
	f := newDirFixture(b)
	s := NewStore(f.j)

	// Setup: observe some absent files
	for i := 0; i < 10; i++ {
		if _, err := s.Observe(f.turn, f.author, fmt.Sprintf("dir/absent_%d.txt", i), func() (string, []byte, error) {
			return journal.ObservationAbsent, nil, nil
		}); err != nil {
			b.Fatal(err)
		}
	}

	// Create a large directory listing
	entries := []Entry{}
	for i := 0; i < 100; i++ {
		entries = append(entries, Entry{
			Type: "f",
			Name: fmt.Sprintf("file%d.txt", i),
		})
		if i < 10 {
			entries = append(entries, Entry{
				Type: "f",
				Name: fmt.Sprintf("absent_%d.txt", i),
			})
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ReconcileDirectoryObservation(f.turn, "dir", entries, f.j); err != nil {
			b.Fatal(err)
		}
	}
}

// End-to-end test: Observe a file not in cached listing returns snapshot_conflict and records nothing.
func TestObserve_UnlistedFileTriggersCacheConflict(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing without "new.txt"
	dirEntries := []Entry{
		{Type: "f", Name: "existing.txt"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "new.txt" (not in the listing) through Store.Observe
	_, err = s.Observe(f.turn, f.author, "dir/new.txt", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("new content"), nil
	})

	// Should get snapshot_conflict error
	if err == nil {
		t.Fatal("expected snapshot_conflict error, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Verify nothing was recorded for "dir/new.txt"
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == "dir/new.txt" {
			t.Fatalf("unexpected observation recorded for dir/new.txt: %+v", o)
		}
	}
}

// End-to-end test: Observe a deleted listed file returns snapshot_conflict and records nothing.
func TestObserve_DeletedListedFileTriggersCacheConflict(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing with "file.txt"
	dirEntries := []Entry{
		{Type: "f", Name: "file.txt"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "file.txt" as absent through Store.Observe
	_, err = s.Observe(f.turn, f.author, "dir/file.txt", func() (string, []byte, error) {
		return journal.ObservationAbsent, nil, nil
	})

	// Should get snapshot_conflict error
	if err == nil {
		t.Fatal("expected snapshot_conflict error, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Verify nothing was recorded for "dir/file.txt"
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == "dir/file.txt" {
			t.Fatalf("unexpected observation recorded for dir/file.txt: %+v", o)
		}
	}
}

// End-to-end test: Observe a type-changed listed file through Observe.
func TestObserve_TypeChangedListedFileTriggersCacheConflict(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a directory listing with "target" as a directory
	dirEntries := []Entry{
		{Type: "d", Name: "target"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	if _, err := s.Observe(f.turn, f.author, "dir", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	}); err != nil {
		t.Fatalf("observe directory: %v", err)
	}

	// Now try to observe "dir/target" as a file (type changed from d to f)
	_, err = s.Observe(f.turn, f.author, "dir/target", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("file content"), nil
	})

	// Should get snapshot_conflict error
	if err == nil {
		t.Fatal("expected snapshot_conflict error for type change, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Verify nothing was recorded for "dir/target"
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Path == "dir/target" {
			t.Fatalf("unexpected observation recorded for dir/target: %+v", o)
		}
	}
}

// End-to-end test: Cached listing properly excludes later live file.
func TestObserve_CachedListingExcludesLaterLiveFile(t *testing.T) {
	f := newDirFixture(t)
	s := NewStore(f.j)

	// First, record a listing of "work" with only [a]
	dirEntries := []Entry{
		{Type: "f", Name: "a"},
	}
	frameData, err := FrameEntries(dirEntries)
	if err != nil {
		t.Fatalf("FrameEntries: %v", err)
	}

	listingObs, err := s.Observe(f.turn, f.author, "work", func() (string, []byte, error) {
		return journal.ObservationListing, frameData, nil
	})
	if err != nil {
		t.Fatalf("observe listing: %v", err)
	}

	// Verify the listing was recorded
	if listingObs.Kind != journal.ObservationListing {
		t.Fatalf("expected listing, got %s", listingObs.Kind)
	}

	// Now try to record "work/new" (not in the listing)
	_, err = s.Observe(f.turn, f.author, "work/new", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("new"), nil
	})

	// Should fail with snapshot_conflict because "new" is not in the cached listing
	if err == nil {
		t.Fatal("expected snapshot_conflict for unlisted file, got nil")
	}
	if !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected ErrSnapshotConflict, got %v", err)
	}

	// Try to observe "work/a" (is in the listing) - should succeed
	fileObs, err := s.Observe(f.turn, f.author, "work/a", func() (string, []byte, error) {
		return journal.ObservationFile, []byte("content a"), nil
	})
	if err != nil {
		t.Fatalf("observe file in listing: %v", err)
	}
	if fileObs.Kind != journal.ObservationFile {
		t.Fatalf("expected file observation, got %s", fileObs.Kind)
	}
}
