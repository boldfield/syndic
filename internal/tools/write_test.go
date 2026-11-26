package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/workspace"
)

type writeFixture struct {
	t     *testing.T
	j     *journal.Store
	root  string
	turn  string
	att   string
	tools *WriteTools
	fs    *workspace.Confined
	calls int
}

// newWriteFixture opens a journal with one turn and attempt over a temporary
// workspace holding files, and write tools.
func newWriteFixture(t *testing.T, files map[string]string) *writeFixture {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &writeFixture{t: t, j: journal.NewStore(db), root: t.TempDir()}
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
	f.fs = c
	f.tools = NewWriteTools(f.j, c, f.turn, f.att)
	return f
}

func (f *writeFixture) write(name, body string) {
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
func (f *writeFixture) call(tool string, args any, exec func(string, json.RawMessage) (Result, error)) (Result, journal.ToolCall) {
	f.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		f.t.Fatal(err)
	}
	var callID string
	if err := f.j.Tx(func(tx *journal.Tx) error {
		call, err := tx.RecordToolCall(f.att, f.calls, fmt.Sprintf("call_%d", f.calls), tool, "write", raw)
		callID = call.ID
		f.calls++
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	res, err := exec(callID, raw)
	if err != nil {
		f.t.Fatal(err)
	}
	var tc journal.ToolCall
	if err := f.j.Tx(func(tx *journal.Tx) error {
		var err error
		tc, err = tx.GetToolCall(callID)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return res, tc
}

func (f *writeFixture) write_(args any) (Result, journal.ToolCall) {
	f.t.Helper()
	return f.call("write_file", args, f.tools.WriteFile)
}

func (f *writeFixture) edit(args any) (Result, journal.ToolCall) {
	f.t.Helper()
	return f.call("edit_file", args, f.tools.EditFile)
}

func (f *writeFixture) unstage(args any) (Result, journal.ToolCall) {
	f.t.Helper()
	return f.call("unstage", args, f.tools.Unstage)
}

// observe records an observation of a path as the read tools do.
func (f *writeFixture) observe(path string) {
	f.t.Helper()
	if _, err := f.tools.observePath(path); err != nil {
		f.t.Fatal(err)
	}
}

// staged returns the current staging map.
func (f *writeFixture) staged() map[string]string {
	f.t.Helper()
	m, err := f.tools.loadStaging()
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestWriteFileCreation(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	res, tc := f.write_(map[string]any{"path": "new.txt", "content": "hello"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging := f.staged()
	if len(staging) != 1 || staging["new.txt"] == "" {
		t.Fatalf("staging = %v", staging)
	}
}

func TestWriteFileReplacement(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "old"})
	f.observe("a.txt")
	res, tc := f.write_(map[string]any{"path": "a.txt", "content": "new"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging := f.staged()
	if len(staging) != 1 || staging["a.txt"] == "" {
		t.Fatalf("staging = %v", staging)
	}
}

func TestWriteFileCreationRequiresObservation(t *testing.T) {
	f := newWriteFixture(t, nil)
	res, tc := f.write_(map[string]any{"path": "new.txt", "content": "hello"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s), want success", res.Content, res.ErrorCode)
	}
}

func TestWriteFileProtected(t *testing.T) {
	f := newWriteFixture(t, map[string]string{".env": "secret"})
	res, tc := f.write_(map[string]any{"path": ".env", "content": "new"})
	if res.ErrorCode != ErrCodeProtected || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want protected error", res.Content, res.ErrorCode)
	}
}

func TestWriteFileDirectory(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "content"})
	f.observe(".")
	res, tc := f.write_(map[string]any{"path": ".", "content": "new"})
	if res.ErrorCode != ErrCodeIsDirectory || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want directory error", res.Content, res.ErrorCode)
	}
}

func TestEditFileReplaces(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "hello world"})
	f.observe("a.txt")
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "world", "new": "there"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging := f.staged()
	if len(staging) != 1 || staging["a.txt"] == "" {
		t.Fatalf("staging = %v", staging)
	}
	body, err := f.tools.content(staging["a.txt"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello there" {
		t.Fatalf("got %q", body)
	}
}

func TestEditFileStagedContent(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "v1"})
	f.observe("a.txt")
	f.write_(map[string]any{"path": "a.txt", "content": "v2"})
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "v2", "new": "v3"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging := f.staged()
	body, err := f.tools.content(staging["a.txt"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v3" {
		t.Fatalf("got %q", body)
	}
}

func TestEditFileNotFound(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	res, tc := f.edit(map[string]any{"path": "nope.txt", "old": "x", "new": "y"})
	if res.ErrorCode != ErrCodeNotFound || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want not found", res.Content, res.ErrorCode)
	}
}

