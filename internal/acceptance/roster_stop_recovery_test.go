package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	rosterStop = "Roster and stop"
	recovery   = "Recovery"
)

func init() {
	cover(rosterStop, "`status.roster_file_changed` is false at start, true after the file is rewritten with a different participant, true after a comment-only edit, false again after it is restored byte for byte", "TestRoster_FileChanged")
	cover(rosterStop, "a hold left in pool `a` survives a restart in which the participant's pool is renamed `b` and the next dispatch to `b` waits", "TestRoster_HoldRenamedPool")
	cover(rosterStop, "a hold on a pool of two endpoints survives a restart that splits them into two pools and both pools wait", "TestRoster_HoldSplitPool")
	cover(rosterStop, "a hold on an endpoint that the new roster places in two pools holds both", "TestRoster_HoldTwoPoolsOneEndpoint")
	cover(rosterStop, "a hold none of whose group members remains in the roster appears in `engine_started.orphaned_holds`, `submit` is refused with `orphaned_holds`, `release{pool: \"a\", attempt_id, reason}` clears it, and `submit` then proceeds", "TestRoster_OrphanedHolds")
	cover(rosterStop, "`SIGTERM` during review ends the turn `incomplete:engine_stop`, calls `Cancel` on the in-flight attempt, sends no terminal delivery, emits `engine_stopping`, and closes the client with `stopping`", "TestRoster_SIGTERMDuringReview")
	cover(rosterStop, "with `CancelUnconfirmed` the hold survives into the next start's `engine_started`", "TestRoster_CancelUnconfirmedHold")
	cover(rosterStop, "`submit` during the grace period is refused with `stopping`", "TestRoster_SubmitDuringGrace")
	cover(rosterStop, "a second `SIGTERM` during the grace period exits and the next start recovers the attempt as `unknown`", "TestRoster_SecondSIGTERM")
	cover(rosterStop, "`syndic probe` against the running engine's journal exits with its distinct code naming the PID and still writes the export", "cmd/syndic.TestProbeLockContention")

	cover(recovery, "kill the engine mid-review, restart, the turn is `incomplete:engine_restart`, the pending attempt is `unknown`, the fake adapter's call count shows nothing re-sent", "TestRecovery_KillMidReview")
	cover(recovery, "a client reconnecting with its last `seq` receives exactly the missed events", "TestRecovery_ReconnectWithLastSeq")
	cover(recovery, "a roster that fails validation at start leaves the recovery transaction committed and no engine run written", "TestRecovery_RosterValidationFailure")
}

// rosterPart is a participant of a roster these tests build. The author is
// "author" on endpoint local; the others review, and all but those marked
// spare are required by the review preset.
type rosterPart struct {
	id, endpoint, model, pool string
	spare                     bool
}

func rosterAuthor() rosterPart {
	return rosterPart{id: "author", endpoint: "local", model: "llama2", pool: "default"}
}

// rosterTOMLOf builds a roster with endpoints local and local2, both serving
// llama2 and llama3, one pool section per entry of pools (the value is the
// section's body), and the review preset requiring every non-spare reviewer.
func rosterTOMLOf(parts []rosterPart, pools map[string]string) string {
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

// rosterOneReviewer is a roster with the author and r1, r1 in pool.
func rosterOneReviewer(pool string) string {
	return rosterTOMLOf([]rosterPart{rosterAuthor(), {id: "r1", endpoint: "local", model: "llama3", pool: pool}},
		map[string]string{pool: "concurrency = 1"})
}

// rosterScripts gives every participant normal steps.
func rosterScripts(ids ...string) map[string][]fake.Step {
	out := map[string][]fake.Step{}
	for _, id := range ids {
		out[id] = repeat(8)
	}
	return out
}

// rosterAttemptBegun waits for the participant's review attempt to begin and
// returns its id.
func (s *suite) rosterAttemptBegun(participant string) string {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && b.Participant == participant && b.Phase == "review"
	})
	b, _ := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
	return b.AttemptID
}

