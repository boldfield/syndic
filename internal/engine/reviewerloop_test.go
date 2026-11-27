package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	// stepNote is text a reviewer's lookup step carries beside its calls. It is
	// journaled and never delivered.
	stepNote = "REVIEWER-PRIVATE-NOTE"
	// fileMarker is the content of the workspace's a.txt, which only a lookup
	// of it can bring into a request.
	fileMarker = "hello from a"
)

// reviewerToolRoster is toolRoster with the readreview preset giving its
// reviewers read, a bound on each reviewer's calls per round, and the given
// round limit.
func reviewerToolRoster(reviewerCalls, rounds string) string {
	r := toolRevisionRoster("5", rounds)
	r = strings.Replace(r, `tools = ["read", "write"]`, `tools = ["read", "write"]`+"\n"+`reviewer_tools = ["read"]`, 1)
	return strings.Replace(r, "[tools]\n", "[tools]\nmax_reviewer_calls = "+reviewerCalls+"\n", 1)
}

// lookupStep is a reviewer's tool step with text of its own.
func lookupStep(calls ...transport.ToolCall) fake.Step {
	s := toolStepWith(calls...)
	s.Text = stepNote
	return s
}

func reviewerCallsOf(t *testing.T, h *enginetest.Harness, turnID, participant string) []journal.ToolCall {
	t.Helper()
	calls, err := h.Store.ToolCallsByTurn(turnID, participant)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// toolMessage returns the tool message answering call id in req, or nil.
func toolMessage(req transport.CompleteRequest, id string) *transport.Message {
	for i, m := range req.Messages {
		if m.Role == "tool" && m.ToolCallID == id {
			return &req.Messages[i]
		}
	}
	return nil
}

func requestsContain(reqs []transport.CompleteRequest, needle string) bool {
	for _, req := range reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, needle) {
				return true
			}
		}
	}
	return false
}

