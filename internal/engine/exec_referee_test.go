package engine_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

const commandAllow = "allow the command reads files"

// commandRun is a turn whose author's tool step puts bash calls to the
// command referee from inside its effect, with the author's slot held.
type commandRun struct {
	h       *enginetest.Harness
	turn    string
	mu      sync.Mutex
	outs    []engine.RefereeOutcome
	errs    []error
	callIDs []string
	// before is the turn's snapshot version and observation count at the
	// decision's start.
	versionBefore, observedBefore int
}

func (r *commandRun) done(n int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outs) >= n
}

func (r *commandRun) journalBash(t *testing.T, attemptID string, index int, command string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"command": command})
	var id string
	err := r.h.Store.Tx(func(tx *journal.Tx) error {
		rec, err := tx.RecordToolCall(attemptID, index, "b"+strconv.Itoa(index), tools.NameBash, journal.ToolClassExec, args)
		id = rec.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *commandRun) snapshot(t *testing.T, turnID string) (version, observed int) {
	t.Helper()
	err := r.h.Store.Tx(func(tx *journal.Tx) error {
		var err error
		if version, err = tx.SnapshotVersion(turnID); err != nil {
			return err
		}
		obs, err := tx.Observations(turnID)
		observed = len(obs)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return version, observed
}

// runCommand submits the web preset (it grants read) with a workspace holding
// a.txt, which the record holds. The author's tool step puts each of commands
// to the referee, answered by replies in order; prior are bash commands
// journaled before them.
func runCommand(t *testing.T, roster string, opts []enginetest.Option, replies []fake.Step, prior []string, commands ...string) *commandRun {
	t.Helper()
	run := &commandRun{}
	var once sync.Once
	hook := func(turnID, attemptID, _ string) {
		once.Do(func() {
			err := run.h.Store.Tx(func(tx *journal.Tx) error {
				if _, err := tx.RecordObservation(turnID, attemptID, "a.txt", journal.ObservationFile, []byte("hello from a\n")); err != nil {
					return err
				}
				listing, err := observation.FrameEntries([]observation.Entry{{Type: "f", Name: "a.txt"}})
				if err != nil {
					return err
				}
				_, err = tx.RecordObservation(turnID, attemptID, observation.Root, journal.ObservationListing, listing)
				return err
			})
			if err != nil {
				t.Error(err)
			}
			for i, p := range prior {
				run.journalBash(t, attemptID, 10+i, p)
			}
			for i, c := range commands {
				id := run.journalBash(t, attemptID, 20+i, c)
				run.versionBefore, run.observedBefore = run.snapshot(t, turnID)
				out, err := run.h.Engine.DispatchCommandReferee(turnID, "author", id, c)
				run.mu.Lock()
				run.outs, run.errs, run.callIDs = append(run.outs, out), append(run.errs, err), append(run.callIDs, id)
				run.mu.Unlock()
			}
		})
	}
	script := append([]fake.Step{toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`))}, replies...)
	script = append(script, enginetest.Reply("final answer"))
	opts = append(opts, enginetest.WithBeforeToolEffect(hook))
	run.h = enginetest.New(t, roster, map[string][]fake.Step{"author": script}, opts...)
	reply, err := run.h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	run.turn = reply.TurnID
	return run
}

func (r *commandRun) decisions(t *testing.T) []journal.RefereeDecision {
	t.Helper()
	ds, err := r.h.Store.RefereeDecisionsByTurn(r.turn)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func readStep(calls ...transport.ToolCall) fake.Step { return toolStepWith(calls...) }

// A read step is accepted and its reads are served from the record and the
// overlay; the next request carries those results and nothing else new; a path
// the record does not hold reads not found and creates no observation; a
// protected path is refused; each read is journaled under its decision.
func TestCommandRefereeReadStep(t *testing.T) {
	roster := refereeRoster("\n[tools]\nprotected_paths = [\".env\"]\n")
	step := readStep(
		call("r1", tools.NameReadFile, `{"path":"a.txt"}`),
		call("r2", tools.NameReadFile, `{"path":"missing.txt"}`),
		call("r3", tools.NameReadFile, `{"path":".env"}`),
		call("r4", "lookup", `{}`),
		call("r5", tools.NameListDir, `{"path":"."}`),
	)
	run := runCommand(t, roster, refereeOpts(), []fake.Step{step, enginetest.Reply(commandAllow)}, []string{"echo one"}, "cat a.txt")
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	if run.errs[0] != nil || !run.outs[0].Allow {
		t.Fatalf("outcome %+v, err %v", run.outs[0], run.errs[0])
	}
	reqs := requestsOf(run.h, "author")
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want author, two referee attempts, author", len(reqs))
	}
	first, second := reqs[1], reqs[2]
	wantFirst := referee.CommandPromptMessages(referee.CommandPayload{UserInput: "question", CallName: tools.NameBash, Command: "cat a.txt", PriorCalls: []string{"echo one"}})
	if !reflect.DeepEqual(first.Messages, wantFirst) {
		t.Fatalf("first request\n%+v\nwant\n%+v", first.Messages, wantFirst)
	}
	var names []string
	for _, d := range first.Tools {
		names = append(names, d.Function.Name)
	}
	if !reflect.DeepEqual(names, []string{tools.NameReadFile, tools.NameListDir, tools.NameSearchFiles}) || first.MaxOutputTokens != 256 {
		t.Fatalf("first request tools %v, max output %d", names, first.MaxOutputTokens)
	}
	if !reflect.DeepEqual(second.Messages[:2], first.Messages) || len(second.Messages) != 2+1+5 || len(second.Tools) != 3 {
		t.Fatalf("second request has %d messages and %d tools", len(second.Messages), len(second.Tools))
	}
	if m := second.Messages[2]; m.Role != "assistant" || len(m.ToolCalls) != 5 {
		t.Fatalf("assistant message %+v", m)
	}
	tool := second.Messages[3:]
	for i, want := range []string{"hello from a", "not found", "protected", "not a tool available", "a.txt"} {
		if tool[i].Role != "tool" || tool[i].ToolCallID != "r"+strconv.Itoa(i+1) || !strings.Contains(tool[i].Content, want) ||
			!strings.Contains(tool[i].Content, "BEGIN TOOL RESULT") || !strings.Contains(tool[i].Content, "untrusted") {
			t.Errorf("tool message %d %+v, want it to hold %q fenced and untrusted", i, tool[i], want)
		}
	}
	if strings.Contains(tool[4].Content, "missing.txt") {
		t.Errorf("listing %q shows a path the record never held", tool[4].Content)
	}

	version, observed := run.snapshot(t, run.turn)
	if version != run.versionBefore || observed != run.observedBefore {
		t.Fatalf("snapshot version %d->%d, observations %d->%d: a referee read observes nothing", run.versionBefore, version, run.observedBefore, observed)
	}
	ds := run.decisions(t)
	if len(ds) != 1 || ds[0].Decision != journal.RefereeAllow || ds[0].ToolCallID != run.callIDs[0] || len(ds[0].Reads) != 4 {
		t.Fatalf("decisions %+v", ds)
	}
	for i, r := range ds[0].Reads {
		wantFailed := i == 1 || i == 2
		if r.AttemptID == "" || (r.SHA256 == "") != wantFailed || r.ArgsSHA256 == "" {
			t.Errorf("read %d %+v", i, r)
		}
	}
	if ds[0].Reads[0].Name != tools.NameReadFile || ds[0].Reads[0].ByteLen != int64(len("hello from a\n")) || ds[0].Reads[3].Name != tools.NameListDir {
		t.Errorf("reads %+v", ds[0].Reads)
	}
	attempts := refereeAttempts(t, run.h, run.turn)
	if len(attempts) != 2 || ds[0].Reads[0].AttemptID != attempts[0].ID {
		t.Fatalf("attempts %+v: each is its own accounted dispatch", attempts)
	}
	for _, a := range attempts {
		if a.EstimatedPromptTokens == nil || a.MaxOutputTokens == nil || *a.MaxOutputTokens != 256 || a.State == "pending" {
			t.Errorf("attempt %+v", a)
		}
	}
}

// The seventh read of a decision is answered with a tool error, the request
// after the bound carries no tool definitions, and calls in its reply are a
// deny.
func TestCommandRefereeReadBound(t *testing.T) {
	var calls []transport.ToolCall
	for i := 1; i <= 7; i++ {
		calls = append(calls, call("r"+strconv.Itoa(i), tools.NameReadFile, `{"path":"a.txt"}`))
	}
	run := runCommand(t, refereeRoster(""), refereeOpts(), []fake.Step{readStep(calls...), readStep(call("late", tools.NameReadFile, `{"path":"a.txt"}`))}, nil, "cat a.txt")
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	if run.errs[0] != nil || run.outs[0].Allow || run.outs[0].Cause != journal.RefereeCauseCallsWithoutTools {
		t.Fatalf("outcome %+v, err %v", run.outs[0], run.errs[0])
	}
	reqs := requestsOf(run.h, "author")
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want author, two referee attempts, author", len(reqs))
	}
	second := reqs[2]
	if len(second.Tools) != 0 {
		t.Fatalf("request after the bound offers %d tools", len(second.Tools))
	}
	if last := second.Messages[len(second.Messages)-1]; last.Role != "user" || !strings.Contains(last.Content, "must now answer") {
		t.Fatalf("last message %+v", last)
	}
	if m := second.Messages[len(second.Messages)-2]; !strings.Contains(m.Content, "no reads remain") {
		t.Fatalf("seventh result %q, want a tool error", m.Content)
	}
	ds := run.decisions(t)
	if len(ds) != 1 || ds[0].Cause != journal.RefereeCauseCallsWithoutTools || len(ds[0].Reads) != 6 {
		t.Fatalf("decisions %+v: six reads run, the seventh does not", ds)
	}
}

// A max_reads the roster sets bounds the reads.
func TestCommandRefereeMaxReadsIsConfigured(t *testing.T) {
	roster := refereeRoster("\n[tools.referee]\nmax_reads = 1\nexec_max_output_tokens = 40\n")
	step := readStep(call("r1", tools.NameReadFile, `{"path":"a.txt"}`), call("r2", tools.NameReadFile, `{"path":"a.txt"}`))
	run := runCommand(t, roster, refereeOpts(), []fake.Step{step, enginetest.Reply(commandAllow)}, nil, "ls")
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	reqs := requestsOf(run.h, "author")
	if len(reqs) != 4 || len(reqs[2].Tools) != 0 || reqs[1].MaxOutputTokens != 40 {
		t.Fatalf("%d requests, second referee request offers %d tools, max output %d", len(reqs), len(reqs[2].Tools), reqs[1].MaxOutputTokens)
	}
	if ds := run.decisions(t); len(ds) != 1 || len(ds[0].Reads) != 1 {
		t.Fatalf("decisions %+v: one read runs, the second does not", ds)
	}
}

// A referee deny, timeout, adapter error, malformed reply, cut-off reply and
// calls under a finish status other than the tool-call status each yield a
// deny that runs nothing.
func TestCommandRefereeDenies(t *testing.T) {
	refused := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusRefused, TransportStatus: transport.TransportStatusOK}
	cutOff := fake.Step{Kind: fake.StepKindReply, Text: "allow because", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}
	timedOut := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusInterrupted, TransportStatus: transport.TransportStatusTimeout}
	wrongStatus := toolStepWith(call("x", tools.NameReadFile, `{"path":"a.txt"}`))
	wrongStatus.Status = transport.StatusComplete
	wrongModel := enginetest.Reply("allow fine")
	wrongModel.ReportedModel = "someone-else"
	cases := []struct {
		name  string
		step  fake.Step
		cause string
	}{
		{"deny", enginetest.Reply("deny not related"), journal.RefereeCauseAnswer},
		{"refusal", refused, journal.RefereeCauseRefusal},
		{"malformed", enginetest.Reply("maybe"), journal.RefereeCauseMalformed},
		{"cut off", cutOff, journal.RefereeCauseCutOff},
		{"calls under another status", wrongStatus, journal.RefereeCauseCallsWrongStatus},
		{"timeout", timedOut, journal.RefereeCauseTimeout},
		{"adapter error", fake.Step{Kind: fake.StepKindError, Err: errors.New("broke")}, journal.RefereeCauseAdapterError},
		{"identity", wrongModel, journal.RefereeCauseMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := runCommand(t, refereeRoster(""), refereeOpts(), []fake.Step{c.step}, nil, "rm -rf x")
			waitFor(t, "the decision", func() bool { return run.done(1) })
			if run.errs[0] != nil || run.outs[0].Allow {
				t.Fatalf("outcome %+v, err %v: must be a deny", run.outs[0], run.errs[0])
			}
			waitFor(t, "the decision record", func() bool { return len(run.decisions(t)) == 1 })
			ds := run.decisions(t)
			if ds[0].Decision != journal.RefereeDeny || ds[0].Cause != c.cause {
				t.Fatalf("decision %+v, want a deny caused by %s", ds[0], c.cause)
			}
		})
	}
}

// A late referee allow runs nothing: the result is quarantined and the
// decision is a deny.
func TestCommandRefereeLateAllowRunsNothing(t *testing.T) {
	var h *enginetest.Harness
	var turn string
	var once sync.Once
	late := enginetest.ReplyFunc(func(transport.CompleteRequest) string {
		once.Do(func() {
			if err := h.Engine.Cancel(turn); err != nil {
				t.Errorf("cancel: %v", err)
			}
		})
		return commandAllow
	})
	var out engine.RefereeOutcome
	var derr error
	var mu sync.Mutex
	finished := false
	hook := func(turnID, attemptID, _ string) {
		var id string
		_ = h.Store.Tx(func(tx *journal.Tx) error {
			rec, err := tx.RecordToolCall(attemptID, 20, "b20", tools.NameBash, journal.ToolClassExec, []byte(`{"command":"ls"}`))
			id = rec.ID
			return err
		})
		o, err := h.Engine.DispatchCommandReferee(turnID, "author", id, "ls")
		mu.Lock()
		out, derr, finished = o, err, true
		mu.Unlock()
	}
	opts := append(refereeOpts(), enginetest.WithBeforeToolEffect(hook))
	h = enginetest.New(t, refereeRoster(""), map[string][]fake.Step{
		"author": {toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`)), late, enginetest.Reply("never")},
	}, opts...)
	reply, err := h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	turn = reply.TurnID
	wantEnd(t, h, turn, engine.StateCancelled, "cancelled")
	waitFor(t, "the outcome", func() bool { mu.Lock(); defer mu.Unlock(); return finished })
	if derr != nil || out.Allow || out.Cause != "late" {
		t.Fatalf("outcome %+v, err %v", out, derr)
	}
	if a := getAttempt(t, h, out.AttemptID); !a.Quarantined {
		t.Fatalf("attempt %+v, want it quarantined", a)
	}
	ds, err := h.Store.RefereeDecisionsByTurn(turn)
	if err != nil || len(ds) != 1 || ds[0].Decision != journal.RefereeDeny {
		t.Fatalf("decisions %+v, err %v", ds, err)
	}
}