// rosterInFlight starts a suite on rosterTOML, holds the participant's
// reviewer call, submits, and returns once the participant's review attempt
// has begun: the attempt is then pending in the journal and its call never
// returns.
func rosterInFlight(t *testing.T, rosterTOML, hung string, ids []string, opts ...enginetest.Option) (s *suite, turnID, attemptID string) {
	t.Helper()
	s = newSuite(t, rosterTOML, rosterScripts(ids...), opts...)
	s.h.Hold(hung)
	c := s.Dial(t)
	turnID = s.Submit(t, c, "s1")
	attemptID = s.rosterAttemptBegun(hung)
	return s, turnID, attemptID
}

// rosterRestarted restarts the suite on next, which scripts normal steps for
// ids, and returns a client of the new engine and the new run's engine_started.
func rosterRestarted(t *testing.T, s *suite, next string, ids ...string) (*client.Client, event, wire.EvEngineStarted) {
	t.Helper()
	s.Restart(next, rosterScripts(ids...))
	c := s.Dial(t)
	runFirst := c.Hello().RunFirstSeq
	ev := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq == runFirst })
	es, _ := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
	return c, ev, es
}

// rosterWaitQueued waits until the engine started at started has announced
// the participant queued, and requires that it has not been asked anything.
func (s *suite) rosterWaitQueued(started event, participant string) {
	s.t.Helper()
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && ev.Seq > started.Seq && p.Participant == participant && p.State == engine.ParticipantQueued
	})
	if n := len(s.calls(participant, "review")); n != 0 {
		s.t.Fatalf("%s was dispatched %d times while its pool was held, want it to wait", participant, n)
	}
}

// rosterWantHold requires the pool to carry exactly the one hold, on the attempt.
func (s *suite) rosterWantHold(t *testing.T, c *client.Client, pool, attemptID string) {
	t.Helper()
	holds := s.holds(t, c, pool)
	if len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool %s holds %+v, want the one on %s", pool, holds, attemptID)
	}
}

// rosterReleaseAndFinish releases the hold on pool and requires the waiting
// turn to go on to approval.
func (s *suite) rosterReleaseAndFinish(t *testing.T, c *client.Client, pool, attemptID, turnID string) {
	t.Helper()
	rel, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: pool, Reason: "roster changed, endpoint checked"})
	if err != nil {
		t.Fatalf("release %s: %v", pool, err)
	}
	if len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("release returned %v, want [%s]", rel.Released, attemptID)
	}
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

func TestRoster_FileChanged(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, rosterScripts("author", "reviewer"))
	c := s.Dial(t)
	original, err := os.ReadFile(s.RosterPath)
	if err != nil {
		t.Fatal(err)
	}
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(s.RosterPath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := s.Status(t, c)
	if start.RosterFileChanged {
		t.Fatal("roster_file_changed is true at start")
	}
	if start.RosterPath != s.RosterPath {
		t.Fatalf("roster_path = %q, want %q", start.RosterPath, s.RosterPath)
	}

	extra := "[[participants]]\nid = \"extra\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"default\"\n\n[[endpoints]]"
	different := strings.Replace(string(original), "[[endpoints]]", extra, 1)
	if different == string(original) {
		t.Fatal("the roster has no [[endpoints]] to add a participant before")
	}
	write(different)
	if got := s.Status(t, c); !got.RosterFileChanged {
		t.Fatal("roster_file_changed is false after the file was rewritten with a different participant")
	}

	write(string(original) + "\n# a comment, nothing else\n")
	if got := s.Status(t, c); !got.RosterFileChanged {
		t.Fatal("roster_file_changed is false after a comment-only edit")
	}

	write(string(original))
	got := s.Status(t, c)
	if got.RosterFileChanged {
		t.Fatal("roster_file_changed is true after the file was restored byte for byte")
	}
	// The engine never reloads: nothing but the flag moved.
	if got.RosterHash != start.RosterHash || got.RunID != start.RunID {
		t.Fatalf("status after the edits: roster_hash %s run %s, at start %s and %s", got.RosterHash, got.RunID, start.RosterHash, start.RunID)
	}
}

