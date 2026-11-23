package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const startTimeProbesPara = "Start-time probing"

const lifecycleAdapterVersion = "lifecycle/1"

func init() {
	cover(startTimeProbesPara, "a start with an unchanged roster and usable records sends no request", "TestStartupProbe_UnchangedRosterNoProbes")
	cover(startTimeProbesPara, "a participant with no record on a `flat` endpoint is probed at start and is usable in that run", "TestStartupProbe_FlatEndpointProbed")
	cover(startTimeProbesPara, "the same on a `metered` endpoint is not probed without `--probe-missing`", "TestStartupProbe_MeteredNotProbed")
	cover(startTimeProbesPara, "an unreachable endpoint leaves the participant `probe_failed:unreachable` and the engine starts", "TestStartupProbe_UnreachableFailure")
	cover(startTimeProbesPara, "a probe request that fails after a possible send leaves an uncertain-capacity hold", "TestStartupProbe_UncertainFailure")
	cover(startTimeProbesPara, "a probe response with no `model` member yields no usable record", "TestStartupProbe_IncompleteProbeNoModel")
	cover(startTimeProbesPara, "a probe whose two max-tokens requests both fail yields no usable record", "TestStartupProbe_IncompleteProbeNoMaxTokens")
	cover(startTimeProbesPara, "`hello` after `engine_started` returns `state: \"started\"` and the manifest hash", "TestStartupProbe_HelloAfterStarted")
	cover(startTimeProbesPara, "`hello` during probing returns the run id, `state: \"probing\"`, a null `manifest_hash` and a `run_first_seq` equal to the `seq` of the emitted `run_opened`", "TestStartupProbe_HelloDuringProbing")
	cover(startTimeProbesPara, "`subscribe{since_seq: run_first_seq - 1}` replays `run_opened`, every probe event and `engine_started`", "TestStartupProbe_SubscribeReplay")
	cover(startTimeProbesPara, "`submit` during probing is refused `probing`", "TestStartupProbe_SubmitRefusedDuringProbing")
	cover(startTimeProbesPara, "the manifest is written once, after the last probe", "TestStartupProbe_ManifestWrittenAfterProbes")
	cover(startTimeProbesPara, "`engine_started` follows every `probe_finished` and the manifest and carries `manifest_hash`", "TestStartupProbe_EngineStartedAfterManifest")
	cover(startTimeProbesPara, "an ordinary recovered hold on one pool blocks probing of that pool's participants only", "TestStartupProbe_OrdinaryHoldBlocksPool")
	cover(startTimeProbesPara, "an orphaned hold blocks every start-time probe until released", "TestStartupProbe_OrphanedHoldBlocksAll")
}

