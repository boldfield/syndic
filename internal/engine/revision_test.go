package engine_test

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const revisedText = "the answer is 42, and here is why"

// revisionRoster is the review roster with a round limit, one reviewer per id.
func revisionRoster(rounds string, reviewers ...string) string {
	toml := enginetest.RosterTOML
	if len(reviewers) > 0 {
		toml = reviewRoster("author", reviewers...)
	}
	return strings.Replace(toml, "max_rounds = 0", "max_rounds = "+rounds, 1)
}

// v2Verdict builds a VERDICT v2 envelope of the given decision for the round,
// with block-state lines for the reviewer's open blocks.
func v2Verdict(round, decision, blockID string, states ...string) fake.Step {
	return verdictStep(func(hash, attempt string) string {
		lines := []string{"VERDICT v2", "candidate: " + hash, "attempt: " + attempt, "round: " + round}
		for _, s := range states {
			lines = append(lines, "block-state: "+s)
		}
		lines = append(lines, "verdict: "+decision)
		if decision == "block" {
			lines = append(lines, "severity: high", "block_id: "+blockID, "evidence: e", "remedy: r")
		}
		lines = append(lines, "reason: r", "END VERDICT")
		return strings.Join(lines, "\n")
	})
}

func revisionDispatches(h *enginetest.Harness, turnID string) int {
	n := 0
	for _, s := range h.TurnStates(turnID) {
		if s == engine.StateRevisionDispatched {
			n++
		}
	}
	return n
}

// One block, then a revision, then approvals: the turn is approved on the
// second candidate, and the block is withdrawn against it.
func TestRevisionBlockThenApprove(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	}
	h := enginetest.New(t, revisionRoster("2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	want := strings.Join([]string{
		engine.StateSubmitted, engine.StateAuthorDispatched, engine.StateCandidateFrozen, engine.StateReviewersDispatched,
		engine.StateRevisionDispatched, engine.StateCandidateFrozen, engine.StateReviewersDispatched, engine.StateApproved,
	}, ",")
	if got := strings.Join(h.TurnStates(turnID), ","); got != want {
		t.Fatalf("turn states = %s, want %s", got, want)
	}

	// The author was sent the history shape of a revision: the input, its
	// previous candidate, and the reviewer's complete output, fenced.
	calls := h.Adapters["author"].Calls()
	if len(calls) != 2 {
		t.Fatalf("author called %d times, want 2", len(calls))
	}
	msgs := calls[1].Request.Messages
	var sawInput, sawPrevious, sawFeedback bool
	for _, m := range msgs {
		sawInput = sawInput || (m.Role == "user" && m.Content == "what is the answer?")
		sawPrevious = sawPrevious || (m.Role == "assistant" && m.Content == authorText)
		sawFeedback = sawFeedback || (m.Role == "user" && strings.Contains(m.Content, "reviewer 1") && strings.Contains(m.Content, "block_id: b1"))
	}
	if !sawInput || !sawPrevious || !sawFeedback {
		t.Fatalf("revision messages lack input/previous/feedback (%v/%v/%v): %+v", sawInput, sawPrevious, sawFeedback, msgs)
	}

	info, err := h.Store.GetRoundInfo(turnID)
	if err != nil || info.CurrentRound != 1 || info.MaxRoundsInEffect != 2 || info.MaxRoundsCeiling != 5 {
		t.Fatalf("round info = %+v, err %v", info, err)
	}
	reviews := h.Adapters["reviewer"].Calls()
	if len(reviews) != 2 || reviews[0].Request.Envelope.CandidateHash == reviews[1].Request.Envelope.CandidateHash {
		t.Fatalf("reviewer calls = %d, want 2 on different candidates", len(reviews))
	}
	if reviews[1].Request.Envelope.Epoch <= reviews[0].Request.Envelope.Epoch {
		t.Fatalf("epoch did not advance: %d then %d", reviews[0].Request.Envelope.Epoch, reviews[1].Request.Envelope.Epoch)
	}
	blocks, err := h.Store.AllBlocksForTurn(turnID)
	if err != nil || len(blocks) != 1 || blocks[0].State != "withdrawn" ||
		blocks[0].WithdrawnAgainstHash == nil || *blocks[0].WithdrawnAgainstHash != reviews[1].Request.Envelope.CandidateHash {
		t.Fatalf("blocks = %+v, err %v", blocks, err)
	}
}