func TestRoster_HoldRenamedPool(t *testing.T) {
	t.Parallel()
	s, _, attemptID := rosterInFlight(t, rosterOneReviewer("a"), "r1", []string{"author", "r1"})
	c, started, es := rosterRestarted(t, s, rosterOneReviewer("b"), "author", "r1")

	if len(es.UncertainCapacity) != 1 || es.UncertainCapacity[0] != "b" || len(es.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v, want the hold on pool b and none orphaned", es)
	}
	s.rosterWantHold(t, c, "b", attemptID)
	next := s.Submit(t, c, "s2")
	s.rosterWaitQueued(started, "r1")
	s.rosterWantHold(t, c, "b", attemptID)
	s.rosterReleaseAndFinish(t, c, "b", attemptID, next)
	if n := len(s.calls("r1", "review")); n != 1 {
		t.Fatalf("r1 was dispatched %d times, want once after the release", n)
	}
}

func TestRoster_HoldSplitPool(t *testing.T) {
	t.Parallel()
	shared := rosterTOMLOf([]rosterPart{
		rosterAuthor(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "shared"},
		{id: "r2", endpoint: "local2", model: "llama3", pool: "shared"},
	}, map[string]string{"shared": "concurrency = 2"})
	s, _, attemptID := rosterInFlight(t, shared, "r1", []string{"author", "r1", "r2"})
	// r2 answers at once. Wait for that, so that the kill leaves one attempt
	// pending and the hold is r1's alone.
	other := s.rosterAttemptBegun("r2")
	s.WaitEvent(func(ev event) bool {
		c, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && c.AttemptID == other
	})
	split := rosterTOMLOf([]rosterPart{
		rosterAuthor(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "p1"},
		{id: "r2", endpoint: "local2", model: "llama3", pool: "p2"},
	}, map[string]string{"p1": "concurrency = 1", "p2": "concurrency = 1"})
	c, started, es := rosterRestarted(t, s, split, "author", "r1", "r2")

	if len(es.UncertainCapacity) != 2 || es.UncertainCapacity[0] != "p1" || es.UncertainCapacity[1] != "p2" || len(es.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v, want the hold on p1 and p2", es)
	}
	next := s.Submit(t, c, "s2")
	s.rosterWaitQueued(started, "r1")
	s.rosterWaitQueued(started, "r2")
	for _, pool := range []string{"p1", "p2"} {
		s.rosterWantHold(t, c, pool, attemptID)
	}
	s.rosterReleaseAndFinish(t, c, "p1", attemptID, next)
	for _, pool := range []string{"p1", "p2"} {
		if h := s.holds(t, c, pool); len(h) != 0 {
			t.Fatalf("pool %s still holds %+v after the release", pool, h)
		}
	}
}

func TestRoster_HoldTwoPoolsOneEndpoint(t *testing.T) {
	t.Parallel()
	s, _, attemptID := rosterInFlight(t, rosterOneReviewer("p"), "r1", []string{"author", "r1"})
	two := rosterTOMLOf([]rosterPart{
		rosterAuthor(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "a"},
		{id: "r2", endpoint: "local", model: "llama3", pool: "b"},
	}, map[string]string{"a": "concurrency = 1", "b": "concurrency = 1"})
	c, started, es := rosterRestarted(t, s, two, "author", "r1", "r2")

	if len(es.UncertainCapacity) != 2 || es.UncertainCapacity[0] != "a" || es.UncertainCapacity[1] != "b" || len(es.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v, want the hold on both a and b", es)
	}
	next := s.Submit(t, c, "s2")
	s.rosterWaitQueued(started, "r1")
	s.rosterWaitQueued(started, "r2")
	for _, pool := range []string{"a", "b"} {
		s.rosterWantHold(t, c, pool, attemptID)
	}
	s.rosterReleaseAndFinish(t, c, "a", attemptID, next)
	for _, pool := range []string{"a", "b"} {
		if h := s.holds(t, c, pool); len(h) != 0 {
			t.Fatalf("pool %s still holds %+v after the release", pool, h)
		}
	}
}

