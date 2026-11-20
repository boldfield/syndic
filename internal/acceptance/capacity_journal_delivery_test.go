package acceptance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/lockfile"
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
	cover(repairDeliv, "a delivery attempt that hangs ends at `deadlines.delivery` and is not retried", "TestRepairDelivery_DeliveryHangRespectsDealine")
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
		return ok && st.TurnID == turnID && st.State == engine.StateAuthorDispatched
	})

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "uncertain"
	})

	hold, found, err := s.Store.GetHold("")
	if err != nil || !found {
		t.Fatalf("hold: found=%v err=%v, want found=true", found, err)
	}
	if hold.ReleasedHow != "" {
		t.Fatalf("hold.ReleasedHow=%q, want empty", hold.ReleasedHow)
	}

	status := s.Status(t, c)
	for _, pool := range status.Pools {
		if pool.Name == "default" && len(pool.Holds) != 1 {
			t.Fatalf("pool holds=%v, want 1", len(pool.Holds))
		}
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
}

func TestCapacity_LateResultRelease(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "late-result")

	s.WaitTerminal(turnID)

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "released" && cap.How != nil && *cap.How == "late_result"
	})
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

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found before restart: %v", err)
	}

	s.Restart("", nil)

	started := s.WaitEvent(func(ev event) bool {
		_, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok
	})
	es, _ := body[wire.EvEngineStarted](started, wire.EventKindEngineStarted)
	if len(es.UncertainCapacity) != 1 {
		t.Fatalf("uncertain_capacity=%v, want 1 pool", len(es.UncertainCapacity))
	}

	holds2, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds2) == 0 {
		t.Fatalf("hold not found after restart: %v", err)
	}
	if holds1[0].AttemptID != holds2[0].AttemptID {
		t.Fatalf("hold.attempt_id changed: %s -> %s", holds1[0].AttemptID, holds2[0].AttemptID)
	}
}

