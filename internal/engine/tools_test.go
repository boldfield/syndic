package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/config"
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
// completeAuthor does; a stale epoch or an ended turn quarantines it.
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

// acceptNow accepts the step at the turn's current epoch and fails the test
// if it is not accepted.
func (f *stepFixture) acceptNow(t *testing.T) {
	t.Helper()
	if !f.accept(t, f.epoch(t)) {
		t.Fatal("tool step not accepted")
	}
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

// bumpEpoch moves the turn past the attempt's epoch without ending it, as a
// stored candidate does.
func (f *stepFixture) bumpEpoch(t *testing.T) {
	t.Helper()
	if err := f.store.Tx(func(tx *journal.Tx) error {
		_, err := tx.BumpEpoch(f.turn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// effect is an effect that records that it ran and journals content as the
// executed result, as the tools package's methods do.
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

func (f *stepFixture) readEffects() map[string]toolEffect {
	return map[string]toolEffect{
		tools.NameReadFile:    f.effect("file"),
		tools.NameListDir:     f.effect("dir"),
		tools.NameSearchFiles: f.effect("matches"),
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
	return transport.ToolCall{ID: id, Type: toolCallType, Function: transport.ToolCallFunction{Name: name, Arguments: args}}
}

func grant(t *testing.T, groups ...string) []transport.Tool {
	t.Helper()
	defs, err := tools.ForGroups(groups, true)
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

// wantStatuses checks the journaled calls' order and statuses.
func wantStatuses(t *testing.T, got []journal.ToolCall, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d calls journaled, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.CallIndex != i || c.Status != want[i] {
			t.Fatalf("call %d (%s %s): index %d status %s, want %s", i, c.ProviderCallID, c.Name, c.CallIndex, c.Status, want[i])
		}
	}
}

// Every call failing a check is answered with a tool error, journaled with its
// code, and the later calls are still checked and run, in the order returned.
// Invalid calls do not count against the bound; the bound answers the call it
// stops with a tool error too.
func TestToolStepInvalidCallsAnsweredLaterStillRun(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
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
		granted:   grant(t, tools.GroupRead),
		effects:   f.readEffects(),
		remaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != "" || out.used != 2 {
		t.Fatalf("ended %q, used %d", out.ended, out.used)
	}
	if len(f.ran) != 2 || f.ran[0] != "c2" || f.ran[1] != "c8" {
		t.Fatalf("effects ran for %v", f.ran)
	}
	E, X := journal.ToolCallError, journal.ToolCallExecuted
	got := f.calls(t)
	wantStatuses(t, got, E, E, X, E, E, E, E, E, X, E)
	codes := []string{toolErrNotOffered, toolErrNotOffered, "", tools.ErrCodeBadArguments, tools.ErrCodeBadArguments,
		tools.ErrCodeBadArguments, tools.ErrCodeBadArguments, tools.ErrCodeBadArguments, "", toolErrCallLimit}
	if len(out.results) != len(got) {
		t.Fatalf("%d outcomes, %d calls", len(out.results), len(got))
	}
	for i, c := range got {
		r := out.results[i]
		if c.Result == nil || c.Result.ErrorCode != codes[i] || r.errorCode != codes[i] || r.call.ID != c.ID || r.status != c.Status {
			t.Fatalf("call %d: journaled %+v, outcome %+v, want code %q", i, c.Result, r, codes[i])
		}
		body, err := f.store.Content(c.Result.ResultSHA256)
		if err != nil || string(body) != string(r.content) || len(body) == 0 {
			t.Fatalf("call %d: journaled %q (%v), model receives %q", i, body, err, r.content)
		}
	}
	if got[0].Class != tools.GroupWrite || got[1].Class != journal.ToolClassNone || got[2].Class != tools.GroupRead {
		t.Fatalf("classes %s %s %s", got[0].Class, got[1].Class, got[2].Class)
	}
}

// A call with no function name, or that is not a function call although it
// names a granted tool, is answered with a malformed-call tool error and has
// no effect; the step is still journaled whole and the later valid call runs.
func TestToolStepMalformedCallsAnsweredLaterStillRun(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
	notFunction := call("c1", tools.NameReadFile, `{"path":"a"}`)
	notFunction.Type = "custom"
	untyped := call("c2", tools.NameReadFile, `{"path":"a"}`)
	untyped.Type = ""
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", "", `{"path":"a"}`),
			notFunction,
			untyped,
			call("c3", tools.NameReadFile, `{"path":"a"}`),
		},
		granted:   grant(t, tools.GroupRead),
		effects:   f.readEffects(),
		remaining: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != "" || out.used != 1 {
		t.Fatalf("ended %q, used %d", out.ended, out.used)
	}
	if len(f.ran) != 1 || f.ran[0] != "c3" {
		t.Fatalf("effects ran for %v", f.ran)
	}
	E := journal.ToolCallError
	got := f.calls(t)
	wantStatuses(t, got, E, E, E, journal.ToolCallExecuted)
	for i, c := range got[:3] {
		if c.Result.ErrorCode != toolErrMalformed || out.results[i].errorCode != toolErrMalformed {
			t.Fatalf("call %d: journaled code %q, outcome %+v", i, c.Result.ErrorCode, out.results[i])
		}
		if c.Class != journal.ToolClassNone {
			t.Fatalf("call %d class %s, want none", i, c.Class)
		}
	}
	if got[0].Name != "" || got[1].Name != tools.NameReadFile || got[3].Class != tools.GroupRead {
		t.Fatalf("calls %+v", got)
	}
}

// A cancel while a call waits inside its effect (a referee decision, a fetch)
// is seen by the re-check after the wait: that call and every later one are
// journaled abandoned, no later effect runs, and the step reports the end.
func TestToolStepCancelDuringWaitAbandonsRest(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
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
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", tools.NameWebSearch, `{"query":"first"}`),
			call("c1", tools.NameWebFetch, `{"url":"https://example.com/"}`),
			call("c2", tools.NameWebSearch, `{"query":"after"}`),
			call("c3", tools.NameWebSearch, `{"bad":1}`), // not even checked
		},
		granted:   grant(t, tools.GroupWeb),
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
	got := f.calls(t)
	wantStatuses(t, got, journal.ToolCallExecuted, A, A, A)
	for i, r := range out.results[1:] {
		if r.status != A || r.content != nil || r.errorCode != "" || got[i+1].Result.ResultSHA256 != "" {
			t.Fatalf("outcome %+v, journaled %+v", r, got[i+1].Result)
		}
	}
}

// An epoch change between two calls stops the next call at its re-check,
// before its effect.
func TestToolStepEpochChangeBeforeEffect(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
	effects := f.readEffects()
	effects[tools.NameReadFile] = func(c journal.ToolCall, a json.RawMessage, r func() error) (tools.Result, error) {
		res, err := f.effect("file")(c, a, r)
		f.bumpEpoch(t)
		return res, err
	}
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls: []transport.ToolCall{
			call("c0", tools.NameReadFile, `{"path":"a"}`),
			call("c1", tools.NameListDir, `{"path":"."}`),
			call("c2", tools.NameListDir, `{"path":"b"}`),
		},
		granted:   grant(t, tools.GroupRead),
		effects:   effects,
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ended != journal.StepEndedEpoch || len(f.ran) != 1 {
		t.Fatalf("ended %q, ran %v", out.ended, f.ran)
	}
	wantStatuses(t, f.calls(t), journal.ToolCallExecuted, journal.ToolCallAbandoned, journal.ToolCallAbandoned)
}

// A step whose turn was cancelled after acceptance and before execution began
// journals its intent and abandons every call; nothing runs.
func TestToolStepCancelledBeforeExecutionRunsNothing(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
	f.cancel(t)
	out, err := runToolStep(f.store, toolStep{
		attemptID: f.attempt,
		calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`), call("c1", tools.NameListDir, `{"path":"."}`)},
		granted:   grant(t, tools.GroupRead),
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
// is journaled and no effect runs. Nor does a step never accepted, or one
// the gate failed.
func TestLateToolStepExecutesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *stepFixture)
	}{
		{"stale epoch", func(t *testing.T, f *stepFixture) {
			epoch := f.epoch(t)
			f.bumpEpoch(t)
			if f.accept(t, epoch) {
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
		{"pending", func(*testing.T, *stepFixture) {}},
		{"gate failed", func(t *testing.T, f *stepFixture) {
			f.acceptNow(t)
			if err := f.store.Tx(func(tx *journal.Tx) error { return tx.SetAttemptGate(f.attempt, "identity mismatch") }); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStepFixture(t)
			tc.prepare(t, f)
			_, err := runToolStep(f.store, toolStep{
				attemptID: f.attempt,
				calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`)},
				granted:   grant(t, tools.GroupRead),
				effects:   f.readEffects(),
				remaining: 10,
			})
			if !errors.Is(err, journal.ErrNotToolStep) {
				t.Fatalf("err = %v, want ErrNotToolStep", err)
			}
			if len(f.ran) != 0 || len(f.calls(t)) != 0 {
				t.Fatalf("ran %v, journaled %d calls", f.ran, len(f.calls(t)))
			}
		})
	}
}