func TestEditFileOldNotFound(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "content"})
	f.observe("a.txt")
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "nope", "new": "y"})
	if res.ErrorCode != ErrCodeBadArguments || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want bad arguments", res.Content, res.ErrorCode)
	}
}

func TestEditFileOldMultipleMatches(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "foo foo foo"})
	f.observe("a.txt")
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "foo", "new": "bar"})
	if res.ErrorCode != ErrCodeBadArguments || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want bad arguments", res.Content, res.ErrorCode)
	}
}

func TestEditFileDirectory(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "content"})
	f.observe(".")
	res, tc := f.edit(map[string]any{"path": ".", "old": "x", "new": "y"})
	if res.ErrorCode != ErrCodeIsDirectory || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want directory error", res.Content, res.ErrorCode)
	}
}

func TestUnstageRemoves(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "content"})
	f.observe("a.txt")
	f.write_(map[string]any{"path": "a.txt", "content": "new"})
	staging := f.staged()
	if len(staging) != 1 {
		t.Fatalf("before unstage: %v", staging)
	}
	res, tc := f.unstage(map[string]any{"path": "a.txt"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging = f.staged()
	if len(staging) != 0 {
		t.Fatalf("after unstage: %v", staging)
	}
}

func TestUnstageNotStaged(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	res, tc := f.unstage(map[string]any{"path": "nope.txt"})
	if res.ErrorCode != ErrCodeBadArguments || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want bad arguments", res.Content, res.ErrorCode)
	}
}

func TestUnstageAllowsRereading(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "original"})
	f.observe("a.txt")
	staging := f.staged()
	if len(staging) != 0 {
		t.Fatalf("before write: staging = %v", staging)
	}
	f.write_(map[string]any{"path": "a.txt", "content": "staged"})
	staging = f.staged()
	if len(staging) != 1 {
		t.Fatalf("after write: staging = %v", staging)
	}
	f.unstage(map[string]any{"path": "a.txt"})
	staging = f.staged()
	if len(staging) != 0 {
		t.Fatalf("after unstage: staging = %v", staging)
	}
}

func TestWriteFileJournalsChange(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	res, call := f.write_(map[string]any{"path": "new.txt", "content": "hello"})
	if res.ErrorCode != "" {
		t.Fatalf("got error %q", res.ErrorCode)
	}
	if call.Status != journal.ToolCallExecuted || call.Result == nil {
		t.Fatalf("call status = %s, result = %v", call.Status, call.Result)
	}
}

func TestEditFileJournalsChange(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "hello world"})
	f.observe("a.txt")
	res, call := f.edit(map[string]any{"path": "a.txt", "old": "world", "new": "there"})
	if res.ErrorCode != "" {
		t.Fatalf("got error %q", res.ErrorCode)
	}
	if call.Status != journal.ToolCallExecuted || call.Result == nil {
		t.Fatalf("call status = %s, result = %v", call.Status, call.Result)
	}
}

func TestEditFileStagedCreation(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	f.write_(map[string]any{"path": "new.txt", "content": "hello world"})
	res, tc := f.edit(map[string]any{"path": "new.txt", "old": "world", "new": "there"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("edit created file: got %q (%s)", res.Content, res.ErrorCode)
	}
	staging := f.staged()
	body, err := f.tools.content(staging["new.txt"])
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello there" {
		t.Fatalf("got %q, want %q", body, "hello there")
	}
}

