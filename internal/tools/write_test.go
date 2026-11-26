package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

type writeFixture struct {
	*readFixture
	w *WriteTools
}

func newWriteFixture(t *testing.T, files map[string]string) *writeFixture {
	t.Helper()
	f := newReadFixture(t, files, 0)
	return &writeFixture{readFixture: f, w: &WriteTools{r: f.tools}}
}

func (f *writeFixture) writeFile(path, content string) Result {
	f.t.Helper()
	res, tc := f.call(NameWriteFile, map[string]any{"path": path, "content": content}, f.w.WriteFile)
	f.journaled(res, tc)
	return res
}

func (f *writeFixture) edit(path, old, new string) Result {
	f.t.Helper()
	res, tc := f.call(NameEditFile, map[string]any{"path": path, "old": old, "new": new}, f.w.EditFile)
	f.journaled(res, tc)
	return res
}

func (f *writeFixture) unstage(path string) Result {
	f.t.Helper()
	res, tc := f.call(NameUnstage, map[string]any{"path": path}, f.w.Unstage)
	f.journaled(res, tc)
	return res
}

// nextAttempt begins a new attempt of the same turn and rebuilds the tools on
// it, as a revision round does.
func (f *writeFixture) nextAttempt() {
	f.t.Helper()
	err := f.j.Tx(func(tx *journal.Tx) (err error) {
		f.att, err = tx.BeginAttempt(f.turn, "author", "author", 2, nil, "")
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.calls = 0
	f.tools = NewReadTools(f.j, f.tools.fs, f.turn, f.att, 0)
	f.w = &WriteTools{r: f.tools}
}

func (f *writeFixture) staged() map[string]string {
	f.t.Helper()
	m, err := f.tools.loadStaging()
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *writeFixture) mustOK(res Result) {
	f.t.Helper()
	if res.ErrorCode != "" {
		f.t.Fatalf("unexpected error %s: %s", res.ErrorCode, res.Content)
	}
}

func (f *writeFixture) wantCode(res Result, code string) {
	f.t.Helper()
	if res.ErrorCode != code {
		f.t.Fatalf("code %q (%s), want %q", res.ErrorCode, res.Content, code)
	}
}

func (f *writeFixture) readStr(path string) (string, string) {
	res, _ := f.read(map[string]any{"path": path})
	return string(res.Content), res.ErrorCode
}

func (f *writeFixture) listStr(path string) string {
	res, _ := f.list(path)
	return string(res.Content)
}

func TestWriteFileOverlaysBaseAndCreation(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "base"})
	res := f.writeFile("a.txt", "new")
	f.mustOK(res)
	if !strings.Contains(string(res.Content), "base: ") {
		t.Fatalf("got %q", res.Content)
	}
	res = f.writeFile("d/n.txt", "made")
	f.mustOK(res)
	if !strings.HasSuffix(string(res.Content), "(created)") {
		t.Fatalf("got %q", res.Content)
	}
	if got, _ := f.readStr("a.txt"); got != "new" {
		t.Fatalf("got %q", got)
	}
	if got, _ := f.readStr("d/n.txt"); got != "made" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkspaceByteIdenticalBeforeApply(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "base", "b.txt": "bee"})
	f.mustOK(f.writeFile("a.txt", "rewritten"))
	f.mustOK(f.edit("b.txt", "bee", "wasp"))
	f.mustOK(f.writeFile("d/n.txt", "created"))
	for name, want := range map[string]string{"a.txt": "base", "b.txt": "bee"} {
		got, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "d")); !os.IsNotExist(err) {
		t.Fatalf("created path reached the disk: %v", err)
	}
	ents, _ := os.ReadDir(f.root)
	if len(ents) != 2 {
		t.Fatalf("workspace has %d entries", len(ents))
	}
}

func TestCreationVisibleInListAndSearchThenRemovedOnUnstage(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "needle base"})
	f.mustOK(f.writeFile("sub/new.txt", "needle created\n"))
	if got := f.listStr("."); got != "a.txt\nsub/\n" {
		t.Fatalf("root list %q", got)
	}
	if got := f.listStr("sub"); got != "new.txt\n" {
		t.Fatalf("sub list %q", got)
	}
	res, _ := f.search(map[string]any{"pattern": "needle"})
	if string(res.Content) != "a.txt:1:needle base\nsub/new.txt:1:needle created\n" {
		t.Fatalf("search %q", res.Content)
	}
	f.mustOK(f.unstage("sub/new.txt"))
	if got := f.listStr("."); got != "a.txt\n" {
		t.Fatalf("root list after unstage %q", got)
	}
	res, _ = f.search(map[string]any{"pattern": "needle"})
	if string(res.Content) != "a.txt:1:needle base\n" {
		t.Fatalf("search after unstage %q", res.Content)
	}
	if _, code := f.readStr("sub/new.txt"); code != ErrCodeNotFound {
		t.Fatalf("read after unstage: %q", code)
	}
}

func TestUnstageRestoresBaseOfEdit(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "base"})
	f.mustOK(f.writeFile("a.txt", "new"))
	f.mustOK(f.unstage("a.txt"))
	if got, _ := f.readStr("a.txt"); got != "base" {
		t.Fatalf("got %q", got)
	}
	f.wantCode(f.unstage("a.txt"), ErrCodeNotStaged)
}

