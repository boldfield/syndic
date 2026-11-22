package acceptance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	journalParagraph  = "Journal and races"
	capacityParagraph = "Capacity"
	deliveryParagraph = "Repair and delivery"
)

func init() {
	cover(journalParagraph, "a crash injected between a state write and its event leaves no such pair (they are one transaction)", "TestJournal_StateAndEventAreOneTransaction")
	cover(journalParagraph, "a result arriving after the turn's epoch was bumped is quarantined and the turn state is unchanged", "TestJournal_ResultAfterEpochBumpQuarantined")
	cover(journalParagraph, "a reviewer result arriving after a block ended the turn is quarantined", "TestJournal_ResultAfterBlockQuarantined")
	cover(journalParagraph, "`OverrideTurn` on a turn without a candidate errors", "TestJournal_OverrideWithoutCandidateErrors")
	cover(journalParagraph, "two blocks with the same reviewer-chosen id from different verdicts both store", "TestJournal_SameReviewerBlockIDBothStore")
	cover(journalParagraph, "migration 004 on a journal holding A-era probe runs leaves them with `kind = 'probe'` and `LatestProbeRecord` finds the newest by creation", "TestJournal_Migration004ProbeRuns")

	cover(capacityParagraph, "a client-deadline on a `Hang` step leaves a `capacity_holds` row", "TestCapacity_ClientDeadlineLeavesHold")
	cover(capacityParagraph, "the next dispatch to that pool waits", "TestCapacity_NextDispatchWaits")
	cover(capacityParagraph, "`/release` clears it with `how: operator`", "TestCapacity_ReleaseClearsWithOperator")
	cover(capacityParagraph, "a late result clears it with `how: late_result`", "TestCapacity_LateResultClears")
	cover(capacityParagraph, "the hold survives engine restart and appears in `engine_started`", "TestCapacity_HoldSurvivesRestart")
	cover(capacityParagraph, "a crash injected between the terminal transaction and the adapter `Cancel` call leaves the hold recorded", "TestCapacity_CrashBeforeCancelLeavesHold")
	cover(capacityParagraph, "a crash injected during recovery, before commit, leaves no scheduler dispatch in the journal", "TestCapacity_CrashDuringRecoveryLeavesNoDispatch")
	cover(capacityParagraph, "`Cancel` returning `ServerConfirmed: yes` releases the hold with `how: server_confirmed`", "TestCapacity_ServerConfirmedReleases")
	cover(capacityParagraph, "`uncertain_release_after = \"1s\"` with a fake clock releases with `how: operator_timeout` and `\"0s\"` never does", "TestCapacity_UncertainReleaseAfter")

	cover(deliveryParagraph, "the repair request's message array contains the original output verbatim as an `assistant` message", "TestRepair_RequestCarriesOriginalOutput")
	cover(deliveryParagraph, "terminal delivery dispatches one `terminal_delivery` attempt per participant and parses none of them", "TestRepair_OneDeliveryPerParticipantNoneParsed")
	cover(deliveryParagraph, "the delivery payload contains a reviewer's commentary that was outside its envelope", "TestRepair_DeliveryCarriesCommentary")
	cover(deliveryParagraph, "a delivery attempt that hangs ends at `deadlines.delivery` and is not retried", "TestRepair_DeliveryHangEndsAtDeadline")
	cover(deliveryParagraph, "a delivery attempt pending at restart is marked unknown and not re-sent", "TestRepair_PendingDeliveryUnknownNotResent")
	cover(deliveryParagraph, "`attempt_completed` events let a test distinguish two delivered from one delivered and one failed", "TestRepair_CompletionEventsDistinguishOutcomes")
	cover(deliveryParagraph, "delimiters differ between two attempts and are not derivable from attempt ids", "TestRepair_DelimitersDifferAndAreNotDerived")
}

const (
	phaseDeliveryName = "terminal_delivery"
	// attemptDeadline is the attempt deadline of enginetest.RosterTOML.
	attemptDeadline = 300 * time.Second
	// deliveryDeadline is the delivery deadline of enginetest.RosterTOML.
	deliveryDeadline = 60 * time.Second
)

// capacityFarFuture starts the fake clock far past the real one, so that a
// scripted Hang, which also honours the request's deadline on the real clock,
// never ends on its own.
var capacityFarFuture = time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)

// capacityTwoReviewers is a roster of an author and two reviewers whose pool
// admits two requests at once, so both reviewers are in flight together.
func capacityTwoReviewers() string {
	return nRolesTOML(2, 0, false) + "\n[pools.default]\nconcurrency = 2\n"
}

// capacityBegun waits for the attempt of the participant in the phase to be
// announced, then for the engine to finish dispatching it (a status request
// takes the engine's lock, which the dispatch holds until its timers are
// armed), and returns the announcement.
func (s *suite) capacityBegun(t *testing.T, c *client.Client, participant, phase string) wire.EvAttemptBegun {
	t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && b.Participant == participant && b.Phase == phase
	})
	s.Status(t, c)
	b, _ := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
	return b
}

// capacityReleased waits for the capacity event announcing the release of the
// attempt's hold.
func (s *suite) capacityReleased(attemptID string) wire.EvCapacity {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		cp, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cp.HeldByAttempt == attemptID && cp.State == "released"
	})
	cp, _ := body[wire.EvCapacity](ev, wire.EventKindCapacity)
	return cp
}

// wantReleasedHow requires the capacity event and the journal row of the hold
// to agree that it was released how.
func (s *suite) wantReleasedHow(t *testing.T, attemptID, how string) wire.EvCapacity {
	t.Helper()
	cp := s.capacityReleased(attemptID)
	if cp.How == nil || *cp.How != how {
		t.Fatalf("capacity event %+v, want released with how %q", cp, how)
	}
	h, found, err := s.Store.GetHold(attemptID)
	if err != nil || !found {
		t.Fatalf("hold %s: found=%v err=%v", attemptID, found, err)
	}
	if h.ReleasedAt == nil || h.ReleasedHow != how {
		t.Fatalf("hold row %+v, want released how %q", h, how)
	}
	return cp
}

// wantHoldUnreleased requires the attempt's hold row to be unreleased and the
// pool's status to list it.
func (s *suite) wantHoldUnreleased(t *testing.T, c *client.Client, attemptID string) journal.Hold {
	t.Helper()
	h, found, err := s.Store.GetHold(attemptID)
	if err != nil || !found {
		t.Fatalf("hold %s: found=%v err=%v, want a row", attemptID, found, err)
	}
	if h.ReleasedAt != nil || h.ReleasedHow != "" {
		t.Fatalf("hold row %+v, want unreleased", h)
	}
	holds := s.holds(t, c, "default")
	if len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool default holds %+v, want only %s", holds, attemptID)
	}
	return h
}