// lifecycleRoster has an author and a reviewer on one endpoint of the billing,
// each in its own pool, so a hold on one pool leaves the other clear.
func lifecycleRoster(billing string) string {
	spending := ""
	if billing == "metered" {
		spending = "\nspending_control = \"external\""
	}
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
declared_billing = %q%s
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer"]
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
`, billing, spending)
}

// lifecycleCapability is a complete record, so a probe of it is usable.
func lifecycleCapability() transport.CapabilityRecord {
	contextBound := 128000
	return transport.CapabilityRecord{
		ContextBound:             &contextBound,
		Calibration:              transport.Calibration{Available: true, BytesPerToken: 4},
		Identity:                 transport.NewUnknownIdentity(),
		AuthMode:                 "none",
		ReportedModel:            enginetest.DefaultReportedModel,
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
		AdapterVersion:           lifecycleAdapterVersion,
		ObservedAt:               time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Raw:                      json.RawMessage("{}"),
	}
}

// lifeCfg is what one boot of the engine is given, per participant.
type lifeCfg struct {
	capability   map[string]transport.CapabilityRecord
	uncertain    map[string]string // probe step that fails after a possible send
	fail         map[string]error  // failure after the first request was announced
	gate         map[string]*gate  // holds the participant's probe open
	scripts      map[string][]fake.Step
	probeMissing bool
}

// gate holds a probe open until it is opened.
type gate struct {
	entered chan struct{}
	open    chan struct{}
	once    sync.Once
}

func newGate() *gate {
	return &gate{entered: make(chan struct{}), open: make(chan struct{})}
}

// release lets the probe go on; it is safe to call twice.
func (g *gate) release() { g.once.Do(func() { close(g.open) }) }

// lifeSpy wraps the fake adapter to count probes, record the requests they
// announce, fail them, and hold them open.
type lifeSpy struct {
	*fake.Adapter
	mu    sync.Mutex
	steps []string
	calls int
	fail  error
	gate  *gate
}

func (s *lifeSpy) Probe(ctx context.Context, endpoint, model string, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	s.mu.Lock()
	s.calls++
	fail := s.fail
	s.mu.Unlock()
	if s.gate != nil {
		select {
		case <-s.gate.entered:
		default:
			close(s.gate.entered)
		}
		select {
		case <-s.gate.open:
		case <-ctx.Done():
			return transport.CapabilityRecord{}, ctx.Err()
		}
	}
	inner := opts.BeforeSend
	opts.BeforeSend = func(r *transport.ProbeRequest) error {
		s.mu.Lock()
		s.steps = append(s.steps, r.Step)
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

func (s *lifeSpy) probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *lifeSpy) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steps...)
}

// lifecycle is a journal, a roster file and a hub that outlive the engines
// booted on them, so a second boot is a restart on the same journal. Each
// boot has fresh spies, and the engine is behind the control socket.
type lifecycle struct {
	*suite
	spies map[string]*lifeSpy
	stop  func()
}

func newLifecycle(t *testing.T, rosterTOML string) *lifecycle {
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
	return &lifecycle{suite: &suite{
		t:          t,
		Store:      store,
		DB:         db,
		Hub:        hub.New(store, 0),
		Clock:      enginetest.NewFakeClock(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)),
		RosterPath: rosterPath,
	}}
}

// prepare builds an engine with fresh spies over the journal and puts it
// behind the socket, without starting it.
func (l *lifecycle) prepare(cfg lifeCfg) *engine.Engine {
	l.t.Helper()
	l.spies = map[string]*lifeSpy{}
	l.Adapters = map[string]*fake.Adapter{}
	roster, err := config.Load(l.RosterPath)
	if err != nil {
		l.t.Fatal(err)
	}
	for _, p := range roster.Participants {
		capability := lifecycleCapability()
		if c, ok := cfg.capability[p.ID]; ok {
			capability = c
		}
		fa := fake.New(cfg.scripts[p.ID], fake.Options{
			DefaultReportedModel: enginetest.DefaultReportedModel,
			Capability:           capability,
			ProbeUncertainStep:   cfg.uncertain[p.ID],
		})
		l.Adapters[p.ID] = fa
		l.spies[p.ID] = &lifeSpy{Adapter: fa, fail: cfg.fail[p.ID], gate: cfg.gate[p.ID]}
	}
	eng, err := engine.New(engine.Options{
		Store:         l.Store,
		Hub:           l.Hub,
		RosterPath:    l.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         l.Clock,
		ProbeMissing:  cfg.probeMissing,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			s, ok := l.spies[p.ID]
			if !ok {
				return nil, "", errors.New("no adapter for " + p.ID)
			}
			return s, lifecycleAdapterVersion, nil
		},
	})
	if err != nil {
		l.t.Fatal(err)
	}
	l.Engine = eng
	var once sync.Once
	l.stop = func() {
		once.Do(func() {
			for _, g := range cfg.gate {
				g.release()
			}
			if err := eng.Stop(context.Background()); err != nil {
				l.t.Errorf("engine.Stop: %v", err)
			}
		})
	}
	l.t.Cleanup(l.stop)
	if l.srv == nil {
		l.serve(l.t)
	} else {
		l.stopServer()
		l.startServer()
		l.events = l.subscribe()
	}
	return eng
}

// boot starts an engine and waits for it to be started.
func (l *lifecycle) boot(cfg lifeCfg) *engine.Engine {
	l.t.Helper()
	eng := l.prepare(cfg)
	if err := eng.Start(); err != nil {
		l.t.Fatalf("Start: %v", err)
	}
	return eng
}

// bootProbing starts an engine in the background and returns once the probe
// of every participant in held is open, so the engine is probing. finish
// opens the gates and waits for the engine to be started.
func (l *lifecycle) bootProbing(cfg lifeCfg, held ...string) (finish func()) {
	l.t.Helper()
	if cfg.gate == nil {
		cfg.gate = map[string]*gate{}
	}
	for _, id := range held {
		cfg.gate[id] = newGate()
	}
	eng := l.prepare(cfg)
	done := make(chan error, 1)
	go func() { done <- eng.Start() }()
	for _, id := range held {
		select {
		case <-cfg.gate[id].entered:
		case err := <-done:
			l.t.Fatalf("Start returned (%v) before the probe of %s opened", err, id)
		case <-time.After(waitLimit):
			l.t.Fatalf("the probe of %s never started", id)
		}
	}
	return func() {
		l.t.Helper()
		for _, g := range cfg.gate {
			g.release()
		}
		select {
		case err := <-done:
			if err != nil {
				l.t.Fatalf("Start: %v", err)
			}
		case <-time.After(waitLimit):
			l.t.Fatal("the engine never started")
		}
	}
}

// restartWith stops the engine and boots a new one on the same journal.
func (l *lifecycle) restartWith(cfg lifeCfg) *engine.Engine {
	l.t.Helper()
	l.stop()
	return l.boot(cfg)
}

type journalEvent struct {
	Seq  int64
	Kind string
	Body json.RawMessage
}

// journaled returns every event of the journal.
func (l *lifecycle) journaled() []journalEvent {
	l.t.Helper()
	evs, err := l.Store.EventsSince(0, 100000)
	if err != nil {
		l.t.Fatal(err)
	}
	out := make([]journalEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, journalEvent{Seq: e.Seq, Kind: e.Kind, Body: e.Data})
	}
	return out
}

// runEvents returns the events of the engine's current run, from its
// run_opened on.
func (l *lifecycle) runEvents() []journalEvent {
	l.t.Helper()
	first := l.Engine.Hello().RunFirstSeq
	var out []journalEvent
	for _, e := range l.journaled() {
		if e.Seq >= first {
			out = append(out, e)
		}
	}
	return out
}

func kindsOf(evs []journalEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// probeFinishes returns the probe_finished events of the run by participant.
func (l *lifecycle) probeFinishes() map[string]wire.EvProbeFinished {
	l.t.Helper()
	out := map[string]wire.EvProbeFinished{}
	for _, e := range l.runEvents() {
		if e.Kind != wire.EventKindProbeFinished {
			continue
		}
		var f wire.EvProbeFinished
		if err := json.Unmarshal(e.Body, &f); err != nil {
			l.t.Fatal(err)
		}
		out[f.Participant] = f
	}
	return out
}

// probeStarts returns the participants of the run's probe_started events.
func (l *lifecycle) probeStarts() []string {
	l.t.Helper()
	var out []string
	for _, e := range l.runEvents() {
		if e.Kind != wire.EventKindProbeStarted {
			continue
		}
		var s wire.EvProbeStarted
		if err := json.Unmarshal(e.Body, &s); err != nil {
			l.t.Fatal(err)
		}
		out = append(out, s.Participant)
	}
	return out
}

// started returns the run's engine_started event.
func (l *lifecycle) started() (wire.EvEngineStarted, int64) {
	l.t.Helper()
	for _, e := range l.runEvents() {
		if e.Kind == wire.EventKindEngineStarted {
			var es wire.EvEngineStarted
			if err := json.Unmarshal(e.Body, &es); err != nil {
				l.t.Fatalf("decode engine_started: %v", err)
			}
			return es, e.Seq
		}
	}
	l.t.Fatalf("the run has no engine_started: %v", kindsOf(l.runEvents()))
	return wire.EvEngineStarted{}, 0
}

// participant returns the participant as status reports it.
func (l *lifecycle) participant(t *testing.T, c *client.Client, id string) wire.Participant {
	t.Helper()
	for _, p := range l.Status(t, c).Participants {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("status lists no participant %q", id)
	return wire.Participant{}
}

// wantProbed requires the participant to be usable in this run, with a record
// in the journal for its model.
func (l *lifecycle) wantProbed(t *testing.T, c *client.Client, id, model string) {
	t.Helper()
	p := l.participant(t, c, id)
	if p.State == engine.ParticipantUnprobed {
		t.Fatalf("%s is %q (%s), want usable", id, p.State, str(p.Detail))
	}
	if _, found, err := l.Store.LatestProbeRecord("ep", model); err != nil || !found {
		t.Fatalf("probe record of %s: found=%v err=%v, want one", model, found, err)
	}
}

// wantUnprobed requires the participant to be unprobed with the detail, and no
// usable record in the journal for its model.
func (l *lifecycle) wantUnprobed(t *testing.T, c *client.Client, id, model, detail string) {
	t.Helper()
	p := l.participant(t, c, id)
	if p.State != engine.ParticipantUnprobed || str(p.Detail) != detail {
		t.Fatalf("%s is %q with detail %q, want unprobed with %q", id, p.State, str(p.Detail), detail)
	}
	if rec, found, err := l.Store.LatestProbeRecord("ep", model); err != nil || found {
		t.Fatalf("probe record of %s: %+v found=%v err=%v, want none", model, rec, found, err)
	}
}

// probeAttempts counts the attempt rows of probe requests.
func (l *lifecycle) probeAttempts() int {
	l.t.Helper()
	var n int
	if err := l.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempts WHERE phase = 'probe'`).Scan(&n); err != nil {
		l.t.Fatal(err)
	}
	return n
}

