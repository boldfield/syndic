package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// reviewRosterWithStopGrace is reviewRoster with the engine's stop_grace set.
func reviewRosterWithStopGrace(authorID string, stopGrace string, reviewers ...string) string {
	toml := reviewRoster(authorID, reviewers...)
	if stopGrace != "" {
		toml += "\n[engine]\nstop_grace = \"" + stopGrace + "\"\n"
	}
	return toml
}

const stopGrace = 5 * time.Second

// stopScripts has the author answer and the reviewer never reach an answer
// before the test lets it.
func stopScripts() map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {enginetest.Reply("approved")},
	}
}

// inReview starts a review turn and returns once the reviewer's attempt is in
// flight, held.
func inReview(t *testing.T, opts ...enginetest.Option) (*enginetest.Harness, string, string) {
	t.Helper()
	h := enginetest.New(t, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts(), opts...)
	h.Hold("reviewer")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	return h, turnID, attemptsOf(h, turnID, "reviewer")[0]
}

func stopAsync(h *enginetest.Harness, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.Engine.Stop(ctx) }()
	return done
}

func wantStopReturns(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
		return nil
	}
}

func wantRefusedStopping(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was not refused after Stop", what)
	}
	_ = wantRefusal(t, err, wire.ErrorCodeStopping)
}

func lastEventKind(h *enginetest.Harness) string {
	events := h.Events()
	return events[len(events)-1].Kind
}

func wantStopEnd(t *testing.T, h *enginetest.Harness, turnID, reason string) {
	t.Helper()
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != reason {
		t.Fatalf("turn ended %q %q, want incomplete %s", row.TerminalState, row.TerminalReason, reason)
	}
}

func TestStopDuringReviewWithConfirmingFake(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, attemptID := inReview(t)

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	wantStopEnd(t, h, turnID, "engine_stop")
	if calls := h.Adapters["reviewer"].CancelCalls(); len(calls) != 1 || calls[0] != attemptID {
		t.Fatalf("reviewer Cancel calls = %v, want [%s]", calls, attemptID)
	}
	if n := len(deliveryAttempts(t, h, turnID)); n != 0 {
		t.Fatalf("%d terminal_delivery attempts for an engine_stop turn", n)
	}
	if got := lastEventKind(h); got != wire.EventKindEngineStopping {
		t.Fatalf("last event = %q, want engine_stopping", got)
	}
	hold, found := getHold(t, h, attemptID)
	if !found || hold.ReleasedAt == nil || hold.ReleasedHow != "server_confirmed" {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
	released := false
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCapacity); ok && p.HeldByAttempt == attemptID && p.State == "released" {
			released = p.How != nil && *p.How == "server_confirmed"
		}
	}
	if !released {
		t.Fatal("no capacity released event with how server_confirmed")
	}
}

func TestStopWithCancelUnconfirmed(t *testing.T) {
	h, turnID, attemptID := inReview(t, enginetest.CancelUnconfirmed())

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v; want unreleased", hold, found)
	}
	wantStopEnd(t, h, turnID, "engine_stop")

	restart(t, h, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
	started := lastStarted(t, h)
	if len(started.UncertainCapacity) != 1 || started.UncertainCapacity[0] != "reviewers" {
		t.Fatalf("engine_started = %+v, want uncertain_capacity [reviewers]", started)
	}
	if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
		t.Fatalf("hold released by the restart: %+v", hold)
	}
}

func TestStopRefusesSubmitCancelOverrideRelease(t *testing.T) {
	h, turnID, _ := inReview(t)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "again"})
	wantRefusedStopping(t, "Submit", err)
	wantRefusedStopping(t, "Cancel", h.Engine.Cancel(turnID))
	wantRefusedStopping(t, "Override", h.Engine.Override(turnID, "because"))
	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", Reason: "because"})
	wantRefusedStopping(t, "Release", err)
}

func TestSubmitRetryWithKnownIDDuringStop(t *testing.T) {
	h, turnID, _ := inReview(t)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("retry during stop: %v", err)
	}
	if reply.TurnID != turnID {
		t.Fatalf("retry returned turn %q, want %q", reply.TurnID, turnID)
	}
	_, err = h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "something else"})
	_ = wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
}