// capacityFixture is a review turn whose reviewer's request outlived its
// attempt deadline, so its slot is held with the outcome unknown.
type capacityFixture struct {
	s         *suite
	c         *client.Client
	turnID    string
	attemptID string
	// release lets a gated reviewer's request go on, when the fixture was
	// built gated.
	release func()
}

// capacityHeld runs a review turn on rosterTOML whose reviewer does not
// answer and advances the fake clock to its attempt deadline. The adapters
// answer Cancel with an unknown confirmation, so the hold stays. If gated, the
// reviewer's request is held at the adapter instead of hanging in it, and
// release lets it answer normally, late.
func capacityHeld(t *testing.T, rosterTOML string, gated bool) *capacityFixture {
	t.Helper()
	first := fake.Step{Kind: fake.StepKindHang}
	if gated {
		first = normal()
	}
	s := newSuite(t, rosterTOML, map[string][]fake.Step{
		"author":   repeat(8),
		"reviewer": append([]fake.Step{first}, repeat(8)...),
	}, enginetest.ClockStart(capacityFarFuture), enginetest.CancelUnconfirmed())
	f := &capacityFixture{s: s, release: func() {}}
	if gated {
		f.release = s.h.Hold("reviewer")
	}
	f.c = s.Dial(t)
	f.turnID = s.Submit(t, f.c, "s1")
	f.attemptID = s.capacityBegun(t, f.c, "reviewer", "review").AttemptID
	s.Clock.Advance(attemptDeadline)
	s.WaitTerminal(f.turnID)
	wantTurn(t, s.Turn(t, f.c, f.turnID), engine.StateIncomplete, "deadline:attempt:"+f.attemptID)
	return f
}

// capacityScripts scripts the participants of enginetest.RosterTOML to answer
// whatever they are asked.
func capacityScripts() map[string][]fake.Step {
	return map[string][]fake.Step{"author": repeat(8), "reviewer": repeat(8)}
}

func TestCapacity_ClientDeadlineLeavesHold(t *testing.T) {
	t.Parallel()
	f := capacityHeld(t, enginetest.RosterTOML, false)
	s := f.s

	h := s.wantHoldUnreleased(t, f.c, f.attemptID)
	if h.Pool != "default" || h.EndpointName != "local" || h.Model != "llama3" || len(h.Group) == 0 {
		t.Fatalf("hold row %+v, want the reviewer's pool, endpoint and model with its group", h)
	}
	s.wantHold(t, f.c, f.attemptID)
	a, err := s.Store.GetAttempt(f.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if str(a.Status) != string(transport.StatusInterrupted) {
		t.Fatalf("attempt ended %s, want interrupted", str(a.Status))
	}
	// The cut-off request is the only one the reviewer was given.
	s.wantReviewCalls(t, 1)
}

func TestCapacity_NextDispatchWaits(t *testing.T) {
	t.Parallel()
	f := capacityHeld(t, enginetest.RosterTOML, false)
	s := f.s
	authorCalls := len(s.Adapters["author"].Calls())

	second := s.Submit(t, f.c, "s2")
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && p.Participant == "author" && p.State == engine.ParticipantQueued
	})
	// Time passing, short of the turn deadline, does not free the pool.
	s.Clock.Advance(10 * time.Minute)

	if rows, err := s.Store.ListAttempts(second); err != nil || len(rows) != 0 {
		t.Fatalf("queued turn has attempts %+v (err %v), want none", rows, err)
	}
	if n := len(s.Adapters["author"].Calls()); n != authorCalls {
		t.Fatalf("author received %d requests while the pool was held, want %d", n, authorCalls)
	}
	if turn := s.Turn(t, f.c, second); terminal(turn.State) {
		t.Fatalf("second turn is %q while the pool is held", turn.State)
	}
	s.wantHoldUnreleased(t, f.c, f.attemptID)
}

func TestCapacity_ReleaseClearsWithOperator(t *testing.T) {
	t.Parallel()
	f := capacityHeld(t, enginetest.RosterTOML, false)
	s := f.s
	second := s.Submit(t, f.c, "s2")
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && p.Participant == "author" && p.State == engine.ParticipantQueued
	})

	reply, err := f.c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "checked the endpoint"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !reflect.DeepEqual(reply.Released, []string{f.attemptID}) {
		t.Fatalf("released %v, want [%s]", reply.Released, f.attemptID)
	}
	cp := s.wantReleasedHow(t, f.attemptID, "operator")
	if cp.Reason == nil || *cp.Reason != "checked the endpoint" {
		t.Fatalf("capacity event %+v, want the operator's reason", cp)
	}
	if h, _, _ := s.Store.GetHold(f.attemptID); h.ReleasedReason != "checked the endpoint" {
		t.Fatalf("hold row reason %q, want the operator's", h.ReleasedReason)
	}
	if h := s.holds(t, f.c, "default"); len(h) != 0 {
		t.Fatalf("pool default holds %+v after release", h)
	}
	s.WaitTerminal(second)
	wantTurn(t, s.Turn(t, f.c, second), engine.StateApproved, "all_approved")
}

func TestCapacity_LateResultClears(t *testing.T) {
	t.Parallel()
	f := capacityHeld(t, enginetest.RosterTOML, true)
	s := f.s
	s.wantHoldUnreleased(t, f.c, f.attemptID)
	before := s.Turn(t, f.c, f.turnID)

	// The request the engine gave up on now answers, completely: the server
	// had finished, so the slot is free.
	f.release()
	cp := s.wantReleasedHow(t, f.attemptID, "late_result")
	if cp.Pool != "default" {
		t.Fatalf("capacity event %+v, want pool default", cp)
	}
	if h := s.holds(t, f.c, "default"); len(h) != 0 {
		t.Fatalf("pool default holds %+v after the late result", h)
	}
	after := s.Turn(t, f.c, f.turnID)
	wantTurn(t, after, before.State, "deadline:attempt:"+f.attemptID)
	if len(after.Verdicts) != 0 {
		t.Fatalf("the late result produced verdicts %+v", after.Verdicts)
	}
	next := s.Submit(t, f.c, "s2")
	s.WaitTerminal(next)
	wantTurn(t, s.Turn(t, f.c, next), engine.StateApproved, "all_approved")
}

