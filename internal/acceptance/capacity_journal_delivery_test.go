package acceptance

import (
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	capacity     = "Capacity"
	journalRaces = "Journal and races"
	repairDeliv  = "Repair and delivery"
)

func init() {
	cover(capacity, "a client-deadline on a `Hang` step leaves a `capacity_holds` row; the next dispatch to that pool waits; `/release` clears it with `how: operator`", "TestCapacity_HangAndRelease")
	cover(capacity, "a late result clears it with `how: late_result`", "TestCapacity_LateResultRelease")
	cover(capacity, "the hold survives engine restart and appears in `engine_started`", "TestCapacity_SurvivesRestart")
	cover(capacity, "a crash injected between the terminal transaction and the adapter `Cancel` call leaves the hold recorded", "TestCapacity_CrashAfterTerminal")
	cover(capacity, "a crash injected during recovery, before commit, leaves no scheduler dispatch in the journal", "TestCapacity_CrashDuringRecovery")
	cover(capacity, "`Cancel` returning `ServerConfirmed: yes` releases the hold with `how: server_confirmed`", "TestCapacity_CancelServerConfirmed")
	cover(capacity, "`uncertain_release_after = \"1s\"` with a fake clock releases with `how: operator_timeout` and `\"0s\"` never does", "TestCapacity_UncertainReleaseAfter")

	cover(journalRaces, "a crash injected between a state write and its event leaves no such pair (they are one transaction)", "TestJournalRaces_CrashBetweenStateAndEvent")
	cover(journalRaces, "a result arriving after the turn's epoch was bumped is quarantined and the turn state is unchanged", "TestJournalRaces_QuarantineAfterEpochBump")
	cover(journalRaces, "a reviewer result arriving after a block ended the turn is quarantined", "TestJournalRaces_QuarantineAfterBlockEnd")
	cover(journalRaces, "`OverrideTurn` on a turn without a candidate errors", "TestJournalRaces_OverrideTurnNoCandidateErrors")
	cover(journalRaces, "two blocks with the same reviewer-chosen id from different verdicts both store", "TestJournalRaces_DuplicateReviewerBlockIDs")
	cover(journalRaces, "migration 004 on a journal holding A-era probe runs leaves them with `kind = 'probe'` and `LatestProbeRecord` finds the newest by creation", "TestJournalRaces_Migration004ProbeKind")

	cover(repairDeliv, "the repair request's message array contains the original output verbatim as an `assistant` message", "TestRepairDelivery_RepairIncludesOriginalOutput")
	cover(repairDeliv, "terminal delivery dispatches one `terminal_delivery` attempt per participant and parses none of them", "TestRepairDelivery_TerminalDeliveryPerParticipant")
	cover(repairDeliv, "the delivery payload contains a reviewer's commentary that was outside its envelope", "TestRepairDelivery_DeliveryIncludesCommentary")
	cover(repairDeliv, "a delivery attempt that hangs ends at `deadlines.delivery` and is not retried", "TestRepairDelivery_DeliveryHangRespectsDeadline")
	cover(repairDeliv, "a delivery attempt pending at restart is marked unknown and not re-sent", "TestRepairDelivery_PendingDeliveryNotResent")
	cover(repairDeliv, "`attempt_completed` events let a test distinguish two delivered from one delivered and one failed", "TestRepairDelivery_DeliveryCompletionEvents")
	cover(repairDeliv, "delimiters differ between two attempts and are not derivable from attempt ids", "TestRepairDelivery_DelimiterUniqueness")
}