// A reviewer's lookup step runs, its next attempt carries the call and the
// result, and a plain reply to that attempt is its verdict. The verdict's
// evidence records the result and the snapshot version, and nothing the
// reviewer saw reaches the author at the terminal delivery: not the step's
// text, not the result.
func TestReviewerLoopLookupThenVerdict(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	dir := workspaceDir(t)
	h := enginetest.New(t, reviewerToolRoster("3", "0"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), ack()},
		"reviewer": {lookupStep(readCall("r1")), approveStep(), ack()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	waitDeliveries(t, h, reply.TurnID, 2)

	reqs := requestsOf(h, "reviewer")
	if len(reqs) != 3 {
		t.Fatalf("%d reviewer requests, want a lookup, a verdict and a delivery", len(reqs))
	}
	if len(reqs[0].Tools) == 0 || len(reqs[1].Tools) == 0 {
		t.Fatalf("review attempts offered %d and %d tools, want definitions while calls remain", len(reqs[0].Tools), len(reqs[1].Tools))
	}
	if m := toolMessage(reqs[1], "r1"); m == nil || !strings.Contains(m.Content, fileMarker) {
		t.Fatalf("continuation lacks the lookup's result: %+v", reqs[1].Messages)
	}
	if reqs[1].Envelope.AttemptID == reqs[0].Envelope.AttemptID {
		t.Fatal("continuation reused the first attempt")
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, reqs[1].Envelope.AttemptID) {
		t.Fatalf("continuation does not name its own attempt id: %+v", last)
	}

	calls := reviewerCallsOf(t, h, reply.TurnID, "reviewer")
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""})
	if got := attemptText(t, h, reply.TurnID, reqs[0].Envelope.AttemptID); got != stepNote {
		t.Fatalf("step text journaled as %q", got)
	}

	rows := verdictRows(t, h, reply.TurnID)
	if len(rows) != 1 || rows[0].AttemptID != reqs[1].Envelope.AttemptID {
		t.Fatalf("verdicts = %+v, want one, from the continuation", rows)
	}
	var ev journal.VerdictEvidence
	if err := h.Store.Tx(func(tx *journal.Tx) (err error) {
		ev, err = tx.GetVerdictEvidence(rows[0].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(ev.ResultIDs) != 1 || ev.ResultIDs[0] != calls[0].Result.ID {
		t.Fatalf("verdict evidence results = %v, want the one result %s", ev.ResultIDs, calls[0].Result.ID)
	}

	if requestsContain(requestsOf(h, "author"), stepNote) || requestsContain(requestsOf(h, "author"), fileMarker) {
		t.Fatal("reviewer lookup data reached the author")
	}
}

// At its bound a reviewer's next attempt carries no definitions and says no
// further lookups are available; a plain reply to it is the verdict.
func TestReviewerLoopBoundWithholdsDefinitions(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, reviewerToolRoster("1", "0"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {lookupStep(readCall("r1")), approveStep()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	reqs := requestsOf(h, "reviewer")
	if len(reqs) != 2 || len(reqs[0].Tools) == 0 {
		t.Fatalf("reviewer requests = %d, first offered %d tools", len(reqs), len(reqs[0].Tools))
	}
	if len(reqs[1].Tools) != 0 {
		t.Fatalf("attempt at the bound offered %d tools", len(reqs[1].Tools))
	}
	if !requestsContain(reqs[1:], "No further lookups are available") {
		t.Fatal("attempt at the bound lacks the statement")
	}
}

// Tool calls in the reply to the tool-free attempt fail that reviewer, and
// nothing of the step runs.
func TestReviewerLoopCallsAtBoundFailReviewer(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, reviewerToolRoster("1", "0"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {lookupStep(readCall("r1")), lookupStep(readCall("r2")), approveStep()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "reviewer_failed:reviewer:tool_calls_when_limited")
	wantCalls(t, reviewerCallsOf(t, h, reply.TurnID, "reviewer"), [2]string{journal.ToolCallExecuted, ""})
	if n := len(requestsOf(h, "reviewer")); n != 2 {
		t.Fatalf("%d reviewer requests, want 2: only one tool-free attempt is offered", n)
	}
	if n := len(verdictRows(t, h, reply.TurnID)); n != 0 {
		t.Fatalf("%d verdicts recorded", n)
	}
}

// The bound is per reviewer per round: a reviewer that used it in the first
// round is offered definitions again in the next.
func TestReviewerLoopBoundResetsEachRound(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, reviewerToolRoster("1", "2"), map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"reviewer": {
			lookupStep(readCall("r1")), blockStep("b1"),
			lookupStep(readCall("r2")), v2Verdict("1", "approve", "", "b1 withdrawn"),
		},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	reqs := requestsOf(h, "reviewer")
	if len(reqs) != 4 {
		t.Fatalf("%d reviewer requests, want 4", len(reqs))
	}
	wantTools := []bool{true, false, true, false}
	for i, req := range reqs {
		if (len(req.Tools) > 0) != wantTools[i] {
			t.Errorf("request %d offered %d tools, want definitions: %v", i, len(req.Tools), wantTools[i])
		}
	}
	wantCalls(t, reviewerCallsOf(t, h, reply.TurnID, "reviewer"),
		[2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallExecuted, ""})
	// The second round's request carries that round's lookup only.
	if toolMessage(reqs[3], "r1") != nil || toolMessage(reqs[3], "r2") == nil {
		t.Fatal("second round's continuation does not carry exactly its own result")
	}
	// The block cited no result: the author's revision carries none.
	if requestsContain(requestsOf(h, "author"), fileMarker) {
		t.Fatal("an uncited reviewer result reached the author")
	}
}

// A malformed verdict after lookups is repaired with the reviewer's own
// results in view and no definitions; the other reviewer's requests never
// carry its lookups.
func TestReviewerLoopRepairCarriesOwnResults(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	dir := workspaceDir(t)
	roster := withPresetTools(t, reviewRoster("author", "r1", "r2"), "review", []string{tools.GroupRead}, []string{tools.GroupRead})
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {lookupStep(readCall("r1c")), enginetest.Reply("not a verdict"), approveStep(), ack()},
		"r2":     {approveStep(), ack()},
	}, enginetest.WithToolCalls("author", transport.TriYes), enginetest.WithToolCalls("r1", transport.TriYes), enginetest.WithToolCalls("r2", transport.TriYes))
	reply, err := h.Engine.Submit(toolReq("s1", "review", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	waitDeliveries(t, h, reply.TurnID, 3)

	reqs := requestsOf(h, "r1")
	if len(reqs) < 4 {
		t.Fatalf("%d r1 requests, want a lookup, a malformed reply, a repair and a delivery", len(reqs))
	}
	repair := reqs[2]
	if row := getAttempt(t, h, repair.Envelope.AttemptID); row.RepairOf == nil {
		t.Fatal("third attempt is not the repair")
	}
	if len(repair.Tools) != 0 {
		t.Fatalf("repair offered %d tools", len(repair.Tools))
	}
	if m := toolMessage(repair, "r1c"); m == nil || !strings.Contains(m.Content, fileMarker) {
		t.Fatalf("repair lacks the reviewer's own result: %+v", repair.Messages)
	}
	var sawCalls bool
	for _, m := range repair.Messages {
		sawCalls = sawCalls || (m.Role == "assistant" && len(m.ToolCalls) == 1)
	}
	if !sawCalls {
		t.Fatal("repair lacks the reviewer's own call")
	}
	for _, id := range []string{"author", "r2"} {
		for _, req := range requestsOf(h, id) {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, fileMarker) || strings.Contains(m.Content, stepNote) {
					t.Fatalf("%s %s request carries another reviewer's lookup", id, req.Envelope.Phase)
				}
			}
		}
	}
}

// A reviewer's read sees the candidate's staged changes, while the diff it
// is given shows the base.
func TestReviewerLoopReadSeesStagedCandidate(t *testing.T) {
	dir := workspaceDir(t)
	h := enginetest.New(t, reviewerToolRoster("3", "0"), map[string][]fake.Step{
		"author": {
			toolStepWith(call("w1", tools.NameWriteFile, `{"path":"a.txt","content":"staged body\n"}`)),
			enginetest.Reply(authorText),
		},
		"reviewer": {lookupStep(readCall("r1")), approveStep()},
	})
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	reqs := requestsOf(h, "reviewer")
	if len(reqs) != 2 {
		t.Fatalf("%d reviewer requests, want 2", len(reqs))
	}
	m := toolMessage(reqs[1], "r1")
	if m == nil || !strings.Contains(m.Content, "staged body") || strings.Contains(m.Content, fileMarker) {
		t.Fatalf("reviewer's read did not see the staged candidate: %+v", m)
	}
	if prompt := reqs[0].Messages[1].Content; !strings.Contains(prompt, "-"+fileMarker) || !strings.Contains(prompt, "+staged body") {
		t.Fatalf("reviewer's diff does not show the base against the change:\n%s", prompt)
	}
}

// A continuation the context admission refuses fails that reviewer, with the
// budget event journaled, and the engine is free for the next turn.
func TestReviewerLoopContinuationAdmissionFailsReviewer(t *testing.T) {
	dir := workspaceDir(t)
	big := strings.Repeat("0123456789abcdef\n", 3500)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	h := enginetest.New(t, reviewerToolRoster("3", "0"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {lookupStep(call("r1", tools.NameReadFile, `{"path":"big.txt"}`)), approveStep()},
	}, enginetest.WithContextBound("reviewer", 30000))
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "reviewer_failed:reviewer:context_admission")
	if n := len(requestsOf(h, "reviewer")); n != 1 {
		t.Fatalf("%d reviewer requests, want only the refused step's", n)
	}
	if n := countEvents(h, wire.EventKindBudget); n != 1 {
		t.Fatalf("%d budget events, want 1", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}
