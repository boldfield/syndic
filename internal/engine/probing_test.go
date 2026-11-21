package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const probeAdapterVersion = "probetest/1"

// probeRoster has an author and a reviewer on one endpoint, each in its own
// pool, so a hold on one pool leaves the other clear.
func probeRoster(billing string) string {
	return fmt.Sprintf(`
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep"
eligible_roles = ["author"]
pool = "pa"

[[participants]]
id = "reviewer"
model = "m-reviewer"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "pb"
accept_unknown_truncation = true

[[endpoints]]
name = "ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = %q
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer", "m-referee"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[pools.pa]
concurrency = 1

[pools.pb]
concurrency = 1

[policy]
local_only = true
allowed_destinations = []
`, billing)
}

// probeSpy wraps an adapter to count Probe calls and record the models and
// steps they reach. fail, when set, is returned after the probe's first
// request was announced to BeforeSend, as an adapter would report it.
type probeSpy struct {
	transport.Adapter
	mu      sync.Mutex
	models  []string
	steps   []string
	fail    error
	onProbe func(model string)
}

func (s *probeSpy) Probe(ctx context.Context, endpoint, model string, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	s.mu.Lock()
	s.models = append(s.models, model)
	fail := s.fail
	s.mu.Unlock()
	if s.onProbe != nil {
		s.onProbe(model)
	}
	inner := opts.BeforeSend
	opts.BeforeSend = func(r *transport.ProbeRequest) error {
		s.mu.Lock()
		s.steps = append(s.steps, model+"/"+r.Step)
		s.mu.Unlock()
		if inner == nil {
			return nil
		}
		return inner(r)
	}
	if fail != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{Model: model, Step: "models"}); err != nil {
			return transport.CapabilityRecord{}, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
		return transport.CapabilityRecord{}, fail
	}
	return s.Adapter.Probe(ctx, endpoint, model, opts)
}

func (s *probeSpy) probed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models...)
}

// goodCapability is a complete record, so a probe of it is usable.
func goodCapability() transport.CapabilityRecord {
	return transport.CapabilityRecord{
		Identity:                 transport.NewUnknownIdentity(),
		AuthMode:                 "none",
		ReportedModel:            "reported",
		MaxTokensField:           "max_tokens",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
		FinishReasons:            map[string]transport.CompletionStatus{},
		OverLength:               "not_run:skipped",
		AdapterVersion:           probeAdapterVersion,
		ObservedAt:               time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Raw:                      json.RawMessage("{}"),
	}
}

// probeEnv is a journal, a roster file and one spy per participant; start
// builds an engine over them, so a second start is a restart.
type probeEnv struct {
	t          *testing.T
	store      *journal.Store
	db         *journal.DB
	hub        *hub.Hub
	rosterPath string
	spies      map[string]*probeSpy
	clock      *testClock
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) AfterFunc(d time.Duration, f func()) func() bool {
	timer := time.AfterFunc(24*time.Hour, f) // never fires within a test
	return timer.Stop
}

func newProbeEnv(t *testing.T, rosterTOML string, capability transport.CapabilityRecord, uncertainStep string) *probeEnv {
	t.Helper()
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	if err := os.WriteFile(rosterPath, []byte(rosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := journal.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := journal.NewStore(db)
	env := &probeEnv{
		t: t, store: store, db: db, hub: hub.New(store, 0), rosterPath: rosterPath,
		spies: map[string]*probeSpy{},
		clock: &testClock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)},
	}
	for _, id := range []string{"author", "reviewer"} {
		env.spies[id] = &probeSpy{Adapter: fake.New(nil, fake.Options{Capability: capability, ProbeUncertainStep: uncertainStep})}
	}
	return env
}