func TestRoster_OrphanedHolds(t *testing.T) {
	t.Parallel()
	initial := rosterTOMLOf([]rosterPart{
		rosterAuthor(),
		{id: "r1", endpoint: "local", model: "llama3", pool: "a"},
		{id: "spare", endpoint: "local2", model: "llama2", pool: "s", spare: true},
	}, map[string]string{"a": "concurrency = 1", "s": "concurrency = 1"})
	s, _, attemptID := rosterInFlight(t, initial, "r1", []string{"author", "r1", "spare"})
	// Nothing in the new roster serves local/llama3.
	next := rosterTOMLOf([]rosterPart{
		rosterAuthor(),
		{id: "spare", endpoint: "local2", model: "llama2", pool: "s"},
	}, map[string]string{"s": "concurrency = 1"})
	c, _, es := rosterRestarted(t, s, next, "author", "spare")

	if len(es.UncertainCapacity) != 0 {
		t.Fatalf("engine_started.uncertain_capacity = %v, want none: the hold is orphaned", es.UncertainCapacity)
	}
	if len(es.OrphanedHolds) != 1 || es.OrphanedHolds[0].AttemptID != attemptID || es.OrphanedHolds[0].Pool != "a" {
		t.Fatalf("engine_started.orphaned_holds = %+v, want the hold of %s on pool a", es.OrphanedHolds, attemptID)
	}
	if g := es.OrphanedHolds[0].Group; len(g) != 1 || g[0] != (wire.GroupMember{EndpointName: "local", Model: "llama3"}) {
		t.Fatalf("orphaned hold group = %+v, want local/llama3", g)
	}
	if st := s.Status(t, c); len(st.OrphanedHolds) != 1 || st.OrphanedHolds[0].AttemptID != attemptID {
		t.Fatalf("status.orphaned_holds = %+v, want the one on %s", st.OrphanedHolds, attemptID)
	}

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "what is the answer?"})
	var refused *wire.Error
	if !errors.As(err, &refused) || refused.Code != wire.ErrorCodeOrphanedHolds {
		t.Fatalf("submit with an orphaned hold: %v, want error orphaned_holds", err)
	}
	if !strings.Contains(string(refused.Holds), attemptID) {
		t.Fatalf("the refusal lists holds %s, want the one on %s", refused.Holds, attemptID)
	}

	rel, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: "a", AttemptID: &attemptID, Reason: "local/llama3 was removed on purpose"})
	if err != nil {
		t.Fatalf("release of the orphaned hold by its old pool name: %v", err)
	}
	if len(rel.Released) != 1 || rel.Released[0] != attemptID {
		t.Fatalf("release returned %v, want [%s]", rel.Released, attemptID)
	}
	if st := s.Status(t, c); len(st.OrphanedHolds) != 0 {
		t.Fatalf("status.orphaned_holds = %+v after the release, want none", st.OrphanedHolds)
	}
	hold, found, err := s.Store.GetHold(attemptID)
	if err != nil || !found || hold.ReleasedAt == nil || hold.ReleasedHow != "operator" {
		t.Fatalf("hold = %+v found=%v err=%v, want released by the operator", hold, found, err)
	}

	turnID := s.Submit(t, c, "s3")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// rosterDrain reads the client's events until the engine ends the
// stream, returning what it read and the error that ended it.
func (s *suite) rosterDrain(c *client.Client) ([]event, error) {
	s.t.Helper()
	var out []event
	ctx := s.ctx()
	for {
		frame, err := c.NextEvent(ctx)
		if err != nil {
			return out, err
		}
		out = append(out, event{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body})
	}
}

func TestRoster_SIGTERMDuringReview(t *testing.T) {
	t.Parallel()
	s, turnID, attemptID := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"})
	watcher := s.Dial(t)
	if _, err := watcher.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	s.StopGracefully()

	events, err := s.rosterDrain(watcher)
	var closing *wire.Error
	if !errors.As(err, &closing) || closing.Code != wire.ErrorCodeStopping {
		t.Fatalf("the client's stream ended with %v, want error{code: stopping}", err)
	}

	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "engine_stop" {
		t.Fatalf("turn ended %q with reason %q, want incomplete:engine_stop", row.TerminalState, row.TerminalReason)
	}
	var announced, stopping bool
	for _, ev := range events {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && st.State == engine.StateIncomplete {
			announced = st.Reason != nil && *st.Reason == "engine_stop"
		}
		if ev.Kind == wire.EventKindEngineStopping {
			stopping = true
		}
	}
	if !announced {
		t.Fatalf("the client was not told the turn ended incomplete:engine_stop; it saw %s", describe(events))
	}
	if !stopping {
		t.Fatalf("the client saw no engine_stopping; it saw %s", describe(events))
	}

	cancels := s.Adapters["reviewer"].CancelCalls()
	if len(cancels) != 1 || cancels[0] != attemptID {
		t.Fatalf("Cancel calls on the reviewer = %v, want exactly [%s]", cancels, attemptID)
	}
	for id, a := range s.Adapters {
		for _, call := range a.Calls() {
			if call.Request.Envelope.Phase == "terminal_delivery" {
				t.Fatalf("%s was sent a terminal delivery %+v", id, call.Request.Envelope)
			}
		}
	}
	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Phase == "terminal_delivery" {
			t.Fatalf("attempt %s is a terminal delivery of a stopped turn", r.ID)
		}
	}
}

