package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// rpart is a participant of a restart test roster. The author is always
// "author" on endpoint local; the others review, and all but those marked
// spare are required by the review preset.
type rpart struct {
	id, endpoint, model, pool string
	spare                     bool
}

func author() rpart { return rpart{id: "author", endpoint: "local", model: "llama2", pool: "default"} }

// restartRoster builds a roster with endpoints local and local2, both serving
// llama2 and llama3, one pool section per non-default pool, and the review
// and solo presets. pools maps a pool to its extra settings lines.
func restartRoster(parts []rpart, pools map[string]string) string {
	var b strings.Builder
	b.WriteString("default_preset = \"review\"\n\n[deadlines]\nattempt = \"300s\"\nturn = \"900s\"\ndelivery = \"60s\"\nprovisional = false\n\n")
	var required []string
	for _, p := range parts {
		role := "reviewer"
		if p.id == "author" {
			role = "author"
		} else if !p.spare {
			required = append(required, fmt.Sprintf("%q", p.id))
		}
		fmt.Fprintf(&b, "[[participants]]\nid = %q\nmodel = %q\nroute = \"api\"\nendpoint = %q\neligible_roles = [%q]\npool = %q\n\n", p.id, p.model, p.endpoint, role, p.pool)
	}
	for i, name := range []string{"local", "local2"} {
		fmt.Fprintf(&b, "[[endpoints]]\nname = %q\nbase_url = \"http://localhost:%d/v1\"\ndeclared_location = \"local\"\ndeclared_execution = \"local\"\ndeclared_billing = \"none\"\nnetwork_dependency = \"none\"\ndialect = \"chat_completions_v1\"\nmodels = [\"llama2\", \"llama3\"]\ntokens_per_byte_max = 1.0\n\n", name, 11434+i)
	}
	fmt.Fprintf(&b, "[presets.review]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [%s]\n\n", strings.Join(required, ", "))
	b.WriteString("[presets.solo]\nmode = \"solo\"\nauthor = \"author\"\nrequired_reviewers = []\n\n[policy]\nlocal_only = true\nallowed_destinations = []\n")
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "\n[pools.%s]\n%s\n", name, pools[name])
	}
	return b.String()
}

// oneReviewer is a roster with the author and r1 in pool p, of width 1.
func oneReviewer(extra string) string {
	return restartRoster([]rpart{author(), {id: "r1", endpoint: "local", model: "llama3", pool: "p"}},
		map[string]string{"p": "concurrency = 1\n" + extra})
}

// second is an engine started on the journal of a harness, with adapters of
// its own, standing for the engine after a restart.
type second struct {
	*engine.Engine
	Adapters map[string]*fake.Adapter
}

