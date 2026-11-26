package observation

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func testStore(tb testing.TB) (*Store, *journal.Store, string, string) {
	tb.Helper()
	dir := tb.TempDir()
	dbPath := dir + "/test.db"
	db, err := journal.Open(dbPath)
	if err != nil {
		tb.Fatalf("open journal: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })

	j := journal.NewStore(db)

	// Create a run and turn so we can record observations.
	runID, _, err := j.CreateRun([]byte("{}"))
	if err != nil {
		tb.Fatalf("create run: %v", err)
	}

	turnID, err := j.CreateTurn(runID, "review", nil)
	if err != nil {
		tb.Fatalf("create turn: %v", err)
	}

	// Create an attempt for the turn.
	attemptID, err := j.BeginAttempt(turnID, "test-author", "revise", 0, nil, "")
	if err != nil {
		tb.Fatalf("begin attempt: %v", err)
	}

	return NewStore(j), j, turnID, attemptID
}

func TestObserveFile(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record a file observation with content.
	content := []byte("hello world")
	record, err := store.Observe(turnID, attemptID, "foo.txt", func() (byte, []byte, error) {
		return KindFile, content, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if record.Kind != KindFile {
		t.Errorf("kind: got %q, want %q", record.Kind, KindFile)
	}
	if record.Path != "foo.txt" {
		t.Errorf("path: got %q, want %q", record.Path, "foo.txt")
	}
	if record.Value == "" {
		t.Errorf("value (content hash) must not be empty")
	}
	if record.AttemptID != attemptID {
		t.Errorf("attempt ID: got %q, want %q", record.AttemptID, attemptID)
	}

	// Verify the hash is valid.
	if !IsSHA256Hex(record.Value) {
		t.Errorf("value must be a 64-character hex hash, got %q", record.Value)
	}
}

func TestObserveAbsence(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record an absence observation with no content.
	record, err := store.Observe(turnID, attemptID, "missing.txt", func() (byte, []byte, error) {
		return KindAbsent, nil, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if record.Kind != KindAbsent {
		t.Errorf("kind: got %q, want %q", record.Kind, KindAbsent)
	}
	if record.Path != "missing.txt" {
		t.Errorf("path: got %q, want %q", record.Path, "missing.txt")
	}
	if record.Value != "" {
		t.Errorf("value must be empty for absence, got %q", record.Value)
	}
}

func TestObserveDirectory(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record a directory observation.
	entries, _ := FrameEntries([]Entry{
		{Type: "f", Name: "file1.txt"},
		{Type: "f", Name: "file2.txt"},
		{Type: "d", Name: "subdir"},
	})

	record, err := store.Observe(turnID, attemptID, "dir", func() (byte, []byte, error) {
		return KindDir, entries, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if record.Kind != KindDir {
		t.Errorf("kind: got %q, want %q", record.Kind, KindDir)
	}
	if !IsSHA256Hex(record.Value) {
		t.Errorf("directory listing hash must be a 64-character hex hash, got %q", record.Value)
	}
}

func TestRepeatedReadIgnoresLiveEdits(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	// Record a file observation.
	content := []byte("original")
	record1, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, content, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	// Try to observe the same path again - should return the recorded observation, not an error.
	record2, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, []byte("modified"), nil
	})
	if err != nil {
		t.Fatalf("repeated observe should return record, got error: %v", err)
	}

	// Verify that the second call returned the same recorded value.
	if record2.Value != record1.Value {
		t.Errorf("repeated read should return same value: %q != %q", record2.Value, record1.Value)
	}

	// Verify the stored observation is the original.
	obs, err := store.ObservationAt(turnID, "file.txt")
	if err != nil {
		t.Fatalf("observation at: %v", err)
	}

	if obs.Value != record1.Value {
		t.Errorf("observation value changed: %q != %q", obs.Value, record1.Value)
	}

	// Verify the content in the store is still the original.
	retrievedContent, err := j.Content(obs.Value)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	if !bytes.Equal(retrievedContent, content) {
		t.Errorf("content changed: %q != %q", retrievedContent, content)
	}
}

func TestObservedAbsenceRemainsAbsent(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record an absence observation.
	record, err := store.Observe(turnID, attemptID, "missing.txt", func() (byte, []byte, error) {
		return KindAbsent, nil, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	if record.Value != "" {
		t.Errorf("absence should have empty value")
	}

	// Try to observe the same path with a different kind - should return the recorded absence, not record as file.
	record2, err := store.Observe(turnID, attemptID, "missing.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content"), nil
	})
	if err != nil {
		t.Fatalf("repeated observe should return record, got error: %v", err)
	}

	// Verify it still reads as absent (the recorded kind should be preserved).
	if record2.Kind != record.Kind {
		t.Errorf("kind should remain absent, got %q", record2.Kind)
	}

	// Verify it still reads as absent from the store.
	obs, err := store.ObservationAt(turnID, "missing.txt")
	if err != nil {
		t.Fatalf("observation at: %v", err)
	}
	if obs.Kind != KindAbsent {
		t.Errorf("kind should remain absent, got %q", obs.Kind)
	}
	if obs.Value != "" {
		t.Errorf("absence should remain empty")
	}
}

func TestConcurrentFirstTouches(t *testing.T) {
	store, j, turnID, _ := testStore(t)

	// Create two attempts for concurrent readers.
	var attemptIDs [2]string
	for i := 0; i < 2; i++ {
		attemptID, err := j.BeginAttempt(turnID, "test-author", "revise", 1+i, nil, "")
		if err != nil {
			t.Fatalf("begin attempt: %v", err)
		}
		attemptIDs[i] = attemptID
	}

	// Track how many times the reader is invoked.
	var readerInvoked atomic.Int32

	// Two goroutines try to observe the same path concurrently.
	var wg sync.WaitGroup
	var records [2]Record
	var errs [2]error

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int, attemptID string) {
			defer wg.Done()
			record, err := store.Observe(turnID, attemptID, "concurrent.txt", func() (byte, []byte, error) {
				readerInvoked.Add(1)
				return KindFile, []byte("content from attempt"), nil
			})
			records[idx] = record
			errs[idx] = err
		}(i, attemptIDs[i])
	}
	wg.Wait()

	// Both should succeed without error (one reads, one gets the recorded observation).
	for i, err := range errs {
		if err != nil {
			t.Errorf("observe[%d] failed: %v", i, err)
		}
	}

	// Both should get the same record value.
	if records[0].Value != records[1].Value {
		t.Errorf("concurrent touches got different values: %q != %q", records[0].Value, records[1].Value)
	}

	// The reader should have been invoked exactly once.
	if readerInvoked.Load() != 1 {
		t.Errorf("reader invoked %d times, want 1", readerInvoked.Load())
	}

	// Verify the observation exists.
	obs, err := store.ObservationAt(turnID, "concurrent.txt")
	if err != nil {
		t.Fatalf("observation at: %v", err)
	}

	if obs.Value != records[0].Value {
		t.Errorf("recorded value mismatch: %q != %q", obs.Value, records[0].Value)
	}
}

func TestSnapshotVersionIncrement(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Initial version should be 0.
	v, err := store.SnapshotVersion(turnID)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	if v != 0 {
		t.Errorf("initial version: got %d, want 0", v)
	}

	// Record first observation - version should be 1.
	rec1, err := store.Observe(turnID, attemptID, "file1.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content1"), nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	v, err = store.SnapshotVersion(turnID)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	if v != 1 {
		t.Errorf("after first observation: got version %d, want 1", v)
	}
	if rec1.SnapshotVersion != 1 {
		t.Errorf("record snapshot version: got %d, want 1", rec1.SnapshotVersion)
	}

	// Record second observation - version should be 2.
	rec2, err := store.Observe(turnID, attemptID, "file2.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content2"), nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	v, err = store.SnapshotVersion(turnID)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	if v != 2 {
		t.Errorf("after second observation: got version %d, want 2", v)
	}
	if rec2.SnapshotVersion != 2 {
		t.Errorf("record snapshot version: got %d, want 2", rec2.SnapshotVersion)
	}
}

func TestObservationsBoundToOriginalAttempt(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record an observation.
	origRec, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content"), nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	// Retrieve observations.
	obs, err := store.Observations(turnID)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}

	if len(obs) != 1 {
		t.Errorf("expected 1 observation, got %d", len(obs))
	}

	if obs[0].Path != "file.txt" {
		t.Errorf("path: got %q, want %q", obs[0].Path, "file.txt")
	}

	if obs[0].Kind != KindFile {
		t.Errorf("kind: got %q, want %q", obs[0].Kind, KindFile)
	}

	if obs[0].AttemptID != attemptID {
		t.Errorf("attempt ID: got %q, want %q", obs[0].AttemptID, attemptID)
	}

	if obs[0].SnapshotVersion != origRec.SnapshotVersion {
		t.Errorf("snapshot version: got %d, want %d", obs[0].SnapshotVersion, origRec.SnapshotVersion)
	}
}

func TestMultipleObservationsOrdered(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	paths := []string{"c.txt", "a.txt", "b.txt"}
	for _, path := range paths {
		_, err := store.Observe(turnID, attemptID, path, func() (byte, []byte, error) {
			return KindFile, []byte("content"), nil
		})
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
	}

	obs, err := store.Observations(turnID)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}

	if len(obs) != 3 {
		t.Errorf("expected 3 observations, got %d", len(obs))
	}

	// Should be sorted bytewise by path.
	expected := []string{"a.txt", "b.txt", "c.txt"}
	for i, exp := range expected {
		if obs[i].Path != exp {
			t.Errorf("obs[%d] path: got %q, want %q", i, obs[i].Path, exp)
		}
	}
}

func TestObservationAtNotFound(t *testing.T) {
	store, _, turnID, _ := testStore(t)

	record, err := store.ObservationAt(turnID, "nonexistent.txt")
	if err == nil {
		t.Errorf("observation at nonexistent path should fail, got %+v", record)
	}
}

func TestFreshStoreReturnsRecordedObservation(t *testing.T) {
	store1, j, turnID, attemptID := testStore(t)

	// First Store records an observation.
	content := []byte("first observation")
	record1, err := store1.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, content, nil
	})
	if err != nil {
		t.Fatalf("first store observe: %v", err)
	}

	// Create a fresh Store instance on the same journal.
	store2 := NewStore(j)

	// Track whether the reader is called on the fresh Store.
	var readerCalled bool

	// Fresh Store should return the recorded observation without calling the reader.
	record2, err := store2.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		readerCalled = true
		return KindFile, []byte("different content"), nil
	})
	if err != nil {
		t.Fatalf("fresh store observe: %v", err)
	}

	// Fresh Store should have returned the same recorded value.
	if record2.Value != record1.Value {
		t.Errorf("fresh store returned different value: %q != %q", record2.Value, record1.Value)
	}

	// The reader should NOT have been called (the observation was served from the record).
	if readerCalled {
		t.Errorf("fresh store called the reader, but it should have used the recorded observation")
	}
}