// A step is executed once; a step whose calls cannot be told apart, or whose
// grant has a tool with no effect, is not executed at all.
func TestToolStepRefusals(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
	step := toolStep{
		attemptID: f.attempt,
		calls:     []transport.ToolCall{call("c0", tools.NameReadFile, `{"path":"a"}`), call("c0", tools.NameListDir, `{"path":"."}`)},
		granted:   grant(t, tools.GroupRead),
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
	step.calls[1].ID = "c1"
	delete(step.effects, tools.NameSearchFiles)
	if _, err := runToolStep(f.store, step); err == nil {
		t.Fatal("granted tool without an effect accepted")
	}
	if len(f.calls(t)) != 0 || len(f.ran) != 0 {
		t.Fatal("refused step journaled or ran")
	}
	step.effects = f.readEffects()
	if _, err := runToolStep(f.store, step); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolStep(f.store, step); !errors.Is(err, journal.ErrToolStepRecorded) {
		t.Fatalf("second run: err = %v", err)
	}
	if len(f.ran) != 2 || len(f.calls(t)) != 2 {
		t.Fatalf("ran %v, journaled %d", f.ran, len(f.calls(t)))
	}
}

// The read tools are effects as they stand: they journal their own result
// against the accepted call, a tool error of theirs is the call's error, and
// a schema-valid integer spelled as a whole float decodes into their integer
// arguments, so it executes rather than failing as bad arguments.
func TestToolStepRunsReadTools(t *testing.T) {
	f := newStepFixture(t)
	f.acceptNow(t)
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
		calls: []transport.ToolCall{
			call("c0", tools.NameReadFile, `{"path":"a.txt"}`),
			call("c1", tools.NameReadFile, `{"path":"missing"}`),
			call("c2", tools.NameReadFile, `{"path":"a.txt","limit":2.0e0,"offset":1.0}`),
			call("c3", tools.NameReadFile, `{"path":"a.txt","limit":1.5}`),
			call("c4", tools.NameListDir, `{"path":"."}`),
		},
		granted: grant(t, tools.GroupRead),
		effects: map[string]toolEffect{
			tools.NameReadFile: sync(rt.ReadFile), tools.NameListDir: sync(rt.ListDir), tools.NameSearchFiles: sync(rt.SearchFiles),
		},
		remaining: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	X, E := journal.ToolCallExecuted, journal.ToolCallError
	got := f.calls(t)
	wantStatuses(t, got, X, E, X, E, X)
	if string(out.results[0].content) != "hello\n" || out.results[1].errorCode != tools.ErrCodeNotFound {
		t.Fatalf("outcomes %+v", out.results[:2])
	}
	if string(out.results[2].content) != "el" || out.results[2].errorCode != "" {
		t.Fatalf("ranged read with float-spelled integers: %+v", out.results[2])
	}
	if out.results[3].errorCode != tools.ErrCodeBadArguments {
		t.Fatalf("fractional limit: %+v", out.results[3])
	}
	if string(out.results[4].content) != "a.txt\n" {
		t.Fatalf("list_dir: %q", out.results[4].content)
	}
	// The journal keeps the arguments as the model sent them.
	body, err := f.store.Content(got[2].ArgsSHA256)
	if err != nil || string(body) != `{"path":"a.txt","limit":2.0e0,"offset":1.0}` {
		t.Fatalf("journaled args %q, %v", body, err)
	}
}

// validateToolArgs accepts what the schema admits, refuses the rest, and
// returns the arguments normalized: integer properties as plain integers,
// keys sorted, strings as they were.
func TestValidateToolArgs(t *testing.T) {
	read, _, _ := tools.Lookup(tools.NameReadFile)
	params := read.Function.Parameters
	for _, tc := range []struct{ in, want string }{
		{`{"path":"a"}`, `{"path":"a"}`},
		{` {"path":"a","offset":0,"limit":1} `, `{"limit":1,"offset":0,"path":"a"}`},
		{`{"path":"a","limit":2.0e0}`, `{"limit":2,"path":"a"}`},
		{`{"path":"a","offset":1E2}`, `{"offset":100,"path":"a"}`},
		{`{"path":"<b>&\"é\"","limit":9007199254740992}`, `{"limit":9007199254740992,"path":"<b>&\"é\""}`},
	} {
		got, err := validateToolArgs(params, tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s normalized to %s, want %s", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{``, `[]`, `"a"`, `null`, `{"path":"a"} {}`, `{"path":"a"}x`, `{"path":null}`, `{}`,
		`{"path":"a","limit":0}`, `{"path":"a","offset":-1}`, `{"path":"a","offset":1.5}`, `{"path":"a","offset":"1"}`,
		`{"path":"a","limit":1e300}`, `{"path":"a","limit":true}`, `{"path":"a","extra":1}`} {
		if _, err := validateToolArgs(params, bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	write, _, _ := tools.Lookup(tools.NameWriteFile)
	got, err := validateToolArgs(write.Function.Parameters, `{"content":"line\n\ttab","path":"p"}`)
	if err != nil || string(got) != `{"content":"line\n\ttab","path":"p"}` {
		t.Fatalf("write args: %s, %v", got, err)
	}
}

// stubSearchProvider is a fake SearchProvider for testing web_search integration.
type stubSearchProvider struct {
	queries []string
	results map[string]string
	err     error
}

func (s *stubSearchProvider) Search(ctx context.Context, query string) (*http.Response, error) {
	s.queries = append(s.queries, query)
	if s.err != nil {
		return nil, s.err
	}
	result, ok := s.results[query]
	if !ok {
		result = "No results found"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(result)),
	}, nil
}

func TestWebSearchNotOfferedWithoutProvider(t *testing.T) {
	f := newStepFixture(t)
	e := &Engine{store: f.store, searchProviderInst: nil, roster: &config.Roster{}}
	defs, _, _ := e.authorToolDefs(&openTurn{
		eff: config.Effective{Preset: config.Preset{Tools: []string{tools.GroupWeb}}},
	})
	for _, d := range defs {
		if d.Function.Name == tools.NameWebSearch {
			t.Fatal("web_search offered when no provider configured")
		}
	}
}

func TestWebSearchIsIncludedInForGroups(t *testing.T) {
	defs, err := tools.ForGroups([]string{tools.GroupWeb}, true)
	if err != nil {
		t.Fatalf("ForGroups failed: %v", err)
	}
	var found bool
	for _, d := range defs {
		if d.Function.Name == tools.NameWebSearch {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("web_search not included in ForGroups output with provider=true")
	}
}

func TestWebSearchEffectReturnsResultFromProvider(t *testing.T) {
	f := newStepFixture(t)
	provider := &stubSearchProvider{
		results: map[string]string{
			"go language": "Title: Go\nURL: https://golang.org",
		},
	}
	e := &Engine{store: f.store, searchProviderInst: provider}
	effect := e.webSearchEffect("", "", nil)
	args := json.RawMessage(`{"query":"go language"}`)
	result, err := effect(journal.ToolCall{ID: "call1", Name: tools.NameWebSearch}, args, func() error { return nil })
	if err != nil {
		t.Fatalf("effect failed: %v", err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("got error code %q", result.ErrorCode)
	}
	if !strings.Contains(string(result.Content), "golang.org") {
		t.Fatalf("result missing expected content: %s", result.Content)
	}
	if len(provider.queries) != 1 || provider.queries[0] != "go language" {
		t.Fatalf("provider not called with correct query: %v", provider.queries)
	}
}

func TestWebSearchEffectReturnsErrorWithoutProvider(t *testing.T) {
	f := newStepFixture(t)
	e := &Engine{store: f.store, searchProviderInst: nil}
	effect := e.webSearchEffect("", "", nil)
	args := json.RawMessage(`{"query":"test"}`)
	result, err := effect(journal.ToolCall{ID: "call1", Name: tools.NameWebSearch}, args, func() error { return nil })
	if err != nil {
		t.Fatalf("effect failed: %v", err)
	}
	if result.ErrorCode != tools.ErrCodeSearchRefused {
		t.Fatalf("got error code %q, want %q", result.ErrorCode, tools.ErrCodeSearchRefused)
	}
}