func (env *probeEnv) newEngine(mutate func(*engine.Options)) *engine.Engine {
	env.t.Helper()
	o := engine.Options{
		Store: env.store, Hub: env.hub, RosterPath: env.rosterPath,
		EngineVersion: "probetest", Clock: env.clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			if s, ok := env.spies[p.ID]; ok {
				return s, probeAdapterVersion, nil
			}
			return nil, "", errors.New("no adapter for " + p.ID)
		},
	}
	if mutate != nil {
		mutate(&o)
	}
	eng, err := engine.New(o)
	if err != nil {
		env.t.Fatal(err)
	}
	return eng
}

func (env *probeEnv) start(mutate func(*engine.Options)) *engine.Engine {
	env.t.Helper()
	eng := env.newEngine(mutate)
	if err := eng.Start(); err != nil {
		env.t.Fatalf("Start: %v", err)
	}
	env.t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	return eng
}

type probeEvent struct {
	Seq     int64
	Kind    string
	Payload json.RawMessage
}

func (env *probeEnv) events() []probeEvent {
	env.t.Helper()
	evs, err := env.store.EventsSince(0, 10000)
	if err != nil {
		env.t.Fatal(err)
	}
	var out []probeEvent
	for _, e := range evs {
		out = append(out, probeEvent{Seq: e.Seq, Kind: e.Kind, Payload: e.Data})
	}
	return out
}

func (env *probeEnv) lastStarted() wire.EvEngineStarted {
	env.t.Helper()
	var started wire.EvEngineStarted
	for _, e := range env.events() {
		if e.Kind == wire.EventKindEngineStarted {
			started = wire.EvEngineStarted{}
			if err := json.Unmarshal(e.Payload, &started); err != nil {
				env.t.Fatal(err)
			}
		}
	}
	return started
}

// finishes returns the probe_finished events of the events after seq.
func (env *probeEnv) finishes(afterSeq int64) map[string]wire.EvProbeFinished {
	env.t.Helper()
	out := map[string]wire.EvProbeFinished{}
	for _, e := range env.events() {
		if e.Seq > afterSeq && e.Kind == wire.EventKindProbeFinished {
			var f wire.EvProbeFinished
			if err := json.Unmarshal(e.Payload, &f); err != nil {
				env.t.Fatal(err)
			}
			out[f.Participant] = f
		}
	}
	return out
}

func (env *probeEnv) lastSeq() int64 {
	evs := env.events()
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

func (env *probeEnv) count(query string, args ...any) int {
	env.t.Helper()
	var n int
	if err := env.db.Conn().QueryRow(query, args...).Scan(&n); err != nil {
		env.t.Fatal(err)
	}
	return n
}

func participantDetail(t *testing.T, eng *engine.Engine, id string) (state, detail string) {
	t.Helper()
	st, err := eng.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		if p.ID == id {
			if p.Detail != nil {
				detail = *p.Detail
			}
			return p.State, detail
		}
	}
	t.Fatalf("participant %s not in status", id)
	return "", ""
}