// runRow is the engine run's row: its state and the manifest it holds.
func (l *lifecycle) runRow(runID string) (state string, manifest, hash *string) {
	l.t.Helper()
	if err := l.DB.Conn().QueryRow(`SELECT state, manifest, manifest_hash FROM runs WHERE id = ?`, runID).Scan(&state, &manifest, &hash); err != nil {
		l.t.Fatal(err)
	}
	return state, manifest, hash
}

func (l *lifecycle) totalProbes() int {
	n := 0
	for _, s := range l.spies {
		n += s.probes()
	}
	return n
}

// seedHold writes an unreleased uncertain-capacity hold for the group on the
// pool into the journal, as a crashed engine would have left it.
func seedHold(t *testing.T, store *journal.Store, pool, endpoint, model string) string {
	t.Helper()
	group := []journal.GroupMember{{EndpointName: endpoint, Model: model}}
	var attemptID string
	err := store.Tx(func(tx *journal.Tx) error {
		runID, err := tx.AllocateEngineRun()
		if err != nil {
			return err
		}
		attemptID, err = tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID: runID, Endpoint: endpoint, Model: model, Pool: pool, Group: group, Step: "models",
		})
		if err != nil {
			return err
		}
		if err := tx.CompleteProbeAttempt(attemptID, "interrupted", "reset"); err != nil {
			return err
		}
		return tx.OpenHold(journal.Hold{
			AttemptID: attemptID, Pool: pool, EndpointName: endpoint, Model: model,
			Group: group, HeldSince: time.Now(),
		})
	})
	if err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	return attemptID
}