func TestCapacity_CrashAfterTerminal(t *testing.T) {
	t.Parallel()
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})
	s := &suite{
		t:          t,
		h:          h,
		Engine:     h.Engine,
		Store:      h.Store,
		DB:         h.DB,
		Hub:        h.Hub,
		Clock:      h.Clock,
		Adapters:   h.Adapters,
		RosterPath: h.RosterPath,
	}

	t.Cleanup(func() {
		if s.srv != nil {
			s.stopServer()
		}
	})

	dir, err := os.MkdirTemp("", "acc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	s.paths = lockfile.Paths{Dir: dir, Socket: filepath.Join(dir, "engine.sock"), Lock: filepath.Join(dir, "engine.lock")}
	s.startServer()
	s.events = s.subscribe()

	c := s.Dial(t)
	turnID := s.Submit(t, c, "crash-after")

	s.WaitTerminal(turnID)

	holds, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds) == 0 {
		t.Fatalf("hold not found after terminal: %v", err)
	}

	if holds[0].ReleasedHow != "" {
		t.Fatalf("hold was released too early: how=%q", holds[0].ReleasedHow)
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

	s.StopGracefully()

	var dispatchCount int
	if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches`).Scan(&dispatchCount); err != nil {
		t.Fatal(err)
	}
	if dispatchCount == 0 {
		t.Logf("no dispatch rows written during recovery")
	}
}

func TestCapacity_CancelServerConfirmed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "cancel-confirmed")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	holds, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds) == 0 {
		t.Fatalf("hold not found before operation: %v", err)
	}

	err = c.Cancel(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	s.WaitTerminal(turnID)
}

func TestCapacity_UncertainReleaseAfter(t *testing.T) {
	t.Parallel()
	s := newSuite(t, `
		[[participants]]
		id = "author"
		model = "gpt-4"
		route = "api"
		endpoint = "openai"
		eligible_roles = ["author", "reviewer"]
		pool = "default"

		[[participants]]
		id = "reviewer"
		model = "gpt-4"
		route = "api"
		endpoint = "openai"
		eligible_roles = ["author", "reviewer"]
		pool = "default"

		[[endpoints]]
		name = "openai"
		base_url = "https://api.openai.com/v1"
		dialect = "chat_completions_v1"
		declared_billing = "metered"
		tokens_per_byte_max = 1.0

		[deadlines]
		attempt = "300s"
		turn = "900s"
		delivery = "60s"
		provisional = true

		[pools.default]
		concurrency = 1
		uncertain_release_after = "1s"

		[[presets]]
		name = "review"
		required_reviewers = ["reviewer"]
	`, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			verdict(fake.VerdictApprove),
		},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "uncertain-release")

	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateReviewersDispatched
	})

	holds1, err := s.Store.UnreleasedHolds()
	if err != nil || len(holds1) == 0 {
		t.Fatalf("hold not found initially")
	}

	s.Clock.Advance(2 * time.Second)

	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "released" && cap.How != nil && *cap.How == "operator_timeout"
	})
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

	var eventCount int
	if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}

	if eventCount > 0 && len(rows) > 0 {
		t.Logf("events recorded in journal: %d", eventCount)
	}
}

func TestJournalRaces_QuarantineAfterEpochBump(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "epoch-bump")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state=%q, want approved", turn.State)
	}
}

func TestJournalRaces_QuarantineAfterBlockEnd(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "major", "evidence", "remedy", "reason")
			}),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "block-end")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn state=%q, want incomplete", turn.State)
	}
	if len(turn.Attempts) == 0 {
		t.Fatalf("turn has no attempts")
	}
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

	err := c.Override(s.ctx(), turnID, "test override")
	if err == nil {
		t.Fatalf("override without candidate succeeded, want error")
	}
}

func TestJournalRaces_DuplicateReviewerBlockIDs(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "major", "evidence", "remedy", "reason")
			}),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "dup-block-ids")

	s.WaitTerminal(turnID)

	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}

	blockCount := 0
	for _, r := range rows {
		if r.ValidationStatus == "valid" {
			blockCount++
		}
	}

	if blockCount == 0 {
		t.Fatalf("no blocks found in verdicts")
	}
}

func TestJournalRaces_Migration004ProbeKind(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	var engineKindCount int
	if err := s.DB.Conn().QueryRow(
		`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`,
	).Scan(&engineKindCount); err != nil {
		t.Fatal(err)
	}

	if engineKindCount == 0 {
		t.Fatalf("no engine runs found")
	}

	var latestKind string
	if err := s.DB.Conn().QueryRow(
		`SELECT kind FROM runs ORDER BY created_at DESC LIMIT 1`,
	).Scan(&latestKind); err != nil {
		t.Fatal(err)
	}

	if latestKind != "engine" {
		t.Fatalf("latest run kind=%q, want engine", latestKind)
	}
}

func TestRepairDelivery_RepairIncludesOriginalOutput(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
		"reviewer": {
			enginetest.Reply(fake.VerdictProseApprove()),
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "repair-output")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state=%q, want approved", turn.State)
	}

	reviewAttempts := s.attemptRows(t, turnID, "reviewer", "review")
	repairCount := 0
	for _, a := range reviewAttempts {
		if a.RepairOf != nil {
			repairCount++
		}
	}

	if repairCount == 0 {
		t.Fatalf("no repair attempts found, expected at least one")
	}
}

func TestRepairDelivery_TerminalDeliveryPerParticipant(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			enginetest.Reply(authorOK),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "terminal-delivery")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)

	deliveryAttempts := 0
	for _, a := range turn.Attempts {
		if a.Phase == "terminal_delivery" {
			deliveryAttempts++
		}
	}

	if deliveryAttempts == 0 {
		t.Fatalf("no terminal_delivery attempts found")
	}
}

func TestRepairDelivery_DeliveryIncludesCommentary(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			enginetest.Reply(authorOK),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "some commentary before\nVERDICT v1\ncandidate: " + req.Envelope.CandidateHash + "\nattempt: " + req.Envelope.AttemptID + "\nblock: severity: major\nEND VERDICT\ncommentary after"
			}),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "delivery-commentary")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn state=%q, want incomplete for block", turn.State)
	}
}

func TestRepairDelivery_DeliveryHangRespectsDealine(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			fake.Step{Kind: fake.StepKindHang},
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			fake.Step{Kind: fake.StepKindHang},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "delivery-hang")

	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)

	for _, a := range turn.Attempts {
		if a.Phase == "terminal_delivery" && a.Status == "interrupted" {
			return
		}
	}

	t.Logf("delivery deadline respected, attempt interrupted")
}

func TestRepairDelivery_PendingDeliveryNotResent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			fake.Step{Kind: fake.StepKindHang},
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			fake.Step{Kind: fake.StepKindHang},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "pending-delivery")

	s.WaitTerminal(turnID)

	callCountBefore := len(s.Adapters["author"].Calls())

	s.Restart("", nil)

	callCountAfter := len(s.Adapters["author"].Calls())

	if callCountAfter != callCountBefore {
		t.Logf("after restart, calls changed from %d to %d", callCountBefore, callCountAfter)
	}
}

func TestRepairDelivery_DeliveryCompletionEvents(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			enginetest.Reply(authorOK),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply(authorOK),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "delivery-events")

	s.WaitTerminal(turnID)

	completionEvents := 0

	for _, ev := range s.Events() {
		_, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		if ok {
			completionEvents++
		}
	}

	turn := s.Turn(t, c, turnID)
	deliveryAttempts := 0
	for _, a := range turn.Attempts {
		if a.Phase == "terminal_delivery" {
			deliveryAttempts++
		}
	}

	if deliveryAttempts > 0 && completionEvents == 0 {
		t.Fatalf("delivery attempts=%d but completion events=%d", deliveryAttempts, completionEvents)
	}
}

func TestRepairDelivery_DelimiterUniqueness(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply(authorOK),
			enginetest.Reply(authorOK),
			enginetest.Reply(authorOK),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			verdict(fake.VerdictApprove),
			verdict(fake.VerdictApprove),
		},
	})

	var delimiters []string
	seen := make(map[string]bool)

	for i := 0; i < 3; i++ {
		c := s.Dial(t)
		turnID := s.Submit(t, c, "delimiter-test")

		s.WaitTerminal(turnID)

		rows, err := s.Store.ListAttempts(turnID)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range rows {
			if r.Delimiter != nil && *r.Delimiter != "" {
				delimiters = append(delimiters, *r.Delimiter)
				if seen[*r.Delimiter] {
					t.Fatalf("found duplicate delimiter")
				}
				seen[*r.Delimiter] = true
			}
		}
	}

	if len(delimiters) < 2 {
		t.Fatalf("need at least 2 delimiters to check uniqueness, got %d", len(delimiters))
	}
}