func TestBinaryPathsAndContent(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	// Path with embedded null bytes and newlines.
	binaryPath := "file\x00with\nnulls"
	binaryContent := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}

	_, err := store.Observe(turnID, attemptID, binaryPath, func() (byte, []byte, error) {
		return KindFile, binaryContent, nil
	})
	if err != nil {
		t.Fatalf("observe with binary path: %v", err)
	}

	// Verify it can be retrieved.
	obs, err := store.ObservationAt(turnID, binaryPath)
	if err != nil {
		t.Fatalf("observation at: %v", err)
	}

	if obs.Path != binaryPath {
		t.Errorf("binary path: got %q, want %q", obs.Path, binaryPath)
	}

	// Verify content is preserved.
	retrievedContent, err := j.Content(obs.Value)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	if !bytes.Equal(retrievedContent, binaryContent) {
		t.Errorf("binary content mismatch")
	}
}

func TestContentStorage(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	content := []byte("test file content")
	record, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, content, nil
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	// Retrieve the content by hash.
	retrieved, err := j.Content(record.Value)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}

	if !bytes.Equal(retrieved, content) {
		t.Errorf("content mismatch: got %q, want %q", retrieved, content)
	}
}

func TestInvalidKindRejected(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Test that invalid kinds are rejected.
	record, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return 'x', []byte("content"), nil
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unknown observation kind")) {
		t.Errorf("expected unknown kind error, got %v", err)
	}
	if record.Path != "" {
		t.Errorf("should not have created record for invalid kind, got %+v", record)
	}

	// A subsequent observe with valid kind should work normally.
	record2, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content"), nil
	})
	if err != nil {
		t.Fatalf("observe after invalid kind error: %v", err)
	}
	if record2.Path != "file.txt" {
		t.Errorf("path: got %q, want file.txt", record2.Path)
	}
}