// newSecond builds, without starting, an engine on h's journal, hub and clock
// with rosterTOML and fresh scripted adapters. The engine h.Engine is left
// running, which is what a killed engine looks like to the journal.
func newSecond(t *testing.T, h *enginetest.Harness, rosterTOML string, scripts map[string][]fake.Step) *second {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roster.toml")
	if err := os.WriteFile(path, []byte(rosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &second{Adapters: map[string]*fake.Adapter{}}
	eng, err := engine.New(engine.Options{
		Store:         h.Store,
		Hub:           h.Hub,
		RosterPath:    path,
		EngineVersion: enginetest.EngineVersion,
		Clock:         h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			a := fake.New(scripts[p.ID], fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel})
			s.Adapters[p.ID] = a
			return a, enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Engine = eng
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return s
}

// restart starts a second engine and requires it to start.
func restart(t *testing.T, h *enginetest.Harness, rosterTOML string, scripts map[string][]fake.Step) *second {
	t.Helper()
	s := newSecond(t, h, rosterTOML, scripts)
	if err := s.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	return s
}

func (s *second) noCalls(t *testing.T) {
	t.Helper()
	for id, a := range s.Adapters {
		if n := len(a.Calls()); n != 0 {
			t.Fatalf("%s received %d calls after the restart", id, n)
		}
	}
}

// lastStarted returns the newest engine_started payload.
func lastStarted(t *testing.T, h *enginetest.Harness) *wire.EvEngineStarted {
	t.Helper()
	var out *wire.EvEngineStarted
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvEngineStarted); ok {
			out = p
		}
	}
	if out == nil {
		t.Fatal("no engine_started")
	}
	return out
}

func (s *second) poolHolds(t *testing.T, pool string) []wire.PoolHold {
	t.Helper()
	st, err := s.Status()
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

func (s *second) waitQueued(t *testing.T, id string) {
	t.Helper()
	waitFor(t, id+" queued", func() bool {
		st, err := s.Status()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range st.Participants {
			if p.ID == id {
				return p.State == engine.ParticipantQueued
			}
		}
		return false
	})
}

func (s *second) submit(t *testing.T, id string) string {
	t.Helper()
	reply, err := s.Submit(wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?"})
	if err != nil {
		t.Fatal(err)
	}
	return reply.TurnID
}

// killedMidReview runs a review turn whose reviewer r1 never answers, and
// returns the harness, the turn and r1's pending attempt.
func killedMidReview(t *testing.T, rosterTOML string) (*enginetest.Harness, string, string) {
	t.Helper()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}, "r1": {{Kind: fake.StepKindHang}}}
	h := enginetest.New(t, rosterTOML, scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	return h, turnID, attemptsOf(h, turnID, "r1")[0]
}

func TestRestartKillMidReview(t *testing.T) {
	h, turnID, attemptID := killedMidReview(t, oneReviewer(""))
	s := restart(t, h, oneReviewer(""), approvers("r1"))

	wantEnd(t, h, turnID, engine.StateIncomplete, "engine_restart")
	if got := getAttempt(t, h, attemptID).State; got != "unknown" {
		t.Fatalf("attempt state = %q", got)
	}
	hold, found := getHold(t, h, attemptID)
	if !found || hold.ReleasedAt != nil || hold.Pool != "p" || hold.EndpointName != "local" || hold.Model != "llama3" {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
	s.noCalls(t)
	wantNoDelivery(t, h, turnID)

	var unknown, incomplete bool
	for _, ev := range h.Events() {
		switch p := ev.Payload.(type) {
		case *wire.EvAttemptUnknown:
			unknown = unknown || p.AttemptID == attemptID
		case *wire.EvTurnState:
			incomplete = incomplete || (p.TurnID == turnID && p.State == engine.StateIncomplete && p.Reason != nil && *p.Reason == "engine_restart")
		}
	}
	if !unknown || !incomplete {
		t.Fatalf("attempt_unknown %v, turn_state incomplete/engine_restart %v", unknown, incomplete)
	}
	started := lastStarted(t, h)
	if started.RecoveredAttempts != 1 || len(started.UncertainCapacity) != 1 || started.UncertainCapacity[0] != "p" || len(started.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v", started)
	}
	if holds := s.poolHolds(t, "p"); len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool holds = %+v", holds)
	}
}

func TestRestartHoldBlocksDispatchUntilRelease(t *testing.T) {
	h, _, attemptID := killedMidReview(t, oneReviewer(""))
	s := restart(t, h, oneReviewer(""), approvers("r1"))

	next := s.submit(t, "s2")
	s.waitQueued(t, "r1")
	h.Clock.Advance(time.Minute)
	if n := len(s.Adapters["r1"].Calls()); n != 0 {
		t.Fatalf("r1 called %d times while its pool is held", n)
	}
	rel, err := s.Release(wire.ReleaseRequest{Pool: "p", Reason: "server restarted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("released = %v", rel.Released)
	}
	wantEnd(t, h, next, engine.StateApproved, "all_approved")
	if hold := waitHold(t, h, attemptID, true); hold.ReleasedHow != "operator" {
		t.Fatalf("hold = %+v", hold)
	}
}

func TestRestartHoldReleasedByTimeoutOfFirstPool(t *testing.T) {
	h, _, attemptID := killedMidReview(t, oneReviewer("uncertain_release_after = \"10m\""))
	restart(t, h, oneReviewer("uncertain_release_after = \"10m\""), approvers("r1"))
	h.Clock.Advance(9 * time.Minute)
	if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
		t.Fatal("released before the timeout")
	}
	h.Clock.Advance(time.Minute)
	if hold := waitHold(t, h, attemptID, true); hold.ReleasedHow != "operator_timeout" {
		t.Fatalf("hold = %+v", hold)
	}
}

func TestRestartKeepsCancelUnconfirmedHold(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}}}
	h := enginetest.New(t, enginetest.RosterTOML, scripts, enginetest.ClockStart(farFuture), enginetest.CancelUnconfirmed())
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
	// The hung call returns after the cancel; restart only once the attempt
	// has left pending, so recovery has nothing to mark unknown.
	waitFor(t, "cancelled attempt settled", func() bool {
		return getAttempt(t, h, attemptID).State != "pending"
	})

	s := restart(t, h, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}})
	if hold, _ := getHold(t, h, attemptID); hold.ReleasedAt != nil {
		t.Fatalf("hold released by the restart: %+v", hold)
	}
	if holds := s.poolHolds(t, "default"); len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool holds = %+v", holds)
	}
	started := lastStarted(t, h)
	if started.RecoveredAttempts != 0 || len(started.UncertainCapacity) != 1 || started.UncertainCapacity[0] != "default" {
		t.Fatalf("engine_started = %+v", started)
	}
	s.submit(t, "s2")
	s.waitQueued(t, "author")
	s.noCalls(t)
}