func TestStartupProbe_UnchangedRosterNoProbes(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{})
	if l.totalProbes() != 2 {
		t.Fatalf("the first start probed %d times, want one probe for each of the two participants", l.totalProbes())
	}
	attempts := l.probeAttempts()
	if attempts == 0 {
		t.Fatal("the first start journaled no probe attempt")
	}

	// A new engine over the same journal and roster file, with fresh adapters.
	l.restartWith(lifeCfg{})
	if n := l.totalProbes(); n != 0 {
		t.Fatalf("the restart probed %d times with usable records, want none", n)
	}
	for id, s := range l.spies {
		if steps := s.requested(); len(steps) != 0 {
			t.Fatalf("%s's adapter was asked for %v on a start with usable records", id, steps)
		}
	}
	if n := l.probeAttempts(); n != attempts {
		t.Fatalf("probe attempts went from %d to %d on a start with usable records", attempts, n)
	}

	// With zero probes, hello and replay still work.
	c := l.Dial(t)
	hello := c.Hello()
	if hello.State != "started" || hello.ManifestHash == nil || hello.RunID == "" {
		t.Fatalf("hello = %+v, want a started run with a manifest hash", hello)
	}
	for _, id := range []string{"author", "reviewer"} {
		if p := l.participant(t, c, id); p.State == engine.ParticipantUnprobed {
			t.Fatalf("%s is unprobed after a restart that kept its record", id)
		}
	}
	got := replay(t, l.suite, hello.RunFirstSeq-1)
	if want := []string{wire.EventKindRunOpened, wire.EventKindEngineStarted}; !reflect.DeepEqual(kindsOf(got), want) {
		t.Fatalf("replay of a run with no probes = %v, want %v", kindsOf(got), want)
	}
	if got[0].Seq != hello.RunFirstSeq {
		t.Fatalf("run_opened seq %d, hello run_first_seq %d", got[0].Seq, hello.RunFirstSeq)
	}
}

// replay subscribes a new client at sinceSeq and reads events until the run's
// engine_started.
func replay(t *testing.T, s *suite, sinceSeq int64) []journalEvent {
	t.Helper()
	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), sinceSeq); err != nil {
		t.Fatalf("subscribe since %d: %v", sinceSeq, err)
	}
	ctx := s.ctx()
	var out []journalEvent
	for {
		frame, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatalf("replay: %v; read %v", err, kindsOf(out))
		}
		out = append(out, journalEvent{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body})
		if frame.Kind == wire.EventKindEngineStarted {
			return out
		}
	}
}

func TestStartupProbe_FlatEndpointProbed(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{scripts: map[string][]fake.Step{"author": {enginetest.Reply(authorOK)}}})
	c := l.Dial(t)

	for id, model := range map[string]string{"author": "m-author", "reviewer": "m-reviewer"} {
		if n := l.spies[id].probes(); n != 1 {
			t.Fatalf("%s was probed %d times at start, want once", id, n)
		}
		if steps := l.spies[id].requested(); len(steps) == 0 || steps[0] != "models" {
			t.Fatalf("%s's probe requested %v, want the probe's own requests starting with models", id, steps)
		}
		l.wantProbed(t, c, id, model)
		if f := l.probeFinishes()[id]; f.Outcome != "probed" || f.Detail != nil {
			t.Fatalf("probe_finished of %s = %+v, want probed", id, f)
		}
	}
	if len(l.Adapters["author"].Calls()) != 0 {
		t.Fatal("the author's adapter completed a request before any submit: the probe is not a turn")
	}

	// Usable in this run: a turn that needs the author is accepted.
	turnID := l.SubmitPreset(t, c, "s1", "solo")
	l.WaitTerminal(turnID)
	wantTurn(t, l.Turn(t, c, turnID), engine.StateSoloComplete, "solo_ok")
}

func TestStartupProbe_MeteredNotProbed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		flag       bool
		wantProbed bool
	}{
		{"without the flag", false, false},
		{"with --probe-missing", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := newLifecycle(t, lifecycleRoster("metered"))
			l.boot(lifeCfg{probeMissing: tc.flag})
			c := l.Dial(t)
			if !tc.wantProbed {
				if n := l.totalProbes(); n != 0 {
					t.Fatalf("a metered endpoint was probed %d times without --probe-missing", n)
				}
				if got := l.probeAttempts(); got != 0 {
					t.Fatalf("%d probe attempts journaled without --probe-missing", got)
				}
				if starts := l.probeStarts(); len(starts) != 0 || len(l.probeFinishes()) != 0 {
					t.Fatalf("probe events %v %v for an endpoint that is not probed", starts, l.probeFinishes())
				}
				l.wantUnprobed(t, c, "author", "m-author", "no_record")
				l.wantUnprobed(t, c, "reviewer", "m-reviewer", "no_record")
				if es, _ := l.started(); len(es.Unprobed) != 2 {
					t.Fatalf("engine_started.unprobed = %v, want both participants", es.Unprobed)
				}
				return
			}
			if n := l.totalProbes(); n != 2 {
				t.Fatalf("--probe-missing probed %d times, want 2", n)
			}
			l.wantProbed(t, c, "author", "m-author")
			l.wantProbed(t, c, "reviewer", "m-reviewer")
		})
	}
}

