package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

var farFuture = time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func getHold(t *testing.T, h *enginetest.Harness, attemptID string) (journal.Hold, bool) {
	t.Helper()
	hold, found, err := h.Store.GetHold(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return hold, found
}

// waitHold waits for the attempt's hold row to exist and, when released is
// set, to be released.
func waitHold(t *testing.T, h *enginetest.Harness, attemptID string, released bool) journal.Hold {
	t.Helper()
	var hold journal.Hold
	waitFor(t, "hold of "+attemptID, func() bool {
		var found bool
		hold, found = getHold(t, h, attemptID)
		return found && (hold.ReleasedAt != nil) == released
	})
	return hold
}

// capacityEvents returns the capacity events for the attempt, in order.
func capacityEvents(h *enginetest.Harness, attemptID string) []wire.EvCapacity {
	var out []wire.EvCapacity
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCapacity); ok && p.HeldByAttempt == attemptID {
			out = append(out, *p)
		}
	}
	return out
}

func wantReleased(t *testing.T, h *enginetest.Harness, attemptID, how string) {
	t.Helper()
	hold := waitHold(t, h, attemptID, true)
	if hold.ReleasedHow != how {
		t.Fatalf("hold released how %q, want %q", hold.ReleasedHow, how)
	}
	waitFor(t, "capacity released event", func() bool {
		evs := capacityEvents(h, attemptID)
		last := evs[len(evs)-1:]
		return len(last) == 1 && last[0].State == "released" && last[0].How != nil && *last[0].How == how
	})
	evs := capacityEvents(h, attemptID)
	if evs[0].State != "uncertain" {
		t.Fatalf("capacity events = %+v, want uncertain first", evs)
	}
}

func poolHolds(t *testing.T, h *enginetest.Harness, pool string) []wire.PoolHold {
	t.Helper()
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Pools {
		if p.Name == pool {
			return p.Holds
		}
	}
	t.Fatalf("no pool %q", pool)
	return nil
}

func TestCancelDuringAuthor(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts)
	release := h.Hold("author")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, turnID, "author")[0]

	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateCancelled || row.TerminalReason != "cancelled" {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	if row.CandidateID != "" {
		t.Fatalf("candidate_id = %q, want none", row.CandidateID)
	}
	if row.PhaseAtCancel != "author" {
		t.Fatalf("phase_at_cancel = %q", row.PhaseAtCancel)
	}
	if calls := h.Adapters["author"].CancelCalls(); len(calls) != 1 || calls[0] != attemptID {
		t.Fatalf("Cancel calls = %v, want [%s]", calls, attemptID)
	}
	if got := getAttempt(t, h, attemptID).CancellationState; got != "confirmed" {
		t.Fatalf("cancellation state = %q", got)
	}
	wantReleased(t, h, attemptID, "server_confirmed")

	// The request is still outstanding: its result arrives after the cancel,
	// is quarantined and does not change the turn.
	release()
	waitQuarantined(t, h, attemptID)
	after, err := h.Store.GetTurn(turnID)
	if err != nil || after.TerminalState != row.TerminalState || after.TerminalReason != row.TerminalReason || after.Epoch != row.Epoch {
		t.Fatalf("turn changed from %+v to %+v, err %v", row, after, err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantIdle)
	if got := joinStates(stateEvents(h, "author")); !strings.Contains(got, "cancelling") {
		t.Fatalf("author states = %s, want cancelling", got)
	}
}

func TestCancelDuringReview(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {approveStep()},
		"r2":     {approveStep()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindVerdictRecorded, 1)
	waitCount(t, h, wire.EventKindAttemptBegun, 3)

	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateCancelled || row.CandidateID == "" || row.PhaseAtCancel != "review" {
		t.Fatalf("turn = %+v", row)
	}
	if rows := verdictRows(t, h, turnID); len(rows) != 1 {
		t.Fatalf("verdicts = %+v", rows)
	}
	if n := len(h.Adapters["r2"].CancelCalls()); n != 1 {
		t.Fatalf("r2 Cancel calls = %d", n)
	}
	if n := len(h.Adapters["r1"].CancelCalls()); n != 0 {
		t.Fatalf("r1 already answered but was cancelled %d times", n)
	}
	if n := len(h.Adapters["author"].CancelCalls()); n != 0 {
		t.Fatalf("author already answered but was cancelled %d times", n)
	}
}

func TestCancelIsIdempotentAndRefusesOthers(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts)
	h.Hold("author")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	events := len(h.Events())

	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatalf("second Cancel = %v", err)
	}
	again, err := h.Store.GetTurn(turnID)
	if err != nil || again.TerminalState != row.TerminalState || again.TerminalReason != row.TerminalReason || again.Epoch != row.Epoch || again.PhaseAtCancel != row.PhaseAtCancel {
		t.Fatalf("turn changed from %+v to %+v, err %v", row, again, err)
	}
	if n := len(h.Events()); n != events {
		t.Fatalf("second Cancel wrote %d events", n-events)
	}
	if n := len(h.Adapters["author"].CancelCalls()); n != 1 {
		t.Fatalf("adapter Cancel called %d times", n)
	}

	wantRefusal(t, h.Engine.Cancel("no-such-turn"), wire.ErrorCodeNotFound)

	solo := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}})
	reply, err := solo.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, solo, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	wantRefusal(t, solo.Engine.Cancel(reply.TurnID), wire.ErrorCodeNotOpen)
}

