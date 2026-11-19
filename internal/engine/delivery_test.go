package engine_test

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const phaseDelivery = "terminal_delivery"

// ack is a delivery reply; it is stored and never read.
func ack() fake.Step { return enginetest.Reply("noted") }

func deliveryAttempts(t *testing.T, h *enginetest.Harness, turnID string) []journal.AttemptRow {
	t.Helper()
	all, err := h.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.AttemptRow
	for _, a := range all {
		if a.Phase == phaseDelivery {
			out = append(out, a)
		}
	}
	return out
}

// waitDeliveries blocks until the turn has n delivery attempts and all of them
// are completed.
func waitDeliveries(t *testing.T, h *enginetest.Harness, turnID string, n int) []journal.AttemptRow {
	t.Helper()
	var got []journal.AttemptRow
	waitFor(t, "deliveries", func() bool {
		got = deliveryAttempts(t, h, turnID)
		if len(got) != n {
			return false
		}
		for _, a := range got {
			if a.State != "completed" {
				return false
			}
		}
		return true
	})
	return got
}

// deliveryCalls returns the terminal_delivery requests the participant's
// adapter received.
func deliveryCalls(h *enginetest.Harness, id string) []transport.CompleteRequest {
	var out []transport.CompleteRequest
	for _, c := range h.Adapters[id].Calls() {
		if c.Request.Envelope.Phase == phaseDelivery {
			out = append(out, c.Request)
		}
	}
	return out
}

func deliveredEvents(h *enginetest.Harness) []*wire.EvVerdictsDelivered {
	var out []*wire.EvVerdictsDelivered
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvVerdictsDelivered); ok {
			out = append(out, p)
		}
	}
	return out
}

// settle returns once the engine has finished everything it does while
// holding its lock, so a delivery that was going to start has started.
func settle(t *testing.T, h *enginetest.Harness) {
	t.Helper()
	if _, err := h.Engine.Status(); err != nil {
		t.Fatal(err)
	}
}

func userContent(req transport.CompleteRequest) string {
	return req.Messages[len(req.Messages)-1].Content
}

func TestDeliveryApprovedToEveryParticipant(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		// A delivery reply that is a well-formed approve must still be ignored.
		"r1": {approveStep(), approveStep()},
		"r2": {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	before := waitEnded(t, h, turnID)

	got := waitDeliveries(t, h, turnID, 3)
	waitCount(t, h, wire.EventKindVerdictsDelivered, 1)
	ev := deliveredEvents(h)
	if len(ev) != 1 || ev[0].TurnID != turnID {
		t.Fatalf("verdicts_delivered = %+v", ev)
	}
	recipients := append([]string(nil), ev[0].Recipients...)
	sort.Strings(recipients)
	if strings.Join(recipients, ",") != "author,r1,r2" || ev[0].Recipients[0] != "author" {
		t.Fatalf("recipients = %v", ev[0].Recipients)
	}
	byParticipant := map[string]journal.AttemptRow{}
	for _, a := range got {
		byParticipant[a.Participant] = a
		if a.Status == nil || *a.Status != "complete" {
			t.Fatalf("delivery %s status = %v", a.Participant, a.Status)
		}
		if a.MaxOutputTokens == nil || *a.MaxOutputTokens != 64 {
			t.Fatalf("delivery %s max output = %v, want 64", a.Participant, a.MaxOutputTokens)
		}
	}
	if len(byParticipant) != 3 {
		t.Fatalf("deliveries went to %v", byParticipant)
	}

	for _, id := range []string{"author", "r1", "r2"} {
		calls := deliveryCalls(h, id)
		if len(calls) != 1 {
			t.Fatalf("%s got %d delivery calls", id, len(calls))
		}
		req := calls[0]
		if req.MaxOutputTokens != 64 {
			t.Fatalf("%s MaxOutputTokens = %d, want 64", id, req.MaxOutputTokens)
		}
		if req.Deadline.IsZero() || req.Envelope.AttemptID != byParticipant[id].ID || req.Envelope.TurnID != turnID {
			t.Fatalf("%s request = %+v", id, req)
		}
		if req.Envelope.Epoch != before.Epoch || before.Epoch == 0 {
			t.Fatalf("%s epoch = %d, want the terminal epoch %d", id, req.Envelope.Epoch, before.Epoch)
		}
		body := userContent(req)
		for _, want := range []string{"approved", "all_approved", req.Envelope.CandidateHash, "--- r1 ---", "--- r2 ---"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s delivery lacks %q:\n%s", id, want, body)
			}
		}
	}

	// No verdict row for any delivery attempt, though r1's reply was an approve.
	for _, v := range verdictRows(t, h, turnID) {
		for _, a := range got {
			if v.AttemptID == a.ID {
				t.Fatalf("verdict recorded for delivery attempt %s", a.ID)
			}
		}
	}
	if rows := verdictRows(t, h, turnID); len(rows) != 2 {
		t.Fatalf("verdict rows = %d, want 2", len(rows))
	}

	after := waitEnded(t, h, turnID)
	if after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason {
		t.Fatalf("turn changed from %q %q to %q %q", before.TerminalState, before.TerminalReason, after.TerminalState, after.TerminalReason)
	}
	if states := h.TurnStates(turnID); states[len(states)-1] != engine.StateApproved {
		t.Fatalf("turn states = %v", states)
	}
}