// The engine waits for every required reviewer before it revises, and an
// approval of the first candidate does not carry to the second.
func TestRevisionWaitsForAllReviewersAndReReviewsAll(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"r1":     {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
		"r2":     {approveStep(), v2Verdict("1", "approve", "")},
	}
	h := enginetest.New(t, revisionRoster("2", "r1", "r2"), scripts)
	release := h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindBlockOpened, 1)
	// r1 has blocked; r2 has not answered, so no revision may start.
	time.Sleep(50 * time.Millisecond)
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("author called %d times while a reviewer was outstanding, want 1", n)
	}
	release()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	if n := len(h.Adapters["r1"].Calls()); n != 2 {
		t.Fatalf("r1 called %d times, want 2", n)
	}
	if n := len(h.Adapters["r2"].Calls()); n != 2 {
		t.Fatalf("r2 called %d times, want 2: its approval of the first candidate must not carry", n)
	}
	var feedback string
	for _, m := range h.Adapters["author"].Calls()[1].Request.Messages {
		if strings.Contains(m.Content, "reviewer 1") {
			feedback = m.Content
		}
	}
	if !strings.Contains(feedback, "reviewer 2") || strings.Contains(feedback, "r1") || strings.Contains(feedback, "r2") {
		t.Fatalf("feedback is not both reviewers' output under blind labels: %q", feedback)
	}
}

// A reviewer failure of any kind ends the turn as before and never starts a
// revision, whichever order the reviewers answer in.
func TestRevisionNeverStartsOnReviewerFailure(t *testing.T) {
	bad := verdictStep(func(hash, attempt string) string { return "no envelope at all" })
	cases := []struct {
		name   string
		r2     []fake.Step
		reason string
	}{
		{"gate", []fake.Step{{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}}, "reviewer_failed:r2:truncated"},
		{"transport", []fake.Step{{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: "http_500"}}, "reviewer_failed:r2:http_500"},
		{"second malformed", []fake.Step{bad, bad}, "malformed_twice:r2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scripts := map[string][]fake.Step{
				"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
				"r1":     {blockStep("b1")},
				"r2":     c.r2,
			}
			h := enginetest.New(t, revisionRoster("2", "r1", "r2"), scripts)
			turnID := submitReview(t, h, "s1")
			wantEnd(t, h, turnID, engine.StateIncomplete, c.reason)
			if n := revisionDispatches(h, turnID); n != 0 {
				t.Fatalf("%d revisions started after a reviewer failure", n)
			}
			if n := len(h.Adapters["author"].Calls()); n != 1 {
				t.Fatalf("author called %d times, want 1", n)
			}
		})
	}
}