func TestConfirmedCancelReleasesAndNextTurnDispatches(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}, enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts, enginetest.ClockStart(farFuture))
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, reply.TurnID, "author")[0]
	if err := h.Engine.Cancel(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	wantReleased(t, h, attemptID, "server_confirmed")
	if got := poolHolds(t, h, "default"); len(got) != 0 {
		t.Fatalf("pool holds = %+v", got)
	}

	next, err := h.Engine.Submit(soloReq("s2"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, next.TurnID, engine.StateSoloComplete, "solo_ok")
	waitParticipantState(t, h, "author", engine.ParticipantIdle)
}

func TestUnconfirmedCancelKeepsPoolHeldUntilOperatorRelease(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}, enginetest.Reply(authorText)}}
	// Deadlines far beyond the clock advance below, so the queued turn lives.
	h := enginetest.New(t, schedRoster("100000h", "100000h", []reviewer{{"r1", "p"}}, nil, ""), scripts,
		enginetest.ClockStart(farFuture), enginetest.CancelUnconfirmed())
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, reply.TurnID, "author")[0]
	if err := h.Engine.Cancel(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	hold := waitHold(t, h, attemptID, false)
	if hold.Pool != "default" || hold.EndpointName != "local" || hold.Model != "llama2" {
		t.Fatalf("hold = %+v", hold)
	}
	if got := getAttempt(t, h, attemptID).CancellationState; got != "unknown" {
		t.Fatalf("cancellation state = %q", got)
	}
	waitParticipantState(t, h, "author", engine.ParticipantUnknown)
	holds := poolHolds(t, h, "default")
	if len(holds) != 1 || holds[0].AttemptID != attemptID || holds[0].HeldSince == "" || len(holds[0].Group) != 1 {
		t.Fatalf("pool holds = %+v", holds)
	}

	// The next turn's dispatch waits, however far the clock goes.
	next, err := h.Engine.Submit(soloReq("s2"))
	if err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantQueued)
	h.Clock.Advance(10 * time.Minute)
	if got := participantState(t, h, "author"); got != engine.ParticipantQueued {
		t.Fatalf("author is %s", got)
	}
	if n := len(attemptsOf(h, next.TurnID, "author")); n != 0 {
		t.Fatalf("queued dispatch has %d attempts", n)
	}
	if _, found := getHold(t, h, attemptID); !found || len(poolHolds(t, h, "default")) != 1 {
		t.Fatal("hold released on its own")
	}

	rel, err := h.Engine.Release(wire.ReleaseRequest{Pool: "default", Reason: "server restarted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("released = %v", rel.Released)
	}
	wantEnd(t, h, next.TurnID, engine.StateSoloComplete, "solo_ok")
	hold = waitHold(t, h, attemptID, true)
	if hold.ReleasedHow != "operator" || hold.ReleasedReason != "server restarted" {
		t.Fatalf("hold = %+v", hold)
	}
	evs := capacityEvents(h, attemptID)
	last := evs[len(evs)-1]
	if last.State != "released" || last.How == nil || *last.How != "operator" || last.Reason == nil || *last.Reason != "server restarted" {
		t.Fatalf("capacity events = %+v", evs)
	}
	got := joinStates(stateEvents(h, "author"))
	for _, want := range []string{"cancelling", "unknown", "queued"} {
		if !strings.Contains(got, want) {
			t.Fatalf("author states = %s, want %s", got, want)
		}
	}
}