func TestStartupProbe_UnreachableFailure(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{fail: map[string]error{"author": transport.ErrUnreachable}})
	c := l.Dial(t)

	if hello := c.Hello(); hello.State != "started" || hello.ManifestHash == nil {
		t.Fatalf("hello = %+v, want the engine started despite the failed probe", hello)
	}
	l.wantUnprobed(t, c, "author", "m-author", "probe_failed:unreachable")
	if f := l.probeFinishes()["author"]; f.Outcome != "probe_failed" || str(f.Detail) != "unreachable" {
		t.Fatalf("probe_finished of author = %+v, want probe_failed unreachable", f)
	}
	es, _ := l.started()
	if !reflect.DeepEqual(es.Unprobed, []string{"author"}) {
		t.Fatalf("engine_started.unprobed = %v, want [author]", es.Unprobed)
	}
	// The other participant's probe is not affected.
	l.wantProbed(t, c, "reviewer", "m-reviewer")
	_, err := c.Submit(l.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: strPtr("solo")})
	_ = wantRefusal(t, err, wire.ErrorCodeUnprobed)
}

func strPtr(s string) *string { return &s }

func TestStartupProbe_UncertainFailure(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{uncertain: map[string]string{"author": "system_role"}})
	c := l.Dial(t)

	holds, err := l.Store.UnreleasedHolds()
	if err != nil || len(holds) != 1 || holds[0].Pool != "pa" || holds[0].EndpointName != "ep" || holds[0].Model != "m-author" {
		t.Fatalf("unreleased holds = %+v, err %v, want one on pool pa for ep/m-author", holds, err)
	}
	var state, status string
	row := l.DB.Conn().QueryRow(`SELECT state, status FROM attempts WHERE id = ?`, holds[0].AttemptID)
	if err := row.Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" {
		t.Fatalf("the held probe attempt is %q/%q, want interrupted", state, status)
	}
	got := l.holds(t, c, "pa")
	if len(got) != 1 || got[0].AttemptID != holds[0].AttemptID {
		t.Fatalf("status holds of pool pa = %+v, want the probe's attempt %s", got, holds[0].AttemptID)
	}
	if other := l.holds(t, c, "pb"); len(other) != 0 {
		t.Fatalf("pool pb holds %+v, want none", other)
	}
	l.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.Pool == "pa" && cap.HeldByAttempt == holds[0].AttemptID && cap.State == "uncertain"
	})
	l.wantUnprobed(t, c, "author", "m-author", "probe_failed:hold")
	if es, _ := l.started(); !reflect.DeepEqual(es.UncertainCapacity, []string{"pa"}) {
		t.Fatalf("engine_started.uncertain_capacity = %v, want [pa]", es.UncertainCapacity)
	}
	// The request that was possibly sent is not sent again within the run.
	if steps := l.spies["author"].requested(); len(steps) == 0 || steps[len(steps)-1] != "system_role" {
		t.Fatalf("the author's probe requested %v, want it to end at system_role", steps)
	}
}

func TestStartupProbe_IncompleteProbeNoModel(t *testing.T) {
	t.Parallel()
	capability := lifecycleCapability()
	capability.ReportedModel = "" // the response had no model member
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{capability: map[string]transport.CapabilityRecord{"author": capability}})
	c := l.Dial(t)

	l.wantUnprobed(t, c, "author", "m-author", "probe_failed:incomplete")
	if f := l.probeFinishes()["author"]; f.Outcome != "probe_failed" || str(f.Detail) != "incomplete" {
		t.Fatalf("probe_finished of author = %+v, want probe_failed incomplete", f)
	}
	if holds, err := l.Store.UnreleasedHolds(); err != nil || len(holds) != 0 {
		t.Fatalf("holds = %+v, err %v: every request got its response, so none is uncertain", holds, err)
	}
	l.wantProbed(t, c, "reviewer", "m-reviewer")
}

func TestStartupProbe_IncompleteProbeNoMaxTokens(t *testing.T) {
	t.Parallel()
	capability := lifecycleCapability()
	capability.MaxTokensField = "" // neither max_tokens nor max_completion_tokens worked
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{capability: map[string]transport.CapabilityRecord{"author": capability}})
	c := l.Dial(t)

	steps := strings.Join(l.spies["author"].requested(), ",")
	if !strings.Contains(steps, "max_tokens,max_completion_tokens") {
		t.Fatalf("the author's probe requested %q, want both max-tokens requests", steps)
	}
	l.wantUnprobed(t, c, "author", "m-author", "probe_failed:incomplete")
	if f := l.probeFinishes()["author"]; f.Outcome != "probe_failed" || str(f.Detail) != "incomplete" {
		t.Fatalf("probe_finished of author = %+v, want probe_failed incomplete", f)
	}
	if holds, err := l.Store.UnreleasedHolds(); err != nil || len(holds) != 0 {
		t.Fatalf("holds = %+v, err %v, want none", holds, err)
	}
	l.wantProbed(t, c, "reviewer", "m-reviewer")
}

