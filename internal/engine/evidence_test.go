package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestUnifiedDiffCreate(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{
			name:    "simple file with trailing newline",
			content: []byte("hello\nworld\n"),
			want:    "--- /dev/null\n+++ b/test.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n",
		},
		{
			name:    "file without trailing newline",
			content: []byte("hello\nworld"),
			want:    "--- /dev/null\n+++ b/test.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n\\ No newline at end of file\n",
		},
		{
			name:    "single line with newline",
			content: []byte("line\n"),
			want:    "--- /dev/null\n+++ b/test.txt\n@@ -0,0 +1,1 @@\n+line\n",
		},
		{
			name:    "empty file",
			content: []byte(""),
			want:    "--- /dev/null\n+++ b/test.txt\n@@ -0,0 +1,0 @@\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unifiedDiffCreate("test.txt", tt.content)
			if got != tt.want {
				t.Errorf("unifiedDiffCreate(%q) =\n%s\nwant\n%s", tt.content, got, tt.want)
			}
		})
	}
}

func TestUnifiedDiffModify(t *testing.T) {
	tests := []struct {
		name string
		base []byte
		new  []byte
		want string
	}{
		{
			name: "simple modification",
			base: []byte("old\n"),
			new:  []byte("new\n"),
			want: "--- a/test.txt\n+++ b/test.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		},
		{
			name: "add line",
			base: []byte("first\n"),
			new:  []byte("first\nsecond\n"),
			want: "--- a/test.txt\n+++ b/test.txt\n@@ -1,1 +1,2 @@\n first\n+second\n",
		},
		{
			name: "remove line",
			base: []byte("first\nsecond\n"),
			new:  []byte("first\n"),
			want: "--- a/test.txt\n+++ b/test.txt\n@@ -1,2 +1,1 @@\n first\n-second\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unifiedDiffModify("test.txt", tt.base, tt.new)
			if !strings.HasPrefix(got, "--- a/test.txt\n+++ b/test.txt\n") {
				t.Errorf("missing proper headers: %s", got)
			}
		})
	}
}

func TestDiffs(t *testing.T) {
	content := []byte("file content\n")
	hash := hashContent(content)

	cs := journal.ChangeSet{
		Paths: []journal.ChangeSetPath{
			{
				Path:       "test.txt",
				Op:         "create",
				BaseSHA256: "",
				NewSHA256:  hash,
				NewLen:     int64(len(content)),
			},
		},
	}

	getContent := func(h string) ([]byte, error) {
		if h == hash {
			return content, nil
		}
		return nil, fmt.Errorf("content not found: %s", h)
	}

	diffs, err := Diffs(cs, getContent)
	if err != nil {
		t.Fatalf("Diffs() error = %v", err)
	}

	if len(diffs) != 1 {
		t.Fatalf("Diffs() = %d diffs, want 1", len(diffs))
	}

	if diffs[0].Path != "test.txt" {
		t.Errorf("Diffs()[0].Path = %q, want %q", diffs[0].Path, "test.txt")
	}
	if diffs[0].Op != "create" {
		t.Errorf("Diffs()[0].Op = %q, want %q", diffs[0].Op, "create")
	}
	if !strings.Contains(diffs[0].Diff, "+file content") {
		t.Errorf("Diff missing content: %q", diffs[0].Diff)
	}
}

func TestUnifiedDiffDelete(t *testing.T) {
	content := []byte("file content\n")
	diff := unifiedDiffDelete("test.txt", content)

	if !strings.HasPrefix(diff, "--- a/test.txt\n+++ /dev/null\n") {
		t.Errorf("missing proper headers: %s", diff)
	}
	if !strings.Contains(diff, "-file content") {
		t.Errorf("missing deleted line: %s", diff)
	}
}

func TestFormatDiff(t *testing.T) {
	diff := UnifiedDiff{
		Path:    "test.txt",
		Op:      "create",
		NewHash: "abc123",
		Diff:    "+content\n",
	}

	formatted := FormatDiff(diff)

	if !strings.Contains(formatted, "Path: test.txt") {
		t.Errorf("formatted missing path: %s", formatted)
	}
	if !strings.Contains(formatted, "Op: create") {
		t.Errorf("formatted missing op: %s", formatted)
	}
	if !strings.Contains(formatted, "New: abc123") {
		t.Errorf("formatted missing hash: %s", formatted)
	}
}