func TestStopWithBlockedCancelReturnsAtGraceAndHoldStays(t *testing.T) {
	h, turnID, attemptID := inReview(t)
	entered, release := h.HoldCancel("reviewer")

	done := stopAsync(h, context.Background())
	<-entered
	select {
	case err := <-done:
		t.Fatalf("Stop returned before the grace period: %v", err)
	default:
	}

	h.Clock.Advance(stopGrace)
	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v; want unreleased", hold, found)
	}
	if got := lastEventKind(h); got != wire.EventKindEngineStopping {
		t.Fatalf("last event = %q, want engine_stopping", got)
	}

	// The cancellation returns after Stop did: nothing is written.
	before, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitFor(t, "cancellation to return", func() bool { return h.Engine.Cancelers() == 0 })
	if after, _ := h.Store.MaxSeq(); after != before {
		t.Fatalf("journal grew from seq %d to %d after Stop returned", before, after)
	}
	if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
		t.Fatalf("hold released after Stop returned: %+v", hold)
	}
}

func TestStopWithContextCancelledDuringGrace(t *testing.T) {
	h, turnID, attemptID := inReview(t)
	entered, _ := h.HoldCancel("reviewer")

	ctx, cancel := context.WithCancel(context.Background())
	done := stopAsync(h, ctx)
	<-entered
	cancel()
	if err := wantStopReturns(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v, want context.Canceled", err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	select {
	case <-h.Engine.Stopping():
	default:
		t.Fatal("Stopping() is not closed after the context ended")
	}
	if got := lastEventKind(h); got != wire.EventKindEngineStopping {
		t.Fatalf("last event = %q, want engine_stopping", got)
	}

	restart(t, h, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
	if got := getAttempt(t, h, attemptID).State; got != "unknown" {
		t.Fatalf("attempt state after restart = %q, want unknown", got)
	}
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v; want unreleased", hold, found)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
}

func TestStopWithNoOpenTurn(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := lastEventKind(h); got != wire.EventKindEngineStopping {
		t.Fatalf("last event = %q, want engine_stopping", got)
	}
	if n := countEvents(h, wire.EventKindEngineStopping); n != 1 {
		t.Fatalf("%d engine_stopping events", n)
	}
	select {
	case <-h.Engine.Stopping():
	default:
		t.Fatal("Stopping() is not closed")
	}
}

func TestStoppingClosedAfterEngineStoppingIsJournaled(t *testing.T) {
	h, _, _ := inReview(t)
	entered, _ := h.HoldCancel("reviewer")
	done := stopAsync(h, context.Background())
	<-entered
	select {
	case <-h.Engine.Stopping():
		t.Fatal("Stopping() closed while a cancellation is outstanding")
	default:
	}
	h.Clock.Advance(stopGrace)
	<-h.Engine.Stopping()
	events, err := h.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if got := events[len(events)-1].Kind; got != wire.EventKindEngineStopping {
		t.Fatalf("last journaled event when Stopping() closed = %q", got)
	}
	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestStopBeforeStart(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.NoStart())
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Engine.Stopping():
	default:
		t.Fatal("Stopping() is not closed")
	}
	if err := h.Engine.Start(); err == nil {
		t.Fatal("Start succeeded after Stop")
	}
}

// Stop while the previous turn's terminal delivery is in flight: the delivery
// is asked to cancel and held, and its turn is untouched.
func TestStopCancelsEarlierTurnDelivery(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {approveStep(), {Kind: fake.StepKindHang}},
	}
	h := enginetest.New(t, oneReviewer(""), scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	var hung string
	waitFor(t, "r1 delivery pending and author's done", func() bool {
		var authorDone bool
		hung = ""
		for _, a := range deliveryAttempts(t, h, turnID) {
			switch {
			case a.Participant == "author" && a.State == "completed":
				authorDone = true
			case a.Participant == "r1" && a.State == "pending":
				hung = a.ID
			}
		}
		return authorDone && hung != ""
	})
	before, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if calls := h.Adapters["r1"].CancelCalls(); len(calls) != 1 || calls[0] != hung {
		t.Fatalf("r1 Cancel calls = %v, want [%s]", calls, hung)
	}
	hold, found := getHold(t, h, hung)
	if !found || hold.ReleasedHow != "server_confirmed" {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
	after, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason || after.Epoch != before.Epoch {
		t.Fatalf("turn changed by Stop: %+v -> %+v", before, after)
	}
	if got := lastEventKind(h); got != wire.EventKindEngineStopping {
		t.Fatalf("last event = %q, want engine_stopping", got)
	}
}

// A delivery still queued for a slot when Stop runs never starts.
func TestStopDropsQueuedDelivery(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), {Kind: fake.StepKindHang}},
		"r1":     {approveStep(), ack()},
	}
	roster := restartRoster([]rpart{{id: "author", endpoint: "local", model: "llama2", pool: "p"}, {id: "r1", endpoint: "local", model: "llama3", pool: "p"}},
		map[string]string{"p": "concurrency = 1\n"})
	h := enginetest.New(t, roster, scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	// The author and r1 share a pool of width 1: the author's delivery hangs
	// in the slot and r1's waits behind it.
	waitFor(t, "author delivery pending", func() bool {
		for _, a := range deliveryAttempts(t, h, turnID) {
			if a.Participant == "author" && a.State == "pending" {
				return true
			}
		}
		return false
	})
	waitFor(t, "r1 queued", func() bool { return participantState(t, h, "r1") == engine.ParticipantQueued })

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range deliveryAttempts(t, h, turnID) {
		if a.Participant == "r1" {
			t.Fatalf("queued delivery to r1 was started: %+v", a)
		}
	}
	if n := len(h.Adapters["r1"].CancelCalls()); n != 0 {
		t.Fatalf("r1 Cancel calls = %d, want 0", n)
	}
	if got := participantState(t, h, "r1"); got != engine.ParticipantIdle {
		t.Fatalf("r1 state = %q, want idle", got)
	}
}

