package engine_test

import (
	"context"
	"errors"
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

func TestStopWithBlockingCancelAndGrace(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, attemptID := inReview(t, enginetest.BlockingCancel())

	done := stopAsync(h, context.Background())

	// Give Stop time to reach the grace period waiting loop
	time.Sleep(100 * time.Millisecond)

	// Advance clock by grace period to allow grace to expire
	h.Clock.Advance(5 * time.Second)

	if err := wantStopReturns(t, done); err != nil {
		t.Fatal(err)
	}

	// The hold should be unreleased because Cancel was blocking and grace expired
	if hold, found := getHold(t, h, attemptID); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v; want unreleased after grace", hold, found)
	}

	wantStopEnd(t, h, turnID, "engine_stop")
}

func TestStopWithContextCancelledDuringGrace(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, attemptID := inReview(t, enginetest.BlockingCancel())

	ctx, cancel := context.WithCancel(context.Background())
	done := stopAsync(h, ctx)

	// Let Stop start the grace period
	time.Sleep(10 * time.Millisecond)
	cancel()

	err := wantStopReturns(t, done)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop returned %v, want context.Canceled", err)
	}

	// The turn should be ended as incomplete with engine_stop
	wantStopEnd(t, h, turnID, "engine_stop")

	// Start a new engine on the same journal
	restart(t, h, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())

	// The pending attempt should be recovered as unknown
	attempts, err := h.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.ID == attemptID && a.State != "unknown" {
			t.Fatalf("recovered attempt state = %q, want unknown", a.State)
		}
	}
}

func TestStopWithDeliveriesInProgress(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, _ := inReview(t)

	// Stop should complete successfully even with deliveries in progress
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	wantStopEnd(t, h, turnID, "engine_stop")
}

func TestStopWithContextCancellationRecovery(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, turnID, attemptID := inReview(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := stopAsync(h, ctx)

	// Cancel the context immediately
	cancel()

	err := wantStopReturns(t, done)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop returned %v, want context.Canceled", err)
	}

	// Restart and verify the attempt is marked unknown
	restart(t, h, reviewRosterWithStopGrace("author", "5s", "reviewer"), stopScripts())
	attempts, _ := h.Store.ListAttempts(turnID)
	for _, a := range attempts {
		if a.ID == attemptID && a.State != "unknown" {
			t.Fatalf("recovered attempt state = %q, want unknown", a.State)
		}
	}
}

func TestStoppingChannelClosedAfterEngineStopping(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	h, _, _ := inReview(t)

	stopped := h.Engine.Stopping()

	// Stop the engine
	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The channel must be closed now
	select {
	case <-stopped:
	default:
		t.Fatal("Stopping() not closed after Stop")
	}

	// Verify that engine_stopping was already emitted and in the journal
	events := h.Events()
	var engineStoppingSeq int64 = -1
	for _, ev := range events {
		if ev.Kind == wire.EventKindEngineStopping {
			engineStoppingSeq = ev.Seq
			break
		}
	}
	if engineStoppingSeq < 0 {
		t.Fatal("no engine_stopping event in journal")
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