func TestRoster_CancelUnconfirmedHold(t *testing.T) {
	t.Parallel()
	s, turnID, attemptID := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"}, enginetest.CancelUnconfirmed())

	s.StopGracefully()
	if cancels := s.Adapters["reviewer"].CancelCalls(); len(cancels) != 1 || cancels[0] != attemptID {
		t.Fatalf("Cancel calls on the reviewer = %v, want exactly [%s]", cancels, attemptID)
	}
	if hold, found, err := s.Store.GetHold(attemptID); err != nil || !found || hold.ReleasedAt != nil {
		t.Fatalf("after the stop the hold is %+v found=%v err=%v, want it held: the cancel was never confirmed", hold, found, err)
	}

	c, _, es := rosterRestarted(t, s, "", "author", "reviewer")
	if len(es.UncertainCapacity) != 1 || es.UncertainCapacity[0] != "default" {
		t.Fatalf("engine_started.uncertain_capacity = %v, want [default]", es.UncertainCapacity)
	}
	s.rosterWantHold(t, c, "default", attemptID)
	// The stopped turn keeps its own reason: the restart recovered nothing of it.
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "engine_stop")
}

func TestRoster_SubmitDuringGrace(t *testing.T) {
	t.Parallel()
	s, _, _ := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"}, enginetest.BlockingCancel())

	// A client connected before the stop, subscribed as the TUI is: a new
	// connection cannot be made once the engine is stopping.
	late := s.Dial(t)
	if _, err := late.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- s.Engine.Stop(s.ctx()) }()
	// engine_stopping is out, and the Cancel is blocked, so the grace period
	// is open until the test releases it or the clock passes stop_grace.
	s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStopping })
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while a Cancel was blocked: the grace period was not open", err)
	default:
	}

	// The server closes every connection with stopping as the stop begins. The
	// client may see that closure before or after it writes, so the test first
	// reads the connection to its end: the closing notice is stopping.
	var closing *wire.Error
	for {
		if _, nerr := late.NextEvent(s.ctx()); nerr != nil {
			if !errors.As(nerr, &closing) || closing.Code != wire.ErrorCodeStopping {
				t.Fatalf("the connection open during the grace period ended with %v, want error{code: stopping}", nerr)
			}
			break
		}
	}
	// A submit over that connection is refused and nothing is recorded.
	_, err := late.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "late-socket", Input: "what is the answer?"})
	var refused *wire.Error
	if err == nil || (!errors.Is(err, client.ErrClosed) && !errors.As(err, &refused)) {
		t.Fatalf("submit over the socket during the grace period: %v, want the connection closed or error{code: stopping}", err)
	}
	if refused != nil && refused.Code != wire.ErrorCodeStopping {
		t.Fatalf("submit over the socket during the grace period: %v, want error{code: stopping}", err)
	}
	if _, found, ferr := s.Store.FindSubmission("late-socket"); ferr != nil || found {
		t.Fatalf("the submission refused over the socket was recorded: found=%v err=%v", found, ferr)
	}

	// The engine's own answer, which the server hands every submit to.
	_, err = s.Engine.Submit(wire.SubmitRequest{SubmissionID: "late", Input: "what is the answer?"})
	if !errors.As(err, &refused) || refused.Code != wire.ErrorCodeStopping {
		t.Fatalf("submit during the grace period: %v, want error{code: stopping}", err)
	}
	if _, found, err := s.Store.FindSubmission("late"); err != nil || found {
		t.Fatalf("the refused submission was recorded: found=%v err=%v", found, err)
	}

	s.h.ReleaseBlockingCancel()
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestRoster_SecondSIGTERM proves both halves of the clause. The process exit
// is proven by running cmd/syndicd's double-signal test, which sends two
// signals to the daemon and requires exit 130. The recovery is proven here:
// cancelling the stop's context (what cmd/syndicd does on the second signal)
// ends the stop at once, and the next start recovers the attempt as unknown.
func TestRoster_SecondSIGTERM(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestDoubleSignalExits130AndRecovers$", "./cmd/syndicd")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmd/syndicd double-signal test (exit 130 on the second signal): %v\n%s", err, out)
	}
	s, turnID, attemptID := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"}, enginetest.BlockingCancel())

	// The first signal starts the stop; its Cancel never returns. The second
	// ends the stop's context, as cmd/syndicd does, and the process exits.
	ctx, second := context.WithCancel(context.Background())
	defer second()
	stopped := make(chan error, 1)
	go func() { stopped <- s.Engine.Stop(ctx) }()
	s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStopping })
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) before the second signal", err)
	default:
	}
	second()
	if err := <-stopped; !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop after the second signal returned %v, want the cancelled context", err)
	}
	before, err := s.Store.GetTurn(turnID)
	if err != nil || before.TerminalState != engine.StateIncomplete || before.TerminalReason != "engine_stop" {
		t.Fatalf("turn after the first signal = %+v err=%v, want incomplete:engine_stop", before, err)
	}

	// The next start finds the attempt whose cancel never confirmed.
	c, _, es := rosterRestarted(t, s, "", "author", "reviewer")
	s.h.ReleaseBlockingCancel()
	if es.RecoveredAttempts != 1 {
		t.Fatalf("engine_started.recovered_attempts = %d, want 1", es.RecoveredAttempts)
	}
	s.WaitEvent(func(ev event) bool {
		u, ok := body[wire.EvAttemptUnknown](ev, wire.EventKindAttemptUnknown)
		return ok && u.AttemptID == attemptID
	})
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	if len(rows) != 1 || rows[0].ID != attemptID || rows[0].State != "unknown" {
		t.Fatalf("reviewer attempts after the restart = %+v, want %s unknown", rows, attemptID)
	}
	s.rosterWantHold(t, c, "default", attemptID)
	// The turn was ended by the stop and keeps that reason.
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "engine_stop")
}