func TestAttemptDeadlineOnHangLeavesHold(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}, enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("60s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts,
		enginetest.ClockStart(farFuture), enginetest.CancelUnconfirmed())
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, reply.TurnID, "author")[0]
	h.Clock.Advance(61 * time.Second)
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "deadline:attempt:"+attemptID)

	waitHold(t, h, attemptID, false)
	waitFor(t, "adapter Cancel", func() bool { return len(h.Adapters["author"].CancelCalls()) == 1 })
	waitParticipantState(t, h, "author", engine.ParticipantUnknown)

	next, err := h.Engine.Submit(soloReq("s2"))
	if err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantQueued)
	if _, err := h.Engine.Release(wire.ReleaseRequest{Pool: "default", AttemptID: &attemptID, Reason: "checked"}); err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, next.TurnID, engine.StateSoloComplete, "solo_ok")
	if hold := waitHold(t, h, attemptID, true); hold.ReleasedHow != "operator" || hold.ReleasedReason != "checked" {
		t.Fatalf("hold = %+v", hold)
	}
}

func TestLateResultReleasesHold(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts, enginetest.CancelUnconfirmed())
	release := h.Hold("author")
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, reply.TurnID, "author")[0]
	if err := h.Engine.Cancel(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	waitHold(t, h, attemptID, false)
	row, err := h.Store.GetTurn(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}

	release()
	wantReleased(t, h, attemptID, "late_result")
	if !getAttempt(t, h, attemptID).Quarantined {
		t.Fatal("late result not quarantined")
	}
	after, err := h.Store.GetTurn(reply.TurnID)
	if err != nil || after.TerminalState != row.TerminalState || after.Epoch != row.Epoch {
		t.Fatalf("turn changed from %+v to %+v, err %v", row, after, err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantIdle)
	if got := poolHolds(t, h, "default"); len(got) != 0 {
		t.Fatalf("pool holds = %+v", got)
	}
}

func TestUncertainReleaseAfter(t *testing.T) {
	for _, tc := range []struct {
		after   string
		release bool
	}{{"1s", true}, {"0s", false}} {
		t.Run(tc.after, func(t *testing.T) {
			extra := "\n[pools.default]\nconcurrency = 1\nuncertain_release_after = \"" + tc.after + "\"\n"
			scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}, enginetest.Reply(authorText)}}
			h := enginetest.New(t, schedRoster("100000h", "100000h", []reviewer{{"r1", "p"}}, nil, extra), scripts,
				enginetest.ClockStart(farFuture), enginetest.CancelUnconfirmed())
			reply, err := h.Engine.Submit(soloReq("s1"))
			if err != nil {
				t.Fatal(err)
			}
			waitCount(t, h, wire.EventKindAttemptBegun, 1)
			attemptID := attemptsOf(h, reply.TurnID, "author")[0]
			if err := h.Engine.Cancel(reply.TurnID); err != nil {
				t.Fatal(err)
			}
			waitHold(t, h, attemptID, false)
			next, err := h.Engine.Submit(soloReq("s2"))
			if err != nil {
				t.Fatal(err)
			}
			waitParticipantState(t, h, "author", engine.ParticipantQueued)

			h.Clock.Advance(999 * time.Millisecond)
			if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
				t.Fatal("released before the timeout")
			}
			if !tc.release {
				h.Clock.Advance(10 * time.Minute)
				if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
					t.Fatalf("zero released on its own: %+v", hold)
				}
				return
			}
			h.Clock.Advance(time.Millisecond)
			wantReleased(t, h, attemptID, "operator_timeout")
			wantEnd(t, h, next.TurnID, engine.StateSoloComplete, "solo_ok")
		})
	}
}

