package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// setupTestWorkspace creates a temporary workspace for testing.
func setupTestWorkspace(t *testing.T) string {
	t.Helper()

	// Create a temporary directory.
	tmpDir, err := os.MkdirTemp("", "tools-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	// Create some test files.
	testFiles := map[string]string{
		"README.md":       "# Project\n\nThis is a test project.\n",
		"main.go":         "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"subdir/file.txt": "Some content\nin multiple lines\nfor testing.\n",
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

	return tmpDir
}

func TestReadFileBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	_ = setupTestWorkspace(t)

	// For this test, we'll need a proper setup with a database.
	// This is a placeholder test to verify the structure.
	t.Skip("full test requires database setup")
}

func TestReadFileWithRange(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestReadFileNotFound(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestReadFileDirectory(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestListDirBasic(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestListDirEmpty(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestListDirNotFound(t *testing.T) {
	t.Skip("full test requires database setup")
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

	// Should have the original entries plus the staged file at root level.
	expectedNames := map[string]bool{
		"existing.txt": true,
		"subdir":       true,
		"new.go":       true,
	}

	for _, entry := range result {
		if !expectedNames[entry.Name] {
			t.Errorf("unexpected entry: %s", entry.Name)
		}
		if entry.Name == "new.go" && entry.Type != "f" {
			t.Errorf("staged entry type mismatch: got %q, want f", entry.Type)
		}
	}

	if len(result) != 3 {
		t.Errorf("entries count: got %d, want 3", len(result))
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
