package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestUnifiedDiffCreate(t *testing.T) {
	content := []byte("hello\nworld\n")
	diff := unifiedDiffCreate("test.txt", content)

	if !contains(diff, "--- /dev/null") {
		t.Errorf("diff missing source header: %s", diff)
	}
	if !contains(diff, "+++ test.txt") {
		t.Errorf("diff missing dest header: %s", diff)
	}
	if !contains(diff, "+hello") {
		t.Errorf("diff missing added line: %s", diff)
	}
	if !contains(diff, "+world") {
		t.Errorf("diff missing added line: %s", diff)
	}
}

func TestUnifiedDiffModify(t *testing.T) {
	base := []byte("old content\n")
	new := []byte("new content\n")
	diff := unifiedDiffModify("test.txt", base, new)

	if !contains(diff, "--- test.txt") {
		t.Errorf("diff missing source header: %s", diff)
	}
	if !contains(diff, "+++ test.txt") {
		t.Errorf("diff missing dest header: %s", diff)
	}
	if !contains(diff, "-old content") {
		t.Errorf("diff missing removed line: %s", diff)
	}
	if !contains(diff, "+new content") {
		t.Errorf("diff missing added line: %s", diff)
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		content []byte
		want    int
	}{
		{[]byte(""), 0},
		{[]byte("one"), 1},
		{[]byte("one\n"), 1},
		{[]byte("one\ntwo"), 2},
		{[]byte("one\ntwo\n"), 2},
	}

	for _, tt := range tests {
		got := countLines(tt.content)
		if got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.content, got, tt.want)
		}
	}
}

func TestDiffs(t *testing.T) {
	content := []byte("file content")
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
		return nil, nil
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
}

func TestFormatDiff(t *testing.T) {
	diff := UnifiedDiff{
		Path:    "test.txt",
		Op:      "create",
		NewHash: "abc123",
		Diff:    "+content\n",
	}

	formatted := FormatDiff(diff)

	if !contains(formatted, "Path: test.txt") {
		t.Errorf("formatted missing path: %s", formatted)
	}
	if !contains(formatted, "Op: create") {
		t.Errorf("formatted missing op: %s", formatted)
	}
	if !contains(formatted, "New: abc123") {
		t.Errorf("formatted missing hash: %s", formatted)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && s[0:len(substr)] == substr) ||
		(len(substr) < len(s) && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