func TestRecovery_KillMidReview(t *testing.T) {
	t.Parallel()
	s, turnID, attemptID := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"})
	before := map[string]int{}
	for id, a := range s.Adapters {
		before[id] = len(a.Calls())
	}
	killed := s.Adapters

	s.Restart("", rosterScripts("author", "reviewer"))
	c := s.Dial(t)
	st := s.WaitTerminal(turnID)
	if st.State != engine.StateIncomplete || st.Reason == nil || *st.Reason != "engine_restart" {
		t.Fatalf("recovered turn announced %+v, want incomplete:engine_restart", st)
	}
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "engine_restart")

	s.WaitEvent(func(ev event) bool {
		u, ok := body[wire.EvAttemptUnknown](ev, wire.EventKindAttemptUnknown)
		return ok && u.AttemptID == attemptID
	})
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	if len(rows) != 1 || rows[0].ID != attemptID || rows[0].State != "unknown" {
		t.Fatalf("reviewer attempts after the restart = %+v, want %s unknown", rows, attemptID)
	}

	for id, a := range killed {
		if n := len(a.Calls()); n != before[id] {
			t.Fatalf("%s: %d calls before the kill, %d after the restart", id, before[id], n)
		}
	}
	for id, a := range s.Adapters {
		if n := len(a.Calls()); n != 0 {
			t.Fatalf("the restarted engine sent %d requests to %s, want nothing re-sent", n, id)
		}
	}
}

