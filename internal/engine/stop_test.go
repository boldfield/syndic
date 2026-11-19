package engine_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// stopGrace is the stop_grace every stop test's roster sets.
const stopGrace = 5 * time.Second

// reviewRosterWithStopGrace is reviewRoster with the engine's stop_grace set.
func reviewRosterWithStopGrace(authorID string, stopGrace string, reviewers ...string) string {
	toml := reviewRoster(authorID, reviewers...)
	if stopGrace != "" {
		toml += "\n[engine]\nstop_grace = \"" + stopGrace + "\"\n"
	}
	return toml
}

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

// wantStoppingThenOutcomes checks that engine_stopping was emitted once and
// that everything after it is the confirmed cancellation of attemptID, as
// recorded during the grace period: its capacity release with how
// server_confirmed, participant's state and, when the cancellation ended the
// adapter call, the attempt's completion.
func wantStoppingThenOutcomes(t *testing.T, h *enginetest.Harness, attemptID, participant string) {
	t.Helper()
	events := h.Events()
	at := -1
	for i, ev := range events {
		if ev.Kind == wire.EventKindEngineStopping {
			if at >= 0 {
				t.Fatalf("engine_stopping emitted twice, at seq %d and %d", events[at].Seq, ev.Seq)
			}
			at = i
		}
	}
	if at < 0 {
		t.Fatal("no engine_stopping event")
	}
	released := false
	for _, ev := range events[at+1:] {
		switch p := ev.Payload.(type) {
		case *wire.EvCapacity:
			if p.HeldByAttempt != attemptID || p.State != "released" || p.How == nil || *p.How != "server_confirmed" {
				t.Fatalf("capacity event after engine_stopping: %+v", p)
			}
			released = true
		case *wire.EvAttemptCompleted:
			if p.AttemptID != attemptID {
				t.Fatalf("attempt_completed after engine_stopping: %+v", p)
			}
		case *wire.EvParticipantState:
			if p.Participant != participant {
				t.Fatalf("participant_state after engine_stopping: %+v", p)
			}
		default:
			t.Fatalf("%s event after engine_stopping: %+v", ev.Kind, ev.Payload)
		}
	}
	if !released {
		t.Fatal("the hold was not released with how server_confirmed after engine_stopping")
	}
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
	// engine_stopping is the last event of the stop itself; only the
	// confirmed cancellation, recorded during the grace period, follows it.
	wantStoppingThenOutcomes(t, h, attemptID, "reviewer")
	hold, found := getHold(t, h, attemptID)
	if !found || hold.ReleasedAt == nil || hold.ReleasedHow != "server_confirmed" {
		t.Fatalf("hold = %+v, found %v", hold, found)
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

func TestStopIdempotent(t *testing.T) {
	h, turnID, attemptID := inReview(t)
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	seq1, _ := h.Store.MaxSeq()

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	seq2, _ := h.Store.MaxSeq()
	if seq1 != seq2 {
		t.Fatalf("second Stop wrote %d rows, want 0", seq2-seq1)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt == nil || hold.ReleasedHow != "server_confirmed" {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
}

func TestStopBlocksSecondSignal(t *testing.T) {
	h, turnID, attemptID := inReview(t)

	done1 := stopAsync(h, context.Background())
	done2 := stopAsync(h, context.Background())

	if err := wantStopReturns(t, done1); err != nil {
		t.Fatal(err)
	}
	if err := wantStopReturns(t, done2); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt == nil || hold.ReleasedHow != "server_confirmed" {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
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
}

func TestStoppingChannelClosed(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	stopped := h.Engine.Stopping()

	select {
	case <-stopped:
		t.Fatal("Stopping() closed before Stop called")
	default:
	}

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-stopped:
	case <-time.After(1 * time.Second):
		t.Fatal("Stopping() not closed after Stop")
	}
}

// blockedStop starts a Stop over a reviewer whose Cancel blocks, and returns
// once Stop is in its grace wait: engine_stopping is out and the adapter's
// Cancel has been called.
func blockedStop(t *testing.T, ctx context.Context, opts ...enginetest.Option) (h *enginetest.Harness, turnID, attemptID string, done <-chan error) {
	t.Helper()
	h, turnID, attemptID = inReview(t, append(opts, enginetest.BlockingCancel())...)
	t.Cleanup(h.ReleaseBlockingCancel)
	done = stopAsync(h, ctx)
	select {
	case <-h.Engine.Stopping():
	case <-time.After(10 * time.Second):
		t.Fatal("Stopping() never closed")
	}
	waitFor(t, "adapter Cancel", func() bool { return len(h.Adapters["reviewer"].CancelCalls()) == 1 })
	return h, turnID, attemptID, done
}

func wantStopStillRunning(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("Stop returned %v while Cancel was blocked", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func journalState(t *testing.T, h *enginetest.Harness, attemptID string) (int, *journal.AttemptRow, journal.Hold) {
	t.Helper()
	hold, found := getHold(t, h, attemptID)
	if !found {
		t.Fatalf("no hold for %s", attemptID)
	}
	return len(h.Events()), getAttempt(t, h, attemptID), hold
}

func TestStopWithBlockingCancelAndGrace(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, attemptID, done := blockedStop(t, context.Background())

	wantStopStillRunning(t, done)
	h.Clock.Advance(stopGrace)
	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v; want unreleased after grace", hold, found)
	}
}

// A Cancel that returns after Stop did writes nothing.
func TestStopLateCancelResultWritesNothing(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, _, attemptID, done := blockedStop(t, context.Background())
	h.Clock.Advance(stopGrace)
	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}
	events, attempt, hold := journalState(t, h, attemptID)

	h.ReleaseBlockingCancel()
	time.Sleep(100 * time.Millisecond)

	events2, attempt2, hold2 := journalState(t, h, attemptID)
	if events2 != events {
		t.Fatalf("%d events after Stop returned, want %d", events2, events)
	}
	if !reflect.DeepEqual(attempt, attempt2) || !reflect.DeepEqual(hold, hold2) {
		t.Fatalf("rows changed after Stop: attempt %+v -> %+v, hold %+v -> %+v", attempt, attempt2, hold, hold2)
	}
}

// A reviewer's result that arrives after Stop returned writes nothing.
func TestStopLateCompleteResultWritesNothing(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h := enginetest.New(t, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts(), enginetest.CancelUnconfirmed())
	release := h.Hold("reviewer")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	attemptID := attemptsOf(h, turnID, "reviewer")[0]
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, attempt, hold := journalState(t, h, attemptID)

	release()
	time.Sleep(100 * time.Millisecond)

	events2, attempt2, hold2 := journalState(t, h, attemptID)
	if events2 != events {
		t.Fatalf("%d events after Stop returned, want %d", events2, events)
	}
	if !reflect.DeepEqual(attempt, attempt2) || !reflect.DeepEqual(hold, hold2) {
		t.Fatalf("rows changed after Stop: attempt %+v -> %+v, hold %+v -> %+v", attempt, attempt2, hold, hold2)
	}
}

func TestStopWithContextCancelledDuringGrace(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, turnID, attemptID, done := blockedStop(t, ctx)

	wantStopStillRunning(t, done)
	cancel()
	if err := wantStopReturns(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop returned %v, want context.Canceled", err)
	}
	wantStopEnd(t, h, turnID, "engine_stop")

	restart(t, h, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
	a := getAttempt(t, h, attemptID)
	if a.State != "unknown" {
		t.Fatalf("recovered attempt state = %q, want unknown", a.State)
	}
}

// A stop with a previous turn's delivery in flight cancels the delivery and
// leaves that turn's state alone.
func TestStopWithDeliveryInFlight(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), {Kind: fake.StepKindHang}, {Kind: fake.StepKindHang}},
		"r1":     {approveStep(), ack()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1"), scripts, enginetest.ClockStart(farFuture))
	turn1 := submitReview(t, h, "s1")
	wantEnd(t, h, turn1, engine.StateApproved, "all_approved")
	before := waitEnded(t, h, turn1)
	waitFor(t, "author delivery in flight", func() bool { return len(deliveryCalls(h, "author")) == 1 })
	deliveryID := deliveryCalls(h, "author")[0].Envelope.AttemptID

	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "next question"})
	if err != nil {
		t.Fatal(err)
	}
	if row, err := h.Store.GetTurn(reply.TurnID); err != nil || row.TerminalState != "" {
		t.Fatalf("second turn: %+v, %v; want it open", row, err)
	}

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStopEnd(t, h, reply.TurnID, "engine_stop")
	cancelled := false
	for _, id := range h.Adapters["author"].CancelCalls() {
		cancelled = cancelled || id == deliveryID
	}
	if !cancelled {
		t.Fatalf("delivery %s got no Cancel: %v", deliveryID, h.Adapters["author"].CancelCalls())
	}
	if _, found := getHold(t, h, deliveryID); !found {
		t.Fatal("no hold for the cancelled delivery")
	}
	after := waitEnded(t, h, turn1)
	if after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason {
		t.Fatalf("previous turn changed to %q %q", after.TerminalState, after.TerminalReason)
	}
}

// Stopping() closes only once engine_stopping is in the journal, and a stop
// that is waiting for a Cancel has already closed it.
func TestStoppingChannelClosedAfterEngineStopping(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, _, done := blockedStop(t, context.Background())

	// blockedStop returned on <-Stopping() with Stop still in its grace wait.
	wantStopStillRunning(t, done)
	var stopping, turnState int64 = -1, -1
	for _, ev := range h.Events() {
		if ev.Kind == wire.EventKindEngineStopping {
			stopping = ev.Seq
		}
		if p, ok := ev.Payload.(*wire.EvTurnState); ok && ev.TurnID == turnID && p.State == "incomplete" {
			turnState = ev.Seq
		}
	}
	if stopping < 0 {
		t.Fatal("Stopping() closed but engine_stopping is not in the journal")
	}
	if turnState < 0 || stopping <= turnState {
		t.Fatalf("engine_stopping seq %d not after turn_state{incomplete} seq %d", stopping, turnState)
	}
	h.Clock.Advance(stopGrace)
	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestEngineStoppingAfterTurnState(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, _ := inReview(t)

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	events := h.Events()
	var turnStateSeq, stoppingSeq int64 = -1, -1
	for _, ev := range events {
		if ev.Kind == wire.EventKindTurnState && ev.TurnID == turnID {
			if p, ok := ev.Payload.(*wire.EvTurnState); ok && p.State == "incomplete" {
				turnStateSeq = ev.Seq
			}
		}
		if ev.Kind == wire.EventKindEngineStopping {
			stoppingSeq = ev.Seq
		}
	}

	if turnStateSeq < 0 {
		t.Fatal("no turn_state{incomplete} event")
	}
	if stoppingSeq < 0 {
		t.Fatal("no engine_stopping event")
	}
	if stoppingSeq <= turnStateSeq {
		t.Fatalf("engine_stopping seq %d <= turn_state seq %d", stoppingSeq, turnStateSeq)
	}
}