func TestStartupProbe_HelloAfterStarted(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{})
	es, _ := l.started()
	hello := l.Dial(t).Hello()
	if hello.State != "started" {
		t.Fatalf("hello state = %q, want started", hello.State)
	}
	if hello.ManifestHash == nil || *hello.ManifestHash != es.ManifestHash || es.ManifestHash == "" {
		t.Fatalf("hello manifest_hash = %v, engine_started carries %q", hello.ManifestHash, es.ManifestHash)
	}
	if hello.RunID != es.RunID {
		t.Fatalf("hello run %q, engine_started run %q", hello.RunID, es.RunID)
	}
}

func TestStartupProbe_HelloDuringProbing(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	finish := l.bootProbing(lifeCfg{}, "reviewer")
	hello := l.Dial(t).Hello()
	if hello.RunID == "" || hello.State != "probing" || hello.ManifestHash != nil {
		t.Fatalf("hello during probing = %+v, want the run id, state probing and no manifest_hash", hello)
	}
	opened := l.runOpened(hello.RunID)
	if hello.RunFirstSeq != opened {
		t.Fatalf("hello run_first_seq %d, run_opened seq %d", hello.RunFirstSeq, opened)
	}
	finish()

	after := l.Dial(t).Hello()
	if after.State != "started" || after.ManifestHash == nil || after.RunID != hello.RunID || after.RunFirstSeq != hello.RunFirstSeq {
		t.Fatalf("hello after start = %+v, during probing = %+v", after, hello)
	}
}

// runOpened returns the seq of the run's run_opened event as the journal has it.
func (l *lifecycle) runOpened(runID string) int64 {
	l.t.Helper()
	for _, e := range l.journaled() {
		if e.Kind != wire.EventKindRunOpened {
			continue
		}
		var ro wire.EvRunOpened
		if err := json.Unmarshal(e.Body, &ro); err != nil {
			l.t.Fatal(err)
		}
		if ro.RunID == runID {
			return e.Seq
		}
	}
	l.t.Fatalf("no run_opened for run %s", runID)
	return 0
}

func TestStartupProbe_SubscribeReplay(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	// The reviewer's probe is open, so the author's events are replayed and
	// the rest arrive live.
	finish := l.bootProbing(lifeCfg{}, "reviewer")
	hello := l.Dial(t).Hello()

	c := l.Dial(t)
	if _, err := c.Subscribe(l.ctx(), hello.RunFirstSeq-1); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	next := func() journalEvent {
		frame, err := c.NextEvent(l.ctx())
		if err != nil {
			t.Fatalf("next event: %v", err)
		}
		return journalEvent{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body}
	}
	var got []journalEvent
	for _, kind := range []string{wire.EventKindRunOpened, wire.EventKindProbeStarted, wire.EventKindProbeFinished, wire.EventKindProbeStarted} {
		ev := next()
		if ev.Kind != kind {
			t.Fatalf("replayed %s, want %s", ev.Kind, kind)
		}
		got = append(got, ev)
	}
	if got[0].Seq != hello.RunFirstSeq {
		t.Fatalf("first replayed event has seq %d, want run_first_seq %d", got[0].Seq, hello.RunFirstSeq)
	}
	finish()
	for {
		ev := next()
		got = append(got, ev)
		if ev.Kind == wire.EventKindEngineStarted {
			break
		}
	}

	want := []string{
		wire.EventKindRunOpened,
		wire.EventKindProbeStarted, wire.EventKindProbeFinished,
		wire.EventKindProbeStarted, wire.EventKindProbeFinished,
		wire.EventKindEngineStarted,
	}
	if !reflect.DeepEqual(kindsOf(got), want) {
		t.Fatalf("subscribed events = %v, want %v", kindsOf(got), want)
	}
	if journaled := l.runEvents(); !reflect.DeepEqual(kindsOf(journaled), want) {
		t.Fatalf("journal of the run = %v, want %v", kindsOf(journaled), want)
	}
	for i := range got {
		if got[i].Seq != l.runEvents()[i].Seq {
			t.Fatalf("event %d has seq %d, the journal has %d", i, got[i].Seq, l.runEvents()[i].Seq)
		}
	}
	// A client that attaches after the start gets the same replay.
	if late := replay(t, l.suite, hello.RunFirstSeq-1); !reflect.DeepEqual(kindsOf(late), want) {
		t.Fatalf("late replay = %v, want %v", kindsOf(late), want)
	}
}

func TestStartupProbe_SubmitRefusedDuringProbing(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	finish := l.bootProbing(lifeCfg{}, "reviewer")
	c := l.Dial(t)
	if c.Hello().State != "probing" {
		t.Fatalf("hello = %+v, want the engine probing", c.Hello())
	}
	_, err := c.Submit(l.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: strPtr("solo")})
	_ = wantRefusal(t, err, wire.ErrorCodeProbing)
	var turns int
	if err := l.DB.Conn().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil || turns != 0 {
		t.Fatalf("turns = %d, err %v: a refused submit must write nothing", turns, err)
	}
	finish()
	// The refusal did not consume the submission id.
	turnID := l.SubmitPreset(t, c, "s1", "solo")
	if turnID == "" {
		t.Fatal("no turn after the probes ended")
	}
}

