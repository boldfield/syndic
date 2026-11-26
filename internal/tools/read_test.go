package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

type readFixture struct {
	t     *testing.T
	j     *journal.Store
	root  string
	turn  string
	att   string
	tools *ReadTools
	calls int
}

// newReadFixture opens a journal with one turn and attempt over a temporary
// workspace holding files, and read tools with the given result limit.
func newReadFixture(t *testing.T, files map[string]string, limit int) *readFixture {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &readFixture{t: t, j: journal.NewStore(db), root: t.TempDir()}
	for name, body := range files {
		f.write(name, body)
	}
	err = f.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		f.att, err = tx.BeginAttempt(f.turn, "author", "author", 1, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := workspace.OpenConfined(f.root, []string{".git/", ".env*"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	f.tools = NewReadTools(f.j, c, f.turn, f.att, limit)
	return f
}

func (f *readFixture) write(name, body string) {
	f.t.Helper()
	p := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// call accepts a call of tool with args and runs it through exec, returning
// its result and the journaled call.
func (f *readFixture) call(tool string, args any, exec func(string, json.RawMessage) (Result, error)) (Result, journal.ToolCall) {
	f.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		f.t.Fatal(err)
	}
	var id string
	err = f.j.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(f.att, f.calls, fmt.Sprintf("p%d", f.calls), tool, "read", raw)
		id = c.ID
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.calls++
	res, err := exec(id, raw)
	if err != nil {
		f.t.Fatal(err)
	}
	var tc journal.ToolCall
	if err := f.j.Tx(func(tx *journal.Tx) (err error) { tc, err = tx.GetToolCall(id); return err }); err != nil {
		f.t.Fatal(err)
	}
	return res, tc
}

func (f *readFixture) read(args map[string]any) (Result, journal.ToolCall) {
	return f.call(NameReadFile, args, f.tools.ReadFile)
}
func (f *readFixture) list(path string) (Result, journal.ToolCall) {
	return f.call(NameListDir, map[string]any{"path": path}, f.tools.ListDir)
}
func (f *readFixture) search(args map[string]any) (Result, journal.ToolCall) {
	return f.call(NameSearchFiles, args, f.tools.SearchFiles)
}

// journaled asserts the call's one result is the returned bytes, status and
// code exactly, as stored in the journal.
func (f *readFixture) journaled(res Result, tc journal.ToolCall) {
	f.t.Helper()
	if tc.Result == nil {
		f.t.Fatal("call has no journaled result")
	}
	wantStatus := journal.ToolCallExecuted
	if res.ErrorCode != "" {
		wantStatus = journal.ToolCallError
	}
	if tc.Result.Status != wantStatus || tc.Result.ErrorCode != res.ErrorCode {
		f.t.Fatalf("journaled status %q code %q, returned code %q", tc.Result.Status, tc.Result.ErrorCode, res.ErrorCode)
	}
	var body []byte
	if err := f.j.Tx(func(tx *journal.Tx) (err error) { body, err = tx.Content(tc.Result.ResultSHA256); return err }); err != nil {
		f.t.Fatal(err)
	}
	if string(body) != string(res.Content) || tc.Result.ResultByteLen != int64(len(res.Content)) {
		f.t.Fatalf("journaled %q (%d bytes), returned %q", body, tc.Result.ResultByteLen, res.Content)
	}
}

// stage records a staged write of content at path, observing it first as the
// write tools do.
func (f *readFixture) stage(path, content string) {
	f.t.Helper()
	if _, err := observation.NewStore(f.j).Observe(f.turn, f.att, path, f.tools.liveReader(path)); err != nil {
		f.t.Fatal(err)
	}
	if err := f.j.Tx(func(tx *journal.Tx) error {
		_, err := tx.StageWrite(f.turn, path, []byte(content))
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

func TestReadFileWholeAndJournaled(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "hello\nworld\n"}, 0)
	res, tc := f.read(map[string]any{"path": "a.txt"})
	if res.ErrorCode != "" || string(res.Content) != "hello\nworld\n" {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	f.journaled(res, tc)
}

func TestReadFileRangeJournalsExactBytes(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "0123456789"}, 0)
	res, tc := f.read(map[string]any{"path": "a.txt", "offset": 2, "limit": 4})
	if string(res.Content) != "2345" {
		t.Fatalf("got %q", res.Content)
	}
	f.journaled(res, tc)

	// Offset alone reads to the end; a limit past the end is clamped.
	res, tc = f.read(map[string]any{"path": "a.txt", "offset": 7})
	if string(res.Content) != "789" {
		t.Fatalf("got %q", res.Content)
	}
	f.journaled(res, tc)
	res, _ = f.read(map[string]any{"path": "a.txt", "offset": 1, "limit": int64(1<<63 - 1)})
	if string(res.Content) != "123456789" {
		t.Fatalf("huge limit: got %q (%s)", res.Content, res.ErrorCode)
	}
	res, _ = f.read(map[string]any{"path": "a.txt", "offset": 10})
	if res.ErrorCode != "" || len(res.Content) != 0 {
		t.Fatalf("offset at end: got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestReadFileErrorsJournalExactText(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "x", "d/b.txt": "y", ".env": "SECRET=1", ".git/config": "c"}, 0)
	for _, tc := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"missing", map[string]any{"path": "nope.txt"}, ErrCodeNotFound},
		{"directory", map[string]any{"path": "d"}, ErrCodeIsDirectory},
		{"root", map[string]any{"path": "."}, ErrCodeIsDirectory},
		{"escape", map[string]any{"path": "../a.txt"}, ErrCodeBadArguments},
		{"absolute", map[string]any{"path": "/etc/passwd"}, ErrCodeBadArguments},
		{"protected file", map[string]any{"path": ".env"}, ErrCodeProtected},
		{"protected dir", map[string]any{"path": ".git/config"}, ErrCodeProtected},
		{"below a file", map[string]any{"path": "a.txt/x"}, ErrCodeNotFound},
		{"offset past end", map[string]any{"path": "a.txt", "offset": 5}, ErrCodeOutOfRange},
		{"negative offset", map[string]any{"path": "a.txt", "offset": -1}, ErrCodeBadArguments},
		{"zero limit", map[string]any{"path": "a.txt", "limit": 0}, ErrCodeBadArguments},
		{"unknown field", map[string]any{"path": "a.txt", "bogus": 1}, ErrCodeBadArguments},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, call := f.read(tc.args)
			if res.ErrorCode != tc.code || len(res.Content) == 0 {
				t.Fatalf("got code %q text %q, want %q", res.ErrorCode, res.Content, tc.code)
			}
			f.journaled(res, call)
		})
	}
}

func TestReadFileDotDotInsideNameIsAllowed(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a..b": "ok"}, 0)
	if res, _ := f.read(map[string]any{"path": "a..b"}); string(res.Content) != "ok" {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestReadFileOversizeIsSizeErrorNotTruncation(t *testing.T) {
	f := newReadFixture(t, map[string]string{"big.txt": "0123456789", "ok.txt": "0123"}, 8)
	res, tc := f.read(map[string]any{"path": "big.txt"})
	if res.ErrorCode != ErrCodeTooLarge || !strings.Contains(string(res.Content), "10 bytes") || strings.Contains(string(res.Content), "0123456789") {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	f.journaled(res, tc)

	// The model may then ask for a range.
	res, tc = f.read(map[string]any{"path": "big.txt", "offset": 2, "limit": 8})
	if res.ErrorCode != "" || string(res.Content) != "23456789" {
		t.Fatalf("range: got %q (%s)", res.Content, res.ErrorCode)
	}
	f.journaled(res, tc)

	// An explicit range is a range even when its limit equals the cap or is
	// larger than the file; a range over the cap is an error, not a cut.
	res, _ = f.read(map[string]any{"path": "ok.txt", "limit": 8})
	if string(res.Content) != "0123" {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	res, _ = f.read(map[string]any{"path": "big.txt", "limit": 9})
	if res.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("over-cap range: got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestReadFileServedFromRecordNotLiveTree(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "first"}, 0)
	f.read(map[string]any{"path": "a.txt"})
	f.write("a.txt", "second")
	if res, _ := f.read(map[string]any{"path": "a.txt"}); string(res.Content) != "first" {
		t.Fatalf("got %q", res.Content)
	}
	// A name the recorded listing lacks stays absent though the live tree gained it.
	f.list(".")
	f.write("late.txt", "x")
	if res, _ := f.read(map[string]any{"path": "late.txt"}); res.ErrorCode != ErrCodeNotFound {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestReadFileSnapshotConflictWhenListedFileVanishes(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "x"}, 0)
	f.list(".")
	if err := os.Remove(filepath.Join(f.root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	res, tc := f.read(map[string]any{"path": "a.txt"})
	if res.ErrorCode != ErrCodeSnapshotConflict || !strings.Contains(string(res.Content), "a.txt") {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	f.journaled(res, tc)
}

func TestReadFileSymlinkRefused(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "x"}, 0)
	if err := os.Symlink("a.txt", filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	res, _ := f.read(map[string]any{"path": "link"})
	if res.ErrorCode != ErrCodeNotRegular {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestReadFileStagedEditAndCreation(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "base"}, 0)
	f.stage("a.txt", "edited")
	f.stage("new/n.txt", "created")
	if res, _ := f.read(map[string]any{"path": "a.txt"}); string(res.Content) != "edited" {
		t.Fatalf("got %q", res.Content)
	}
	if res, _ := f.read(map[string]any{"path": "new/n.txt", "offset": 3}); string(res.Content) != "ated" {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
}

func TestListDir(t *testing.T) {
	f := newReadFixture(t, map[string]string{"b.txt": "1", "a.txt": "2", "sub/c.txt": "3", ".env": "s", ".git/HEAD": "h"}, 0)
	if err := os.Symlink("a.txt", filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	res, tc := f.list(".")
	if want := "a.txt\nb.txt\nlink\nsub/\n"; string(res.Content) != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
	f.journaled(res, tc)

	// The listing is the record: later live files do not appear, and a staged
	// creation does, along with a directory it implies.
	f.write("late.txt", "x")
	f.stage("sub/d.txt", "s")
	f.stage("fresh/x.txt", "s")
	res, _ = f.list(".")
	if want := "a.txt\nb.txt\nfresh/\nlink\nsub/\n"; string(res.Content) != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
	res, _ = f.list("sub")
	if want := "c.txt\nd.txt\n"; string(res.Content) != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
	res, _ = f.list("fresh")
	if want := "x.txt\n"; string(res.Content) != want {
		t.Fatalf("got %q, want %q", res.Content, want)
	}
}

func TestListDirErrors(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "x", ".git/HEAD": "h"}, 0)
	for path, code := range map[string]string{"a.txt": ErrCodeNotDirectory, "nope": ErrCodeNotFound, ".git": ErrCodeProtected, "..": ErrCodeBadArguments} {
		res, tc := f.list(path)
		if res.ErrorCode != code {
			t.Errorf("%s: got %q (%s), want %s", path, res.Content, res.ErrorCode, code)
		}
		f.journaled(res, tc)
	}
}
