package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/workspace"
)

// stepFixture is one open turn whose author attempt has returned a tool step.
type stepFixture struct {
	store   *journal.Store
	turn    string
	attempt string
	ran     []string // provider call ids whose effect ran, in order
}

func newStepFixture(t *testing.T) *stepFixture {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &stepFixture{store: journal.NewStore(db)}
	err = f.store.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		turn, err := tx.GetTurn(f.turn)
		if err != nil {
			return err
		}
		f.attempt, err = tx.BeginAttempt(f.turn, "author", "author", turn.Epoch, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// accept accepts the attempt's result as a tool step at the given epoch, as
// completeAuthor does; a stale epoch quarantines it.
func (f *stepFixture) accept(t *testing.T, epoch int) bool {
	t.Helper()
	var accepted bool
	err := f.store.Tx(func(tx *journal.Tx) error {
		var err error
		accepted, err = tx.AcceptResult(f.attempt, epoch, "", journal.AttemptOutput{
			RawScrubbed: "{}", Status: string(transport.StatusToolCalls), TransportStatus: transport.TransportStatusOK,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func (f *stepFixture) epoch(t *testing.T) int {
	t.Helper()
	turn, err := f.store.GetTurn(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	return turn.Epoch
}

// cancel ends the turn as Engine.Cancel's terminal transaction does.
func (f *stepFixture) cancel(t *testing.T) {
	t.Helper()
	if err := f.store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.BumpEpoch(f.turn); err != nil {
			return err
		}
		return tx.EndTurn(f.turn, StateCancelled, reasonCancelled)
	}); err != nil {
		t.Fatal(err)
	}
}

// effect is an effect that records it ran and journals content as the
// executed result, as the tools package's effects do.
func (f *stepFixture) effect(content string) toolEffect {
	return func(call journal.ToolCall, _ json.RawMessage, _ func() error) (tools.Result, error) {
		f.ran = append(f.ran, call.ProviderCallID)
		err := f.store.Tx(func(tx *journal.Tx) error {
			_, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte(content), "")
			return err
		})
		return tools.Result{Content: []byte(content)}, err
	}
}

func (f *stepFixture) calls(t *testing.T) []journal.ToolCall {
	t.Helper()
	calls, err := f.store.ToolCallsByTurn(f.turn, "")
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func call(id, name, args string) transport.ToolCall {
	return transport.ToolCall{ID: id, Type: "function", Function: transport.ToolCallFunction{Name: name, Arguments: args}}
}

func readGrant(t *testing.T) []transport.Tool {
	t.Helper()
	defs, err := tools.ForGroups([]string{tools.GroupRead}, false)
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func (f *stepFixture) readEffects() map[string]toolEffect {
	return map[string]toolEffect{
		tools.NameReadFile:    f.effect("file"),
		tools.NameListDir:     f.effect("dir"),
		tools.NameSearchFiles: f.effect("matches"),
	}
}

func wantStatuses(t *testing.T, got []journal.ToolCall, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d calls journaled, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.CallIndex != i || c.Status != want[i] {
			t.Fatalf("call %d (%s %s) index %d status %s, want %s", i, c.ProviderCallID, c.Name, c.CallIndex, c.Status, want[i])
		}
	}
}

// Every failed check answers its call with a tool error and the later calls
// are still checked and run, in the order returned.
func TestToolStepInvalidCallsAnsweredLaterStillRun(t *testing.T) {
	f := newStepFixture(t)
	if !f.accept(t, f.epoch(t)) {
		t.Fatal("tool step not accepted")
	}
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", tools.NameWriteFile, `{"path":"a","content":"x"}`), // not granted to the role
			call("c1", "run_shell", `{"cmd":"ls"}`),                       // in no group
			call("c2", tools.NameReadFile, `{"path":"a"}`),
			call("c3", tools.NameReadFile, `{"path":7}`),               // wrong type
			call("c4", tools.NameReadFile, `{"path":"a","mode":"r"}`),  // unknown argument
			call("c5", tools.NameReadFile, `{"offset":1}`),             // missing path
			call("c6", tools.NameReadFile, `{"path":"a","offset":-1}`), // below minimum
			call("c7", tools.NameListDir, `{"path":"."`),               // does not parse
			call("c8", tools.NameListDir, `{"path":"."}`),
			call("c9", tools.NameSearchFiles, `{"pattern":"x"}`), // over the bound
		},
		granted:   readGrant(t),
		effects:   f.readEffects(),
		remaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != "" || out.used != 2 {
		t.Fatalf("ended %q used %d", out.ended, out.used)
	}
	if len(f.ran) != 2 || f.ran[0] != "c2" || f.ran[1] != "c8" {
		t.Fatalf("effects ran for %v", f.ran)
	}
	E, X := journal.ToolCallError, journal.ToolCallExecuted
	got := f.calls(t)
	wantStatuses(t, got, E, E, X, E, E, E, E, E, X, E)
	codes := []string{toolErrNotOffered, toolErrNotOffered, "", tools.ErrCodeBadArguments, tools.ErrCodeBadArguments,
		tools.ErrCodeBadArguments, tools.ErrCodeBadArguments, tools.ErrCodeBadArguments, "", toolErrCallLimit}
	for i, c := range got {
		if c.Result.ErrorCode != codes[i] || out.results[i].errorCode != codes[i] || out.results[i].call.ID != c.ID {
			t.Fatalf("call %d: journaled code %q, outcome %+v, want %q", i, c.Result.ErrorCode, out.results[i], codes[i])
		}
		body, err := f.store.Content(c.Result.ResultSHA256)
		if err != nil || string(body) != string(out.results[i].content) {
			t.Fatalf("call %d: journaled %q (%v), model receives %q", i, body, err, out.results[i].content)
		}
	}
	if got[0].Class != tools.GroupWrite || got[1].Class != journal.ToolClassNone || got[2].Class != tools.GroupRead {
		t.Fatalf("classes %s %s %s", got[0].Class, got[1].Class, got[2].Class)
	}
}

// A cancel while a call waits inside its effect (a referee decision) is seen
// by the re-check after the wait: that call and every later one are journaled
// abandoned and no later effect runs.
func TestToolStepCancelDuringWaitAbandonsRest(t *testing.T) {
	f := newStepFixture(t)
	f.accept(t, f.epoch(t))
	waited := false
	effects := map[string]toolEffect{
		tools.NameWebFetch: func(c journal.ToolCall, _ json.RawMessage, recheck func() error) (tools.Result, error) {
			f.ran = append(f.ran, c.ProviderCallID)
			f.cancel(t) // the user cancels while the referee is awaited
			waited = true
			if err := recheck(); err != nil {
				return tools.Result{}, err
			}
			t.Fatal("re-check after the wait passed on a cancelled turn")
			return tools.Result{}, nil
		},
		tools.NameWebSearch: f.effect("results"),
	}
	grant, err := tools.ForGroups([]string{tools.GroupWeb}, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", tools.NameWebSearch, `{"query":"first"}`),
			call("c1", tools.NameWebFetch, `{"url":"https://example.com/"}`),
			call("c2", tools.NameWebSearch, `{"query":"after"}`),
			call("c3", tools.NameWebSearch, `{"bad":1}`),
		},
		granted:   grant,
		effects:   effects,
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !waited || out.ended != journal.StepEndedTurn {
		t.Fatalf("waited %t, ended %q", waited, out.ended)
	}
	if len(f.ran) != 2 || f.ran[0] != "c0" || f.ran[1] != "c1" {
		t.Fatalf("effects ran for %v", f.ran)
	}
	A := journal.ToolCallAbandoned
	wantStatuses(t, f.calls(t), journal.ToolCallExecuted, A, A, A)
	for _, r := range out.results[1:] {
		if r.status != A || r.content != nil {
			t.Fatalf("outcome %+v", r)
		}
	}
}

// An epoch change between two calls stops the next call at its re-check
// before its effect.
func TestToolStepEpochChangeBeforeEffect(t *testing.T) {
	f := newStepFixture(t)
	f.accept(t, f.epoch(t))
	effects := f.readEffects()
	effects[tools.NameReadFile] = func(c journal.ToolCall, a json.RawMessage, r func() error) (tools.Result, error) {
		res, err := f.effect("file")(c, a, r)
		if err := f.store.Tx(func(tx *journal.Tx) error {
			_, err := tx.BumpEpoch(f.turn)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res, err
	}
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", tools.NameReadFile, `{"path":"a"}`),
			call("c1", tools.NameListDir, `{"path":"."}`),
		},
		granted:   readGrant(t),
		effects:   effects,
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != journal.StepEndedEpoch || len(f.ran) != 1 {
		t.Fatalf("ended %q, ran %v", out.ended, f.ran)
	}
	wantStatuses(t, f.calls(t), journal.ToolCallExecuted, journal.ToolCallAbandoned)
}

// A step whose turn was cancelled after it was accepted and before execution
// began journals its intent and abandons every call.
func TestToolStepCancelledBeforeExecutionRunsNothing(t *testing.T) {
	f := newStepFixture(t)
	f.accept(t, f.epoch(t))
	f.cancel(t)
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`), call("c1", tools.NameListDir, `{"path":"."}`)},
		granted:   readGrant(t),
		effects:   f.readEffects(),
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != journal.StepEndedTurn || len(f.ran) != 0 {
		t.Fatalf("ended %q, ran %v", out.ended, f.ran)
	}
	wantStatuses(t, f.calls(t), journal.ToolCallAbandoned, journal.ToolCallAbandoned)
}

// A late tool step is quarantined at acceptance and executes nothing: no call
// is journaled and no effect runs. So is a step never accepted at all.
func TestLateToolStepExecutesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *stepFixture)
	}{
		{"quarantined", func(t *testing.T, f *stepFixture) {
			if f.accept(t, f.epoch(t)-1) {
				t.Fatal("late step accepted")
			}
		}},
		{"after the turn ended", func(t *testing.T, f *stepFixture) {
			epoch := f.epoch(t)
			f.cancel(t)
			if f.accept(t, epoch) {
				t.Fatal("step after cancel accepted")
			}
		}},
		{"pending", func(t *testing.T, f *stepFixture) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStepFixture(t)
			tc.prepare(t, f)
			_, err := runToolStep(f.store, toolStep{
				attemptID: f.attempt,
				calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`)},
				granted:   readGrant(t),
				effects:   f.readEffects(),
				remaining: 10,
			})
			if !errors.Is(err, journal.ErrNotToolStep) {
				t.Fatalf("err = %v", err)
			}
			if len(f.ran) != 0 || len(f.calls(t)) != 0 {
				t.Fatalf("ran %v, journaled %d calls", f.ran, len(f.calls(t)))
			}
		})
	}
}

