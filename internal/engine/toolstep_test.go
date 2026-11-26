package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const toolStepText = "calling a tool"

// toolStep is a tool step naming a tool no preset offers.
func toolStep() fake.Step {
	return toolStepWith(call("call_1", "lookup", `{"q":"x"}`))
}

// toolStepWith is a tool step carrying the given calls.
func toolStepWith(calls ...transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            toolStepText,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
	}
}

func call(id, name, args string) transport.ToolCall {
	return transport.ToolCall{ID: id, Type: "function", Function: transport.ToolCallFunction{Name: name, Arguments: args}}
}

// toolRoster is the harness roster with two presets that give the author
// tools: readsolo (read, solo) and readreview (read and write, review), and
// the given bound on the author's calls per turn.
func toolRoster(maxCalls string) string {
	return enginetest.RosterTOML + `
[presets.readsolo]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]

[presets.readreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write"]

[tools]
max_calls_per_turn = ` + maxCalls + `
`
}

// workspaceDir is a workspace holding a.txt.
func workspaceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello from a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func toolReq(id, preset, workspace string) wire.SubmitRequest {
	return wire.SubmitRequest{SubmissionID: id, Input: "question", Preset: &preset, Workspace: workspace}
}

func attemptText(t *testing.T, h *enginetest.Harness, turnID, attemptID string) string {
	t.Helper()
	texts, err := h.Store.AttemptTexts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return texts[attemptID]
}

// toolCalls returns the turn's journaled author calls in step order, once
// every one of them has its result: a step's execution outlives the terminal
// transaction of a cancel that lands during it.
func toolCalls(t *testing.T, h *enginetest.Harness, turnID string) []journal.ToolCall {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		calls, err := h.Store.ToolCallsByTurn(turnID, "author")
		if err != nil {
			t.Fatal(err)
		}
		settled := true
		for _, c := range calls {
			if c.Result == nil {
				settled = false
			}
		}
		if settled || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// wantCalls checks the journaled calls' statuses and error codes in order.
func wantCalls(t *testing.T, calls []journal.ToolCall, want ...[2]string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("%d calls journaled, want %d", len(calls), len(want))
	}
	for i, c := range calls {
		code := ""
		if c.Result != nil {
			code = c.Result.ErrorCode
		}
		if c.Status != want[i][0] || code != want[i][1] {
			t.Errorf("call %d (%s): status %q code %q, want %q %q", i, c.Name, c.Status, code, want[i][0], want[i][1])
		}
	}
}

// requestsOf returns the requests the author's adapter received, in order.
func requestsOf(h *enginetest.Harness, participant string) []transport.CompleteRequest {
	var reqs []transport.CompleteRequest
	for _, c := range h.Adapters[participant].Calls() {
		reqs = append(reqs, c.Request)
	}
	return reqs
}