func TestDeliveryCarriesCommentaryAndRepairOutput(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1": {
			enginetest.Reply("FIRST-OUTPUT not a verdict"),
			verdictStep(func(hash, attempt string) string {
				return "DISSENT-OUTSIDE-ENVELOPE\n" + fake.VerdictApprove(hash, attempt)
			}),
			ack(),
		},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	waitDeliveries(t, h, turnID, 2)

	for _, id := range []string{"author", "r1"} {
		calls := deliveryCalls(h, id)
		if len(calls) != 1 {
			t.Fatalf("%s got %d delivery calls", id, len(calls))
		}
		body := userContent(calls[0])
		first, second := strings.Index(body, "FIRST-OUTPUT"), strings.Index(body, "DISSENT-OUTSIDE-ENVELOPE")
		if first < 0 || second < 0 || first > second {
			t.Fatalf("%s delivery lacks both outputs in dispatch order:\n%s", id, body)
		}
	}
}

func TestDeliveryCarriesOpenBlock(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {blockStep("b1"), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts)
	turnID := submitReview(t, h, "s1")
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, "block:") {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	blocks, err := h.Store.OpenBlocks(turnID)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks = %+v, err %v", blocks, err)
	}
	waitDeliveries(t, h, turnID, 2)
	for _, id := range []string{"author", "r1"} {
		body := userContent(deliveryCalls(h, id)[0])
		for _, want := range []string{"incomplete", row.TerminalReason, "Block ID: " + blocks[0].ID, "Severity: high", "Evidence: evidence", "Remedy: remedy"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s delivery lacks %q:\n%s", id, want, body)
			}
		}
	}
}

func TestDeliveryNoneForSoloCancelledOrNoCandidate(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	t.Run("solo", func(t *testing.T) {
		h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(authorText), ack()}})
		reply, err := h.Engine.Submit(soloReq("s1"))
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
		settle(t, h)
		wantNoDelivery(t, h, reply.TurnID)
	})
	t.Run("cancelled", func(t *testing.T) {
		h := enginetest.New(t, reviewRoster("author", "r1"), reviewScripts(approveStep()))
		release := h.Hold("r1")
		turnID := submitReview(t, h, "s1")
		waitCount(t, h, wire.EventKindAttemptBegun, 2)
		if err := h.Engine.Cancel(turnID); err != nil {
			t.Fatal(err)
		}
		release()
		wantEnd(t, h, turnID, engine.StateCancelled, "cancelled")
		settle(t, h)
		wantNoDelivery(t, h, turnID)
	})
	t.Run("no candidate", func(t *testing.T) {
		scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindError, Err: errors.New("boom")}, ack()}}
		h := enginetest.New(t, enginetest.RosterTOML, scripts)
		turnID := submitReview(t, h, "s1")
		if row := waitEnded(t, h, turnID); row.TerminalState != engine.StateIncomplete {
			t.Fatalf("turn ended %q", row.TerminalState)
		}
		settle(t, h)
		wantNoDelivery(t, h, turnID)
	})
}