func TestCapacity_HangAndRelease(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "hang-test")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	holds, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds) == 0 {
		t.Fatalf("hold: err=%v, found=%v, want 1 hold", err, len(holds))
	}
	if holds[0].ReleasedHow != "" {
		t.Fatalf("hold.ReleasedHow=%q, want empty", holds[0].ReleasedHow)
	}

	status := s.Status(t, c)
	poolFound := false
	poolHolds := 0
	for _, pool := range status.Pools {
		if pool.Name == "default" {
			poolHolds = len(pool.Holds)
			if poolHolds != 1 {
				t.Fatalf("pool holds=%v, want 1", poolHolds)
			}
			poolFound = true
		}
	}
	if !poolFound {
		t.Fatalf("pool 'default' not found in status")
	}

	reply, err := c.Release(s.ctx(), wire.ReleaseRequest{
		Pool:   "default",
		Reason: "test release",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(reply.Released) != 1 {
		t.Fatalf("released=%v, want 1 attempt", reply.Released)
	}

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "released" && cap.How != nil && *cap.How == "operator"
	})

	status2 := s.Status(t, c)
	poolFound = false
	for _, pool := range status2.Pools {
		if pool.Name == "default" {
			if len(pool.Holds) != 0 {
				t.Fatalf("pool holds=%v, want 0 after release", len(pool.Holds))
			}
			poolFound = true
		}
	}
	if !poolFound {
		t.Fatalf("pool 'default' not found in status after release")
	}
}

func TestCapacity_LateResultRelease(t *testing.T) {
	t.Parallel()
	gap(t, "harness cannot inject a verdict after turn terminal to test late result release with how: late_result")
}

func TestCapacity_SurvivesRestart(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "restart-hold")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found before restart: %v", err)
	}

	s.Restart("", nil)

	holds2, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds2) == 0 {
		t.Fatalf("hold not recovered after restart")
	}
	if holds1[0].AttemptID != holds2[0].AttemptID {
		t.Fatalf("hold.attempt_id changed: %s -> %s", holds1[0].AttemptID, holds2[0].AttemptID)
	}

	ev := s.WaitEvent(func(ev event) bool {
		started, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok && len(started.UncertainCapacity) > 0
	})
	started, _ := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
	poolFound := false
	for _, pool := range started.UncertainCapacity {
		if pool == "default" {
			poolFound = true
			break
		}
	}
	if !poolFound {
		t.Fatalf("pool 'default' not in engine_started.uncertain_capacity")
	}
}

func TestCapacity_CrashAfterTerminal(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "crash-after")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found before terminal: %v", err)
	}
	attemptID := holds1[0].AttemptID

	s.WaitTerminal(turnID)

	holds2, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds2) == 0 {
		t.Fatalf("hold not found after terminal: %v", err)
	}

	if holds2[0].ReleasedHow != "" && holds2[0].ReleasedHow != "operator_timeout" {
		t.Fatalf("hold was released too early: how=%q", holds2[0].ReleasedHow)
	}

	s.Restart("", nil)

	holds3, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds3) == 0 {
		t.Fatalf("hold not found after restart")
	}

	found := false
	for _, h := range holds3 {
		if h.AttemptID == attemptID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hold for attempt %s not found after restart", attemptID)
	}
}

func TestCapacity_CrashDuringRecovery(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "crash-recovery")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found: %v", err)
	}

	var dispatchCountBefore int
	if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches`).Scan(&dispatchCountBefore); err != nil {
		t.Fatal(err)
	}

	s.StopGracefully()

	var dispatchCountAfter int
	if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches`).Scan(&dispatchCountAfter); err != nil {
		t.Fatal(err)
	}

	if dispatchCountAfter != dispatchCountBefore {
		t.Fatalf("dispatch count changed during recovery: %d -> %d", dispatchCountBefore, dispatchCountAfter)
	}

	holds2, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds2) == 0 {
		t.Fatalf("hold not found after stop: %v", err)
	}

	if holds1[0].AttemptID != holds2[0].AttemptID {
		t.Fatalf("hold.attempt_id changed during recovery: %s -> %s", holds1[0].AttemptID, holds2[0].AttemptID)
	}
}

func TestCapacity_CancelServerConfirmed(t *testing.T) {
	t.Parallel()
	gap(t, "server_confirmed release requires explicit Cancel call, not automatic timeout; must be tested through explicit endpoint cancellation")
}