func TestRestartPendingDeliveryIsUnknownAndNotResent(t *testing.T) {
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

	s := restart(t, h, oneReviewer(""), approvers("r1"))
	if got := getAttempt(t, h, hung).State; got != "unknown" {
		t.Fatalf("delivery attempt state = %q", got)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.TerminalState != engine.StateApproved {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	if hold, found := getHold(t, h, hung); !found || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v, found %v", hold, found)
	}
	if n := len(deliveryAttempts(t, h, turnID)); n != 2 {
		t.Fatalf("%d delivery attempts", n)
	}
	s.noCalls(t)
}

func TestRestartRenamedPoolHoldsNewPool(t *testing.T) {
	h, _, attemptID := killedMidReview(t, oneReviewer(""))
	renamed := restartRoster([]rpart{author(), {id: "r1", endpoint: "local", model: "llama3", pool: "b"}}, map[string]string{"b": "concurrency = 1"})
	s := restart(t, h, renamed, approvers("r1"))

	started := lastStarted(t, h)
	if len(started.UncertainCapacity) != 1 || started.UncertainCapacity[0] != "b" || len(started.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v", started)
	}
	next := s.submit(t, "s2")
	s.waitQueued(t, "r1")
	if holds := s.poolHolds(t, "b"); len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool holds = %+v", holds)
	}
	if _, err := s.Release(wire.ReleaseRequest{Pool: "b", Reason: "restart"}); err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, next, engine.StateApproved, "all_approved")
}

// A hold on a pool of two endpoints applies to both pools it is split into.
func TestRestartSplitPoolHoldsBothPools(t *testing.T) {
	shared := restartRoster([]rpart{
		author(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "shared"},
		{id: "r2", endpoint: "local2", model: "llama3", pool: "shared"},
	}, map[string]string{"shared": "concurrency = 2"})
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}, "r1": {{Kind: fake.StepKindHang}}, "r2": {approveStep()}}
	h := enginetest.New(t, shared, scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	attemptID := attemptsOf(h, turnID, "r1")[0]

	split := restartRoster([]rpart{
		author(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "p1"},
		{id: "r2", endpoint: "local2", model: "llama3", pool: "p2"},
	}, map[string]string{"p1": "concurrency = 1", "p2": "concurrency = 1"})
	s := restart(t, h, split, approvers("r1", "r2"))

	started := lastStarted(t, h)
	if len(started.UncertainCapacity) != 2 || started.UncertainCapacity[0] != "p1" || started.UncertainCapacity[1] != "p2" {
		t.Fatalf("engine_started = %+v", started)
	}
	next := s.submit(t, "s2")
	s.waitQueued(t, "r1")
	s.waitQueued(t, "r2")
	for _, pool := range []string{"p1", "p2"} {
		if holds := s.poolHolds(t, pool); len(holds) != 1 || holds[0].AttemptID != attemptID {
			t.Fatalf("pool %s holds = %+v", pool, holds)
		}
	}
	rel, err := s.Release(wire.ReleaseRequest{Pool: "p1", Reason: "restart"})
	if err != nil || len(rel.Released) != 1 {
		t.Fatalf("release = %+v, err %v", rel, err)
	}
	wantEnd(t, h, next, engine.StateApproved, "all_approved")
	for _, pool := range []string{"p1", "p2"} {
		if holds := s.poolHolds(t, pool); len(holds) != 0 {
			t.Fatalf("pool %s holds = %+v", pool, holds)
		}
	}
}