func wantNoDelivery(t *testing.T, h *enginetest.Harness, turnID string) {
	t.Helper()
	if got := deliveryAttempts(t, h, turnID); len(got) != 0 {
		t.Fatalf("delivery attempts = %+v", got)
	}
	if n := countEvents(h, wire.EventKindVerdictsDelivered); n != 0 {
		t.Fatalf("%d verdicts_delivered events", n)
	}
	for id, a := range h.Adapters {
		for _, c := range a.Calls() {
			if c.Request.Envelope.Phase == phaseDelivery {
				t.Fatalf("%s received a delivery", id)
			}
		}
	}
}

func TestDeliveryHangEndsAtDeliveryDeadline(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), {Kind: fake.StepKindHang}},
		"r1":     {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts, enginetest.ClockStart(farFuture), enginetest.CancelUnconfirmed())
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	before := waitEnded(t, h, turnID)
	waitFor(t, "author delivery begun", func() bool { return len(deliveryCalls(h, "author")) == 1 })
	attemptID := deliveryCalls(h, "author")[0].Envelope.AttemptID
	if got := begunDeadline(t, h, attemptID); !got.Equal(h.Clock.Now().Add(60 * time.Second)) {
		t.Fatalf("delivery deadline = %v, want now + 60s", got)
	}

	h.Clock.Advance(61 * time.Second)
	waitFor(t, "delivery interrupted", func() bool {
		a := getAttempt(t, h, attemptID)
		return a.State == "completed" && a.Status != nil && *a.Status == "interrupted"
	})
	waitHold(t, h, attemptID, false)
	waitFor(t, "adapter Cancel", func() bool { return len(h.Adapters["author"].CancelCalls()) == 1 })
	waitDeliveries(t, h, turnID, 2)

	if n := len(deliveryCalls(h, "author")); n != 1 {
		t.Fatalf("author received %d delivery calls, want 1 (no retry)", n)
	}
	if after := waitEnded(t, h, turnID); after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason {
		t.Fatalf("turn changed to %q %q", after.TerminalState, after.TerminalReason)
	}
}

func TestDeliveryUnreachableIsNotRetried(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	unreachable := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: transport.TransportStatusUnreachable}
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), unreachable, ack()},
		"r1":     {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := waitDeliveries(t, h, turnID, 2)
	for _, a := range got {
		if a.Participant != "author" {
			continue
		}
		if a.Status == nil || *a.Status != "error" || a.TransportStatus == nil || *a.TransportStatus != transport.TransportStatusUnreachable {
			t.Fatalf("author delivery = %+v", a)
		}
		if a.RetryCount != nil && *a.RetryCount != 0 {
			t.Fatalf("retry count = %d", *a.RetryCount)
		}
	}
	if n := len(deliveryCalls(h, "author")); n != 1 {
		t.Fatalf("author received %d delivery calls, want 1", n)
	}
}

func TestDeliveryCompletionsDistinguishSuccessFromFailure(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	failed := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: "http_500"}
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {approveStep(), ack()},
		"r2":     {approveStep(), failed},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := waitDeliveries(t, h, turnID, 3)
	byID := map[string]string{}
	for _, a := range got {
		byID[a.ID] = a.Participant
	}
	completed := map[string]wire.EvAttemptCompleted{}
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptCompleted); ok && byID[p.AttemptID] != "" {
			completed[byID[p.AttemptID]] = *p
		}
	}
	if len(completed) != 3 {
		t.Fatalf("attempt_completed for deliveries = %+v", completed)
	}
	if completed["r1"].Status != "complete" || completed["author"].Status != "complete" {
		t.Fatalf("successes = %+v", completed)
	}
	if completed["r2"].Status != "error" || completed["r2"].TransportStatus != "http_500" {
		t.Fatalf("failure = %+v", completed["r2"])
	}
}