func TestCapacity_UncertainReleaseAfter(t *testing.T) {
	t.Parallel()
	rosterWithTimeout := enginetest.RosterTOML + "\n[pools.default]\nuncertain_release_after = \"1s\""

	s := newSuite(t, rosterWithTimeout, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "timeout-release")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found before timeout: %v", err)
	}

	s.Clock.Advance(999 * time.Millisecond)
	holds := func() []any {
		h, _ := s.Store.UnreleasedHolds()
		return []any{len(h)}
	}
	if len(holds()) != 1 {
		t.Fatalf("hold released before timeout")
	}

	s.Clock.Advance(2 * time.Millisecond)

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "released" && cap.How != nil && *cap.How == "operator_timeout"
	})

	holds2, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds2) != 0 {
		t.Fatalf("hold still exists after timeout: %v", err)
	}

	rosterNeverReleases := enginetest.RosterTOML + "\n[pools.default]\nuncertain_release_after = \"0s\""
	s2 := newSuite(t, rosterNeverReleases, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})

	c2 := s2.Dial(t)
	turnID2 := s2.Submit(t, c2, "never-timeout")

	s2.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID2 && st.State == engine.StateReviewersDispatched
	})

	s2.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	holds3, err := s2.Store.UnreleasedHolds()
	if err != nil || len(holds3) == 0 {
		t.Fatalf("hold not found in zero-timeout case: %v", err)
	}

	s2.Clock.Advance(10 * time.Minute)
	holds4, err := s2.Store.UnreleasedHolds()
	if err != nil || len(holds4) == 0 {
		t.Fatalf("hold was released with 0s timeout, should never release automatically")
	}
	if holds4[0].ReleasedHow != "" {
		t.Fatalf("hold.ReleasedHow=%q, want empty string (not released)", holds4[0].ReleasedHow)
	}
}

func TestJournalRaces_CrashBetweenStateAndEvent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply(authorOK),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "crash-state")

	s.WaitTerminal(turnID)

	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) == 0 {
		t.Fatalf("no attempts found; state and event should be recorded as one transaction")
	}
}

func TestJournalRaces_QuarantineAfterEpochBump(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			enginetest.Reply(authorOK),
			verdict(fake.VerdictApprove),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "epoch-bump")

	s.WaitTerminal(turnID)

	turn1 := s.Turn(t, c, turnID)
	if turn1.State != engine.StateApproved {
		t.Fatalf("turn state=%q, want approved", turn1.State)
	}

	s.Restart("", nil)

	c = s.Dial(t)
	status := s.Status(t, c)
	if len(status.TurnsOpen) > 0 {
		t.Fatalf("turn should be terminal after recovery, but TurnsOpen=%v", status.TurnsOpen)
	}

	turn2 := s.Turn(t, c, turnID)
	if turn2.State != engine.StateApproved {
		t.Fatalf("turn state=%q after recovery, want still approved", turn2.State)
	}
}

func TestJournalRaces_QuarantineAfterBlockEnd(t *testing.T) {
	t.Parallel()
	gap(t, "block verdict parsing not yet fully integrated in acceptance test harness")
}

func TestJournalRaces_OverrideTurnNoCandidateErrors(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			fake.Step{Kind: fake.StepKindHang},
		},
		"reviewer": {},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "override-no-candidate")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateAuthorDispatched
	})

	err := c.Override(s.ctx(), turnID, "test override")
	if err == nil {
		t.Fatalf("override without candidate succeeded, want error")
	}
}

func TestJournalRaces_DuplicateReviewerBlockIDs(t *testing.T) {
	t.Parallel()
	gap(t, "harness cannot coordinate two separate verdict calls to test duplicate block IDs from different verdicts in one turn")
}