func TestRestartEndpointInTwoPoolsHoldsBoth(t *testing.T) {
	h, _, attemptID := killedMidReview(t, oneReviewer(""))
	two := restartRoster([]rpart{
		author(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "a"},
		{id: "r2", endpoint: "local", model: "llama3", pool: "b"},
	}, map[string]string{"a": "concurrency = 1", "b": "concurrency = 1"})
	s := restart(t, h, two, approvers("r1", "r2"))

	started := lastStarted(t, h)
	if len(started.UncertainCapacity) != 2 || started.UncertainCapacity[0] != "a" || started.UncertainCapacity[1] != "b" {
		t.Fatalf("engine_started = %+v", started)
	}
	next := s.submit(t, "s2")
	s.waitQueued(t, "r1")
	s.waitQueued(t, "r2")
	rel, err := s.Release(wire.ReleaseRequest{Pool: "a", Reason: "restart"})
	if err != nil || len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("release = %+v, err %v", rel, err)
	}
	wantEnd(t, h, next, engine.StateApproved, "all_approved")
	if holds := s.poolHolds(t, "b"); len(holds) != 0 {
		t.Fatalf("pool b holds = %+v", holds)
	}
}

func TestRestartOrphanedHold(t *testing.T) {
	initial := restartRoster([]rpart{
		author(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "a"},
		{id: "spare", endpoint: "local2", model: "llama2", pool: "s", spare: true},
	}, map[string]string{"a": "concurrency = 1", "s": "concurrency = 1"})
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}, "r1": {{Kind: fake.StepKindHang}}}
	h := enginetest.New(t, initial, scripts, enginetest.ClockStart(farFuture))
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	attemptID := attemptsOf(h, turnID, "r1")[0]

	// Nothing in the new roster serves local/llama3.
	next := restartRoster([]rpart{author(), {id: "spare", endpoint: "local2", model: "llama2", pool: "s"}}, map[string]string{"s": "concurrency = 1"})
	s := restart(t, h, next, approvers("spare"))

	started := lastStarted(t, h)
	if len(started.UncertainCapacity) != 0 || len(started.OrphanedHolds) != 1 || started.OrphanedHolds[0].AttemptID != attemptID || started.OrphanedHolds[0].Pool != "a" {
		t.Fatalf("engine_started = %+v", started)
	}
	st, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.OrphanedHolds) != 1 || st.OrphanedHolds[0].AttemptID != attemptID || st.OrphanedHolds[0].Pool != "a" || len(st.OrphanedHolds[0].Group) != 1 {
		t.Fatalf("status orphaned holds = %+v", st.OrphanedHolds)
	}
	_, err = s.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "q"})
	we := wantRefusal(t, err, wire.ErrorCodeOrphanedHolds)
	if !strings.Contains(string(we.Holds), attemptID) {
		t.Fatalf("refusal holds = %s", we.Holds)
	}

	rel, err := s.Release(wire.ReleaseRequest{Pool: "a", AttemptID: &attemptID, Reason: "operator checked"})
	if err != nil || len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("release = %+v, err %v", rel, err)
	}
	if hold := waitHold(t, h, attemptID, true); hold.ReleasedHow != "operator" || hold.ReleasedReason != "operator checked" {
		t.Fatalf("hold = %+v", hold)
	}
	if st, err = s.Status(); err != nil || len(st.OrphanedHolds) != 0 {
		t.Fatalf("status = %+v, err %v", st.OrphanedHolds, err)
	}
	wantEnd(t, h, s.submit(t, "s3"), engine.StateApproved, "all_approved")
}

func TestRestartRosterErrorAfterRecoveryCommitted(t *testing.T) {
	h, turnID, attemptID := killedMidReview(t, oneReviewer(""))
	s := newSecond(t, h, "this is not a roster", nil)
	err := s.Start()
	var re *engine.RosterError
	if !errors.As(err, &re) {
		t.Fatalf("Start = %v, want *RosterError", err)
	}
	wantEnd(t, h, turnID, engine.StateIncomplete, "engine_restart")
	if got := getAttempt(t, h, attemptID).State; got != "unknown" {
		t.Fatalf("attempt state = %q", got)
	}
	if _, found := getHold(t, h, attemptID); !found {
		t.Fatal("no hold after recovery")
	}
	var runs int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("engine runs = %d, err %v", runs, err)
	}
}