func TestReaderErrorHandling(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Test that errors from the reader are propagated.
	testErr := fmt.Errorf("reader error")
	record, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return 0, nil, testErr
	})
	if err != testErr {
		t.Errorf("expected reader error %v, got %v", testErr, err)
	}
	if record.Path != "" {
		t.Errorf("should not have created record on reader error, got %+v", record)
	}

	// Subsequent observe with successful reader should work normally.
	content := []byte("content")
	record2, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, content, nil
	})
	if err != nil {
		t.Fatalf("observe after reader error: %v", err)
	}
	if record2.Path != "file.txt" {
		t.Errorf("path: got %q, want file.txt", record2.Path)
	}
}

func TestReaderCanTouchJournal(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	// Test that the reader can safely call journal methods without deadlocking.
	// This verifies the reader runs outside the journal transaction.
	record, err := store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		// Reader touches the journal - this should not deadlock.
		err := j.Tx(func(tx *journal.Tx) error {
			_, err := tx.Observations(turnID)
			return err
		})
		if err != nil {
			return 0, nil, err
		}
		return KindFile, []byte("content"), nil
	})
	if err != nil {
		t.Fatalf("observe with journal-touching reader: %v", err)
	}
	if record.Path != "file.txt" {
		t.Errorf("path: got %q, want file.txt", record.Path)
	}
}

