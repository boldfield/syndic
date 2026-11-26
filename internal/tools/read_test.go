package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// setupTestWorkspace creates a temporary workspace for testing.
func setupTestWorkspace(t *testing.T) *workspace.Confined {
	t.Helper()

	// Create a temporary directory.
	tmpDir := t.TempDir()

	// Create some test files.
	testFiles := map[string]string{
		"README.md":       "# Project\n\nThis is a test project.\n",
		"main.go":         "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"subdir/file.txt": "Some content\nin multiple lines\nfor testing.\n",
		"large.bin":       string(make([]byte, 20*1024*1024)), // 20MB file
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	// Open the workspace with confinement.
	conf, err := workspace.OpenConfined(tmpDir, nil)
	if err != nil {
		t.Fatalf("open confined: %v", err)
	}
	t.Cleanup(func() { _ = conf.Close() })

	return conf
}

// setupTestStore creates a journal store for testing.
func setupTestStore(t *testing.T) (*journal.Store, string, string) {
	t.Helper()

	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := journal.NewStore(db)
	var turnID, attemptID string
	err = store.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if turnID, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		if attemptID, err = tx.BeginAttempt(turnID, "author", "author", 1, nil, ""); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return store, turnID, attemptID
}

func TestReadFileBasic(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ReadFileArgs{Path: "README.md"}
	result, err := ReadFile(ctx, args, nil, 10*1024*1024)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}

	var parsed ReadResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Content) == 0 {
		t.Error("content is empty")
	}
}

func TestReadFileWithRange(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	offset := int64(2)
	limit := int64(5)
	args := ReadFileArgs{Path: "README.md", Offset: &offset, Limit: &limit}
	result, err := ReadFile(ctx, args, nil, 10*1024*1024)
	if err != nil {
		t.Fatalf("read_file with range: %v", err)
	}

	var parsed ReadResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Content) != 5 {
		t.Errorf("expected 5 bytes, got %d", len(parsed.Content))
	}
}

func TestReadFileNotFound(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ReadFileArgs{Path: "nonexistent.txt"}
	_, err := ReadFile(ctx, args, nil, 10*1024*1024)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadFileDirectory(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ReadFileArgs{Path: "subdir"}
	_, err := ReadFile(ctx, args, nil, 10*1024*1024)
	if err == nil {
		t.Error("expected error when reading directory")
	}
}

func TestReadFileOversizeReturnsError(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ReadFileArgs{Path: "large.bin"}
	result, err := ReadFile(ctx, args, nil, 1024*1024) // 1MB limit
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if errMsg, ok := parsed["error"]; !ok {
		t.Error("expected size error result, got content instead")
	} else if errMsg != "file too large" {
		t.Errorf("expected 'file too large' error, got %q", errMsg)
	}
}

func TestListDirBasic(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ListDirArgs{Path: "."}
	result, err := ListDirFunc(ctx, args, nil)
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}

	var parsed ListResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Entries) == 0 {
		t.Error("expected entries in root directory")
	}

	// Check that expected files are present
	names := make(map[string]bool)
	for _, e := range parsed.Entries {
		names[e.Name] = true
	}

	expectedEntries := map[string]bool{"README.md": true, "main.go": true, "subdir": true}
	for name := range expectedEntries {
		if !names[name] {
			t.Errorf("expected entry %q not found", name)
		}
	}
}

func TestListDirSubdirectory(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ListDirArgs{Path: "subdir"}
	result, err := ListDirFunc(ctx, args, nil)
	if err != nil {
		t.Fatalf("list_dir subdir: %v", err)
	}

	var parsed ListResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check for file.txt
	found := false
	for _, e := range parsed.Entries {
		if e.Name == "file.txt" && e.Type == "f" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected file.txt in subdir")
	}
}