func TestRestartRecoveryTxIsAtomic(t *testing.T) {
	h, turnID, attemptID := killedMidReview(t, oneReviewer(""))
	before, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	s := newSecond(t, h, oneReviewer(""), approvers("r1"))
	// The last event of the recovery Tx fails to marshal.
	s.SetPayloadHook(func(kind string, payload any) any {
		if p, ok := payload.(wire.EvTurnState); ok && p.State == engine.StateIncomplete {
			return unmarshalable{C: make(chan int)}
		}
		return payload
	})
	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded despite the failing recovery transaction")
	}
	if after, err := h.Store.MaxSeq(); err != nil || after != before {
		t.Fatalf("seq %d -> %d, err %v", before, after, err)
	}
	if got := getAttempt(t, h, attemptID).State; got != "pending" {
		t.Fatalf("attempt state = %q", got)
	}
	if row, err := h.Store.GetTurn(turnID); err != nil || row.TerminalState != "" {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	if _, found := getHold(t, h, attemptID); found {
		t.Fatal("hold survived the failed transaction")
	}
	s.noCalls(t)
}

func TestRestartResubscribeReceivesEventsSince(t *testing.T) {
	h, _, _ := killedMidReview(t, oneReviewer(""))
	seen := int64(0)
	for _, ev := range h.Events() {
		seen = ev.Seq
	}
	restart(t, h, oneReviewer(""), approvers("r1"))
	want, err := h.Store.EventsSince(seen, 1000)
	if err != nil || len(want) == 0 {
		t.Fatalf("EventsSince = %d events, err %v", len(want), err)
	}

	sub, _, err := h.Hub.Subscribe(seen)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kinds := map[string]bool{}
	for i, w := range want {
		ev, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if ev.Seq != w.Seq || ev.Seq != seen+int64(i)+1 {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, w.Seq)
		}
		kinds[ev.Kind] = true
	}
	for _, k := range []string{wire.EventKindAttemptUnknown, wire.EventKindTurnState, wire.EventKindEngineStarted} {
		if !kinds[k] {
			t.Fatalf("resubscription missed %s; got %v", k, kinds)
		}
	}
}

// incompleteWithBlock returns a harness whose turn ended incomplete with an
// open block and a candidate, and a restarted engine, scripted with acks, that
// sends deliveries.
func incompleteWithBlock(t *testing.T, acks map[string][]fake.Step) (*enginetest.Harness, *second, string) {
	t.Helper()
	return incompleteWithScripts(t, map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {blockStep("b1")},
		"r2":     {approveStep()},
	}, acks)
}

func incompleteWithScripts(t *testing.T, scripts, acks map[string][]fake.Step) (*enginetest.Harness, *second, string) {
	t.Helper()
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts, enginetest.ClockStart(farFuture))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	release()
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, "block:") {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	if blocks, err := h.Store.OpenBlocks(turnID); err != nil || len(blocks) != 1 {
		t.Fatalf("open blocks = %v, err %v", blocks, err)
	}
	t.Cleanup(engine.EnableDelivery())
	return h, restart(t, h, reviewRoster("author", "r1", "r2"), acks), turnID
}

// storeOverrideReason returns the reason of the turn's override event as
// written to the journal.
func storeOverrideReason(t *testing.T, h *enginetest.Harness, turnID string) string {
	t.Helper()
	evs, err := h.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind != wire.EventKindOverride {
			continue
		}
		var body wire.EvOverride
		if err := json.Unmarshal(ev.Data, &body); err != nil {
			t.Fatal(err)
		}
		if body.TurnID == turnID {
			return body.Reason
		}
	}
	return ""
}

func ackAll() map[string][]fake.Step {
	return map[string][]fake.Step{"author": {ack()}, "r1": {ack()}, "r2": {ack()}}
}