// A malformed first verdict is not an accepted result: the engine waits for
// its repair before it counts the reviewer as having answered.
func TestRevisionWaitsForRepair(t *testing.T) {
	bad := verdictStep(func(hash, attempt string) string { return "no envelope at all" })
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"r1":     {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
		"r2":     {bad, approveStep(), v2Verdict("1", "approve", "")},
	}
	h := enginetest.New(t, revisionRoster("2", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if n := revisionDispatches(h, turnID); n != 1 {
		t.Fatalf("%d revisions, want 1", n)
	}
}

// Identical prose over a different patch is a change; identical prose over the
// same patch ends the turn revision_unchanged with the block open.
func TestRevisionUnchangedDetection(t *testing.T) {
	run := func(t *testing.T, lines func(n int32) []string, wantState, wantReason string) (*enginetest.Harness, string) {
		t.Helper()
		var n atomic.Int32
		t.Cleanup(engine.SetAuthorChangeLines(func(transport.CompleteResult) []string { return lines(n.Add(1)) }))
		scripts := map[string][]fake.Step{
			"author":   {enginetest.Reply(authorText), enginetest.Reply(authorText)},
			"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
		}
		h := enginetest.New(t, revisionRoster("2"), scripts)
		turnID := submitReview(t, h, "s1")
		wantEnd(t, h, turnID, wantState, wantReason)
		return h, turnID
	}

	t.Run("different patch is changed", func(t *testing.T) {
		_, _ = run(t, func(n int32) []string {
			if n == 1 {
				return []string{"a.txt write 0 1 5"}
			}
			return []string{"a.txt write 0 2 5"}
		}, engine.StateApproved, "all_approved")
	})
	t.Run("same patch is unchanged", func(t *testing.T) {
		h, turnID := run(t, func(int32) []string { return []string{"a.txt write 0 1 5"} }, engine.StateIncomplete, "revision_unchanged")
		blocks, err := h.Store.GetBlocksByState(turnID, "open")
		if err != nil || len(blocks) != 1 {
			t.Fatalf("open blocks = %+v, err %v", blocks, err)
		}
		if n := len(h.Adapters["reviewer"].Calls()); n != 1 {
			t.Fatalf("reviewer called %d times after an unchanged revision, want 1", n)
		}
	})
	t.Run("same text and no patch is unchanged", func(t *testing.T) {
		_, _ = run(t, func(int32) []string { return nil }, engine.StateIncomplete, "revision_unchanged")
	})
}

// The round limit is binding: the loop stops at it with rounds_exhausted and
// the latest candidate's blocks open.
func TestRevisionRoundsExhausted(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"reviewer": {blockStep("b1"), v2Verdict("1", "block", "b2", "b1 maintained")},
	}
	h := enginetest.New(t, revisionRoster("1"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "rounds_exhausted")
	if n := len(h.Adapters["author"].Calls()); n != 2 {
		t.Fatalf("author called %d times, want 2", n)
	}
	blocks, err := h.Store.GetBlocksByState(turnID, "open")
	if err != nil || len(blocks) != 2 {
		t.Fatalf("open blocks = %+v, err %v", blocks, err)
	}
}

// A client may ask for any limit up to the ceiling, and a value above it is
// refused with no turn.
func TestRevisionClientLimit(t *testing.T) {
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
			"reviewer": {blockStep("b1"), v2Verdict("1", "block", "b2", "b1 maintained")},
		}
	}
	rounds := func(n int) wire.SubmitRequest {
		return wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?", MaxRounds: &n}
	}

	t.Run("above the ceiling", func(t *testing.T) {
		h := enginetest.New(t, revisionRoster("2"), scripts())
		_, err := h.Engine.Submit(rounds(6))
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != wire.ErrorCodeMaxRounds || we.Ceiling == nil || *we.Ceiling != 5 {
			t.Fatalf("submit error = %v, want max_rounds with ceiling 5", err)
		}
	})
	t.Run("zero is the alpha behaviour", func(t *testing.T) {
		h := enginetest.New(t, revisionRoster("2"), scripts())
		reply, err := h.Engine.Submit(rounds(0))
		if err != nil {
			t.Fatal(err)
		}
		row := waitEnded(t, h, reply.TurnID)
		if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, "block:") {
			t.Fatalf("turn ended %q %q, want a block", row.TerminalState, row.TerminalReason)
		}
		if n := len(h.Adapters["author"].Calls()); n != 1 {
			t.Fatalf("author called %d times, want 1", n)
		}
	})
	t.Run("replaces the default", func(t *testing.T) {
		h := enginetest.New(t, revisionRoster("0"), scripts())
		reply, err := h.Engine.Submit(rounds(1))
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "rounds_exhausted")
		info, err := h.Store.GetRoundInfo(reply.TurnID)
		if err != nil || info.MaxRoundsInEffect != 1 || info.MaxRoundsCeiling != 5 {
			t.Fatalf("round info = %+v, err %v", info, err)
		}
	})
}

