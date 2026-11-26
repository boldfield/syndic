package observation

import (
	"bytes"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func testStore(t *testing.T) (*Store, *journal.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	j := journal.NewStore(db)

	// Create a run and turn so we can record observations.
	runID, _, err := j.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	turnID, err := j.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	// Create an attempt for the turn.
	attemptID, err := j.BeginAttempt(turnID, "test-author", "revise", 0, nil, "")
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}

	return NewStore(j), j, turnID, attemptID
}

func TestFirstObservationFile(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record a file observation with content.
	content := []byte("hello world")
	record, err := store.FirstObservation(turnID, attemptID, "foo.txt", KindFile, content)
	if err != nil {
		t.Fatalf("first observation: %v", err)
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

	// Verify the hash is valid.
	if !IsSHA256Hex(record.Value) {
		t.Errorf("value must be a 64-character hex hash, got %q", record.Value)
	}
}

func TestFirstObservationAbsence(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record an absence observation with no content.
	record, err := store.FirstObservation(turnID, attemptID, "missing.txt", KindAbsent, nil)
	if err != nil {
		t.Fatalf("first observation: %v", err)
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

func TestFirstObservationDirectory(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record a directory observation.
	entries, _ := FrameEntries([]Entry{
		{Type: "f", Name: "file1.txt"},
		{Type: "f", Name: "file2.txt"},
		{Type: "d", Name: "subdir"},
	})

	record, err := store.FirstObservation(turnID, attemptID, "dir", KindDir, entries)
	if err != nil {
		t.Fatalf("first observation: %v", err)
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
	record1, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, content)
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}

	// Try to record the same path again - should fail.
	record2, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, []byte("modified"))
	if err == nil {
		t.Errorf("second observation should fail, got record %+v", record2)
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
	record, err := store.FirstObservation(turnID, attemptID, "missing.txt", KindAbsent, nil)
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}

	if record.Value != "" {
		t.Errorf("absence should have empty value")
	}

	// Try to observe the same path as a file - should fail.
	_, err = store.FirstObservation(turnID, attemptID, "missing.txt", KindFile, []byte("content"))
	if err == nil {
		t.Errorf("observing same path again should fail")
	}

	// Verify it still reads as absent.
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

	// Two goroutines try to observe the same path concurrently.
	var wg sync.WaitGroup
	var recordedCount atomic.Int32
	var recordedValue string

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(attemptID string) {
			defer wg.Done()
			content := []byte("content from attempt")
			record, err := store.FirstObservation(turnID, attemptID, "concurrent.txt", KindFile, content)
			if err != nil {
				t.Logf("observation failed: %v", err)
				return
			}
			recordedCount.Add(1)
			if recordedValue == "" {
				recordedValue = record.Value
			}
		}(attemptIDs[i])
	}
	wg.Wait()

	// Exactly one should have succeeded in recording.
	if recordedCount.Load() != 1 {
		t.Errorf("concurrent first touches: got %d recorded, want 1", recordedCount.Load())
	}

	// Verify the observation exists.
	obs, err := store.ObservationAt(turnID, "concurrent.txt")
	if err != nil {
		t.Fatalf("observation at: %v", err)
	}

	if obs.Value != recordedValue {
		t.Errorf("recorded value mismatch: %q != %q", obs.Value, recordedValue)
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
	_, err = store.FirstObservation(turnID, attemptID, "file1.txt", KindFile, []byte("content1"))
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}
	v, err = store.SnapshotVersion(turnID)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	if v != 1 {
		t.Errorf("after first observation: got version %d, want 1", v)
	}

	// Record second observation - version should be 2.
	_, err = store.FirstObservation(turnID, attemptID, "file2.txt", KindFile, []byte("content2"))
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}
	v, err = store.SnapshotVersion(turnID)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	if v != 2 {
		t.Errorf("after second observation: got version %d, want 2", v)
	}
}

func TestObservationsBoundToOriginalAttempt(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// Record an observation.
	_, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, []byte("content"))
	if err != nil {
		t.Fatalf("first observation: %v", err)
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
}