func TestListDirNotFound(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	args := ListDirArgs{Path: "nonexistent"}
	_, err := ListDirFunc(ctx, args, nil)
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestReadResultMarshal(t *testing.T) {
	result := ReadResult{Content: "test content"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed ReadResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.Content != result.Content {
		t.Errorf("content mismatch: got %q, want %q", parsed.Content, result.Content)
	}
}

func TestListResultMarshal(t *testing.T) {
	result := ListResult{
		Entries: []DirEntry{
			{Name: "file.txt", Type: "f"},
			{Name: "dir", Type: "d"},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed ListResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Entries) != len(result.Entries) {
		t.Errorf("entries count mismatch: got %d, want %d", len(parsed.Entries), len(result.Entries))
	}

	for i, entry := range parsed.Entries {
		if entry.Name != result.Entries[i].Name || entry.Type != result.Entries[i].Type {
			t.Errorf("entry %d mismatch: got %+v, want %+v", i, entry, result.Entries[i])
		}
	}
}

func TestSizeErrorResultMarshal(t *testing.T) {
	result := SizeErrorResult{
		Error: "file too large",
		Size:  1024000,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["error"] != result.Error {
		t.Errorf("error mismatch: got %q, want %q", parsed["error"], result.Error)
	}
}

func TestRangedRead(t *testing.T) {
	content := []byte("0123456789")

	tests := []struct {
		name   string
		offset int64
		limit  int64
		want   string
	}{
		{"full", 0, 10, "0123456789"},
		{"from middle", 3, 10, "3456789"},
		{"middle range", 2, 5, "23456"},
		{"beyond end", 5, 100, "56789"},
		{"empty", 10, 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rangedRead(content, tt.offset, tt.limit)
			if err != nil {
				t.Fatalf("ranged read: %v", err)
			}

			var parsed ReadResult
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if parsed.Content != tt.want {
				t.Errorf("got %q, want %q", parsed.Content, tt.want)
			}
		})
	}
}

func TestApplyStagedChanges(t *testing.T) {
	entries := []DirEntry{
		{Name: "existing.txt", Type: "f"},
		{Name: "subdir", Type: "d"},
	}

	staging := map[string]string{
		"new.go":               "abc123",
		"subdir/newfile.txt":   "def456",
		"deep/nested/file.txt": "ghi789",
	}

	result := applyStagedChanges(entries, observation.Root, staging)

	// Should have the original entries plus the staged file at root level and synthesized parent dir.
	expectedNames := map[string]bool{
		"existing.txt": true,
		"subdir":       true,
		"new.go":       true,
		"deep":         true, // synthesized parent directory
	}

	for _, entry := range result {
		if !expectedNames[entry.Name] {
			t.Errorf("unexpected entry: %s", entry.Name)
		}
		if entry.Name == "new.go" && entry.Type != "f" {
			t.Errorf("staged entry type mismatch: got %q, want f", entry.Type)
		}
		if entry.Name == "deep" && entry.Type != "d" {
			t.Errorf("synthesized parent should be directory, got %q", entry.Type)
		}
	}

	if len(result) != 4 {
		t.Errorf("entries count: got %d, want 4", len(result))
	}
}

func TestApplyStagedChangesSubdir(t *testing.T) {
	entries := []DirEntry{
		{Name: "file1.txt", Type: "f"},
	}

	staging := map[string]string{
		"newfile.txt":      "abc123",
		"subdir/file2.txt": "def456",
	}

	result := applyStagedChanges(entries, "subdir", staging)

	// Should have the original entry plus the staged file in the subdir.
	expectedNames := map[string]bool{
		"file1.txt": true,
		"file2.txt": true,
	}

	for _, entry := range result {
		if !expectedNames[entry.Name] {
			t.Errorf("unexpected entry: %s", entry.Name)
		}
	}

	if len(result) != 2 {
		t.Errorf("entries count: got %d, want 2", len(result))
	}
}

func TestListDirWithStagedCreations(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	staging := map[string]string{
		"newfile.txt":            "abc123",
		"newdir/nested_file.txt": "def456",
	}

	args := ListDirArgs{Path: "."}
	result, err := ListDirFunc(ctx, args, staging)
	if err != nil {
		t.Fatalf("list_dir with staging: %v", err)
	}

	var parsed ListResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check for synthesized directory
	names := make(map[string]string) // name -> type
	for _, e := range parsed.Entries {
		names[e.Name] = e.Type
	}

	if names["newfile.txt"] != "f" {
		t.Error("expected staged file newfile.txt")
	}
	if names["newdir"] != "d" {
		t.Error("expected synthesized directory newdir")
	}
}

func TestReadFileOversizeWithExplicitLimit(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	// Read a large file with explicit limit equal to maxResultBytes
	// Should return the requested bytes, not a size error
	maxBytes := int64(4)
	limit := maxBytes
	args := ReadFileArgs{Path: "large.bin", Limit: &limit}
	result, err := ReadFile(ctx, args, nil, maxBytes)
	if err != nil {
		t.Fatalf("read_file with explicit limit: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should return content, not error
	if _, hasError := parsed["error"]; hasError {
		t.Error("expected content, not size error when explicit limit is provided")
	}

	if content, ok := parsed["content"]; !ok {
		t.Error("expected content field in result")
	} else if contentStr, ok := content.(string); !ok {
		t.Error("content field is not a string")
	} else if len(contentStr) != 4 {
		t.Errorf("expected 4 bytes, got %d", len(contentStr))
	}
}

func TestReadFileRangeOverflow(t *testing.T) {
	conf := setupTestWorkspace(t)
	store, turnID, attemptID := setupTestStore(t)

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	// Test with offset=1 and limit=MaxInt64 (should not panic)
	offset := int64(1)
	limit := int64(9223372036854775807) // MaxInt64
	args := ReadFileArgs{Path: "README.md", Offset: &offset, Limit: &limit}
	result, err := ReadFile(ctx, args, nil, 10*1024*1024)
	if err != nil {
		t.Fatalf("read_file with large limit: %v", err)
	}

	var parsed ReadResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should have returned content without panicking
	if len(parsed.Content) == 0 {
		t.Error("expected content from ranged read with large limit")
	}
}