func TestOverrideIncompleteTurnDeliversOverridden(t *testing.T) {
	h, s, turnID := incompleteWithBlock(t, ackAll())
	if err := s.Override(turnID, "the block is a false alarm"); err != nil {
		t.Fatal(err)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.TerminalState != engine.StateOverridden {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	if blocks, err := h.Store.OpenBlocks(turnID); err != nil || len(blocks) != 0 {
		t.Fatalf("open blocks = %v, err %v", blocks, err)
	}
	var blockState, turnState bool
	for _, ev := range h.Events() {
		switch p := ev.Payload.(type) {
		case *wire.EvBlockState:
			blockState = blockState || (p.BlockID != "" && p.State == engine.StateOverridden)
		case *wire.EvTurnState:
			turnState = turnState || (p.TurnID == turnID && p.State == engine.StateOverridden)
		}
	}
	override := storeOverrideReason(t, h, turnID) == "the block is a false alarm"
	if !override || !blockState || !turnState {
		t.Fatalf("override event %v, block_state %v, turn_state %v", override, blockState, turnState)
	}

	got := waitDeliveries(t, h, turnID, 3)
	var recipients []string
	for _, a := range got {
		recipients = append(recipients, a.Participant)
	}
	sort.Strings(recipients)
	if strings.Join(recipients, ",") != "author,r1,r2" {
		t.Fatalf("delivery recipients = %v", recipients)
	}
	for id, a := range s.Adapters {
		var calls int
		for _, c := range a.Calls() {
			if c.Request.Envelope.Phase == phaseDelivery {
				calls++
				if !strings.Contains(userContent(c.Request), "overridden") {
					t.Fatalf("%s delivery does not say overridden: %q", id, userContent(c.Request))
				}
			}
		}
		if calls != 1 {
			t.Fatalf("%s got %d deliveries", id, calls)
		}
	}
	// Not repeatable: the turn is no longer incomplete.
	_ = wantRefusal(t, s.Override(turnID, "again"), wire.ErrorCodeNotIncomplete)
}

func TestOverrideDoesNotTouchTheOpenTurn(t *testing.T) {
	// The second engine's own turn stays open: its reviewers hang.
	h, s, turnID := incompleteWithBlock(t, map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {{Kind: fake.StepKindHang}, ack()},
		"r2":     {{Kind: fake.StepKindHang}, ack()},
	})
	open := s.submit(t, "s2")
	waitFor(t, "reviewers of the open turn called", func() bool {
		return len(s.Adapters["r1"].Calls()) == 1 && len(s.Adapters["r2"].Calls()) == 1
	})
	if err := s.Override(turnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	waitDeliveries(t, h, turnID, 3)
	if row, err := h.Store.GetTurn(open); err != nil || row.TerminalState != "" {
		t.Fatalf("open turn = %+v, err %v", row, err)
	}
	if n := len(deliveryAttempts(t, h, open)); n != 0 {
		t.Fatalf("%d deliveries for the open turn", n)
	}
	st, err := s.Status()
	if err != nil || len(st.TurnsOpen) != 1 || st.TurnsOpen[0] != open {
		t.Fatalf("status turns_open = %v, err %v", st.TurnsOpen, err)
	}
}

func TestOverrideErrors(t *testing.T) {
	t.Run("reason required", func(t *testing.T) {
		h := enginetest.New(t, enginetest.RosterTOML, nil)
		_ = wantRefusal(t, h.Engine.Override("t", ""), wire.ErrorCodeReasonRequired)
	})
	t.Run("not found", func(t *testing.T) {
		h := enginetest.New(t, enginetest.RosterTOML, nil)
		we := wantRefusal(t, h.Engine.Override("nope", "r"), wire.ErrorCodeNotFound)
		if we.TurnID == nil || *we.TurnID != "nope" {
			t.Fatalf("refusal = %+v", we)
		}
	})
	t.Run("approved", func(t *testing.T) {
		h := enginetest.New(t, reviewRoster("author", "r1"), approvers("r1"))
		turnID := submitReview(t, h, "s1")
		wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
		_ = wantRefusal(t, h.Engine.Override(turnID, "r"), wire.ErrorCodeNotIncomplete)
	})
	t.Run("cancelled", func(t *testing.T) {
		h := enginetest.New(t, reviewRoster("author", "r1"), approvers("r1"))
		release := h.Hold("r1")
		turnID := submitReview(t, h, "s1")
		waitCount(t, h, wire.EventKindAttemptBegun, 2)
		if err := h.Engine.Cancel(turnID); err != nil {
			t.Fatal(err)
		}
		release()
		wantEnd(t, h, turnID, engine.StateCancelled, "cancelled")
		_ = wantRefusal(t, h.Engine.Override(turnID, "r"), wire.ErrorCodeNotIncomplete)
	})
	t.Run("no candidate", func(t *testing.T) {
		scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindError, Err: errors.New("boom")}}}
		h := enginetest.New(t, enginetest.RosterTOML, scripts)
		turnID := submitReview(t, h, "s1")
		if row := waitEnded(t, h, turnID); row.TerminalState != engine.StateIncomplete {
			t.Fatalf("turn ended %q", row.TerminalState)
		}
		_ = wantRefusal(t, h.Engine.Override(turnID, "r"), wire.ErrorCodeNotIncomplete)
	})
	t.Run("not yet terminal", func(t *testing.T) {
		h := enginetest.New(t, reviewRoster("author", "r1"), approvers("r1"))
		release := h.Hold("author")
		turnID := submitReview(t, h, "s1")
		_ = wantRefusal(t, h.Engine.Override(turnID, "r"), wire.ErrorCodeNotIncomplete)
		release()
	})
}