func TestCapacity_HoldSurvivesRestart(t *testing.T) {
	t.Parallel()
	f := capacityHeld(t, enginetest.RosterTOML, false)
	s := f.s
	s.Restart(enginetest.RosterTOML, capacityScripts())

	c2 := s.Dial(t)
	hello := c2.Hello()
	ev := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq > hello.RunFirstSeq })
	started, _ := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
	if !reflect.DeepEqual(started.UncertainCapacity, []string{"default"}) || len(started.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v, want uncertain_capacity [default] and no orphaned holds", started)
	}
	s.wantHoldUnreleased(t, c2, f.attemptID)

	second := s.Submit(t, c2, "s2")
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && p.Participant == "author" && p.State == engine.ParticipantQueued && ev.Seq > hello.RunFirstSeq
	})
	if rows, err := s.Store.ListAttempts(second); err != nil || len(rows) != 0 {
		t.Fatalf("the new engine dispatched %+v (err %v) into the held pool", rows, err)
	}
}

// TestCapacity_CrashBeforeCancelLeavesHold stops the engine where a crash
// between the terminal transaction and the adapter's Cancel call would: the
// fake adapter's Cancel blocks, so the engine has committed the terminal
// transaction and goes no further, and the journal is what a process killed at
// that point leaves. The engine's own crash hook is test-only inside package
// engine and cannot be reached from here.
func TestCapacity_CrashBeforeCancelLeavesHold(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(8),
		"reviewer": append([]fake.Step{{Kind: fake.StepKindHang}}, repeat(8)...),
	}, enginetest.ClockStart(capacityFarFuture), enginetest.BlockingCancel())
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	attemptID := s.capacityBegun(t, c, "reviewer", "review").AttemptID
	// Cancel does not return while the adapter's Cancel blocks. The engine is
	// asked directly, because a socket request parked in it would keep the
	// server from stopping at the restart below.
	cancelled := make(chan error, 1)
	go func() { cancelled <- s.Engine.Cancel(turnID) }()
	// Runs before the engines are stopped: a blocked Cancel would stall Stop.
	t.Cleanup(func() {
		s.h.ReleaseBlockingCancel()
		if err := <-cancelled; err != nil {
			t.Logf("cancel: %v", err)
		}
	})
	s.WaitTerminal(turnID)

	s.wantHoldUnreleased(t, c, attemptID)
	if a, err := s.Store.GetAttempt(attemptID); err != nil || a.CancellationState != "requested" {
		t.Fatalf("attempt cancellation state %v (err %v), want requested: Cancel has not returned", a, err)
	}

	s.Restart(enginetest.RosterTOML, capacityScripts())
	c2 := s.Dial(t)
	hello := c2.Hello()
	ev := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq > hello.RunFirstSeq })
	started, _ := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
	if !reflect.DeepEqual(started.UncertainCapacity, []string{"default"}) {
		t.Fatalf("engine_started = %+v, want uncertain_capacity [default]", started)
	}
	s.wantHoldUnreleased(t, c2, attemptID)
	wantTurn(t, s.Turn(t, c2, turnID), engine.StateCancelled, "cancelled")
}

// capacityFailedStart builds an engine on the suite's journal as Restart does,
// after the same crash of the old one, but returns what Start says instead of
// failing the test, with the adapters the new engine built.
func (s *suite) capacityFailedStart(scripts map[string][]fake.Step) (map[string]*fake.Adapter, error) {
	s.t.Helper()
	s.stopServer()
	s.h.Crash()
	var mu sync.Mutex
	adapters := map[string]*fake.Adapter{}
	eng, err := engine.New(engine.Options{
		NoStartupProbes: true, // these tests seed the records they need
		Store:           s.Store,
		Hub:             s.Hub,
		RosterPath:      s.RosterPath,
		EngineVersion:   enginetest.EngineVersion,
		Clock:           s.Clock,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			a := fake.New(scripts[p.ID], fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel})
			mu.Lock()
			adapters[p.ID] = a
			mu.Unlock()
			return a, enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		s.t.Fatalf("engine.New: %v", err)
	}
	s.t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	startErr := eng.Start()
	mu.Lock()
	defer mu.Unlock()
	return adapters, startErr
}

// journalCount reads the journal's row counts the protocol does not show.
func (s *suite) journalCount(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := s.DB.Conn().QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestCapacity_CrashDuringRecoveryLeavesNoDispatch makes the recovery
// transaction fail on its last event, with a trigger in the journal, which is
// what a crash before its commit amounts to: the transaction rolls back.
func TestCapacity_CrashDuringRecoveryLeavesNoDispatch(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(8),
		"reviewer": append([]fake.Step{{Kind: fake.StepKindHang}}, repeat(8)...),
	}, enginetest.ClockStart(capacityFarFuture))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.capacityBegun(t, c, "reviewer", "review")

	rowsBefore, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	seqBefore, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	const dispatches = `SELECT COUNT(*) FROM attempt_dispatches`
	const holds = `SELECT COUNT(*) FROM capacity_holds`
	dispatchesBefore := s.journalCount(t, dispatches)

	if _, err := s.DB.Conn().Exec(`CREATE TRIGGER capacity_recovery_crash BEFORE INSERT ON events
		WHEN NEW.event_type = 'turn_state' AND json_extract(NEW.data, '$.reason') = 'engine_restart'
		BEGIN SELECT RAISE(ABORT, 'injected crash before the recovery commit'); END`); err != nil {
		t.Fatal(err)
	}
	adapters, startErr := s.capacityFailedStart(capacityScripts())
	if startErr == nil {
		t.Fatal("Start succeeded despite the failing recovery transaction")
	}

	if seq, err := s.Store.MaxSeq(); err != nil || seq != seqBefore {
		t.Fatalf("event seq %d -> %d (err %v): the failed recovery wrote events", seqBefore, seq, err)
	}
	rowsAfter, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfter) != len(rowsBefore) {
		t.Fatalf("turn has %d attempts after the failed recovery, want %d: nothing was dispatched", len(rowsAfter), len(rowsBefore))
	}
	for i := range rowsBefore {
		if rowsAfter[i].ID != rowsBefore[i].ID || rowsAfter[i].State != rowsBefore[i].State || str(rowsAfter[i].Status) != str(rowsBefore[i].Status) {
			t.Fatalf("attempt %+v changed to %+v by the failed recovery", rowsBefore[i], rowsAfter[i])
		}
	}
	if n := s.journalCount(t, dispatches); n != dispatchesBefore {
		t.Fatalf("%d attempt_dispatches rows, want %d: the failed recovery dispatched", n, dispatchesBefore)
	}
	if n := s.journalCount(t, holds); n != 0 {
		t.Fatalf("%d capacity holds, want none from a recovery that did not commit", n)
	}
	if row, err := s.Store.GetTurn(turnID); err != nil || row.TerminalState != "" {
		t.Fatalf("turn %+v (err %v), want it still open", row, err)
	}
	for id, a := range adapters {
		if n := len(a.Calls()); n != 0 {
			t.Fatalf("%s received %d requests from the engine whose recovery failed", id, n)
		}
	}

	// Without the fault the same journal recovers: the failure is what left
	// it untouched.
	if _, err := s.DB.Conn().Exec(`DROP TRIGGER capacity_recovery_crash`); err != nil {
		t.Fatal(err)
	}
	s.Restart(enginetest.RosterTOML, capacityScripts())
	st := s.WaitTerminal(turnID)
	if st.State != engine.StateIncomplete || st.Reason == nil || *st.Reason != "engine_restart" {
		t.Fatalf("recovered turn %+v, want incomplete engine_restart", st)
	}
	for id, a := range s.Adapters {
		if n := len(a.Calls()); n != 0 {
			t.Fatalf("%s received %d requests during recovery, want none re-sent", id, n)
		}
	}
}