func TestDeliveryNotSentWhenItDoesNotFitContext(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	big := strings.Repeat("long dissent. ", 1500)
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1": {
			verdictStep(func(hash, attempt string) string { return fake.VerdictApprove(hash, attempt) + "\n" + big }),
			ack(),
		},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts, enginetest.WithContextBound("author", 5000))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := waitDeliveries(t, h, turnID, 2)
	for _, a := range got {
		switch a.Participant {
		case "author":
			if a.Status == nil || *a.Status != "error" || a.TransportStatus == nil || *a.TransportStatus != "not_sent:budget" {
				t.Fatalf("author delivery = %+v", a)
			}
		default:
			if a.Status == nil || *a.Status != "complete" {
				t.Fatalf("%s delivery = %+v", a.Participant, a)
			}
		}
	}
	if n := len(deliveryCalls(h, "author")); n != 0 {
		t.Fatalf("author adapter called %d times for a refused delivery", n)
	}
	if n := len(deliveryCalls(h, "r1")); n != 1 {
		t.Fatalf("r1 received %d deliveries, want 1", n)
	}
	// Both attempts announce themselves; the refused one also completes.
	var begun, completed int
	for _, a := range got {
		for _, ev := range h.Events() {
			switch p := ev.Payload.(type) {
			case *wire.EvAttemptBegun:
				if p.AttemptID == a.ID && a.Participant == "author" {
					begun++
				}
			case *wire.EvAttemptCompleted:
				if p.AttemptID == a.ID && a.Participant == "author" {
					completed++
				}
			}
		}
	}
	if begun != 1 || completed != 1 {
		t.Fatalf("refused delivery events: begun %d completed %d", begun, completed)
	}
	if ev := deliveredEvents(h); len(ev) != 1 || len(ev[0].Recipients) != 2 {
		t.Fatalf("verdicts_delivered = %+v", ev)
	}
}

func TestDeliveryNotSentWhenTurnBudgetExhausted(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	spent := approveStep()
	spent.Usage = &transport.Usage{Prompt: 10_000_000, Completion: 1}
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {spent, ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := waitDeliveries(t, h, turnID, 2)
	for _, a := range got {
		if a.Status == nil || *a.Status != "error" || a.TransportStatus == nil || *a.TransportStatus != "not_sent:budget" {
			t.Fatalf("delivery = %+v", a)
		}
	}
	for _, id := range []string{"author", "r1"} {
		if n := len(deliveryCalls(h, id)); n != 0 {
			t.Fatalf("%s adapter called %d times", id, n)
		}
	}
	waitCount(t, h, wire.EventKindVerdictsDelivered, 1)
}

func TestDeliveriesShareAPoolOneAtATime(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	roster := schedRoster("300s", "900s", []reviewer{{"r1", "default"}}, nil, "")
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {approveStep(), ack()},
	}
	h := enginetest.New(t, roster, scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := waitDeliveries(t, h, turnID, 2)
	seq := func(kind, id string) int64 {
		for _, ev := range h.Events() {
			switch p := ev.Payload.(type) {
			case *wire.EvAttemptBegun:
				if kind == wire.EventKindAttemptBegun && p.AttemptID == id {
					return ev.Seq
				}
			case *wire.EvAttemptCompleted:
				if kind == wire.EventKindAttemptCompleted && p.AttemptID == id {
					return ev.Seq
				}
			}
		}
		return 0
	}
	first, second := got[0], got[1]
	if seq(wire.EventKindAttemptBegun, first.ID) > seq(wire.EventKindAttemptBegun, second.ID) {
		first, second = second, first
	}
	if done, begun := seq(wire.EventKindAttemptCompleted, first.ID), seq(wire.EventKindAttemptBegun, second.ID); done == 0 || begun < done {
		t.Fatalf("second delivery began (seq %d) before the first completed (seq %d)", begun, done)
	}
}

func TestSubmitAcceptedWhileDeliveryRuns(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), {Kind: fake.StepKindHang}},
		"r1":     {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	waitFor(t, "author delivery in flight", func() bool { return len(deliveryCalls(h, "author")) == 1 })

	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "next question"})
	if err != nil {
		t.Fatalf("submit while a delivery runs: %v", err)
	}
	if reply.TurnID == turnID {
		t.Fatal("second submit reused the turn")
	}
	if a := getAttempt(t, h, deliveryCalls(h, "author")[0].Envelope.AttemptID); a.State != "pending" {
		t.Fatalf("delivery state = %s, want it still running", a.State)
	}
}

func TestDeliveryForgetsTurnOnceFinished(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {approveStep(), ack()},
		"r2":     {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	waitDeliveries(t, h, turnID, 3)
	waitFor(t, "deliveries forgotten", func() bool { return h.Engine.PendingDeliveryTurns() == 0 })
}
