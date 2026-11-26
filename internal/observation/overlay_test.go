package observation

import (
	"sort"
	"strings"
	"testing"
)

func TestStagingOverlayIsStagedFile(t *testing.T) {
	so := NewStagingOverlay(map[string]string{"a.txt": "hash1", "dir/b.txt": "hash2"})
	if !so.IsStagedFile("a.txt") {
		t.Fatal("a.txt should be staged")
	}
	if !so.IsStagedFile("dir/b.txt") {
		t.Fatal("dir/b.txt should be staged")
	}
	if so.IsStagedFile("nope.txt") {
		t.Fatal("nope.txt should not be staged")
	}
}

func TestStagingOverlayStagedHash(t *testing.T) {
	so := NewStagingOverlay(map[string]string{"a.txt": "hash1", "dir/b.txt": "hash2"})
	if so.StagedHash("a.txt") != "hash1" {
		t.Fatal("wrong hash for a.txt")
	}
	if so.StagedHash("dir/b.txt") != "hash2" {
		t.Fatal("wrong hash for dir/b.txt")
	}
	if so.StagedHash("nope.txt") != "" {
		t.Fatal("should return empty for unstaged")
	}
}

func TestStagingOverlayStagedChildren(t *testing.T) {
	so := NewStagingOverlay(map[string]string{
		"a.txt":         "hash1",
		"dir/b.txt":     "hash2",
		"dir/sub/c.txt": "hash3",
		"other.txt":     "hash4",
		"d/file.txt":    "hash5",
	})
	children := so.StagedChildren(Root)
	want := map[string]string{"a.txt": "f", "dir": "d", "other.txt": "f", "d": "d"}
	if len(children) != len(want) {
		t.Fatalf("got %v, want %v", children, want)
	}
	for k, v := range want {
		if children[k] != v {
			t.Fatalf("children[%q] = %q, want %q", k, children[k], v)
		}
	}
}

func TestStagingOverlayStagedChildrenNested(t *testing.T) {
	so := NewStagingOverlay(map[string]string{
		"dir/a.txt":     "hash1",
		"dir/sub/b.txt": "hash2",
		"dir/sub/c.txt": "hash3",
		"other/x.txt":   "hash4",
	})
	children := so.StagedChildren("dir")
	want := map[string]string{"a.txt": "f", "sub": "d"}
	if len(children) != len(want) {
		t.Fatalf("got %v, want %v", children, want)
	}
	for k, v := range want {
		if children[k] != v {
			t.Fatalf("children[%q] = %q, want %q", k, children[k], v)
		}
	}
}

func TestStagingOverlayMergeListings(t *testing.T) {
	recorded := []Entry{
		{Type: "f", Name: "a.txt"},
		{Type: "f", Name: "b.txt"},
		{Type: "d", Name: "dir"},
	}
	so := NewStagingOverlay(map[string]string{
		"new.txt": "hash1",
		"dir/sub": "hash2",
		"fresh/x": "hash3",
	})
	merged := so.MergeListings(recorded)
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	want := []Entry{
		{Type: "f", Name: "a.txt"},
		{Type: "f", Name: "b.txt"},
		{Type: "d", Name: "dir"},
		{Type: "d", Name: "fresh"},
		{Type: "f", Name: "new.txt"},
	}
	if len(merged) != len(want) {
		t.Fatalf("got %d entries, want %d", len(merged), len(want))
	}
	for i, e := range merged {
		if e != want[i] {
			t.Fatalf("merged[%d] = %v, want %v", i, e, want[i])
		}
	}
}

func TestStagingOverlayMergeListingsUnder(t *testing.T) {
	recorded := []Entry{
		{Type: "f", Name: "a.txt"},
		{Type: "f", Name: "b.txt"},
		{Type: "d", Name: "sub"},
	}
	so := NewStagingOverlay(map[string]string{
		"dir/new.txt":   "hash1",
		"dir/sub/x.txt": "hash2",
	})
	merged := so.MergeListingsUnder("dir", recorded)
	want := map[string]string{"a.txt": "f", "b.txt": "f", "sub": "d", "new.txt": "f"}
	if len(merged) != len(want) {
		t.Fatalf("got %d entries, want %d", len(merged), len(want))
	}
	for _, e := range merged {
		if want[e.Name] != e.Type {
			t.Fatalf("%q: got %s, want %s", e.Name, e.Type, want[e.Name])
		}
	}
}

func TestStagingOverlayPathIsStaged(t *testing.T) {
	so := NewStagingOverlay(map[string]string{
		"a.txt":         "hash1",
		"dir/b.txt":     "hash2",
		"dir/sub/c.txt": "hash3",
	})
	tests := []struct {
		path string
		want bool
	}{
		{"a.txt", true},
		{"dir", true},
		{"dir/b.txt", true},
		{"dir/sub", true},
		{"dir/sub/c.txt", true},
		{"nope.txt", false},
		{"dir/nope", false},
	}
	for _, tc := range tests {
		if got := so.PathIsStaged(tc.path); got != tc.want {
			t.Errorf("PathIsStaged(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestStagingOverlayStagedPathsUnder(t *testing.T) {
	so := NewStagingOverlay(map[string]string{
		"a.txt":         "hash1",
		"dir/b.txt":     "hash2",
		"dir/sub/c.txt": "hash3",
		"dir/x.txt":     "hash4",
		"other.txt":     "hash5",
	})
	paths := so.StagedPathsUnder("dir")
	want := []string{"b.txt", "sub", "x.txt"}
	sort.Strings(paths)
	sort.Strings(want)
	if len(paths) != len(want) {
		t.Fatalf("got %v, want %v", paths, want)
	}
	for i, p := range paths {
		if !strings.HasPrefix(p, "dir/") && p != "b.txt" && p != "sub" && p != "x.txt" {
			t.Fatalf("paths[%d] = %q", i, p)
		}
	}
}

func TestStagingOverlayStagedPathsRecursive(t *testing.T) {
	so := NewStagingOverlay(map[string]string{
		"a.txt":         "hash1",
		"dir/b.txt":     "hash2",
		"dir/sub/c.txt": "hash3",
		"dir/x.txt":     "hash4",
		"other.txt":     "hash5",
	})
	paths := so.StagedPathsRecursive("dir")
	wantSet := map[string]bool{
		"dir/b.txt":     true,
		"dir/sub/c.txt": true,
		"dir/x.txt":     true,
	}
	if len(paths) != len(wantSet) {
		t.Fatalf("got %v, want %d paths", paths, len(wantSet))
	}
	for _, p := range paths {
		if !wantSet[p] {
			t.Fatalf("unexpected path %q", p)
		}
	}
}

func TestStagingOverlayEmptyStaging(t *testing.T) {
	so := NewStagingOverlay(map[string]string{})
	if so.IsStagedFile("a.txt") {
		t.Fatal("empty staging: should not be staged")
	}
	children := so.StagedChildren(Root)
	if len(children) != 0 {
		t.Fatalf("empty staging: got %v children", children)
	}
	paths := so.StagedPathsUnder(".")
	if len(paths) != 0 {
		t.Fatalf("empty staging: got %v paths", paths)
	}
}
