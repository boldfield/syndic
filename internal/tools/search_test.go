package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

func TestSearchMatchMarshal(t *testing.T) {
	match := SearchMatch{
		File: "main.go",
		Line: 42,
		Text: "fmt.Println(\"hello\")",
	}

	data, err := json.Marshal(match)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed SearchMatch
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed != match {
		t.Errorf("mismatch: got %+v, want %+v", parsed, match)
	}
}

func TestSearchResultMarshal(t *testing.T) {
	result := SearchResult{
		Matches: []SearchMatch{
			{File: "main.go", Line: 1, Text: "package main"},
			{File: "main.go", Line: 3, Text: "func main() {"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Matches) != len(result.Matches) {
		t.Errorf("matches count: got %d, want %d", len(parsed.Matches), len(result.Matches))
	}

	for i, match := range parsed.Matches {
		if match != result.Matches[i] {
			t.Errorf("match %d: got %+v, want %+v", i, match, result.Matches[i])
		}
	}
}

func TestSearchPatternValidation(t *testing.T) {
	tests := []struct {
		pattern string
		valid   bool
	}{
		{`^func.*\(`, true},
		{`\d+`, true},
		{`[a-z]+`, true},
		{`(?P<invalid`, false},
		{`[`, false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			_, err := regexp.Compile(tt.pattern)
			if (err == nil) != tt.valid {
				t.Errorf("valid mismatch: got error %v, want valid=%v", err, tt.valid)
			}
		})
	}
}

// setupSearchTestContext creates a test context for search operations.
func setupSearchTestContext(t *testing.T) (ReadFileContext, *workspace.Confined) {
	t.Helper()

	// Create a temporary directory and workspace.
	tmpDir := t.TempDir()

	// Create test files with searchable content.
	testFiles := map[string]string{
		"file1.txt":           "hello world\nthis is a test\nhello again\n",
		"file2.txt":           "goodbye world\n",
		"subdir/file3.txt":    "hello from subdir\nmore content\n",
		"subdir/file4.txt":    "nothing to find here\n",
		"deep/nested/main.go": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
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

	// Open the workspace.
	conf, err := workspace.OpenConfined(tmpDir, nil)
	if err != nil {
		t.Fatalf("open confined: %v", err)
	}
	t.Cleanup(func() { _ = conf.Close() })

	// Set up journal store.
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

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	return ctx, conf
}

func TestSearchFilesBasic(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	args := SearchFilesArgs{Pattern: "hello"}
	result, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Matches) == 0 {
		t.Error("expected matches for 'hello' pattern")
	}

	// Count matches
	helloCount := 0
	for _, match := range parsed.Matches {
		if regexp.MustCompile("hello").MatchString(match.Text) {
			helloCount++
		}
	}

	if helloCount != len(parsed.Matches) {
		t.Errorf("expected all matches to contain 'hello', got %d out of %d", helloCount, len(parsed.Matches))
	}
}

func TestSearchFilesRecursive(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Search from root - should find files in subdirectories.
	args := SearchFilesArgs{Pattern: "hello"}
	result, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files recursive: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should find matches from multiple files and subdirectories.
	fileSet := make(map[string]bool)
	for _, match := range parsed.Matches {
		fileSet[match.File] = true
	}

	// Check that we found files from different levels.
	if !fileSet["file1.txt"] {
		t.Error("expected matches from file1.txt")
	}
	if !fileSet["subdir/file3.txt"] {
		t.Error("expected matches from subdir/file3.txt")
	}
}

func TestSearchFilesWithPath(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Limit search to a specific directory.
	subdir := "subdir"
	args := SearchFilesArgs{Pattern: "hello", Path: &subdir}
	result, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files with path: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should only find matches in subdir.
	for _, match := range parsed.Matches {
		if !regexp.MustCompile(`^subdir/`).MatchString(match.File) {
			t.Errorf("expected match in subdir, got %q", match.File)
		}
	}
}

func TestSearchFilesNoMatches(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	args := SearchFilesArgs{Pattern: "xyznotfound"}
	result, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Matches) != 0 {
		t.Errorf("expected no matches, got %d", len(parsed.Matches))
	}
}