func TestStartupProbeMakesParticipantsUsableInThisRun(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	eng := env.start(nil)

	for id, model := range map[string]string{"author": "m-author", "reviewer": "m-reviewer"} {
		if got := env.spies[id].probed(); len(got) != 1 || got[0] != model {
			t.Fatalf("%s probed %v, want [%s]", id, got, model)
		}
		if state, detail := participantDetail(t, eng, id); state == engine.ParticipantUnprobed || detail != "" {
			t.Fatalf("%s state %q detail %q, want usable in this run", id, state, detail)
		}
	}
	if started := env.lastStarted(); len(started.Unprobed) != 0 {
		t.Fatalf("engine_started.unprobed = %v", started.Unprobed)
	}
	hello := eng.Hello()
	if hello.State != "started" || hello.ManifestHash == nil {
		t.Fatalf("hello = %+v", hello)
	}

	// The frozen manifest records the probed participants as usable.
	run, err := env.store.GetRun(hello.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "started" || !strings.Contains(string(run.Manifest), "reported") {
		t.Fatalf("manifest does not freeze the probe records: state %q manifest %s", run.State, run.Manifest)
	}

	// Event order: run_opened, probe events, engine_started.
	var kinds []string
	for _, e := range env.events() {
		kinds = append(kinds, e.Kind)
	}
	order := strings.Join(kinds, ",")
	if !strings.HasPrefix(order, "run_opened,probe_started,probe_finished,probe_started,probe_finished,engine_started") {
		t.Fatalf("event order = %s", order)
	}
	for id, f := range env.finishes(0) {
		if f.Outcome != "probed" || f.Detail != nil {
			t.Fatalf("%s finished %+v", id, f)
		}
	}

	// Every request was a dispatch: an attempt row of phase probe, settled,
	// owned by the run and by no turn, with no hold and no phantom turn.
	if n := env.count(`SELECT COUNT(*) FROM attempts WHERE phase = 'probe' AND state = 'completed' AND turn_id IS NULL AND probe_run_id = ?`, hello.RunID); n == 0 {
		t.Fatal("no settled probe attempt rows")
	}
	if n := env.count(`SELECT COUNT(*) FROM attempts WHERE state != 'completed'`); n != 0 {
		t.Fatalf("%d attempts left pending or unknown", n)
	}
	if n := env.count(`SELECT COUNT(*) FROM capacity_holds`); n != 0 {
		t.Fatalf("%d holds after clean probes", n)
	}
	if n := env.count(`SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("%d turns; probing must not create any", n)
	}
	steps := len(env.spies["author"].steps) + len(env.spies["reviewer"].steps)
	if n := env.count(`SELECT COUNT(*) FROM attempts WHERE phase = 'probe'`); n != steps || steps == 0 {
		t.Fatalf("attempt rows = %d, requests announced = %d; want one row per request", n, steps)
	}
}

func TestStartupProbeSendsNothingForUsableRecords(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	env.start(nil)
	first := env.lastSeq()
	attempts := env.count(`SELECT COUNT(*) FROM attempts`)

	env.start(nil) // restart over the records the first start wrote

	for id, spy := range env.spies {
		if n := len(spy.probed()); n != 1 {
			t.Fatalf("%s probed %d times across two starts, want 1", id, n)
		}
	}
	if got := env.count(`SELECT COUNT(*) FROM attempts`); got != attempts {
		t.Fatalf("attempts %d -> %d: a restart with usable records sent requests", attempts, got)
	}
	if len(env.finishes(first)) != 0 {
		t.Fatalf("probe events on a start with usable records: %v", env.finishes(first))
	}
}

func TestStartupProbeBillingGate(t *testing.T) {
	for _, tc := range []struct {
		billing    string
		flag       bool
		wantProbed bool
		noExternal bool
	}{
		{"none", false, true, false},
		{"flat", false, true, false},
		{"metered", false, false, false},
		{"unknown", false, false, false},
		{"metered", true, true, false},
		{"unknown", true, true, false},
		{"metered", true, false, true},
	} {
		t.Run(fmt.Sprintf("%s/flag=%v", tc.billing, tc.flag), func(t *testing.T) {
			roster := probeRoster(tc.billing)
			if tc.billing == "metered" && !tc.noExternal {
				roster = strings.Replace(roster, `declared_billing = "metered"`, `declared_billing = "metered"`+"\nspending_control = \"external\"", 1)
			}
			env := newProbeEnv(t, roster, goodCapability(), "")
			eng := env.start(func(o *engine.Options) { o.ProbeMissing = tc.flag })
			probed := len(env.spies["author"].probed()) + len(env.spies["reviewer"].probed())
			if tc.wantProbed != (probed == 2) || (!tc.wantProbed && probed != 0) {
				t.Fatalf("probed %d participants, want probed=%v", probed, tc.wantProbed)
			}
			state, detail := participantDetail(t, eng, "author")
			if tc.wantProbed && state == engine.ParticipantUnprobed {
				t.Fatalf("author still unprobed: %q", detail)
			}
			if !tc.wantProbed && (state != engine.ParticipantUnprobed || detail != "no_record") {
				t.Fatalf("author state %q detail %q, want unprobed no_record", state, detail)
			}
			if !tc.wantProbed && len(env.finishes(0)) != 0 {
				t.Fatalf("probe events for an endpoint that is not probed: %v", env.finishes(0))
			}
		})
	}
}

func TestStartupProbeFailureLeavesEngineAvailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"unreachable", transport.ErrUnreachable, "unreachable"},
		{"unauthorized", transport.ErrUnauthorized, "unauthorized"},
		{"model_unavailable", transport.ErrModelUnavailable, "model_unavailable"},
		{"incomplete", fmt.Errorf("%w: no model", transport.ErrIncompleteProbe), "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
			env.spies["reviewer"].fail = tc.err
			eng := env.start(nil)

			hello := eng.Hello()
			if hello.State != "started" || hello.ManifestHash == nil {
				t.Fatalf("hello = %+v: a failed probe must not stop the engine", hello)
			}
			state, detail := participantDetail(t, eng, "reviewer")
			if state != engine.ParticipantUnprobed || detail != "probe_failed:"+tc.reason {
				t.Fatalf("reviewer state %q detail %q, want unprobed probe_failed:%s", state, detail, tc.reason)
			}
			if state, detail := participantDetail(t, eng, "author"); state == engine.ParticipantUnprobed {
				t.Fatalf("author unprobed (%s): one failure must not spoil the others", detail)
			}
			f := env.finishes(0)["reviewer"]
			if f.Outcome != "probe_failed" || f.Detail == nil || *f.Detail != tc.reason {
				t.Fatalf("probe_finished = %+v", f)
			}
			if got := env.lastStarted().Unprobed; len(got) != 1 || got[0] != "reviewer" {
				t.Fatalf("engine_started.unprobed = %v", got)
			}
			// No usable record was written, and recovery and reads work: the
			// request that failed definitively holds nothing.
			if env.count(`SELECT COUNT(*) FROM capacity_holds`) != 0 {
				t.Fatal("a definitive failure left a hold")
			}
			if env.count(`SELECT COUNT(*) FROM attempts WHERE state != 'completed'`) != 0 {
				t.Fatal("a settled request left its attempt pending")
			}
			if _, err := eng.Status(); err != nil {
				t.Fatalf("status: %v", err)
			}
		})
	}
}

func TestStartupProbePostSendFailureLeavesHoldAndRestartDoesNotResend(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	env.spies["author"].fail = transport.ErrUncertain
	eng := env.start(nil)

	if state, detail := participantDetail(t, eng, "author"); state != engine.ParticipantUnprobed || detail != "probe_failed:hold" {
		t.Fatalf("author state %q detail %q, want probe_failed:hold", state, detail)
	}
	if state, _ := participantDetail(t, eng, "reviewer"); state == engine.ParticipantUnprobed {
		t.Fatal("the other pool was not probed")
	}
	holds, err := env.store.UnreleasedHolds()
	if err != nil || len(holds) != 1 || holds[0].Pool != "pa" {
		t.Fatalf("holds = %+v, %v; want one uncertain-capacity hold on pa", holds, err)
	}
	attempt, err := env.store.GetAttempt(holds[0].AttemptID)
	if err != nil || attempt.Phase != "probe" || attempt.TurnID != "" {
		t.Fatalf("held attempt = %+v, %v", attempt, err)
	}
	var capacity *wire.EvCapacity
	for _, e := range env.events() {
		if e.Kind == wire.EventKindCapacity {
			capacity = new(wire.EvCapacity)
			if err := json.Unmarshal(e.Payload, capacity); err != nil {
				t.Fatal(err)
			}
		}
	}
	if capacity == nil || capacity.Pool != "pa" || capacity.HeldByAttempt != holds[0].AttemptID {
		t.Fatalf("capacity event = %+v", capacity)
	}
	if got := env.lastStarted().UncertainCapacity; len(got) != 1 || got[0] != "pa" {
		t.Fatalf("engine_started.uncertain_capacity = %v", got)
	}

	// Restart: the held pool is not probed again, however the endpoint
	// answers; the free pool still is (it already has its record).
	env.spies["author"].fail = nil
	before := len(env.spies["author"].probed())
	seq := env.lastSeq()
	eng2 := env.start(nil)
	if got := len(env.spies["author"].probed()); got != before {
		t.Fatalf("restart probed the held pool again (%d -> %d)", before, got)
	}
	if state, detail := participantDetail(t, eng2, "author"); state != engine.ParticipantUnprobed || detail != "probe_failed:hold" {
		t.Fatalf("author after restart: state %q detail %q", state, detail)
	}
	f := env.finishes(seq)["author"]
	if f.Outcome != "probe_failed" || f.Detail == nil || *f.Detail != "hold" {
		t.Fatalf("restart probe_finished = %+v", f)
	}
	if _, started := env.finishes(seq)["reviewer"]; started {
		t.Fatal("a participant with a usable record was probed on restart")
	}
}

func TestStartupProbeUncertainStepOfFakeAdapter(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "max_tokens")
	eng := env.start(nil)
	for _, id := range []string{"author", "reviewer"} {
		if _, detail := participantDetail(t, eng, id); detail != "probe_failed:hold" {
			t.Fatalf("%s detail %q, want probe_failed:hold", id, detail)
		}
	}
	holds, err := env.store.UnreleasedHolds()
	if err != nil || len(holds) != 2 {
		t.Fatalf("holds = %+v, %v; want one per pool", holds, err)
	}
	// models settled before the failing request; max_tokens is the one held.
	var steps []string
	for _, spy := range env.spies {
		steps = append(steps, spy.steps...)
	}
	if len(steps) != 4 {
		t.Fatalf("steps = %v", steps)
	}
	if n := env.count(`SELECT COUNT(*) FROM attempts WHERE state = 'completed' AND status = 'complete'`); n != 2 {
		t.Fatalf("settled attempts = %d, want the two models requests", n)
	}
	if n := env.count(`SELECT COUNT(*) FROM attempts WHERE status = 'interrupted'`); n != 2 {
		t.Fatalf("interrupted attempts = %d, want the two held requests", n)
	}
}

func TestStartupProbeCrashLeavesPendingRequestThatRestartHolds(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	// A previous process journaled a probe request for pool pa and died
	// before it knew the outcome.
	var attemptID string
	err := env.store.Tx(func(tx *journal.Tx) error {
		runID, err := tx.AllocateEngineRun()
		if err != nil {
			return err
		}
		attemptID, err = tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID: runID, Participant: "author", Endpoint: "ep", Model: "m-author", Pool: "pa", Step: "max_tokens",
			Group: []journal.GroupMember{{EndpointName: "ep", Model: "m-author"}},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	eng := env.start(nil)

	if got := env.spies["author"].probed(); len(got) != 0 {
		t.Fatalf("restart re-sent a probe for %v whose outcome was unknown", got)
	}
	attempt, err := env.store.GetAttempt(attemptID)
	if err != nil || attempt.State != "unknown" {
		t.Fatalf("attempt = %+v, %v; want unknown", attempt, err)
	}
	if h, found, err := env.store.GetHold(attemptID); err != nil || !found || h.Pool != "pa" || h.ReleasedAt != nil {
		t.Fatalf("hold = %+v found=%v err=%v", h, found, err)
	}
	if _, detail := participantDetail(t, eng, "author"); detail != "probe_failed:hold" {
		t.Fatalf("author detail %q", detail)
	}
	if _, detail := participantDetail(t, eng, "reviewer"); detail == "probe_failed:hold" {
		t.Fatal("an ordinary hold on pa blocked pb")
	}
	if got := env.spies["reviewer"].probed(); len(got) != 1 {
		t.Fatalf("reviewer probed %v, want its own pool probed", got)
	}
	if started := env.lastStarted(); started.RecoveredAttempts != 1 {
		t.Fatalf("recovered_attempts = %d", started.RecoveredAttempts)
	}
}

func TestStartupProbeOrphanedHoldBlocksEveryProbe(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	// A hold from a roster that no longer has its group: no current pool
	// contains a member, so no pool can be shown clear of it.
	err := env.store.Tx(func(tx *journal.Tx) error {
		runID, err := tx.AllocateEngineRun()
		if err != nil {
			return err
		}
		id, err := tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID: runID, Endpoint: "gone", Model: "gone-model", Pool: "old",
			Group: []journal.GroupMember{{EndpointName: "gone", Model: "gone-model"}},
		})
		if err != nil {
			return err
		}
		if err := tx.CompleteProbeAttempt(id, "interrupted", "reset"); err != nil {
			return err
		}
		return tx.OpenHold(journal.Hold{
			AttemptID: id, Pool: "old", EndpointName: "gone", Model: "gone-model",
			Group:     []journal.GroupMember{{EndpointName: "gone", Model: "gone-model"}},
			HeldSince: time.Now(),
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	eng := env.start(nil)

	for id, spy := range env.spies {
		if got := spy.probed(); len(got) != 0 {
			t.Fatalf("%s probed %v while an orphaned hold exists", id, got)
		}
		if _, detail := participantDetail(t, eng, id); detail != "probe_failed:hold" {
			t.Fatalf("%s detail %q, want probe_failed:hold", id, detail)
		}
	}
	if hello := eng.Hello(); hello.State != "started" {
		t.Fatalf("hello = %+v", hello)
	}
	if started := env.lastStarted(); len(started.OrphanedHolds) != 1 || len(started.Unprobed) != 2 {
		t.Fatalf("engine_started = %+v", started)
	}
}

func TestStartupProbeOrdinaryHoldBlocksOnlyItsPool(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	err := env.store.Tx(func(tx *journal.Tx) error {
		runID, err := tx.AllocateEngineRun()
		if err != nil {
			return err
		}
		group := []journal.GroupMember{{EndpointName: "ep", Model: "m-reviewer"}}
		id, err := tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID: runID, Endpoint: "ep", Model: "m-reviewer", Pool: "pb", Group: group,
		})
		if err != nil {
			return err
		}
		if err := tx.CompleteProbeAttempt(id, "interrupted", "reset"); err != nil {
			return err
		}
		return tx.OpenHold(journal.Hold{AttemptID: id, Pool: "pb", EndpointName: "ep", Model: "m-reviewer", Group: group, HeldSince: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	eng := env.start(nil)

	if got := env.spies["reviewer"].probed(); len(got) != 0 {
		t.Fatalf("held pool probed: %v", got)
	}
	if _, detail := participantDetail(t, eng, "reviewer"); detail != "probe_failed:hold" {
		t.Fatalf("reviewer detail %q", detail)
	}
	if got := env.spies["author"].probed(); len(got) != 1 {
		t.Fatalf("author probed %v; a hold on another pool must not block it", got)
	}
	if state, _ := participantDetail(t, eng, "author"); state == engine.ParticipantUnprobed {
		t.Fatal("author unprobed")
	}
}

func TestStartupProbeRefereeIdentity(t *testing.T) {
	restore := engine.SetRefereeIdentities(func(*config.Roster) []engine.RefereeIdentity {
		return []engine.RefereeIdentity{{Endpoint: "ep", Model: "m-referee"}}
	})
	defer restore()

	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	env.start(nil)

	found := false
	for _, spy := range env.spies {
		for _, m := range spy.probed() {
			if m == "m-referee" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the referee identity was not probed")
	}
	rec, ok, err := env.store.LatestProbeRecord("ep", "m-referee")
	if err != nil || !ok || !strings.Contains(string(rec.Manifest), "m-referee") {
		t.Fatalf("referee record = %+v found=%v err=%v", rec, ok, err)
	}
	var referee wire.EvProbeFinished
	for label, f := range env.finishes(0) {
		if strings.HasPrefix(label, "referee:") {
			referee = f
		}
	}
	if referee.Outcome != "probed" {
		t.Fatalf("referee probe_finished = %+v", referee)
	}

	// With its record usable, a restart probes nothing, not even the referee.
	total := 0
	for _, spy := range env.spies {
		total += len(spy.probed())
	}
	env.start(nil)
	after := 0
	for _, spy := range env.spies {
		after += len(spy.probed())
	}
	if after != total {
		t.Fatalf("restart probed again: %d -> %d", total, after)
	}
}

func TestStartupProbeHelloAndSubmitDuringProbing(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	eng := env.newEngine(nil)

	var hello wire.HelloReply
	var submitErr, statusErr error
	var status wire.StatusReply
	var attempts int
	once := sync.Once{}
	env.spies["author"].onProbe = func(string) {
		once.Do(func() {
			hello = eng.Hello()
			status, statusErr = eng.Status()
			_, submitErr = eng.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
			attempts = env.count(`SELECT COUNT(*) FROM turns`)
		})
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })

	if hello.State != "probing" || hello.ManifestHash != nil || hello.RunID == "" {
		t.Fatalf("hello during probing = %+v", hello)
	}
	opened, found, err := env.store.RunOpenedSeq(hello.RunID)
	if err != nil || !found || hello.RunFirstSeq != opened {
		t.Fatalf("run_first_seq %d, run_opened %d (found %v, %v)", hello.RunFirstSeq, opened, found, err)
	}
	if statusErr != nil || status.RunID != hello.RunID || status.RosterHash == "" || hello.RosterHash != status.RosterHash || len(status.Participants) == 0 {
		t.Fatalf("status during probing = %+v, %v", status, statusErr)
	}
	var we *wire.Error
	if !errors.As(submitErr, &we) || we.Code != wire.ErrorCodeProbing {
		t.Fatalf("submit during probing = %v, want probing refusal", submitErr)
	}
	if attempts != 0 {
		t.Fatalf("submit during probing wrote %d turns", attempts)
	}
	if after := eng.Hello(); after.State != "started" || after.ManifestHash == nil || after.RunID != hello.RunID || after.RunFirstSeq != hello.RunFirstSeq {
		t.Fatalf("hello after start = %+v, during = %+v", after, hello)
	}
}

func TestStartupProbeWritesRawRecordAndExport(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	raw, export := t.TempDir(), t.TempDir()
	env.start(func(o *engine.Options) { o.ProbeRawDir, o.ProbeExportDir = raw, export })
	for name, dir := range map[string]string{"raw": raw, "export": export} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("%s dir has %d entries, err %v; want the probes' evidence", name, len(entries), err)
		}
	}
}

func TestStartupOnRunOpenedRunsBeforeProbes(t *testing.T) {
	env := newProbeEnv(t, probeRoster("flat"), goodCapability(), "")
	var probesAtOpen int
	var state string
	var eng *engine.Engine
	eng = env.newEngine(func(o *engine.Options) {
		o.OnRunOpened = func() error {
			probesAtOpen = len(env.spies["author"].probed()) + len(env.spies["reviewer"].probed())
			state = eng.Hello().State
			return nil
		}
	})
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	if probesAtOpen != 0 || state != "probing" {
		t.Fatalf("at run opened: %d probes, state %q; want 0 and probing", probesAtOpen, state)
	}
}