func TestStartupProbe_ManifestWrittenAfterProbes(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	// The author's probe is done and its record is in the journal while the
	// reviewer's probe is open.
	finish := l.bootProbing(lifeCfg{scripts: map[string][]fake.Step{"author": {enginetest.Reply(authorOK)}}}, "reviewer")
	runID := l.Dial(t).Hello().RunID
	if _, found, err := l.Store.LatestProbeRecord("ep", "m-author"); err != nil || !found {
		t.Fatalf("the author's record: found=%v err=%v, want it journaled", found, err)
	}
	if n := len(l.probeFinishes()); n != 1 {
		t.Fatalf("%d probe_finished events while the reviewer's probe is open, want the author's alone", n)
	}
	state, manifest, hash := l.runRow(runID)
	if state != "probing" || manifest != nil || hash != nil {
		t.Fatalf("run row while probing = %q manifest=%v hash=%v, want probing and no manifest", state, manifest != nil, hash != nil)
	}
	finish()

	state, manifest, hash = l.runRow(runID)
	if state != "started" || manifest == nil || hash == nil {
		t.Fatalf("run row after the probes = %q manifest=%v hash=%v, want started with a manifest", state, manifest != nil, hash != nil)
	}
	sum := sha256.Sum256([]byte(*manifest))
	if hex.EncodeToString(sum[:]) != *hash {
		t.Fatalf("manifest_hash %s is not the hash of the stored manifest", *hash)
	}
	// The manifest was written once, after both probes, so it holds both records.
	var written int
	if err := l.DB.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine' AND manifest IS NOT NULL`).Scan(&written); err != nil || written != 1 {
		t.Fatalf("%d engine runs hold a manifest, err %v, want 1", written, err)
	}
	var doc struct {
		ProbeRecords map[string]json.RawMessage `json:"probe_records"`
	}
	if err := json.Unmarshal([]byte(*manifest), &doc); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	for _, id := range []string{"author", "reviewer"} {
		if _, ok := doc.ProbeRecords[id]; !ok {
			t.Fatalf("manifest holds no probe record of %s: %s", id, *manifest)
		}
	}
	if !strings.Contains(*manifest, enginetest.DefaultReportedModel) {
		t.Fatalf("manifest lacks the probes' reported model: %s", *manifest)
	}
	// Nothing rewrites it later in the run.
	l.WaitTerminal(l.SubmitPreset(t, l.Dial(t), "s1", "solo"))
	if _, again, _ := l.runRow(runID); again == nil || *again != *manifest {
		t.Fatal("the run's manifest changed after it was written")
	}
}

func TestStartupProbe_EngineStartedAfterManifest(t *testing.T) {
	t.Parallel()
	// One probe fails, so probe_finished is announced for a failure as well.
	l := newLifecycle(t, lifecycleRoster("flat"))
	l.boot(lifeCfg{fail: map[string]error{"reviewer": transport.ErrUnauthorized}})

	es, startedSeq := l.started()
	_, manifest, hash := l.runRow(es.RunID)
	if manifest == nil || hash == nil {
		t.Fatal("engine_started is announced and the run holds no manifest")
	}
	if es.ManifestHash == "" || es.ManifestHash != *hash {
		t.Fatalf("engine_started.manifest_hash %q, stored manifest_hash %v", es.ManifestHash, *hash)
	}
	if hello := l.Dial(t).Hello(); hello.ManifestHash == nil || *hello.ManifestHash != es.ManifestHash {
		t.Fatalf("hello manifest_hash %v, engine_started %q", hello.ManifestHash, es.ManifestHash)
	}
	finishes := 0
	for _, e := range l.runEvents() {
		if e.Kind == wire.EventKindProbeFinished {
			finishes++
			if e.Seq >= startedSeq {
				t.Fatalf("probe_finished at seq %d is not before engine_started at %d", e.Seq, startedSeq)
			}
		}
	}
	if finishes != 2 {
		t.Fatalf("%d probe_finished events, want one per participant", finishes)
	}
	if f := l.probeFinishes()["reviewer"]; f.Outcome != "probe_failed" || str(f.Detail) != "unauthorized" {
		t.Fatalf("probe_finished of reviewer = %+v, want probe_failed unauthorized", f)
	}
}