func TestCapacity_ServerConfirmedReleases(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(8),
		"reviewer": append([]fake.Step{{Kind: fake.StepKindHang}}, repeat(8)...),
	}, enginetest.ClockStart(capacityFarFuture))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	attemptID := s.capacityBegun(t, c, "reviewer", "review").AttemptID
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)

	cp := s.wantReleasedHow(t, attemptID, "server_confirmed")
	if cp.Pool != "default" || cp.Reason != nil {
		t.Fatalf("capacity event %+v, want pool default released with no operator reason", cp)
	}
	if got := s.Adapters["reviewer"].CancelCalls(); !reflect.DeepEqual(got, []string{attemptID}) {
		t.Fatalf("Cancel calls %v, want [%s]", got, attemptID)
	}
	if h := s.holds(t, c, "default"); len(h) != 0 {
		t.Fatalf("pool default holds %+v after a confirmed cancel", h)
	}
	// The pool is free without any operator action.
	next := s.Submit(t, c, "s2")
	s.WaitTerminal(next)
	wantTurn(t, s.Turn(t, c, next), engine.StateApproved, "all_approved")
}

func TestCapacity_UncertainReleaseAfter(t *testing.T) {
	for _, after := range []string{"1s", "0s"} {
		t.Run(after, func(t *testing.T) {
			t.Parallel()
			roster := enginetest.RosterTOML + "\n[pools.default]\nconcurrency = 1\nuncertain_release_after = \"" + after + "\"\n"
			f := capacityHeld(t, roster, false)
			s := f.s
			s.wantHoldUnreleased(t, f.c, f.attemptID)

			if after == "0s" {
				// Zero means the hold is never released on its own, however
				// long the clock runs.
				s.Clock.Advance(24 * time.Hour)
				s.wantHoldUnreleased(t, f.c, f.attemptID)
				for _, ev := range s.Events() {
					if cp, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok && cp.State == "released" {
						t.Fatalf("capacity event %+v: the hold was released", cp)
					}
				}
				return
			}
			s.Clock.Advance(999 * time.Millisecond)
			s.wantHoldUnreleased(t, f.c, f.attemptID)
			s.Clock.Advance(time.Millisecond)
			cp := s.wantReleasedHow(t, f.attemptID, "operator_timeout")
			if cp.Reason == nil || *cp.Reason != "uncertain_release_after" {
				t.Fatalf("capacity event %+v, want reason uncertain_release_after", cp)
			}
			if h := s.holds(t, f.c, "default"); len(h) != 0 {
				t.Fatalf("pool default holds %+v after the timeout", h)
			}
		})
	}
}