func TestEditFileCreatedMessageFormat(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	f.write_(map[string]any{"path": "new.txt", "content": "hello world"})
	res, tc := f.edit(map[string]any{"path": "new.txt", "old": "world", "new": "there"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	if !strings.Contains(string(res.Content), "created") {
		t.Fatalf("got %q, want message with 'created'", res.Content)
	}
}

func TestEditFileEmptyOld(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": ""})
	f.observe("a.txt")
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "", "new": "x"})
	if res.ErrorCode != ErrCodeBadArguments || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want bad arguments", res.Content, res.ErrorCode)
	}
}

func TestEditFileMismatchDoesNotMutateStagingMap(t *testing.T) {
	f := newWriteFixture(t, map[string]string{"a.txt": "content"})
	f.observe("a.txt")
	stagingBefore := f.staged()
	res, tc := f.edit(map[string]any{"path": "a.txt", "old": "nope", "new": "y"})
	if res.ErrorCode != ErrCodeBadArguments || tc.Status != journal.ToolCallError {
		t.Fatalf("got %q (%s), want bad arguments", res.Content, res.ErrorCode)
	}
	stagingAfter := f.staged()
	if len(stagingBefore) != 0 || len(stagingAfter) != 0 {
		t.Fatalf("staging before = %v, after = %v, both should be empty", stagingBefore, stagingAfter)
	}
}

func TestUnstageCreatedFile(t *testing.T) {
	f := newWriteFixture(t, nil)
	f.observe(".")
	f.write_(map[string]any{"path": "new.txt", "content": "hello"})
	staging := f.staged()
	if len(staging) != 1 {
		t.Fatalf("before unstage: %v", staging)
	}
	res, tc := f.unstage(map[string]any{"path": "new.txt"})
	if res.ErrorCode != "" || tc.Status != journal.ToolCallExecuted {
		t.Fatalf("got %q (%s)", res.Content, res.ErrorCode)
	}
	staging = f.staged()
	if len(staging) != 0 {
		t.Fatalf("after unstage: %v", staging)
	}
}

func TestCreationVisibleInListThenRemovedOnUnstage(t *testing.T) {
	wf := newWriteFixture(t, nil)
	wf.observe(".")
	wf.write_(map[string]any{"path": "new.txt", "content": "hello"})

	readTools := NewReadTools(wf.j, wf.fs, wf.turn, wf.att, 0)

	raw, err := json.Marshal(map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	callIdx := wf.calls
	if err := wf.j.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(wf.att, callIdx, fmt.Sprintf("p%d", callIdx), "list_dir", "read", raw)
		id = c.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wf.calls++

	res, err := readTools.ListDir(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("listDir before unstage failed: %q", res.ErrorCode)
	}
	if !strings.Contains(string(res.Content), "new.txt") {
		t.Fatalf("new.txt not in listing before unstage: %q", res.Content)
	}

	wf.unstage(map[string]any{"path": "new.txt"})

	raw, err = json.Marshal(map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	callIdx = wf.calls
	if err := wf.j.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(wf.att, callIdx, fmt.Sprintf("p%d", callIdx), "list_dir", "read", raw)
		id = c.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wf.calls++

	res, err = readTools.ListDir(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("listDir after unstage failed: %q", res.ErrorCode)
	}
	if strings.Contains(string(res.Content), "new.txt") {
		t.Fatalf("new.txt still in listing after unstage: %q", res.Content)
	}
}

func TestWorkspaceByteIdenticalAfterStaging(t *testing.T) {
	content := "original file content"
	f := newWriteFixture(t, map[string]string{"file.txt": content})
	f.observe("file.txt")

	origBody, err := os.ReadFile(filepath.Join(f.root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}

	f.write_(map[string]any{"path": "file.txt", "content": "modified content"})

	finalBody, err := os.ReadFile(filepath.Join(f.root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if string(origBody) != string(finalBody) {
		t.Fatalf("workspace file changed: was %q, now %q", origBody, finalBody)
	}
}