func TestStartupProbe_OrdinaryHoldBlocksPool(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	// First start: the author's probe fails after a possible send, which the
	// journal keeps as a hold on its pool; the reviewer's probe is incomplete
	// and leaves no hold and no record.
	noModel := lifecycleCapability()
	noModel.ReportedModel = ""
	l.boot(lifeCfg{
		uncertain:  map[string]string{"author": "system_role"},
		capability: map[string]transport.CapabilityRecord{"reviewer": noModel},
	})
	held, err := l.Store.UnreleasedHolds()
	if err != nil || len(held) != 1 || held[0].Pool != "pa" {
		t.Fatalf("holds after the first start = %+v, err %v, want one on pool pa", held, err)
	}
	attemptsBefore := l.probeAttempts()

	// Restart: the hold is recovered from the journal as an ordinary hold on
	// pool pa, and the author, who has no record, is not probed again.
	l.restartWith(lifeCfg{})
	c := l.Dial(t)
	if n := l.spies["author"].probes(); n != 0 {
		t.Fatalf("the author was probed %d times with an ordinary hold on its pool", n)
	}
	l.wantUnprobed(t, c, "author", "m-author", "probe_failed:hold")
	if f := l.probeFinishes()["author"]; f.Outcome != "probe_failed" || str(f.Detail) != "hold" {
		t.Fatalf("probe_finished of author = %+v, want probe_failed hold", f)
	}
	for _, who := range l.probeStarts() {
		if who == "author" {
			t.Fatal("probe_started was sent for a participant a hold blocks")
		}
	}
	// The reviewer is on another pool: probed and usable.
	if n := l.spies["reviewer"].probes(); n != 1 {
		t.Fatalf("the reviewer was probed %d times, want once: a hold on another pool does not block it", n)
	}
	l.wantProbed(t, c, "reviewer", "m-reviewer")
	if n := l.probeAttempts(); n <= attemptsBefore {
		t.Fatalf("probe attempts %d -> %d, want the reviewer's new attempts", attemptsBefore, n)
	}
	if got := l.holds(t, c, "pa"); len(got) != 1 || got[0].AttemptID != held[0].AttemptID {
		t.Fatalf("pool pa holds %+v after the restart, want the recovered %s", got, held[0].AttemptID)
	}
	if es, _ := l.started(); !reflect.DeepEqual(es.UncertainCapacity, []string{"pa"}) || len(es.OrphanedHolds) != 0 {
		t.Fatalf("engine_started = %+v, want pa uncertain and no orphaned hold", es)
	}

	// Released and restarted, the pool's participant is probed.
	if _, err := c.Release(l.ctx(), wire.ReleaseRequest{Pool: "pa", Reason: "checked"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	l.restartWith(lifeCfg{})
	if n := l.spies["author"].probes(); n != 1 {
		t.Fatalf("the author was probed %d times after the release, want once", n)
	}
	l.wantProbed(t, l.Dial(t), "author", "m-author")
}

func TestStartupProbe_OrphanedHoldBlocksAll(t *testing.T) {
	t.Parallel()
	l := newLifecycle(t, lifecycleRoster("flat"))
	// A hold of a group no current pool contains: no pool can be shown clear.
	attemptID := seedHold(t, l.Store, "old", "gone", "gone-model")

	l.boot(lifeCfg{})
	c := l.Dial(t)
	if n := l.totalProbes(); n != 0 {
		t.Fatalf("%d probes ran while an orphaned hold exists, want none", n)
	}
	if starts := l.probeStarts(); len(starts) != 0 {
		t.Fatalf("probe_started for %v while an orphaned hold exists", starts)
	}
	finishes := l.probeFinishes()
	for id, model := range map[string]string{"author": "m-author", "reviewer": "m-reviewer"} {
		l.wantUnprobed(t, c, id, model, "probe_failed:hold")
		if f := finishes[id]; f.Outcome != "probe_failed" || str(f.Detail) != "hold" {
			t.Fatalf("probe_finished of %s = %+v, want probe_failed hold", id, f)
		}
	}
	if hello := c.Hello(); hello.State != "started" {
		t.Fatalf("hello = %+v, want the engine started", hello)
	}
	st := l.Status(t, c)
	if len(st.OrphanedHolds) != 1 || st.OrphanedHolds[0].AttemptID != attemptID {
		t.Fatalf("status.orphaned_holds = %+v, want the hold %s", st.OrphanedHolds, attemptID)
	}
	if es, _ := l.started(); len(es.OrphanedHolds) != 1 || len(es.Unprobed) != 2 {
		t.Fatalf("engine_started = %+v, want the orphaned hold and both participants unprobed", es)
	}

	// Released and restarted, every probe runs.
	if _, err := c.Release(l.ctx(), wire.ReleaseRequest{Pool: "old", AttemptID: &attemptID, Reason: "the group was removed"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	l.restartWith(lifeCfg{})
	if n := l.totalProbes(); n != 2 {
		t.Fatalf("%d probes ran after the release, want one for each participant", n)
	}
	c = l.Dial(t)
	l.wantProbed(t, c, "author", "m-author")
	l.wantProbed(t, c, "reviewer", "m-reviewer")
}

// TestCoverage_StartTimeProbingClauses requires a registered test for each
// clause of the design's Start-time probing paragraph, so a clause added to
// it, or a registration dropped, fails here.
func TestCoverage_StartTimeProbingClauses(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := moduleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "design", "v5-delta-conversations-revision-tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	const lead = "Start-time probing: "
	var paragraph string
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(line, lead) {
			paragraph = strings.TrimSuffix(strings.TrimPrefix(line, lead), ".")
		}
	}
	if paragraph == "" {
		t.Fatalf("the design has no %q paragraph", lead)
	}
	clauses := strings.Split(paragraph, "; ")
	registered := map[string]bool{}
	for _, e := range registry {
		if e.paragraph == startTimeProbesPara {
			registered[e.test] = true
		}
	}
	if len(registered) != len(clauses) {
		t.Fatalf("%d clauses in the design's paragraph, %d registered tests", len(clauses), len(registered))
	}
	if n := countEntries(startTimeProbesPara); n != len(clauses) {
		t.Fatalf("%d clauses in the design's paragraph, %d registered items", len(clauses), n)
	}
}

func countEntries(paragraph string) int {
	n := 0
	for _, e := range registry {
		if e.paragraph == paragraph {
			n++
		}
	}
	return n
}