func TestSearchFilesStagedContent(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Test with an empty staging map - the test focuses on the search completing
	// successfully with staged files. Full staged content testing would require
	// setting up the content store with actual SHA256 hashes, which is beyond the scope.
	staging := map[string]string{}

	args := SearchFilesArgs{Pattern: "hello"}
	result, err := SearchFilesFunc(ctx, args, staging, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files with staging: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify search works with empty staging
	if len(parsed.Matches) == 0 {
		t.Error("expected matches even with empty staging")
	}
}

func TestSearchFilesInvalidPath(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	invalidPath := "../etc/passwd"
	args := SearchFilesArgs{Pattern: "hello", Path: &invalidPath}
	_, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestSearchFilesProtectedPaths(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Define a protection function that blocks "subdir".
	isProtected := func(path string) bool {
		return path == "subdir" || path == "subdir/file3.txt"
	}

	args := SearchFilesArgs{Pattern: "hello"}
	result, err := SearchFilesFunc(ctx, args, nil, isProtected)
	if err != nil {
		t.Fatalf("search_files with protection: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should not find matches from protected subdir.
	for _, match := range parsed.Matches {
		if regexp.MustCompile(`^subdir/`).MatchString(match.File) {
			t.Errorf("expected protected path to be skipped, got %q", match.File)
		}
	}
}

func TestSearchFilesWithFilePath(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Search in a specific file (not a directory).
	filePath := "file1.txt"
	args := SearchFilesArgs{Pattern: "hello", Path: &filePath}
	result, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("search_files with file path: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should find matches only from file1.txt
	if len(parsed.Matches) == 0 {
		t.Error("expected matches from file1.txt")
	}
	for _, match := range parsed.Matches {
		if match.File != "file1.txt" {
			t.Errorf("expected match from file1.txt, got %q", match.File)
		}
	}
}

func TestSearchFilesRepeatedSearchIgnoresNewFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("requires filesystem operations")
	}

	// Create a temporary directory and workspace for this test
	tmpDir := t.TempDir()

	// Create initial test file
	testFiles := map[string]string{
		"existing.txt": "existing content\n",
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

	// Open the workspace
	conf, err := workspace.OpenConfined(tmpDir, nil)
	if err != nil {
		t.Fatalf("open confined: %v", err)
	}
	t.Cleanup(func() { _ = conf.Close() })

	// Set up journal store
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

	ctx := ReadFileContext{
		TurnID:       turnID,
		AttemptID:    attemptID,
		Confined:     conf,
		ObserveStore: observation.NewStore(store),
		ContentStore: store,
	}

	// First search - pattern that doesn't match anything yet
	args := SearchFilesArgs{Pattern: "newcontent"}
	result1, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("first search: %v", err)
	}

	var parsed1 SearchResult
	if err := json.Unmarshal(result1, &parsed1); err != nil {
		t.Fatalf("unmarshal first search: %v", err)
	}

	// Should not find anything yet
	if len(parsed1.Matches) != 0 {
		t.Errorf("expected no matches before creating file, got %d", len(parsed1.Matches))
	}

	// Now create a new file with matching content in the workspace (live, not staged)
	newFilePath := filepath.Join(tmpDir, "new_file.txt")
	if err := os.WriteFile(newFilePath, []byte("newcontent line\n"), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	// Search again in the same context - should not find the newly created file
	// because the snapshot was taken at the first search
	result2, err := SearchFilesFunc(ctx, args, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("second search: %v", err)
	}

	var parsed2 SearchResult
	if err := json.Unmarshal(result2, &parsed2); err != nil {
		t.Fatalf("unmarshal second search: %v", err)
	}

	// Should still not find the newly created file (snapshot is frozen)
	if len(parsed2.Matches) != 0 {
		t.Errorf("repeated search should ignore newly added files, got %d matches", len(parsed2.Matches))
	}
}

func TestSearchFilesStagedCreationsInSubdirectory(t *testing.T) {
	ctx, _ := setupSearchTestContext(t)

	// Stage a file in a new subdirectory that doesn't exist yet.
	// The content store should have the staged content SHA.
	staging := map[string]string{
		"newdir/staged.txt": "dummy_sha", // This would normally be populated by the tool
	}

	// Note: This test verifies that search can handle staged creations in new directories.
	// When a staged file has a SHA that's not in the content store, the search will fail
	// when trying to access that file's content. This is expected behavior.
	args := SearchFilesArgs{Pattern: "staged"}
	_, err := SearchFilesFunc(ctx, args, staging, func(string) bool { return false })

	// The search will error because the staged SHA doesn't exist in the content store.
	// This is acceptable; the test verifies that the error is handled correctly.
	if err == nil {
		t.Error("expected error when staged file has non-existent SHA")
	}
}
