package engine_test

import (
	"strconv"
	"strings"
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

// toolRevisionRoster is toolRoster with a round limit, so the readreview
// preset's author runs its tool loop in revision rounds too.
func toolRevisionRoster(maxCalls, rounds string) string {
	return strings.Replace(toolRoster(maxCalls), "max_rounds = 0", "max_rounds = "+rounds, 1)
}

// readCall is a valid read of the workspace's a.txt.
func readCall(id string) transport.ToolCall {
	return call(id, tools.NameReadFile, `{"path":"a.txt"}`)
}

// wantFinalNoTool checks that req is the author's final tool-free attempt: no
// definitions, and the statement that no further calls are available last.
func wantFinalNoTool(t *testing.T, req transport.CompleteRequest) {
	t.Helper()
	if len(req.Tools) != 0 {
		t.Fatalf("final attempt offered %d tools", len(req.Tools))
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "No further tool calls") {
		t.Fatalf("final attempt ends with %+v, want the statement", last)
	}
}

// In review mode, an author at its call bound is offered exactly one
// tool-free attempt; a plain reply to it is the candidate, which freezes and
// goes to the reviewer.
func TestReviewFinalNoToolAttemptFreezesCandidate(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRoster("1"), map[string][]fake.Step{
		"author":   {toolStepWith(readCall("c1")), enginetest.Reply(authorText)},
		"reviewer": {approveStep()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	wantCalls(t, toolCalls(t, h, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})

	reqs := requestsOf(h, "author")
	if len(reqs) != 2 {
		t.Fatalf("%d author requests, want 2", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Fatal("first request offered no tools")
	}
	wantFinalNoTool(t, reqs[1])
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 1 {
		t.Fatalf("%d candidates frozen, want 1", n)
	}
	row, err := h.Store.GetTurn(reply.TurnID)
	if err != nil || row.CandidateID == "" {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	cand, err := h.Store.GetCandidate(row.CandidateID)
	if err != nil || cand.ByteLen != int64(len(authorText)) {
		t.Fatalf("candidate = %+v, err %v; want the final plain reply", cand, err)
	}
	var frozen string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCandidateFrozen); ok && ev.TurnID == reply.TurnID {
			frozen = p.CandidateHash
		}
	}
	reviews := h.Adapters["reviewer"].Calls()
	if len(reviews) != 1 || reviews[0].Request.Envelope.CandidateHash != frozen {
		t.Fatalf("reviewer calls = %d, want 1 on the frozen candidate", len(reviews))
	}
}

// In review mode, tool calls in reply to the final tool-free attempt end the
// turn tool_limit: nothing is frozen, nothing of the step runs, and no
// reviewer is dispatched.
func TestReviewCallsAfterFinalNoToolAttemptEndToolLimit(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRoster("1"), map[string][]fake.Step{
		"author":   {toolStepWith(readCall("c1")), toolStepWith(readCall("c2")), enginetest.Reply("never sent")},
		"reviewer": {approveStep()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "tool_limit")
	wantCalls(t, toolCalls(t, h, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})
	reqs := requestsOf(h, "author")
	if len(reqs) != 2 {
		t.Fatalf("%d author requests, want 2: only one tool-free attempt is offered", len(reqs))
	}
	wantFinalNoTool(t, reqs[1])
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
	if n := len(attemptsOf(h, reply.TurnID, "reviewer")); n != 0 {
		t.Fatalf("%d reviewer attempts begun", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

// A revision round's author phase is a tool loop like the first candidate's:
// its tool step runs, the continuation carries the feedback and the results,
// and its plain reply is the revised candidate.
func TestRevisionRoundRunsAuthorToolLoop(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRevisionRoster("3", "2"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), toolStepWith(readCall("c1")), enginetest.Reply(revisedText)},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	wantCalls(t, toolCalls(t, h, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})

	reqs := requestsOf(h, "author")
	if len(reqs) != 3 {
		t.Fatalf("%d author requests, want 3", len(reqs))
	}
	for i, req := range reqs {
		if len(req.Tools) == 0 {
			t.Fatalf("author request %d offered no tools although calls remain", i)
		}
	}
	var sawFeedback, sawResult bool
	for _, m := range reqs[2].Messages {
		sawFeedback = sawFeedback || (m.Role == "user" && strings.Contains(m.Content, "block_id: b1"))
		sawResult = sawResult || (m.Role == "tool" && m.ToolCallID == "c1" && strings.Contains(m.Content, "hello from a"))
	}
	if !sawFeedback || !sawResult {
		t.Fatalf("continuation lacks feedback/result (%v/%v)", sawFeedback, sawResult)
	}
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 2 {
		t.Fatalf("%d candidates frozen, want 2", n)
	}
	info, err := h.Store.GetRoundInfo(reply.TurnID)
	if err != nil || info.CurrentRound != 1 {
		t.Fatalf("round info = %+v, err %v", info, err)
	}
	reviews := h.Adapters["reviewer"].Calls()
	if len(reviews) != 2 || reviews[0].Request.Envelope.CandidateHash == reviews[1].Request.Envelope.CandidateHash {
		t.Fatalf("reviewer calls = %d, want 2 on different candidates", len(reviews))
	}
}

// The call bound covers the whole turn, not a round: calls made for the first
// candidate leave none for the revision, whose first attempt is the final
// tool-free one, and calls in reply to it end the turn tool_limit.
func TestToolCallBoundSpansRounds(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, toolRevisionRoster("1", "2"), map[string][]fake.Step{
		"author":   {toolStepWith(readCall("c1")), enginetest.Reply(authorText), toolStepWith(readCall("c2"))},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "tool_limit")
	wantCalls(t, toolCalls(t, h, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})
	reqs := requestsOf(h, "author")
	if len(reqs) != 3 {
		t.Fatalf("%d author requests, want 3", len(reqs))
	}
	wantFinalNoTool(t, reqs[1])
	wantFinalNoTool(t, reqs[2])
	if n := revisionDispatches(h, reply.TurnID); n != 1 {
		t.Fatalf("%d revision dispatches, want 1", n)
	}
	if n := len(h.Adapters["reviewer"].Calls()); n != 1 {
		t.Fatalf("reviewer called %d times, want 1", n)
	}
	blocks, err := h.Store.GetBlocksByState(reply.TurnID, "open")
	if err != nil || len(blocks) != 1 {
		t.Fatalf("open blocks = %+v, err %v", blocks, err)
	}
}

// Every attempt of the loop is reserved on its own and charged against the
// one turn budget: a first step that uses more than it reserved leaves too
// little for the continuation, whose reservation ends the turn budget:turn
// with the step's calls run and nothing more dispatched.
func TestAuthorToolLoopChargesTurnBudget(t *testing.T) {
	dir := workspaceDir(t)
	scripts := func() map[string][]fake.Step {
		step := toolStepWith(readCall("c1"))
		step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
		return map[string][]fake.Step{"author": {step, enginetest.Reply("final answer")}}
	}
	probe := enginetest.New(t, withBudgets(toolRoster("3"), "turn_max_estimated_tokens = 1000000"), scripts())
	ok, err := probe.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, probe, ok.TurnID, engine.StateSoloComplete, "solo_ok")
	for _, id := range attemptsOf(probe, ok.TurnID, "author") {
		if a := getAttempt(t, probe, id); a.EstimatedPromptTokens == nil || *a.EstimatedPromptTokens <= 0 {
			t.Fatalf("attempt %s has no reservation", id)
		}
	}
	first := budgetOf(t, probe, ok.TurnID).Projected

	h := enginetest.New(t, withBudgets(toolRoster("3"), "turn_max_estimated_tokens = "+strconv.Itoa(first)), scripts())
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "budget:turn")
	wantCalls(t, toolCalls(t, h, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("author called %d times, want 1", n)
	}
}

// The turn deadline covers every attempt of the loop and is not extended by
// a tool step: a continuation dispatched late in the turn gets what is left
// of it, and the turn ends at it with nothing more dispatched.
func TestAuthorToolLoopKeepsTurnDeadline(t *testing.T) {
	dir := workspaceDir(t)
	roster := strings.Replace(toolRoster("3"), `attempt     = "300s"`, `attempt     = "800s"`, 1)
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author": {toolStepWith(readCall("c1")), {Kind: fake.StepKindHang}},
	})
	release := h.Hold("author")
	reply, err := h.Engine.Submit(toolReq("s1", "readsolo", dir))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	h.Clock.Advance(700 * time.Second)
	release()
	waitFor(t, "continuation", func() bool { return len(attemptsOf(h, reply.TurnID, "author")) == 2 })

	row, err := h.Store.GetTurn(reply.TurnID)
	if err != nil || row.TurnDeadline == nil {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	attempts := attemptsOf(h, reply.TurnID, "author")
	if d := getAttempt(t, h, attempts[1]).Deadline; d == nil || !d.Equal(*row.TurnDeadline) {
		t.Fatalf("continuation deadline %v, want the turn's %v (700s in, with an 800s attempt deadline)", d, row.TurnDeadline)
	}
	h.Clock.Advance(201 * time.Second)
	// The continuation's deadline is the turn's, so either timer may end it.
	if row := waitEnded(t, h, reply.TurnID); row.TerminalState != engine.StateIncomplete {
		t.Fatalf("turn ended %q %q, want incomplete", row.TerminalState, row.TerminalReason)
	}
	if n := len(attemptsOf(h, reply.TurnID, "author")); n != 2 {
		t.Fatalf("%d author attempts, want 2", n)
	}
}

// A revision round's tool loop is checked against the round's projection
// once, when the round starts; its continuations are checked by reservation
// as any later dispatch is. Sweeping the limit above the first round's
// projection, every round that starts dispatches its continuation: the
// round's projection, which already fit, covers more than the continuation
// reserves.
func TestRevisionToolLoopContinuationIsReservedNotReprojected(t *testing.T) {
	dir := workspaceDir(t)
	heavy := func(step fake.Step) fake.Step {
		step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
		return step
	}
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   {heavy(enginetest.Reply(authorText)), toolStepWith(readCall("c1")), enginetest.Reply(revisedText)},
			"reviewer": {heavy(blockStep("b1")), v2Verdict("1", "approve", "", "b1 withdrawn")},
		}
	}
	roster := func(limit int) string {
		return withBudgets(toolRevisionRoster("3", "2"), "turn_max_estimated_tokens = "+strconv.Itoa(limit))
	}
	probe := enginetest.New(t, roster(1000000), scripts())
	ok, err := probe.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, probe, ok.TurnID, engine.StateApproved, "all_approved")
	first := budgetOf(t, probe, ok.TurnID).Projected

	step := 1000
	if testing.Short() {
		step = 2500
	}
	var continued int
	for limit := first; limit < first+40000; limit += step {
		h := enginetest.New(t, roster(limit), scripts())
		reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
		if err != nil {
			t.Fatal(err)
		}
		row := waitEnded(t, h, reply.TurnID)
		if revisionDispatches(h, reply.TurnID) == 0 {
			continue
		}
		attempts := attemptsOf(h, reply.TurnID, "author")
		if len(attempts) < 2 {
			t.Fatalf("limit %d: round started with %d author attempts", limit, len(attempts))
		}
		if len(attempts) == 3 {
			continued++
			continue
		}
		b := budgetOf(t, h, reply.TurnID)
		t.Fatalf("limit %d: turn ended %q %q (charged %d) before the revision's continuation", limit, row.TerminalState, row.TerminalReason, b.Charged)
	}
	if continued == 0 {
		t.Fatal("no limit started a revision round")
	}
}