func TestAbsenceWithContentRejected(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Test that an absence with content is rejected.
	record, err := store.Observe(turnID, attemptID, "missing.txt", func() (byte, []byte, error) {
		return KindAbsent, []byte("unwanted content"), nil
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("absence observation must not have content")) {
		t.Errorf("expected absence-with-content error, got %v", err)
	}
	if record.Path != "" {
		t.Errorf("should not have created record for invalid absence, got %+v", record)
	}

	// A subsequent observe with valid absence should work normally.
	record2, err := store.Observe(turnID, attemptID, "missing.txt", func() (byte, []byte, error) {
		return KindAbsent, nil, nil
	})
	if err != nil {
		t.Fatalf("observe after absence-with-content error: %v", err)
	}
	if record2.Kind != KindAbsent {
		t.Errorf("kind: got %q, want %q", record2.Kind, KindAbsent)
	}
}

func BenchmarkObserve(b *testing.B) {
	store, _, turnID, attemptID := testStore(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := "file" + fmt.Sprintf("%03d", i%100) + ".txt"
		_, _ = store.Observe(turnID, attemptID, path, func() (byte, []byte, error) {
			return KindFile, []byte("benchmark"), nil
		})
	}
}

func BenchmarkObserveRepeatedly(b *testing.B) {
	store, _, turnID, attemptID := testStore(b)

	// Pre-observe a path.
	_, _ = store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
		return KindFile, []byte("content"), nil
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Repeated observe of same path should be fast (get from cache).
		_, _ = store.Observe(turnID, attemptID, "file.txt", func() (byte, []byte, error) {
			return KindFile, []byte("x"), nil
		})
	}
}
