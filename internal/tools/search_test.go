package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

var searchFiles = map[string]string{
	"a.txt":      "hello a\nnothing\n",
	"sub/b.txt":  "say hello\n",
	"sub/c.bin":  "hello\x00binary",
	".env":       "hello secret\n",
	".git/c":     "hello git\n",
	"sub/deep/d": "hello deep\r\nx\n",
}

func TestSearchFilesWholeWorkspaceSkipsProtectedAndBinary(t *testing.T) {
	f := newReadFixture(t, searchFiles, 0)
	res, tc := f.search(map[string]any{"pattern": "hello"})
	want := "a.txt:1:hello a\nsub/b.txt:1:say hello\nsub/deep/d:1:hello deep\n"
	if res.ErrorCode != "" || string(res.Content) != want {
		t.Fatalf("got %q (%s), want %q", res.Content, res.ErrorCode, want)
	}
	f.journaled(res, tc)
}

func TestSearchFilesPathScopes(t *testing.T) {
	f := newReadFixture(t, searchFiles, 0)
	res, _ := f.search(map[string]any{"pattern": "hello", "path": "sub"})
	if want := "sub/b.txt:1:say hello\nsub/deep/d:1:hello deep\n"; string(res.Content) != want {
		t.Fatalf("dir: got %q (%s)", res.Content, res.ErrorCode)
	}
	res, _ = f.search(map[string]any{"pattern": "^no", "path": "a.txt"})
	if want := "a.txt:2:nothing\n"; string(res.Content) != want {
		t.Fatalf("file: got %q (%s)", res.Content, res.ErrorCode)
	}
	res, _ = f.search(map[string]any{"pattern": "zzz"})
	if string(res.Content) != "no matches" || res.ErrorCode != "" {
		t.Fatalf("none: got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestSearchFilesErrors(t *testing.T) {
	f := newReadFixture(t, searchFiles, 0)
	for name, tc := range map[string]struct {
		args map[string]any
		code string
	}{
		"bad pattern":     {map[string]any{"pattern": "("}, ErrCodeBadArguments},
		"missing scope":   {map[string]any{"pattern": "x", "path": "nope"}, ErrCodeNotFound},
		"protected scope": {map[string]any{"pattern": "x", "path": ".git"}, ErrCodeProtected},
		"escape":          {map[string]any{"pattern": "x", "path": ".."}, ErrCodeBadArguments},
	} {
		t.Run(name, func(t *testing.T) {
			res, call := f.search(tc.args)
			if res.ErrorCode != tc.code {
				t.Fatalf("got %q (%s), want %s", res.Content, res.ErrorCode, tc.code)
			}
			f.journaled(res, call)
		})
	}
}

func TestSearchFilesRepeatedSearchIgnoresNewLiveFiles(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "hello\n", "sub/b.txt": "hello\n"}, 0)
	first, _ := f.search(map[string]any{"pattern": "hello"})

	f.write("new.txt", "hello\n")        // new file in a recorded directory
	f.write("sub/new2.txt", "hello\n")   // new file in a recorded subdirectory
	f.write("a.txt", "changed hello\n")  // changed content of an observed file
	f.write("freshdir/x.txt", "hello\n") // new directory
	second, _ := f.search(map[string]any{"pattern": "hello"})
	if string(first.Content) != string(second.Content) || strings.Contains(string(second.Content), "new") {
		t.Fatalf("first %q, second %q", first.Content, second.Content)
	}
	// A narrower scope later is served from the same record.
	third, _ := f.search(map[string]any{"pattern": "hello", "path": "sub"})
	if want := "sub/b.txt:1:hello\n"; string(third.Content) != want {
		t.Fatalf("got %q", third.Content)
	}
}

func TestSearchFilesObservesTraversedScope(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "x\n", "sub/b.txt": "x\n", ".env": "x"}, 0)
	f.search(map[string]any{"pattern": "x"})
	obs, err := f.j.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range obs {
		got[o.Path] = o.Kind
	}
	want := map[string]string{".": journal.ObservationListing, "a.txt": journal.ObservationFile, "sub": journal.ObservationListing, "sub/b.txt": journal.ObservationFile}
	if len(got) != len(want) {
		t.Fatalf("observed %v, want %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Fatalf("observed %v, want %v", got, want)
		}
	}
}

func TestSearchFilesSymlinkDoesNotAbortSearch(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "hello\n"}, 0)
	if err := os.Symlink("a.txt", filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	res, _ := f.search(map[string]any{"pattern": "hello"})
	if want := "a.txt:1:hello\n"; res.ErrorCode != "" || string(res.Content) != want {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestSearchFilesOversizeIsSizeError(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "match one\nmatch two\nmatch three\n"}, 16)
	res, tc := f.search(map[string]any{"pattern": "match"})
	if res.ErrorCode != ErrCodeTooLarge || strings.Contains(string(res.Content), "match one") {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	f.journaled(res, tc)
}

func TestSearchFilesStagedOverlay(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "hello base\n", "sub/b.txt": "plain\n"}, 0)
	f.stage("a.txt", "plain\n")                  // staged edit removes the match
	f.stage("sub/new.txt", "hello created\n")    // staged creation in a recorded directory
	f.stage("brand/nested/n.txt", "hello new\n") // staged creation under a new directory
	res, tc := f.search(map[string]any{"pattern": "hello"})
	want := "brand/nested/n.txt:1:hello new\nsub/new.txt:1:hello created\n"
	if res.ErrorCode != "" || string(res.Content) != want {
		t.Fatalf("got %q (%s), want %q", res.Content, res.ErrorCode, want)
	}
	f.journaled(res, tc)
	res, _ = f.search(map[string]any{"pattern": "hello", "path": "brand"})
	if want := "brand/nested/n.txt:1:hello new\n"; string(res.Content) != want {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestSearchFilesNoPhantomLineAfterFinalNewline(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "x\n\ny"}, 0)
	res, _ := f.search(map[string]any{"pattern": "^$"})
	if want := "b.txt:2:\n"; res.ErrorCode != "" || string(res.Content) != want {
		t.Fatalf("got %q (%s), want %q", res.Content, res.ErrorCode, want)
	}
}

func TestSearchFilesEmptyFileHasNoLines(t *testing.T) {
	f := newReadFixture(t, map[string]string{"empty.txt": "", "a.txt": "x\n"}, 0)
	res, _ := f.search(map[string]any{"pattern": "^$"})
	if res.ErrorCode != "" || string(res.Content) != "no matches" {
		t.Fatalf("got %q (%s), want no matches", res.Content, res.ErrorCode)
	}
}

func TestSearchFilesCRLFAnchoredPattern(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "hello\r\nworld\r\n"}, 0)
	res, _ := f.search(map[string]any{"pattern": "^hello$"})
	if want := "a.txt:1:hello\n"; res.ErrorCode != "" || string(res.Content) != want {
		t.Fatalf("got %q (%s), want %q", res.Content, res.ErrorCode, want)
	}
	res, _ = f.search(map[string]any{"pattern": "\r"})
	if res.ErrorCode != "" || string(res.Content) != "no matches" {
		t.Fatalf("got %q (%s), want no matches", res.Content, res.ErrorCode)
	}
}