// Two reviewers of one pool are in flight when the turn is cancelled, so the
// pool has two holds. The members of the pool have different models.
func TestReleaseErrorsAndHoldGroup(t *testing.T) {
	roster := strings.Replace(reviewRoster("author", "r1", "r2"), "id = \"r2\"\nmodel = \"llama3\"", "id = \"r2\"\nmodel = \"llama2\"", 1)
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {approveStep()},
		"r2":     {approveStep()},
	}
	h := enginetest.New(t, roster, scripts, enginetest.CancelUnconfirmed())
	h.Hold("r1")
	h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}
	r1 := attemptsOf(h, turnID, "r1")[0]
	r2 := attemptsOf(h, turnID, "r2")[0]

	// The group is every {endpoint_name, model} of the pool's members, not
	// only the held attempt's.
	want := []journal.GroupMember{{EndpointName: "local", Model: "llama2"}, {EndpointName: "local", Model: "llama3"}}
	for id, model := range map[string]string{r1: "llama3", r2: "llama2"} {
		hold := waitHold(t, h, id, false)
		if hold.Pool != "reviewers" || hold.Model != model || len(hold.Group) != 2 || hold.Group[0] != want[0] || hold.Group[1] != want[1] {
			t.Fatalf("hold = %+v", hold)
		}
	}
	if holds := poolHolds(t, h, "reviewers"); len(holds) != 2 || len(holds[0].Group) != 2 {
		t.Fatalf("pool holds = %+v", holds)
	}

	_, err := h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", Reason: "x"})
	ambiguous := wantRefusal(t, err, wire.ErrorCodeAmbiguous)
	var ids []string
	if err := json.Unmarshal(ambiguous.Holds, &ids); err != nil || len(ids) != 2 || (ids[0] != r1 && ids[1] != r1) || (ids[0] != r2 && ids[1] != r2) {
		t.Fatalf("ambiguous holds = %s, err %v", ambiguous.Holds, err)
	}
	if ambiguous.Pool == nil || *ambiguous.Pool != "reviewers" {
		t.Fatalf("ambiguous = %+v", ambiguous)
	}

	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "default", Reason: "x"})
	wantRefusal(t, err, wire.ErrorCodeNoHold)
	wrong := "no-such-attempt"
	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", AttemptID: &wrong, Reason: "x"})
	wantRefusal(t, err, wire.ErrorCodeNoSuchHold)
	// An attempt held in another pool is not that pool's hold.
	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "default", AttemptID: &r1, Reason: "x"})
	wantRefusal(t, err, wire.ErrorCodeNoSuchHold)
	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", AttemptID: &r1, Reason: ""})
	wantRefusal(t, err, wire.ErrorCodeReasonRequired)
	if _, found := getHold(t, h, r1); !found {
		t.Fatal("hold vanished")
	}
	if hold, _ := getHold(t, h, r1); hold.ReleasedAt != nil {
		t.Fatal("a refused release released the hold")
	}

	rel, err := h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", AttemptID: &r2, Reason: "x"})
	if err != nil || len(rel.Released) != 1 || rel.Released[0] != r2 {
		t.Fatalf("Release = %+v, %v", rel, err)
	}
	// With one hold left, the pool's only hold is named without an id.
	rel, err = h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", Reason: "y"})
	if err != nil || len(rel.Released) != 1 || rel.Released[0] != r1 {
		t.Fatalf("Release = %+v, %v", rel, err)
	}
	_, err = h.Engine.Release(wire.ReleaseRequest{Pool: "reviewers", Reason: "y"})
	wantRefusal(t, err, wire.ErrorCodeNoHold)
}

func TestCrashAfterTerminalTxLeavesHold(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts)
	h.Engine.SetCrashHook(func() bool { return true })
	h.Hold("author")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, turnID, "author")[0]
	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}

	if n := len(h.Adapters["author"].CancelCalls()); n != 0 {
		t.Fatalf("Adapter.Cancel called %d times after the crash point", n)
	}
	hold, found := getHold(t, h, attemptID)
	if !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
	if got := getAttempt(t, h, attemptID).CancellationState; got != "requested" {
		t.Fatalf("cancellation state = %q, want requested", got)
	}
	if row, err := h.Store.GetTurn(turnID); err != nil || row.TerminalState != engine.StateCancelled {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
}

// A block ends the turn while another reviewer is still in flight: that
// attempt is cancelled and its slot held, like any other terminal path.
func TestBlockCancelsInflightReviewer(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {approveStep()},
		"r2":     {blockStep("b1")},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts, enginetest.CancelUnconfirmed())
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, "block:") {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	r1 := attemptsOf(h, turnID, "r1")[0]
	waitHold(t, h, r1, false)
	waitFor(t, "r1 Cancel", func() bool { return len(h.Adapters["r1"].CancelCalls()) == 1 })
	waitFor(t, "unknown cancellation", func() bool { return getAttempt(t, h, r1).CancellationState == "unknown" })
	if n := len(h.Adapters["r2"].CancelCalls()); n != 0 {
		t.Fatalf("the blocking reviewer was cancelled %d times", n)
	}
	waitParticipantState(t, h, "r1", engine.ParticipantUnknown)

	release()
	wantReleased(t, h, r1, "late_result")
	waitParticipantState(t, h, "r1", engine.ParticipantIdle)
}

// A deadline ending the turn while a queued dispatch waits drops that
// dispatch and writes nothing for it.
func TestTerminalPathDropsQueuedDispatches(t *testing.T) {
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}, {"r2", "p"}}, nil, ""), approvers("r1", "r2"))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	waitParticipantState(t, h, "r2", engine.ParticipantQueued)
	if err := h.Engine.Cancel(turnID); err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "r2", engine.ParticipantIdle)
	release()
	waitParticipantState(t, h, "r1", engine.ParticipantIdle)
	if n := len(attemptsOf(h, turnID, "r2")); n != 0 {
		t.Fatalf("queued reviewer has %d attempts", n)
	}
	if n := len(h.Adapters["r2"].Calls()); n != 0 {
		t.Fatalf("queued reviewer was called %d times", n)
	}
}