// A turn's deadline covers every round and is not extended by a revision: a
// revision dispatched late in the turn gets what is left of it, not a fresh
// attempt deadline.
func TestRevisionKeepsOriginalDeadline(t *testing.T) {
	roster := strings.Replace(revisionRoster("2"), `attempt     = "300s"`, `attempt     = "800s"`, 1)
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), {Kind: fake.StepKindHang}},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	}
	h := enginetest.New(t, roster, scripts)
	release := h.Hold("reviewer")
	turnID := submitReview(t, h, "s1")
	h.WaitTurnState(turnID, engine.StateReviewersDispatched)
	h.Clock.Advance(700 * time.Second)
	release()
	h.WaitTurnState(turnID, engine.StateRevisionDispatched)

	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.TurnDeadline == nil {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	author := attemptsOf(h, turnID, "author")
	if len(author) != 2 {
		t.Fatalf("author attempts = %v, want 2", author)
	}
	d := getAttempt(t, h, author[1]).Deadline
	if d == nil || !d.Equal(*row.TurnDeadline) {
		t.Fatalf("revision attempt deadline %v, want the turn's %v (700s in, with an 800s attempt deadline)", d, row.TurnDeadline)
	}

	h.Clock.Advance(201 * time.Second)
	row = waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete {
		t.Fatalf("turn ended %q %q, want incomplete", row.TerminalState, row.TerminalReason)
	}
	if n := len(h.Adapters["reviewer"].Calls()); n != 1 {
		t.Fatalf("reviewer called %d times after the deadline, want 1", n)
	}
}

// A revision round that does not fit what is left of the turn's budget is not
// started: the turn ends budget:turn with the first candidate's blocks open.
func TestRevisionProjectionMustFit(t *testing.T) {
	// The first round reports more usage than it was projected to need, so
	// what is left of the budget cannot cover another.
	heavy := func(step fake.Step) fake.Step {
		step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
		return step
	}
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   {heavy(enginetest.Reply(authorText)), enginetest.Reply(revisedText)},
			"reviewer": {heavy(blockStep("b1")), v2Verdict("1", "approve", "", "b1 withdrawn")},
		}
	}
	// The submit projection covers the first round only; a limit just above it
	// admits the turn and leaves no room for a second round.
	probe := enginetest.New(t, withBudgets(revisionRoster("2"), "turn_max_estimated_tokens = 1000000"), scripts())
	first := budgetOf(t, probe, submitReview(t, probe, "s1")).Projected

	h := enginetest.New(t, withBudgets(revisionRoster("2"), "turn_max_estimated_tokens = "+strconv.Itoa(first)), scripts())
	turnID := submitReview(t, h, "s1")

	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:turn")
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("author called %d times, want 1", n)
	}
	if n := revisionDispatches(h, turnID); n != 0 {
		t.Fatalf("%d revisions started", n)
	}
	blocks, err := h.Store.GetBlocksByState(turnID, "open")
	if err != nil || len(blocks) != 1 {
		t.Fatalf("open blocks = %+v, err %v", blocks, err)
	}
}