// An adapter result that arrives after Stop returned adds no row and no event.
func TestLateAdapterResultAddsNothing(t *testing.T) {
	for _, phase := range []string{"author", "reviewer"} {
		t.Run(phase, func(t *testing.T) {
			h := enginetest.New(t, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
			release := h.Hold(phase)
			turnID := submitReview(t, h, "s1")
			want := 1
			if phase == "reviewer" {
				want = 2
			}
			waitCount(t, h, wire.EventKindAttemptBegun, want)
			attemptID := attemptsOf(h, turnID, phase)[0]

			if err := h.Engine.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			before, err := h.Store.MaxSeq()
			if err != nil {
				t.Fatal(err)
			}
			state := getAttempt(t, h, attemptID).State

			release()
			waitFor(t, "late result handled", func() bool { return h.Engine.Inflight(phase) == 0 })
			waitFor(t, "cancellations returned", func() bool { return h.Engine.Cancelers() == 0 })

			if after, _ := h.Store.MaxSeq(); after != before {
				t.Fatalf("journal grew from seq %d to %d after Stop returned", before, after)
			}
			if got := getAttempt(t, h, attemptID).State; got != state {
				t.Fatalf("attempt state %q -> %q after Stop returned", state, got)
			}
		})
	}
}

func TestStopCalls(t *testing.T) {
	h, _, _ := inReview(t)
	entered, _ := h.HoldCancel("reviewer")

	ctx, cancel := context.WithCancel(context.Background())
	first := stopAsync(h, ctx)
	<-entered
	second := stopAsync(h, context.Background())
	waitFor(t, "second Stop to wait", func() bool { return h.Engine.StopWaiters() == 1 })

	cancel()
	if err := wantStopReturns(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Stop = %v, want context.Canceled", err)
	}
	if err := wantStopReturns(t, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Stop = %v, want the first call's result", err)
	}
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after it finished = %v, want nil", err)
	}
}

func TestStopRetriedAfterTerminalTransactionFails(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}, enginetest.NoStart())
	var failing atomic.Bool
	h.Engine.SetPayloadHook(func(kind string, payload any) any {
		if failing.Load() && kind == wire.EventKindTurnState {
			return make(chan int) // cannot be marshalled
		}
		return payload
	})
	if err := h.Engine.Start(); err != nil {
		t.Fatal(err)
	}
	h.Hold("author")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)

	failing.Store(true)
	if err := h.Engine.Stop(context.Background()); err == nil {
		t.Fatal("Stop succeeded though the turn could not be ended")
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.TerminalState != "" {
		t.Fatalf("turn = %+v, err %v; want still open", row, err)
	}
	select {
	case <-h.Engine.Stopping():
		t.Fatal("Stopping() closed though engine_stopping was not emitted")
	default:
	}
	_, err = h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "x"})
	wantRefusedStopping(t, "Submit", err)

	failing.Store(false)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
}

// A turn ended by a stop says engine_stop; the same turn found open after a
// kill says engine_restart.
func TestStopAndKillReasonsDiffer(t *testing.T) {
	h, stoppedTurn, _ := inReview(t)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, stoppedTurn, "engine_stop")

	killed, killedTurn, _ := inReview(t)
	restart(t, killed, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
	wantStopEnd(t, killed, killedTurn, "engine_restart")
	if strings.Contains(stoppedTurn, killedTurn) {
		t.Fatal("turns are not distinct")
	}
}