// A step is executed once; a step whose calls cannot be told apart is not
// executed at all.
func TestToolStepRefusals(t *testing.T) {
	f := newStepFixture(t)
	f.accept(t, f.epoch(t))
	step := toolStep{
		attemptID: f.attempt,
		calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`), call("c0", tools.NameListDir, `{"path":"."}`)},
		granted:   readGrant(t),
		effects:   f.readEffects(),
		remaining: 10,
	}
	if _, err := runToolStep(f.store, step); !errors.Is(err, errToolStepIDs) {
		t.Fatalf("duplicate ids: err = %v", err)
	}
	step.calls[1].ID = ""
	if _, err := runToolStep(f.store, step); !errors.Is(err, errToolStepIDs) {
		t.Fatalf("empty id: err = %v", err)
	}
	if len(f.calls(t)) != 0 || len(f.ran) != 0 {
		t.Fatal("refused step journaled or ran")
	}
	step.calls[1].ID = "c1"
	if _, err := runToolStep(f.store, step); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolStep(f.store, step); !errors.Is(err, journal.ErrToolStepRecorded) {
		t.Fatalf("second run: err = %v", err)
	}
	if len(f.ran) != 2 || len(f.calls(t)) != 2 {
		t.Fatalf("ran %v", f.ran)
	}
	delete(step.effects, tools.NameSearchFiles)
	if _, err := runToolStep(f.store, step); err == nil {
		t.Fatal("granted tool without an effect accepted")
	}
}

// The read tools are effects as they stand: they journal their own result
// against the accepted call, and a tool error of theirs is the call's error.
func TestToolStepRunsReadTools(t *testing.T) {
	f := newStepFixture(t)
	f.accept(t, f.epoch(t))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := workspace.OpenConfined(root, []string{".env*"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	rt := tools.NewReadTools(f.store, fs, f.turn, f.attempt, 0)
	sync := func(fn func(string, json.RawMessage) (tools.Result, error)) toolEffect {
		return func(c journal.ToolCall, a json.RawMessage, _ func() error) (tools.Result, error) { return fn(c.ID, a) }
	}
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a.txt"}`), call("c1", tools.NameReadFile, `{"path":"missing"}`)},
		granted:   readGrant(t),
		effects: map[string]toolEffect{
			tools.NameReadFile: sync(rt.ReadFile), tools.NameListDir: sync(rt.ListDir), tools.NameSearchFiles: sync(rt.SearchFiles),
		},
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses(t, f.calls(t), journal.ToolCallExecuted, journal.ToolCallError)
	if string(out.results[0].content) != "hello\n" || out.results[1].errorCode != tools.ErrCodeNotFound || out.results[1].status != journal.ToolCallError {
		t.Fatalf("outcomes %+v", out.results)
	}
}

func TestValidateToolArgs(t *testing.T) {
	read, _, _ := tools.Lookup(tools.NameReadFile)
	params := read.Function.Parameters
	for _, ok := range []string{`{"path":"a"}`, ` {"path":"a","offset":0,"limit":1} `, `{"path":"a","limit":2.0e0}`} {
		if _, err := validateToolArgs(params, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{``, `[]`, `"a"`, `null`, `{"path":"a"} {}`, `{"path":"a"}x`, `{"path":null}`,
		`{"path":"a","limit":0}`, `{"path":"a","offset":1.5}`, `{"path":"a","offset":"1"}`, `{}`} {
		if _, err := validateToolArgs(params, bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