// journalFailEvent installs a trigger that aborts the insert of any event for
// which cond holds, so a transaction that wrote state before that event rolls
// back whole.
func (s *suite) journalFailEvent(t *testing.T, name, cond string) (remove func()) {
	t.Helper()
	if _, err := s.DB.Conn().Exec(`CREATE TRIGGER ` + name + ` BEFORE INSERT ON events WHEN ` + cond +
		` BEGIN SELECT RAISE(ABORT, 'injected failure at the event'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := s.DB.Conn().Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJournal_StateAndEventAreOneTransaction(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {{Kind: fake.StepKindHang}},
		"reviewer": repeat(4),
	}, enginetest.ClockStart(capacityFarFuture))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	attemptID := s.capacityBegun(t, c, "author", "author").AttemptID

	turnBefore, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	attemptBefore, err := s.Store.GetAttempt(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	seqBefore, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}

	// The cancel's terminal transaction bumps the epoch, requests the
	// attempt's cancellation, opens its hold and ends the turn, and its last
	// write is the turn_state event. That event fails.
	remove := s.journalFailEvent(t, "journal_inject_failure",
		`NEW.event_type = 'turn_state' AND json_extract(NEW.data, '$.state') = 'cancelled'`)
	if err := c.Cancel(s.ctx(), turnID); err == nil {
		t.Fatal("cancel succeeded although the transaction could not commit")
	}
	turn, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.TerminalState != "" || turn.Epoch != turnBefore.Epoch {
		t.Fatalf("turn is %q at epoch %d, want it open at epoch %d: a state write outlived its event", turn.TerminalState, turn.Epoch, turnBefore.Epoch)
	}
	if _, found, err := s.Store.GetHold(attemptID); err != nil || found {
		t.Fatalf("hold found=%v err=%v: a state write outlived its event", found, err)
	}
	if a, err := s.Store.GetAttempt(attemptID); err != nil || a.CancellationState != attemptBefore.CancellationState {
		t.Fatalf("cancellation state %v (err %v), want %q", a, err, attemptBefore.CancellationState)
	}
	if seq, err := s.Store.MaxSeq(); err != nil || seq != seqBefore {
		t.Fatalf("event seq %d -> %d (err %v): an event outlived its state", seqBefore, seq, err)
	}
	if n := len(s.Adapters["author"].CancelCalls()); n != 0 {
		t.Fatalf("Cancel was called %d times for a transaction that did not commit", n)
	}
	if st := s.Turn(t, c, turnID).State; terminal(st) {
		t.Fatalf("turn reports %q", st)
	}

	// With the fault gone the same cancel writes both: the pair exists or
	// neither does.
	remove()
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTurnState(turnID, engine.StateCancelled)
	turn, err = s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.TerminalState != engine.StateCancelled || turn.Epoch != turnBefore.Epoch+1 {
		t.Fatalf("turn is %q at epoch %d, want cancelled at epoch %d", turn.TerminalState, turn.Epoch, turnBefore.Epoch+1)
	}
	if _, found, err := s.Store.GetHold(attemptID); err != nil || !found {
		t.Fatalf("hold found=%v err=%v, want the row written with the state", found, err)
	}
}

func TestJournal_ResultAfterEpochBumpQuarantined(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	}, enginetest.ClockStart(capacityFarFuture))
	release := s.h.Hold("reviewer")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	attemptID := s.capacityBegun(t, c, "reviewer", "review").AttemptID
	// Cancelling bumps the turn's epoch while the reviewer's request is out.
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	before, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Epoch == 0 {
		t.Fatal("cancel did not bump the epoch")
	}
	turnBefore := s.Turn(t, c, turnID)

	// The reviewer now answers with a perfectly good approval of the epoch
	// it was asked in.
	release()
	s.WaitEvent(func(ev event) bool {
		cp, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && cp.AttemptID == attemptID
	})

	a, err := s.Store.GetAttempt(attemptID)
	if err != nil || !a.Quarantined {
		t.Fatalf("attempt %+v (err %v), want it quarantined", a, err)
	}
	after, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason || after.Epoch != before.Epoch {
		t.Fatalf("turn changed from %+v to %+v by a quarantined result", before, after)
	}
	turnAfter := s.Turn(t, c, turnID)
	if turnAfter.State != engine.StateCancelled || len(turnAfter.Verdicts) != 0 || len(turnAfter.OpenBlocks) != 0 || !reflect.DeepEqual(turnAfter.TerminalReason, turnBefore.TerminalReason) {
		t.Fatalf("turn changed from %+v to %+v", turnBefore, turnAfter)
	}
	if rows := s.verdictRows(t, turnID); len(rows) != 0 {
		t.Fatalf("verdicts %+v stored from a quarantined result", rows)
	}
	for _, ev := range s.Events() {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && st.State != engine.StateCancelled && terminal(st.State) {
			t.Fatalf("turn announced %q after it was cancelled", st.State)
		}
	}
}

func TestJournal_ResultAfterBlockQuarantined(t *testing.T) {
	t.Parallel()
	scripts := map[string][]fake.Step{
		"author": repeat(4),
		"r1":     repeat(4),
		"r2": {verdict(func(hash, attempt string) string {
			return fake.VerdictBlock(hash, attempt, "b1", "high", "the evidence", "the remedy", "the reason")
		}), normal(), normal()},
	}
	s := newSuite(t, capacityTwoReviewers(), scripts, enginetest.ClockStart(capacityFarFuture))
	releaseApprover := s.h.Hold("r1")
	releaseBlocker := s.h.Hold("r2")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	late := s.capacityBegun(t, c, "r1", "review").AttemptID
	s.capacityBegun(t, c, "r2", "review")

	// The blocker answers first and ends the turn; the approver is still out.
	releaseBlocker()
	s.WaitTerminal(turnID)
	before := s.Turn(t, c, turnID)
	if len(before.OpenBlocks) != 1 || before.State != engine.StateIncomplete {
		t.Fatalf("turn %+v, want incomplete with one open block", before)
	}
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}

	releaseApprover()
	s.WaitEvent(func(ev event) bool {
		cp, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && cp.AttemptID == late
	})
	a, err := s.Store.GetAttempt(late)
	if err != nil || !a.Quarantined {
		t.Fatalf("approver's attempt %+v (err %v), want it quarantined", a, err)
	}
	for _, v := range s.verdictRows(t, turnID) {
		if v.AttemptID == late {
			t.Fatalf("verdict %+v recorded from the late result", v)
		}
	}
	after := s.Turn(t, c, turnID)
	wantTurn(t, after, engine.StateIncomplete, *before.TerminalReason)
	if len(after.Verdicts) != 1 || len(after.OpenBlocks) != 1 {
		t.Fatalf("turn %+v, want the block's verdict and block alone", after)
	}
	if now, err := s.Store.GetTurn(turnID); err != nil || now.TerminalState != row.TerminalState || now.Epoch != row.Epoch {
		t.Fatalf("turn row %+v (err %v), want %+v", now, err, row)
	}
}

func TestJournal_OverrideWithoutCandidateErrors(t *testing.T) {
	t.Parallel()
	scripts := reviewScripts(verdict(fake.VerdictApprove))
	scripts["author"] = []fake.Step{{Kind: fake.StepKindReply, Text: "cut off", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}}
	s := newSuite(t, enginetest.RosterTOML, scripts)
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	before := s.Turn(t, c, turnID)
	wantTurn(t, before, engine.StateIncomplete, "author_failed:truncated")
	if before.CandidateHash != nil {
		t.Fatalf("turn has candidate %s, want none", *before.CandidateHash)
	}

	err := c.Override(s.ctx(), turnID, "the operator accepts it")
	_ = runsRefusal(t, err, wire.ErrorCodeNotIncomplete)

	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "author_failed:truncated")
	row, err := s.Store.GetTurn(turnID)
	if err != nil || row.TerminalState != engine.StateIncomplete {
		t.Fatalf("turn row %+v (err %v), want it still incomplete", row, err)
	}
	for _, ev := range s.Events() {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.State == engine.StateOverridden {
			t.Fatalf("turn_state %+v announced for a refused override", st)
		}
	}
}

func TestJournal_SameReviewerBlockIDBothStore(t *testing.T) {
	t.Parallel()
	block := verdict(func(hash, attempt string) string {
		return fake.VerdictBlock(hash, attempt, "b1", "high", "the evidence", "the remedy", "the reason")
	})
	scripts := map[string][]fake.Step{
		"author": repeat(4),
		"r1":     {block, normal(), normal()},
		"r2":     {block, normal(), normal()},
	}
	s := newSuite(t, capacityTwoReviewers(), scripts, enginetest.ClockStart(capacityFarFuture))
	releaseFirst := s.h.Hold("r1")
	releaseSecond := s.h.Hold("r2")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.capacityBegun(t, c, "r1", "review")
	secondAttempt := s.capacityBegun(t, c, "r2", "review").AttemptID

	releaseFirst()
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if len(turn.OpenBlocks) != 1 || turn.CandidateHash == nil {
		t.Fatalf("turn %+v, want one open block on a candidate", turn)
	}
	first := turn.OpenBlocks[0].ID
	if !strings.HasSuffix(first, ":b1") {
		t.Fatalf("block id %q, want the reviewer's id b1 after the verdict's", first)
	}

	// The turn has ended, so the second reviewer's block cannot arrive through
	// the engine; a verdict of the other reviewer's attempt, naming the same
	// block id, is stored directly.
	var second string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		verdictID, err := tx.RecordVerdict(secondAttempt, *turn.CandidateHash, "block", "the reason", "valid")
		if err != nil {
			return err
		}
		second, err = tx.OpenBlockFor(verdictID, "b1", *turn.CandidateHash, "high", "the evidence", "the remedy")
		return err
	})
	releaseSecond()
	if err != nil {
		t.Fatalf("second block with the same reviewer-chosen id: %v", err)
	}
	if second == first || !strings.HasSuffix(second, ":b1") {
		t.Fatalf("block ids %q and %q, want two distinct ids ending in :b1", first, second)
	}
	blocks, err := s.Store.OpenBlocks(turnID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, b := range blocks {
		if b.ReviewerBlockID != "b1" {
			t.Fatalf("stored block %+v, want reviewer id b1", b)
		}
		ids[b.ID] = true
	}
	if len(blocks) != 2 || !ids[first] || !ids[second] {
		t.Fatalf("stored blocks %+v, want both %s and %s", blocks, first, second)
	}
	if got := s.Turn(t, c, turnID).OpenBlocks; len(got) != 2 {
		t.Fatalf("turn reports open blocks %+v, want both", got)
	}
}

func TestJournal_Migration004ProbeRuns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	// The schema as A left it: migrations 001 to 003 applied.
	exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	for _, f := range []string{"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql"} {
		sqlText, err := os.ReadFile(filepath.Join("..", "journal", "migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		exec(string(sqlText))
	}
	exec(`INSERT INTO schema_version (version) VALUES (1), (2), (3)`)
	var hasKind int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name = 'kind'`).Scan(&hasKind); err != nil || hasKind != 0 {
		t.Fatalf("pre-004 runs table has kind: count %d, err %v", hasKind, err)
	}

	// A-era probe runs: bare exports. The run created last by the clock is
	// not the one inserted last.
	manifest := func(endpoint, model string) string {
		return fmt.Sprintf(`{"endpoint_name":%q,"model":%q,"redacted":true}`, endpoint, model)
	}
	insert := func(id, createdAt, endpoint, model string) {
		exec(`INSERT INTO runs (id, created_at, manifest, manifest_hash) VALUES (?, ?, ?, ?)`, id, createdAt, manifest(endpoint, model), "hash-"+id)
	}
	insert("probe-oldest", "2026-01-01 00:00:00", "local", "llama3")
	insert("probe-newest", "2026-03-01 00:00:00", "local", "llama3")
	insert("probe-middle", "2026-02-01 00:00:00", "local", "llama3")
	insert("probe-other", "2026-02-15 00:00:00", "local", "llama2")
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	rows, err := db.Conn().Query(`SELECT id, kind FROM runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		kinds[id] = kind
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"probe-oldest": "probe", "probe-newest": "probe", "probe-middle": "probe", "probe-other": "probe"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("run kinds after migration %v, want every A-era run a probe: %v", kinds, want)
	}

	store := journal.NewStore(db)
	rec, found, err := store.LatestProbeRecord("local", "llama3")
	if err != nil || !found {
		t.Fatalf("LatestProbeRecord: found=%v err=%v", found, err)
	}
	if rec.RunID != "probe-newest" || !rec.CreatedAt.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("LatestProbeRecord = %s created %v, want probe-newest created 2026-03-01, not the last inserted", rec.RunID, rec.CreatedAt)
	}
	if string(rec.Manifest) != manifest("local", "llama3") {
		t.Fatalf("record manifest %s, want the stored bytes", rec.Manifest)
	}
	if rec, found, err := store.LatestProbeRecord("local", "llama2"); err != nil || !found || rec.RunID != "probe-other" {
		t.Fatalf("LatestProbeRecord for llama2 = %+v found=%v err=%v, want probe-other", rec, found, err)
	}
	if _, found, err := store.LatestProbeRecord("local", "unknown"); err != nil || found {
		t.Fatalf("LatestProbeRecord for an unprobed model: found=%v err=%v, want none", found, err)
	}

	// An engine run written afterwards is newer than every probe and is never
	// taken for one.
	engineRun, _, err := store.CreateEngineRun([]byte(manifest("local", "llama3")))
	if err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := db.Conn().QueryRow(`SELECT kind FROM runs WHERE id = ?`, engineRun).Scan(&kind); err != nil || kind != "engine" {
		t.Fatalf("new run kind %q (err %v), want engine", kind, err)
	}
	if rec, _, err := store.LatestProbeRecord("local", "llama3"); err != nil || rec.RunID != "probe-newest" {
		t.Fatalf("LatestProbeRecord = %+v (err %v) after an engine run, want probe-newest", rec, err)
	}
}

// repairDeliveries waits until n delivery attempts have begun and every one
// that has begun has completed, and returns the announcements of those that
// began, in order.
func (s *suite) repairDeliveries(n int) []wire.EvAttemptBegun {
	s.t.Helper()
	var begun []wire.EvAttemptBegun
	done := map[string]bool{}
	s.WaitEvent(func(ev event) bool {
		if b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun); ok && b.Phase == phaseDeliveryName {
			begun = append(begun, b)
		}
		if cp, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok {
			done[cp.AttemptID] = true
		}
		if len(begun) < n {
			return false
		}
		for _, b := range begun {
			if !done[b.AttemptID] {
				return false
			}
		}
		return true
	})
	return begun
}

// repairUserText is the text of the last message of a request.
func repairUserText(req transport.CompleteRequest) string {
	return req.Messages[len(req.Messages)-1].Content
}

func TestRepair_RequestCarriesOriginalOutput(t *testing.T) {
	t.Parallel()
	original := fake.VerdictProseApprove()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": {enginetest.Reply(original), verdict(fake.VerdictApprove), normal(), normal()},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	var requests []transport.CompleteRequest
	for _, call := range s.Adapters["reviewer"].Calls() {
		if call.Request.Envelope.Phase != phaseDeliveryName {
			requests = append(requests, call.Request)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("reviewer received %d requests before the verdict, want the review and its repair", len(requests))
	}
	review, repair := requests[0], requests[1]
	var repairRecord *wire.AttemptRecord
	for i, a := range turn.Attempts {
		if a.RepairOf != nil {
			repairRecord = &turn.Attempts[i]
		}
	}
	if repairRecord == nil || repairRecord.ID != repair.Envelope.AttemptID || *repairRecord.RepairOf != review.Envelope.AttemptID {
		t.Fatalf("attempts %+v, want the second request to be the repair of the first", turn.Attempts)
	}

	msgs := repair.Messages
	if len(msgs) != len(review.Messages)+2 {
		t.Fatalf("repair has %d messages, want the review's %d plus the output and the instruction", len(msgs), len(review.Messages))
	}
	// The system message names the attempt the reply is for; the request the
	// reviewer is asked about is the same.
	if msgs[0].Role != "system" || !reflect.DeepEqual(msgs[1:len(review.Messages)], review.Messages[1:]) {
		t.Fatalf("repair's leading messages %+v differ from the review's %+v", msgs[:len(review.Messages)], review.Messages)
	}
	echo := msgs[len(review.Messages)]
	if echo.Role != "assistant" || echo.Content != original {
		t.Fatalf("repair message %+v, want the original output verbatim as an assistant message", echo)
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" {
		t.Fatalf("repair ends with a %q message, want the user's instruction", last.Role)
	}
}

func TestRepair_OneDeliveryPerParticipantNoneParsed(t *testing.T) {
	t.Parallel()
	// Every delivery reply is a well-formed approval of the candidate, which
	// would be a verdict if it were parsed.
	approve := verdict(fake.VerdictApprove)
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK), approve},
		"r1":     {approve, approve},
		"r2":     {approve, approve},
	}
	s := newSuite(t, nRolesTOML(2, 0, false), scripts)
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	deliveries := s.repairDeliveries(3)
	delivered := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindVerdictsDelivered })
	vd, _ := body[wire.EvVerdictsDelivered](delivered, wire.EventKindVerdictsDelivered)

	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
	perParticipant := map[string]int{}
	ids := map[string]bool{}
	for _, d := range deliveries {
		perParticipant[d.Participant]++
		ids[d.AttemptID] = true
	}
	if !reflect.DeepEqual(perParticipant, map[string]int{"author": 1, "r1": 1, "r2": 1}) {
		t.Fatalf("delivery attempts per participant %v, want exactly one each", perParticipant)
	}
	if vd.TurnID != turnID || len(vd.Recipients) != 3 {
		t.Fatalf("verdicts_delivered %+v, want the three participants", vd)
	}
	for _, id := range []string{"author", "r1", "r2"} {
		if n := len(s.calls(id, phaseDeliveryName)); n != 1 {
			t.Fatalf("%s received %d delivery requests, want 1", id, n)
		}
	}
	// None of the replies was parsed: no verdict, no gate error, no change to
	// the turn.
	verdicts := s.verdictRows(t, turnID)
	if len(verdicts) != 2 {
		t.Fatalf("turn has %d verdicts, want the two reviewers' own", len(verdicts))
	}
	for _, v := range verdicts {
		if ids[v.AttemptID] {
			t.Fatalf("verdict %+v recorded from a delivery attempt", v)
		}
	}
	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rows {
		if ids[a.ID] && a.GateError != nil {
			t.Fatalf("delivery attempt %+v was gated: it was parsed", a)
		}
	}
	if turn := s.Turn(t, c, turnID); len(turn.Verdicts) != 2 {
		t.Fatalf("turn reports %d verdicts after delivery, want 2", len(turn.Verdicts))
	}
}

func TestRepair_DeliveryCarriesCommentary(t *testing.T) {
	t.Parallel()
	const before, after = "COMMENTARY-BEFORE-THE-ENVELOPE", "COMMENTARY-AFTER-THE-ENVELOPE"
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": repeat(4),
		"reviewer": {verdict(func(hash, attempt string) string {
			return before + "\n" + fake.VerdictApprove(hash, attempt) + "\n" + after
		}), normal(), normal()},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	s.repairDeliveries(2)

	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
	for _, id := range []string{"author", "reviewer"} {
		calls := s.calls(id, phaseDeliveryName)
		if len(calls) != 1 {
			t.Fatalf("%s received %d delivery requests, want 1", id, len(calls))
		}
		text := repairUserText(calls[0])
		bi, ai := strings.Index(text, before), strings.Index(text, after)
		if bi < 0 || ai < 0 || bi > ai {
			t.Fatalf("%s's delivery lacks the reviewer's commentary, before and after its envelope:\n%s", id, text)
		}
		if strings.Count(text, before) != 1 || strings.Count(text, after) != 1 {
			t.Fatalf("%s's delivery repeats the commentary:\n%s", id, text)
		}
	}
}

func TestRepair_DeliveryHangEndsAtDeadline(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK), {Kind: fake.StepKindHang}, normal()},
		"reviewer": repeat(4),
	}, enginetest.ClockStart(capacityFarFuture))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	hung := s.capacityBegun(t, c, "author", phaseDeliveryName)

	deadline, err := time.Parse(time.RFC3339Nano, hung.Deadline)
	if err != nil || !deadline.Equal(s.Clock.Now().Add(deliveryDeadline)) {
		t.Fatalf("delivery deadline %q (err %v), want now + %v", hung.Deadline, err, deliveryDeadline)
	}
	// One tick short of the deadline the delivery is still waiting.
	s.Clock.Advance(deliveryDeadline - time.Millisecond)
	if a, err := s.Store.GetAttempt(hung.AttemptID); err != nil || a.State != "pending" {
		t.Fatalf("delivery attempt %+v (err %v), want it pending before its deadline", a, err)
	}
	s.Clock.Advance(time.Millisecond)
	ev := s.WaitEvent(func(ev event) bool {
		cp, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && cp.AttemptID == hung.AttemptID
	})
	completed, _ := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
	if completed.Status != string(transport.StatusInterrupted) {
		t.Fatalf("delivery completed %+v, want interrupted", completed)
	}

	// Not retried: one request, one dispatch, and the turn is as it was.
	s.repairDeliveries(2)
	if n := len(s.calls("author", phaseDeliveryName)); n != 1 {
		t.Fatalf("author received %d delivery requests, want 1: the hang was retried", n)
	}
	if n := s.dispatchRows(t, hung.AttemptID); n != 1 {
		t.Fatalf("%d attempt_dispatches rows for the delivery, want 1", n)
	}
	if a, err := s.Store.GetAttempt(hung.AttemptID); err != nil || retries(*a) != 0 {
		t.Fatalf("delivery attempt %+v (err %v), want no retry", a, err)
	}
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

func TestRepair_PendingDeliveryUnknownNotResent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": {verdict(fake.VerdictApprove), {Kind: fake.StepKindHang}},
	}, enginetest.ClockStart(capacityFarFuture))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	pending := s.capacityBegun(t, c, "reviewer", phaseDeliveryName).AttemptID
	if a, err := s.Store.GetAttempt(pending); err != nil || a.State != "pending" {
		t.Fatalf("delivery attempt %+v (err %v), want it pending when the engine dies", a, err)
	}

	s.Restart(enginetest.RosterTOML, capacityScripts())
	s.WaitEvent(func(ev event) bool {
		u, ok := body[wire.EvAttemptUnknown](ev, wire.EventKindAttemptUnknown)
		return ok && u.AttemptID == pending
	})

	a, err := s.Store.GetAttempt(pending)
	if err != nil || a.State != "unknown" {
		t.Fatalf("delivery attempt %+v (err %v), want it unknown", a, err)
	}
	c2 := s.Dial(t)
	wantTurn(t, s.Turn(t, c2, turnID), engine.StateApproved, "all_approved")
	for id, adapter := range s.Adapters {
		if n := len(adapter.Calls()); n != 0 {
			t.Fatalf("%s received %d requests after the restart, want the delivery not re-sent", id, n)
		}
	}
	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	for _, a := range rows {
		if a.Phase == phaseDeliveryName {
			deliveries++
		}
	}
	if deliveries != 2 {
		t.Fatalf("turn has %d delivery attempts after the restart, want the 2 it had", deliveries)
	}
}

func TestRepair_CompletionEventsDistinguishOutcomes(t *testing.T) {
	failed := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: "http_500"}
	cases := []struct {
		name     string
		reviewer []fake.Step
		// want is the author's and the reviewer's delivery status.
		wantAuthor, wantReviewer string
	}{
		{"two delivered", []fake.Step{verdict(fake.VerdictApprove), normal()}, string(transport.StatusComplete), string(transport.StatusComplete)},
		{"one delivered and one failed", []fake.Step{verdict(fake.VerdictApprove), failed}, string(transport.StatusComplete), string(transport.StatusError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{"author": repeat(4), "reviewer": tc.reviewer})
			c := s.Dial(t)
			turnID := s.Submit(t, c, "s1")
			s.WaitTerminal(turnID)
			deliveries := s.repairDeliveries(2)

			who := map[string]string{}
			for _, d := range deliveries {
				who[d.AttemptID] = d.Participant
			}
			got := map[string]wire.EvAttemptCompleted{}
			for _, ev := range s.Events() {
				if cp, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok && who[cp.AttemptID] != "" {
					got[who[cp.AttemptID]] = cp
				}
			}
			if len(got) != 2 {
				t.Fatalf("attempt_completed for deliveries %+v, want one per participant", got)
			}
			if got["author"].Status != tc.wantAuthor || got["reviewer"].Status != tc.wantReviewer {
				t.Fatalf("delivery statuses author=%q reviewer=%q, want %q and %q", got["author"].Status, got["reviewer"].Status, tc.wantAuthor, tc.wantReviewer)
			}
			if tc.wantReviewer == string(transport.StatusError) {
				if got["reviewer"].TransportStatus != "http_500" || got["author"].TransportStatus != transport.TransportStatusOK {
					t.Fatalf("transport statuses %+v, want ok for the delivered and http_500 for the failed", got)
				}
			} else if got["reviewer"].TransportStatus != transport.TransportStatusOK || got["author"].TransportStatus != transport.TransportStatusOK {
				t.Fatalf("transport statuses %+v, want ok for both", got)
			}
			wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
		})
	}
}

// repairDerived reports whether the delimiter is derived from the attempt id:
// it equals, contains or is contained in the id or a simple transformation of
// it, or shares a long substring with it.
func repairDerived(delimiter, id string) bool {
	sum := sha256.Sum256([]byte(id))
	reversed := []byte(id)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	forms := []string{
		id,
		strings.ToUpper(id),
		string(reversed),
		hex.EncodeToString([]byte(id)),
		hex.EncodeToString(sum[:]),
	}
	for _, f := range forms {
		if strings.Contains(delimiter, f) || strings.Contains(f, delimiter) {
			return true
		}
	}
	const window = 12
	for i := 0; i+window <= len(id); i++ {
		if strings.Contains(delimiter, id[i:i+window]) {
			return true
		}
	}
	return false
}

// delimiterScripts scripts the participants to answer a request for every turn
// and a delivery after it.
func delimiterScripts(ids []string) map[string][]fake.Step {
	out := map[string][]fake.Step{}
	for _, id := range ids {
		out[id] = repeat(16)
	}
	return out
}

func TestRepair_DelimitersDifferAndAreNotDerived(t *testing.T) {
	t.Parallel()
	const reviewers, turns = 5, 5
	s := newSuite(t, nRolesTOML(reviewers, 0, false), delimiterScripts(nRolesIDs(reviewers)))
	c := s.Dial(t)
	var turnIDs []string
	for i := 0; i < turns; i++ {
		turnID := s.Submit(t, c, fmt.Sprintf("s%d", i))
		s.WaitTerminal(turnID)
		wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
		turnIDs = append(turnIDs, turnID)
	}

	type attempt struct{ id, participant, delimiter string }
	var attempts []attempt
	for _, turnID := range turnIDs {
		rows, err := s.Store.ListAttempts(turnID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rows {
			if a.Phase != "review" {
				continue
			}
			if a.Delimiter == nil || *a.Delimiter == "" {
				t.Fatalf("review attempt %+v has no delimiter", a)
			}
			attempts = append(attempts, attempt{a.ID, a.Participant, *a.Delimiter})
		}
	}
	if len(attempts) != reviewers*turns || len(attempts) < 20 {
		t.Fatalf("collected %d review attempts, want %d (at least 20)", len(attempts), reviewers*turns)
	}

	seen := map[string]string{}
	for _, a := range attempts {
		if other, dup := seen[a.delimiter]; dup {
			t.Fatalf("attempts %s and %s share the delimiter %q", other, a.id, a.delimiter)
		}
		seen[a.delimiter] = a.id
		for _, other := range attempts {
			if repairDerived(a.delimiter, other.id) {
				t.Fatalf("delimiter %q of attempt %s is derivable from attempt id %s", a.delimiter, a.id, other.id)
			}
		}
		// The delimiter is the one the reviewer was actually given.
		sent := false
		for _, req := range s.calls(a.participant, "review") {
			if req.Envelope.AttemptID != a.id {
				continue
			}
			sent = true
			var all strings.Builder
			for _, m := range req.Messages {
				all.WriteString(m.Content)
			}
			if !strings.Contains(all.String(), a.delimiter) {
				t.Fatalf("request of attempt %s does not carry its delimiter %q", a.id, a.delimiter)
			}
		}
		if !sent {
			t.Fatalf("no request was sent for attempt %s", a.id)
		}
	}
}