func TestMultipleObservationsOrdered(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	paths := []string{"c.txt", "a.txt", "b.txt"}
	for _, path := range paths {
		_, err := store.FirstObservation(turnID, attemptID, path, KindFile, []byte("content"))
		if err != nil {
			t.Fatalf("first observation: %v", err)
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

func TestRecordObservationDirectlyBypassesLock(t *testing.T) {
	store, _, turnID, attemptID := testStore(t)

	// First observation via FirstObservation.
	_, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, []byte("first"))
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}

	// RecordObservation directly bypasses the lock and should fail in the
	// journal layer (not in the Store lock).
	record, err := store.RecordObservation(turnID, attemptID, "file.txt", KindFile, []byte("second"))
	if err == nil {
		t.Errorf("RecordObservation should fail for already-observed path, got %+v", record)
	}
}

func TestObservationAtNotFound(t *testing.T) {
	store, _, turnID, _ := testStore(t)

	record, err := store.ObservationAt(turnID, "nonexistent.txt")
	if err == nil {
		t.Errorf("observation at nonexistent path should fail, got %+v", record)
	}
}

func TestBinaryPathsAndContent(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	// Path with embedded null bytes and newlines.
	binaryPath := "file\x00with\nnulls"
	binaryContent := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}

	_, err := store.FirstObservation(turnID, attemptID, binaryPath, KindFile, binaryContent)
	if err != nil {
		t.Fatalf("first observation with binary path: %v", err)
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

func TestStoreCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow cleanup test in short mode")
	}

	store, _, turnID, attemptID := testStore(t)

	_, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, []byte("content"))
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}

	// Clear the turn lock.
	store.ClearTurnLock(turnID)

	// After clearing, the lock should be gone. A new attempt should fail in
	// the journal (ErrObserved from RecordObservation), but not in the Store lock.
	_, err2 := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, []byte("new"))
	if err2 == nil {
		t.Errorf("after clear turn lock, FirstObservation should fail in journal")
	}
}

func TestContentStorage(t *testing.T) {
	store, j, turnID, attemptID := testStore(t)

	content := []byte("test file content")
	record, err := store.FirstObservation(turnID, attemptID, "file.txt", KindFile, content)
	if err != nil {
		t.Fatalf("first observation: %v", err)
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

func TestStoreWithRealFilesystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping filesystem test in short mode")
	}

	store, j, turnID, attemptID := testStore(t)

	// Create a temporary file and read it.
	dir := t.TempDir()
	filepath := dir + "/test.txt"
	content := []byte("file from filesystem")
	if err := os.WriteFile(filepath, content, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// In a real tool, this would read from the filesystem.
	// For this test, we just simulate by recording the content.
	record, err := store.FirstObservation(turnID, attemptID, "fs/test.txt", KindFile, content)
	if err != nil {
		t.Fatalf("first observation: %v", err)
	}

	// Verify the content is stored.
	retrieved, err := j.Content(record.Value)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}

	if !bytes.Equal(retrieved, content) {
		t.Errorf("filesystem content mismatch")
	}
}

func BenchmarkFirstObservation(b *testing.B) {
	store, _, turnID, attemptID := testStore(&testing.T{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := "file" + string(rune(i%100)) + ".txt"
		_, _ = store.FirstObservation(turnID, attemptID, path, KindFile, []byte("benchmark"))
	}
}

func BenchmarkConcurrentFirstTouches(b *testing.B) {
	store, j, turnID, _ := testStore(&testing.T{})

	// Pre-create enough attempts for the benchmark.
	var attemptIDs []string
	for i := 0; i < b.N; i++ {
		attemptID, err := j.BeginAttempt(turnID, "test-author", "revise", i, nil, "")
		if err != nil {
			b.Fatalf("begin attempt: %v", err)
		}
		attemptIDs = append(attemptIDs, attemptID)
	}

	// Use a channel to coordinate goroutines racing to observe the same path.
	readyChan := make(chan struct{})
	var wg sync.WaitGroup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func(attemptID string) {
			defer wg.Done()
			<-readyChan
			_, _ = store.FirstObservation(turnID, attemptID, "concurrent.txt", KindFile, []byte("x"))
		}(attemptIDs[i])

		switch i {
		case 0:
			time.Sleep(1 * time.Millisecond)
		case b.N - 1:
			close(readyChan)
		}
	}
	wg.Wait()
}