func TestRecovery_ReconnectWithLastSeq(t *testing.T) {
	t.Parallel()
	s, _, _ := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"})

	// A client follows the engine until the review attempt begins, and so has
	// seen part of the journal when the engine is killed.
	seen := s.Dial(t)
	if _, err := seen.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	for {
		frame, err := seen.NextEvent(s.ctx())
		if err != nil {
			t.Fatalf("reading events before the kill: %v", err)
		}
		if b, ok := body[wire.EvAttemptBegun](event{Kind: frame.Kind, Body: frame.Body}, wire.EventKindAttemptBegun); ok && b.Participant == "reviewer" {
			break
		}
	}
	last := seen.LastSeq()

	s.Restart("", rosterScripts("author", "reviewer"))
	// Recovery committed and the new run announced: the journal holds what
	// the client missed.
	runFirst := s.Dial(t).Hello().RunFirstSeq
	s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq == runFirst })
	missed, err := s.Store.EventsSince(last, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) < 3 || missed[0].Seq != last+1 {
		t.Fatalf("the journal holds %d events after seq %d, first %v: the scenario missed nothing", len(missed), last, missed)
	}

	back := s.Dial(t)
	if _, err := back.Subscribe(s.ctx(), last); err != nil {
		t.Fatalf("subscribe since %d: %v", last, err)
	}
	kinds := map[string]bool{}
	for i, want := range missed {
		frame, err := back.NextEvent(s.ctx())
		if err != nil {
			t.Fatalf("event %d of %d missed: %v", i+1, len(missed), err)
		}
		if frame.Seq != want.Seq || frame.Kind != want.Kind {
			t.Fatalf("event %d is seq %d %s, want seq %d %s: events were skipped or repeated", i+1, frame.Seq, frame.Kind, want.Seq, want.Kind)
		}
		kinds[frame.Kind] = true
	}
	for _, k := range []string{wire.EventKindAttemptUnknown, wire.EventKindTurnState, wire.EventKindEngineStarted} {
		if !kinds[k] {
			t.Fatalf("the reconnecting client was not sent %s; it got %v", k, kinds)
		}
	}
	if got := back.LastSeq(); got != missed[len(missed)-1].Seq {
		t.Fatalf("last seq received %d, want %d", got, missed[len(missed)-1].Seq)
	}
}

func TestRecovery_RosterValidationFailure(t *testing.T) {
	t.Parallel()
	s, turnID, attemptID := rosterInFlight(t, enginetest.RosterTOML, "reviewer", []string{"author", "reviewer"})
	runs := func() int {
		t.Helper()
		var n int
		if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	runsBefore := runs()
	if runsBefore != 1 {
		t.Fatalf("%d engine runs before the restart, want 1", runsBefore)
	}

	// A roster that parses but fails validation: the preset names a reviewer
	// nobody is.
	invalid := strings.Replace(enginetest.RosterTOML, `required_reviewers = ["reviewer"]`, `required_reviewers = ["ghost"]`, 1)
	if invalid == enginetest.RosterTOML {
		t.Fatal("the roster has no required_reviewers to invalidate")
	}

	// The harness's Restart requires the new engine to start, so the same
	// steps are taken here with the start expected to fail.
	s.stopServer()
	s.h.Crash()
	if err := os.WriteFile(s.RosterPath, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Options{
		Store:         s.Store,
		Hub:           s.Hub,
		RosterPath:    s.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         s.Clock,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	t.Cleanup(func() { _ = next.Stop(context.Background()) })
	err = next.Start()
	var rosterErr *engine.RosterError
	if !errors.As(err, &rosterErr) {
		t.Fatalf("Start with an invalid roster = %v, want a *engine.RosterError", err)
	}

	// Recovery committed before the roster was read.
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "engine_restart" {
		t.Fatalf("turn = %q:%q, want incomplete:engine_restart committed by recovery", row.TerminalState, row.TerminalReason)
	}
	attempts, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range attempts {
		if a.ID == attemptID {
			found = true
			if a.State != "unknown" {
				t.Fatalf("attempt %s is %q, want unknown", a.ID, a.State)
			}
		}
	}
	if !found {
		t.Fatalf("attempt %s is missing", attemptID)
	}
	if hold, ok, err := s.Store.GetHold(attemptID); err != nil || !ok || hold.ReleasedAt != nil {
		t.Fatalf("hold = %+v found=%v err=%v, want recovery's hold on the attempt", hold, ok, err)
	}
	// No second engine run, and no engine_started for one.
	if got := runs(); got != runsBefore {
		t.Fatalf("%d engine runs after the failed start, want the %d of the first start", got, runsBefore)
	}
	evs, err := s.Store.EventsSince(0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, ev := range evs {
		if ev.Kind == wire.EventKindEngineStarted {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("the journal holds %d engine_started events, want only the first run's", started)
	}
}