func TestOverrideTxIsAtomic(t *testing.T) {
	h, s, turnID := incompleteWithBlock(t, ackAll())
	s.SetPayloadHook(func(kind string, payload any) any {
		if p, ok := payload.(wire.EvTurnState); ok && p.State == engine.StateOverridden {
			return unmarshalable{C: make(chan int)}
		}
		return payload
	})
	if err := s.Override(turnID, "false alarm"); err == nil {
		t.Fatal("Override succeeded despite the failing transaction")
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.TerminalState != engine.StateIncomplete {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
	if blocks, err := h.Store.OpenBlocks(turnID); err != nil || len(blocks) != 1 {
		t.Fatalf("open blocks = %v, err %v", blocks, err)
	}
	if reason := storeOverrideReason(t, h, turnID); reason != "" {
		t.Fatalf("override event leaked: %q", reason)
	}
	if n := len(deliveryAttempts(t, h, turnID)); n != 0 {
		t.Fatalf("%d deliveries after a failed override", n)
	}
	s.SetPayloadHook(nil)
	if err := s.Override(turnID, "false alarm"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestOverrideDeliversReviewerTextNotRawBody(t *testing.T) {
	// r1's transport body differs from its reply text, as with a real adapter.
	blocker := blockStep("b1")
	blocker.RawOutput = "RAW-TRANSPORT-BODY"
	_, s, turnID := incompleteWithScripts(t, map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {blocker},
		"r2":     {approveStep()},
	}, map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {ack()},
		"r2":     {ack()},
	})
	if err := s.Override(turnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deliveries", func() bool { return len(s.Adapters["r2"].Calls()) == 1 })
	var content string
	for _, c := range s.Adapters["r2"].Calls() {
		if c.Request.Envelope.Phase == phaseDelivery {
			content = userContent(c.Request)
		}
	}
	if content == "" || strings.Contains(content, "RAW-TRANSPORT-BODY") || !strings.Contains(content, "b1") {
		t.Fatalf("delivery content = %q", content)
	}
}

func TestOverrideDeliversGateFailedReviewerText(t *testing.T) {
	trunc := fake.Step{Kind: fake.StepKindReply, Text: "GATE-FAILED-REVIEW-TEXT", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {trunc},
		"r2":     {approveStep()},
	}, enginetest.ClockStart(farFuture))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	release()
	row := waitEnded(t, h, turnID)
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "reviewer_failed:r1:truncated" {
		t.Fatalf("turn ended %q %q", row.TerminalState, row.TerminalReason)
	}
	t.Cleanup(engine.EnableDelivery())
	s := restart(t, h, reviewRoster("author", "r1", "r2"), map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), ack()},
		"r1":     {ack()},
		"r2":     {ack()},
	})
	if err := s.Override(turnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deliveries", func() bool { return len(s.Adapters["r2"].Calls()) == 1 })
	var content string
	for _, c := range s.Adapters["r2"].Calls() {
		if c.Request.Envelope.Phase == phaseDelivery {
			content = userContent(c.Request)
		}
	}
	if !strings.Contains(content, "GATE-FAILED-REVIEW-TEXT") {
		t.Fatalf("delivery content = %q", content)
	}
}