// An accepted tool step of a solo author runs each call in order: an invalid
// call (a tool not offered, bad arguments) is answered with a journaled tool
// error while the later valid call still runs; the next author attempt then
// carries the step and every result as tool messages, fenced as data, and
// its answer completes the turn.
func TestSoloToolStepExecutesAndDispatchesResults(t *testing.T) {
	dir := workspaceDir(t)
	var second atomic.Pointer[transport.CompleteRequest]
	h := enginetest.New(t, toolRoster("3"), map[string][]fake.Step{"author": {
		toolStepWith(
			call("c1", tools.NameReadFile, `{"path":"a.txt"}`),
			call("c2", "lookup", `{"q":"x"}`),
			call("c3", tools.NameReadFile, `{"path":"a.txt","limit":"many"}`),
			call("c4", tools.NameListDir, `{"path":"."}`),
		),
		enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			r := req
			second.Store(&r)
			return "final answer"
		}),
	}})
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")

	calls := toolCalls(t, h, reply.TurnID)
	wantCalls(t, calls,
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallError, "not_offered"},
		[2]string{journal.ToolCallError, tools.ErrCodeBadArguments},
		[2]string{journal.ToolCallExecuted, ""},
	)
	attempts := attemptsOf(h, reply.TurnID, "author")
	if len(attempts) != 2 {
		t.Fatalf("%d author attempts, want 2", len(attempts))
	}
	if got := attemptText(t, h, reply.TurnID, attempts[0]); got != toolStepText {
		t.Fatalf("step text journaled = %q", got)
	}
	if got := attemptText(t, h, reply.TurnID, attempts[1]); got != "final answer" {
		t.Fatalf("answer journaled = %q", got)
	}

	req := second.Load()
	if req == nil {
		t.Fatal("no second author request")
	}
	if len(req.Tools) == 0 {
		t.Fatal("second request offers no tools although calls remain")
	}
	first := requestsOf(h, "author")[0]
	n := len(first.Messages)
	if len(req.Messages) != n+5 {
		t.Fatalf("second request has %d messages, want %d", len(req.Messages), n+5)
	}
	step := req.Messages[n]
	if step.Role != "assistant" || step.Content != toolStepText || len(step.ToolCalls) != 4 {
		t.Fatalf("step message = %+v", step)
	}
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		m := req.Messages[n+1+i]
		if m.Role != "tool" || m.ToolCallID != id {
			t.Fatalf("message %d = role %q call %q", n+1+i, m.Role, m.ToolCallID)
		}
		if !strings.Contains(m.Content, "BEGIN TOOL RESULT") || !strings.Contains(m.Content, "END TOOL RESULT") || !strings.Contains(m.Content, "untrusted") {
			t.Fatalf("tool message %s not fenced as data:\n%s", id, m.Content)
		}
	}
	if c := req.Messages[n+1].Content; !strings.Contains(c, "hello from a") {
		t.Fatalf("read_file result not dispatched:\n%s", c)
	}
	if c := req.Messages[n+2].Content; !strings.Contains(c, "not_offered") || !strings.Contains(c, "not available") {
		t.Fatalf("not_offered error not dispatched:\n%s", c)
	}
	if c := req.Messages[n+3].Content; !strings.Contains(c, tools.ErrCodeBadArguments) {
		t.Fatalf("bad_arguments error not dispatched:\n%s", c)
	}
	if c := req.Messages[n+4].Content; !strings.Contains(c, "a.txt") {
		t.Fatalf("list_dir result not dispatched:\n%s", c)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

// In review mode the author's tool steps precede its candidate: a staged
// write and a read are executed and journaled, the candidate freezes with
// the change set, and the reviewer receives the author's calls and results
// as evidence beside the diffs.
func TestReviewToolStepResultsReachReviewers(t *testing.T) {
	dir := workspaceDir(t)
	var reviewerReq atomic.Pointer[transport.CompleteRequest]
	scripts := map[string][]fake.Step{
		"author": {
			toolStepWith(
				call("w1", tools.NameWriteFile, `{"path":"b.txt","content":"new file\n"}`),
				call("r1", tools.NameReadFile, `{"path":"a.txt"}`),
			),
			enginetest.Reply(authorText),
		},
		"reviewer": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			r := req
			reviewerReq.Store(&r)
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	}
	h := enginetest.New(t, toolRoster("3"), scripts)
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	wantCalls(t, toolCalls(t, h, reply.TurnID),
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallExecuted, ""},
	)
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 1 {
		t.Fatalf("%d candidates frozen, want 1", n)
	}
	if n := len(attemptsOf(h, reply.TurnID, "author")); n != 2 {
		t.Fatalf("%d author attempts, want 2", n)
	}
	req := reviewerReq.Load()
	if req == nil {
		t.Fatal("reviewer never dispatched")
	}
	var all strings.Builder
	for _, m := range req.Messages {
		all.WriteString(m.Content)
	}
	for _, want := range []string{"BEGIN AUTHOR EVIDENCE", "hello from a", "BEGIN CHANGE SET", "b.txt", "new file"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("reviewer request lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("staged write touched the workspace: %v", err)
	}
}

// A cancel that lands while a call of the step is executing is seen by the
// re-check after the wait: that call and every later one are journaled
// abandoned, no later effect runs, nothing is dispatched to the author, and
// the engine is free for the next submit.
func TestToolStepCancelMidStepAbandonsRest(t *testing.T) {
	dir := workspaceDir(t)
	var h *enginetest.Harness
	var effects atomic.Int32
	hook := func(turnID, _, _ string) {
		if effects.Add(1) == 2 {
			if err := h.Engine.Cancel(turnID); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}
	}
	h = enginetest.New(t, toolRoster("3"), map[string][]fake.Step{"author": {
		toolStepWith(
			call("c1", tools.NameReadFile, `{"path":"a.txt"}`),
			call("c2", tools.NameReadFile, `{"path":"a.txt"}`),
			call("c3", tools.NameListDir, `{"path":"."}`),
		),
		enginetest.Reply("never sent"),
	}}, enginetest.WithBeforeToolEffect(hook))
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateCancelled, "cancelled")
	wantCalls(t, toolCalls(t, h, reply.TurnID),
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallAbandoned, ""},
		[2]string{journal.ToolCallAbandoned, ""},
	)
	if n := effects.Load(); n != 2 {
		t.Fatalf("%d effects reached, want 2", n)
	}
	if n := len(attemptsOf(h, reply.TurnID, "author")); n != 1 {
		t.Fatalf("%d author attempts, want 1: results of an ended step were dispatched", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

// A tool step that arrives after the turn was cancelled is late: it is
// quarantined at acceptance, none of its calls is journaled and nothing runs.
func TestLateToolStepIsQuarantinedAndRunsNothing(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRoster("3"), map[string][]fake.Step{"author": {
		toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`)),
		enginetest.Reply("never sent"),
	}})
	release := h.Hold("author")
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	if err := h.Engine.Cancel(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateCancelled, "cancelled")
	release()
	attempts := attemptsOf(h, reply.TurnID, "author")
	if len(attempts) != 1 {
		t.Fatalf("%d author attempts, want 1", len(attempts))
	}
	waitQuarantined(t, h, attempts[0])
	if calls := toolCalls(t, h, reply.TurnID); len(calls) != 0 {
		t.Fatalf("late step journaled %d calls", len(calls))
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

// At the call bound the call that exceeds it is answered call_limit, the next
// author attempt is sent without tool definitions and with the statement that
// no further calls are available, and a tool step in reply ends the turn
// tool_limit.
func TestToolLimitWithholdsDefinitionsAndEndsTurn(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRoster("1"), map[string][]fake.Step{"author": {
		toolStepWith(
			call("c1", tools.NameReadFile, `{"path":"a.txt"}`),
			call("c2", tools.NameListDir, `{"path":"."}`),
		),
		toolStepWith(call("c3", tools.NameReadFile, `{"path":"a.txt"}`)),
	}})
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "tool_limit")
	wantCalls(t, toolCalls(t, h, reply.TurnID),
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallError, "call_limit"},
	)
	reqs := requestsOf(h, "author")
	if len(reqs) != 2 {
		t.Fatalf("%d author requests, want 2", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Fatal("first request offered no tools")
	}
	if len(reqs[1].Tools) != 0 {
		t.Fatalf("limited request offered %d tools", len(reqs[1].Tools))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "No further tool calls") {
		t.Fatalf("limited request ends with %+v, want the statement", last)
	}
	if n := len(attemptsOf(h, reply.TurnID, "author")); n != 2 {
		t.Fatalf("%d author attempts, want 2", n)
	}
}

// A tool step from an author offered no definitions, because its preset
// grants no tool or its conversation has no workspace, ends the turn with
// nothing executed and nothing journaled as a call.
func TestSoloToolStepNotOfferedEndsTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  wire.SubmitRequest
	}{
		{"no tools in preset", soloReq("s1")},
		{"no workspace", toolReq("s1", "readsolo", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := enginetest.New(t, toolRoster("3"), map[string][]fake.Step{"author": {toolStep()}})
			reply, err := h.Engine.Submit(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "tool_step:not_offered")
			if got := attemptText(t, h, reply.TurnID, attemptsOf(h, reply.TurnID, "author")[0]); got != toolStepText {
				t.Fatalf("journaled text = %q", got)
			}
			if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
				t.Fatalf("%d candidates frozen", n)
			}
			if calls := toolCalls(t, h, reply.TurnID); len(calls) != 0 {
				t.Fatalf("%d calls journaled", len(calls))
			}
			if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
				t.Fatalf("engine stayed busy: %v", err)
			}
		})
	}
}

func TestReviewAuthorToolStepNotOfferedFreezesNothing(t *testing.T) {
	scripts := reviewScripts(approveStep())
	scripts["author"] = []fake.Step{toolStep()}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "tool_step:not_offered")
	if got := attemptText(t, h, turnID, attemptsOf(h, turnID, "author")[0]); got != toolStepText {
		t.Fatalf("journaled text = %q", got)
	}
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
	if n := len(attemptsOf(h, turnID, "reviewer")); n != 0 {
		t.Fatalf("%d reviewer attempts begun", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewerToolStepIsNotParsed(t *testing.T) {
	// The tool step's text is a well-formed approve verdict; it must not count.
	step := toolStep()
	step.Build = func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	}
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(step))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "tool_step:not_offered")
	if rows := verdictRows(t, h, turnID); len(rows) != 0 {
		t.Fatalf("verdicts = %+v", rows)
	}
	if got := attemptText(t, h, turnID, attemptsOf(h, turnID, "reviewer")[0]); got == "" {
		t.Fatal("reviewer tool-step text not journaled")
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}