func TestEditFileOnStagedCreation(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "x"})
	f.mustOK(f.writeFile("new.txt", "hello world"))
	res := f.edit("new.txt", "world", "there")
	f.mustOK(res)
	if !strings.HasSuffix(string(res.Content), "(created)") {
		t.Fatalf("got %q", res.Content)
	}
	if got, _ := f.readStr("new.txt"); got != "hello there" {
		t.Fatalf("got %q", got)
	}
}

func TestEditFileMismatchLeavesStagingUntouched(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "one two two"})
	f.wantCode(f.edit("a.txt", "zzz", "y"), ErrCodeNoMatch)
	f.wantCode(f.edit("a.txt", "two", "y"), ErrCodeAmbiguousMatch)
	f.wantCode(f.edit("a.txt", "", "y"), ErrCodeBadArguments)
	f.wantCode(f.edit("missing.txt", "a", "b"), ErrCodeNotFound)
	if len(f.staged()) != 0 {
		t.Fatalf("staged %v", f.staged())
	}

	f.mustOK(f.writeFile("a.txt", "alpha beta beta"))
	before := f.staged()["a.txt"]
	f.wantCode(f.edit("a.txt", "gamma", "y"), ErrCodeNoMatch)
	f.wantCode(f.edit("a.txt", "beta", "y"), ErrCodeAmbiguousMatch)
	if after := f.staged()["a.txt"]; after != before {
		t.Fatalf("staged hash changed %s -> %s", before, after)
	}
	if got, _ := f.readStr("a.txt"); got != "alpha beta beta" {
		t.Fatalf("got %q", got)
	}
}

func TestStagingPersistsAcrossAttempts(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "base needle"})
	f.mustOK(f.writeFile("new.txt", "created needle"))
	f.mustOK(f.edit("a.txt", "base", "edited"))
	f.nextAttempt()
	if got, _ := f.readStr("new.txt"); got != "created needle" {
		t.Fatalf("got %q", got)
	}
	if got, _ := f.readStr("a.txt"); got != "edited needle" {
		t.Fatalf("got %q", got)
	}
	if got := f.listStr("."); got != "a.txt\nnew.txt\n" {
		t.Fatalf("list %q", got)
	}
	res, _ := f.search(map[string]any{"pattern": "needle"})
	if string(res.Content) != "a.txt:1:edited needle\nnew.txt:1:created needle\n" {
		t.Fatalf("search %q", res.Content)
	}
	f.mustOK(f.edit("new.txt", "created", "revised"))
	f.mustOK(f.unstage("new.txt"))
	if got := f.listStr("."); got != "a.txt\n" {
		t.Fatalf("list after retract %q", got)
	}
	if _, code := f.readStr("new.txt"); code != ErrCodeNotFound {
		t.Fatalf("read after retract: %q", code)
	}
	res, _ = f.search(map[string]any{"pattern": "needle"})
	if string(res.Content) != "a.txt:1:edited needle\n" {
		t.Fatalf("search after retract %q", res.Content)
	}
}

func TestWriteBeneathFileIsRefused(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "base"})
	f.wantCode(f.writeFile("a.txt/b", "child"), ErrCodeNotDirectory)
	f.mustOK(f.writeFile("n", "file"))
	f.wantCode(f.writeFile("n/c", "child"), ErrCodeNotDirectory)
	if st := f.staged(); len(st) != 1 || st["n"] == "" {
		t.Fatalf("staged %v", st)
	}
	if got := f.listStr("."); got != "a.txt\nn\n" {
		t.Fatalf("list %q", got)
	}
	if got, _ := f.readStr("a.txt"); got != "base" {
		t.Fatalf("got %q", got)
	}
	f.wantCode(f.edit("a.txt/b", "x", "y"), ErrCodeNotFound)
}

func TestWriteOverStagedDirectoryIsRefused(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"x": "x"})
	f.mustOK(f.writeFile("d/c", "child"))
	f.wantCode(f.writeFile("d", "file"), ErrCodeIsDirectory)
	if st := f.staged(); len(st) != 1 {
		t.Fatalf("staged %v", st)
	}
	if got := f.listStr("d"); got != "c\n" {
		t.Fatalf("list %q", got)
	}
}

func TestWriteOverExistingDirectoryAndRootIsRefused(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"d/x": "x"})
	f.wantCode(f.writeFile("d", "file"), ErrCodeIsDirectory)
	f.wantCode(f.writeFile(".", "file"), ErrCodeIsDirectory)
	f.wantCode(f.writeFile("/abs", "file"), ErrCodeBadArguments)
	f.wantCode(f.writeFile("../out", "file"), ErrCodeBadArguments)
	f.wantCode(f.writeFile(".env", "secret"), ErrCodeProtected)
	if len(f.staged()) != 0 {
		t.Fatalf("staged %v", f.staged())
	}
}

func TestWriteBeneathRecordedFileThatBecameDirectoryIsRefused(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"file.txt": "base"})
	f.listStr(".") // records file.txt as a file
	p := filepath.Join(f.root, "file.txt")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantCode(f.writeFile("file.txt/child", "x"), ErrCodeSnapshotConflict)
	if len(f.staged()) != 0 {
		t.Fatalf("staged %v", f.staged())
	}
}