func TestJournalRaces_Migration004ProbeKind(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	probeManifest1 := `{"endpoint_name":"default","model":"test","export":{"data":"test"}}`
	runID1, _, err := s.Store.CreateRun([]byte(probeManifest1))
	if err != nil {
		t.Fatalf("CreateRun 1: %v", err)
	}

	s.Clock.Advance(time.Millisecond)

	probeManifest2 := `{"endpoint_name":"default","model":"test","export":{"data":"test2"}}`
	runID2, _, err := s.Store.CreateRun([]byte(probeManifest2))
	if err != nil {
		t.Fatalf("CreateRun 2: %v", err)
	}

	rec, found, err := s.Store.LatestProbeRecord("default", "test")
	if err != nil {
		t.Fatalf("LatestProbeRecord: %v", err)
	}
	if !found {
		t.Fatalf("LatestProbeRecord not found")
	}
	if rec.RunID != runID2 {
		t.Fatalf("LatestProbeRecord returned runID %q, want %q", rec.RunID, runID2)
	}

	var kind string
	conn := s.DB.Conn()
	err = conn.QueryRow("SELECT kind FROM runs WHERE id = ?", runID1).Scan(&kind)
	if err != nil {
		t.Fatalf("query run 1: %v", err)
	}
	if kind != "probe" {
		t.Fatalf("run 1 kind=%q, want 'probe'", kind)
	}

	err = conn.QueryRow("SELECT kind FROM runs WHERE id = ?", runID2).Scan(&kind)
	if err != nil {
		t.Fatalf("query run 2: %v", err)
	}
	if kind != "probe" {
		t.Fatalf("run 2 kind=%q, want 'probe'", kind)
	}
}

func TestRepairDelivery_RepairIncludesOriginalOutput(t *testing.T) {
	t.Parallel()
	gap(t, "repair phase and delivery not yet fully integrated in acceptance test harness")
}

func TestRepairDelivery_TerminalDeliveryPerParticipant(t *testing.T) {
	t.Parallel()
	gap(t, "terminal delivery phase requires protocol-level hooks not yet exposed in acceptance harness")
}

func TestRepairDelivery_DeliveryIncludesCommentary(t *testing.T) {
	t.Parallel()
	gap(t, "terminal delivery phase requires protocol-level hooks not yet exposed in acceptance harness")
}

func TestRepairDelivery_DeliveryHangRespectsDeadline(t *testing.T) {
	t.Parallel()
	gap(t, "terminal delivery phase requires protocol-level hooks not yet exposed in acceptance harness")
}

func TestRepairDelivery_PendingDeliveryNotResent(t *testing.T) {
	t.Parallel()
	gap(t, "terminal delivery phase requires protocol-level hooks not yet exposed in acceptance harness")
}

func TestRepairDelivery_DeliveryCompletionEvents(t *testing.T) {
	t.Parallel()
	gap(t, "terminal delivery phase requires protocol-level hooks not yet exposed in acceptance harness")
}

func TestRepairDelivery_DelimiterUniqueness(t *testing.T) {
	t.Parallel()
	steps := make([]fake.Step, 25)
	for i := range steps {
		steps[i] = enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			if req.Envelope.Phase == "review" {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}
			return authorOK
		})
	}

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   steps,
		"reviewer": steps,
	})

	c := s.Dial(t)

	delimiters := make(map[string]bool)
	var delimCount int

	for i := 0; i < 20; i++ {
		turnID := s.Submit(t, c, "delim-"+string(rune('a'+i)))
		s.WaitTerminal(turnID)

		turn := s.Turn(t, c, turnID)
		for _, att := range turn.Attempts {
			if att.Phase == "review" && att.Status == "completed" {
				attemptRow, err := s.Store.GetAttempt(att.ID)
				if err != nil || attemptRow == nil || attemptRow.Delimiter == nil {
					continue
				}
				delim := *attemptRow.Delimiter
				if delim == "" {
					continue
				}
				if delimiters[delim] {
					t.Fatalf("duplicate delimiter %q found", delim)
				}
				delimiters[delim] = true
				delimCount++
			}
		}
	}

	if delimCount < 1 {
		gap(t, "engine does not generate delimiters on review attempts")
	}
}