// Overriding a turn that ended after a revision delivers only the reviews of
// the latest candidate, as the live path does.
func TestRevisionOverrideDeliversLatestRoundOnly(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), enginetest.Reply(revisedText), ack()},
		"reviewer": {blockStep("b1"), v2Verdict("1", "block", "b2", "b1 maintained"), ack()},
	}
	h := enginetest.New(t, revisionRoster("1"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "rounds_exhausted")
	waitDeliveries(t, h, turnID, 2)
	if err := h.Engine.Override(turnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	var seen int
	waitFor(t, "override deliveries", func() bool {
		seen = 0
		for _, c := range h.Adapters["author"].Calls() {
			if c.Request.Envelope.Phase == phaseDelivery && strings.Contains(userContent(c.Request), "overridden") {
				seen++
				if n := strings.Count(userContent(c.Request), "VERDICT v"); n != 1 {
					t.Fatalf("override delivery carries %d reviewer outputs, want 1: %q", n, userContent(c.Request))
				}
			}
		}
		return seen == 1
	})
}

// A round whose projection does not fit the budget is never started, however
// large the revised answer and however a reviewer may need repair. Sweeping the
// limit above the first round's projection, a revision that starts must not
// then run out of budget, and some limits must refuse one after the author's
// own request would still have fit.
func TestRevisionProjectionMustFitSweep(t *testing.T) {
	heavy := func(step fake.Step) fake.Step {
		step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
		return step
	}
	for name, revised := range map[string]string{"short": revisedText, "long": strings.Repeat("a long answer. ", 400)} {
		t.Run(name, func(t *testing.T) {
			scripts := func() map[string][]fake.Step {
				return map[string][]fake.Step{
					"author":   {heavy(enginetest.Reply(authorText)), enginetest.Reply(revised)},
					"reviewer": {heavy(blockStep("b1")), v2Verdict("1", "approve", "", "b1 withdrawn")},
				}
			}
			probe := enginetest.New(t, withBudgets(revisionRoster("2"), "turn_max_estimated_tokens = 1000000"), scripts())
			first := budgetOf(t, probe, submitReview(t, probe, "s1")).Projected
			var started, refused int
			for limit := first; limit < first+40000; limit += 1000 {
				h := enginetest.New(t, withBudgets(revisionRoster("2"), "turn_max_estimated_tokens = "+strconv.Itoa(limit)), scripts())
				turnID := submitReview(t, h, "s1")
				row := waitEnded(t, h, turnID)
				if revisionDispatches(h, turnID) == 0 {
					if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "budget:turn" {
						t.Fatalf("limit %d: turn ended %q %q, want incomplete budget:turn", limit, row.TerminalState, row.TerminalReason)
					}
					if n := len(h.Adapters["author"].Calls()); n != 1 {
						t.Fatalf("limit %d: refused round but author called %d times", limit, n)
					}
					refused++
					continue
				}
				started++
				if row.TerminalState != engine.StateApproved {
					t.Fatalf("limit %d: revision started but turn ended %q %q", limit, row.TerminalState, row.TerminalReason)
				}
			}
			if started == 0 || refused == 0 {
				t.Fatalf("sweep saw %d started and %d refused rounds, want both", started, refused)
			}
		})
	}
}

// A turn that ends revision_unchanged keeps its reviewed candidate, so its
// terminal delivery and a later override both carry that round's reviews.
func TestRevisionUnchangedDeliversAndOverrides(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), enginetest.Reply(authorText), ack(), ack()},
		"reviewer": {blockStep("b1"), ack(), ack()},
	}
	h := enginetest.New(t, revisionRoster("2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "revision_unchanged")
	waitDeliveries(t, h, turnID, 2)
	delivered := func(marker string) int {
		seen := 0
		for _, c := range h.Adapters["author"].Calls() {
			if c.Request.Envelope.Phase == phaseDelivery && strings.Contains(userContent(c.Request), marker) {
				if n := strings.Count(userContent(c.Request), "VERDICT v"); n != 1 {
					t.Fatalf("%s delivery carries %d reviewer outputs, want 1: %q", marker, n, userContent(c.Request))
				}
				seen++
			}
		}
		return seen
	}
	waitFor(t, "terminal delivery", func() bool { return delivered("revision_unchanged") == 1 })
	if err := h.Engine.Override(turnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "override delivery", func() bool { return delivered("overridden") == 1 })
}