// A referee attempt is reserved over its rendered request, and a reservation
// failure ends the turn budget:turn with no decision.
func TestCommandRefereeReservationFailureEndsBudgetTurn(t *testing.T) {
	step := readStep(call("r1", tools.NameReadFile, `{"path":"a.txt"}`))
	probe := runCommand(t, withBudgets(refereeRoster(""), "turn_max_estimated_tokens = 1000000"), refereeOpts(), []fake.Step{step, enginetest.Reply(commandAllow)}, nil, "ls")
	wantEnd(t, probe.h, probe.turn, engine.StateSoloComplete, "solo_ok")
	limit := budgetOf(t, probe.h, probe.turn).Projected

	// With no room beyond the author's own projection the first attempt does
	// not fit.
	run := runCommand(t, withBudgets(refereeRoster(""), "turn_max_estimated_tokens = "+strconv.Itoa(limit)), refereeOpts(), []fake.Step{step, enginetest.Reply(commandAllow)}, nil, "ls")
	wantEnd(t, run.h, run.turn, engine.StateIncomplete, "budget:turn")
	waitFor(t, "the outcome", func() bool { return run.done(1) })
	if !errors.Is(run.errs[0], engine.ErrToolAbandoned) || run.outs[0].Allow {
		t.Fatalf("outcome %+v, err %v: a reservation failure is no decision", run.outs[0], run.errs[0])
	}
	if ds := run.decisions(t); len(ds) != 0 {
		t.Fatalf("decisions %+v", ds)
	}
	if n := len(refereeAttempts(t, run.h, run.turn)); n != 0 {
		t.Fatalf("%d referee attempts begun for a request that did not fit", n)
	}

	// Room for the first attempt only: the second, which carries the read
	// results and is reserved over its own rendered request, does not fit.
	first := refereeAttempts(t, probe.h, probe.turn)[0]
	room := limit + *first.EstimatedPromptTokens + *first.MaxOutputTokens
	second := runCommand(t, withBudgets(refereeRoster(""), "turn_max_estimated_tokens = "+strconv.Itoa(room)), refereeOpts(), []fake.Step{step, enginetest.Reply(commandAllow)}, nil, "ls")
	wantEnd(t, second.h, second.turn, engine.StateIncomplete, "budget:turn")
	waitFor(t, "the outcome", func() bool { return second.done(1) })
	if !errors.Is(second.errs[0], engine.ErrToolAbandoned) || second.outs[0].Allow || len(second.decisions(t)) != 0 {
		t.Fatalf("outcome %+v, err %v", second.outs[0], second.errs[0])
	}
	if n := len(refereeAttempts(t, second.h, second.turn)); n != 1 {
		t.Fatalf("%d referee attempts begun, want the first only", n)
	}
}
