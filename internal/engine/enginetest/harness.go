package enginetest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	// DefaultContextBound is the context bound the seeded exports record
	// unless WithContextBound says otherwise.
	DefaultContextBound = 128000
	// DefaultCalibration is the calibrated bytes per token the seeded probe
	// records unless WithCalibration says otherwise. With the default
	// calibration safety of 0.75 the engine estimates at 3.0.
	DefaultCalibration = 4.0

	// DefaultReportedModel is the model name the seeded exports record and
	// the fake adapters report unless a step says otherwise.
	DefaultReportedModel = "enginetest-model"
	// AdapterVersion is the adapter version the harness reports and seeds
	// fingerprints with.
	AdapterVersion = "enginetest/1"
	// EngineVersion is the engine version the harness starts with.
	EngineVersion = "enginetest"

	waitTimeout = 10 * time.Second
)

// RosterTOML is a valid roster with an author, a reviewer, a review default
// preset, a solo preset and a quick profile, and non-provisional deadlines. Its
// round limit is zero, the behaviour before the revision loop: a block or a
// revision request ends the turn. A test of the loop sets a limit of its own.
const RosterTOML = `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[revision]
max_rounds = 0

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[quick.fast]
preset = "solo"
drafts = false
[quick.fast.budgets]
author_max_output_tokens = 2048

[policy]
local_only = true
allowed_destinations = []
`

type options struct {
	skip           map[string]bool
	outputBound    map[string]int
	eligible       map[string]transport.Tri
	toolCalls      map[string]transport.Tri
	modelToolCalls map[[2]string]transport.Tri
	contextBound   map[string]int
	calibration    map[string]float64
	noStart        bool
	unconfirmed    bool
	blockingCancel bool
	clockStart     time.Time
	changeLines    func(transport.CompleteResult) []string
	beforeEffect   func(turnID, attemptID, name string)
	afterWalk      func(i int, name string)
	beforeApply    func(turnID string)
	providers      []tools.SearchProvider
	searchNet      *tools.SearchNetwork
	secrets        config.SecretResolver
	exec           engine.ExecOptions
}

// Option adjusts how New seeds and starts the harness.
type Option func(*options)

// SkipProbe leaves the participant without a probe record.
func SkipProbe(id string) Option {
	return func(o *options) { o.skip[id] = true }
}

// WithOutputBound seeds the participant's export with an output bound.
func WithOutputBound(id string, tokens int) Option {
	return func(o *options) { o.outputBound[id] = tokens }
}

// WithContextBound seeds the participant's export with a context bound. Zero
// seeds none.
func WithContextBound(id string, tokens int) Option {
	return func(o *options) { o.contextBound[id] = tokens }
}

// WithCalibration seeds the participant's calibration with bytes per token.
// Zero seeds a calibration the server could not provide.
func WithCalibration(id string, bytesPerToken float64) Option {
	return func(o *options) { o.calibration[id] = bytesPerToken }
}

// WithEligibleRequiredReviewer seeds the participant's export with the given
// required-reviewer eligibility.
func WithEligibleRequiredReviewer(id string, v transport.Tri) Option {
	return func(o *options) { o.eligible[id] = v }
}

// WithToolCalls seeds the participant's export with the given tool capability.
func WithToolCalls(id string, v transport.Tri) Option {
	return func(o *options) { o.toolCalls[id] = v }
}

// WithModelToolCalls seeds a probe record for a model that is not a roster
// participant (such as an endpoint's referee model) with the given tool
// capability.
func WithModelToolCalls(endpoint, model string, v transport.Tri) Option {
	return func(o *options) { o.modelToolCalls[[2]string{endpoint, model}] = v }
}

// ClockStart sets the fake clock's initial reading. A test that needs a
// scripted Hang to outlast the real wall clock starts it in the far future.
func ClockStart(t time.Time) Option {
	return func(o *options) { o.clockStart = t }
}

// CancelUnconfirmed makes every fake adapter answer Cancel with a server
// confirmation of unknown.
func CancelUnconfirmed() Option {
	return func(o *options) { o.unconfirmed = true }
}

// BlockingCancel makes every fake adapter block on Cancel until released.
func BlockingCancel() Option {
	return func(o *options) { o.blockingCancel = true }
}

// WithAuthorChangeLines gives every author result the change-set lines f
// returns, for tests of unchanged-revision detection.
func WithAuthorChangeLines(f func(transport.CompleteResult) []string) Option {
	return func(o *options) { o.changeLines = f }
}

// WithBeforeToolEffect calls f inside every author tool call's effect, before
// the tool runs, as a wait inside the effect would; the engine re-checks the
// step once f returns. A test of a cancel landing mid-step cancels in f.
func WithBeforeToolEffect(f func(turnID, attemptID, name string)) Option {
	return func(o *options) { o.beforeEffect = f }
}

// WithAfterWalkComponent calls f inside every tool step's confined path walk,
// after a component is opened and before the next openat, so a test can swap
// that next component in flight.
func WithAfterWalkComponent(f func(i int, name string)) Option {
	return func(o *options) { o.afterWalk = f }
}

// WithBeforeApply calls f once an apply is open, before it touches the
// workspace, with the engine unlocked; a test blocks in f to issue requests
// during an apply.
func WithBeforeApply(f func(turnID string)) Option {
	return func(o *options) { o.beforeApply = f }
}

// WithSearchProvider injects a web_search provider, as the daemon would
// through Options.SearchProviders.
func WithSearchProvider(p tools.SearchProvider) Option {
	return func(o *options) { o.providers = append(o.providers, p) }
}

// WithSearchNetwork replaces the resolver, dialer and TLS configuration of
// the search provider connection.
func WithSearchNetwork(n *tools.SearchNetwork) Option {
	return func(o *options) { o.searchNet = n }
}

// WithSecrets replaces the resolver of the search provider credential.
func WithSecrets(r config.SecretResolver) Option {
	return func(o *options) { o.secrets = r }
}

// WithExec sets the engine's command execution seam, as the daemon would
// through Options.Exec.
func WithExec(x engine.ExecOptions) Option {
	return func(o *options) { o.exec = x }
}

// NoStart builds the engine without starting it.
func NoStart() Option {
	return func(o *options) { o.noStart = true }
}

// Harness is a running engine with its journal, adapters, clock and event
// stream.
type Harness struct {
	T          *testing.T
	Engine     *engine.Engine
	Store      *journal.Store
	DB         *journal.DB
	Hub        *hub.Hub
	Clock      *FakeClock
	RosterPath string
	// Adapters are the scripted fakes, by participant id.
	Adapters map[string]*fake.Adapter

	gated map[string]*gatedAdapter
	dead  atomic.Bool

	mu     sync.Mutex
	events []Event
	cancel context.CancelFunc
	done   chan struct{}
}

// Reply is a complete, transport-ok scripted answer.
func Reply(text string) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            text,
		Status:          transport.StatusComplete,
		TransportStatus: transport.TransportStatusOK,
	}
}

// ReplyFunc is a complete, transport-ok scripted answer built from the
// request it answers, for replies that carry the attempt id or candidate hash.
func ReplyFunc(build func(req transport.CompleteRequest) string) fake.Step {
	step := Reply("")
	step.Build = build
	return step
}

// New writes rosterTOML to a temporary file, opens a temporary journal,
// seeds a usable probe record for every participant (except those passed to
// SkipProbe), builds one fake adapter per participant from scripts, starts
// the engine on a fake clock and subscribes to the hub from seq 0. Everything
// is released when the test ends.
func New(t *testing.T, rosterTOML string, scripts map[string][]fake.Step, opts ...Option) *Harness {
	t.Helper()
	o := &options{
		skip:           map[string]bool{},
		outputBound:    map[string]int{},
		eligible:       map[string]transport.Tri{},
		toolCalls:      map[string]transport.Tri{},
		modelToolCalls: map[[2]string]transport.Tri{},
		contextBound:   map[string]int{},
		calibration:    map[string]float64{},
		clockStart:     time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC),
	}
	for _, opt := range opts {
		opt(o)
	}

	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	if err := os.WriteFile(rosterPath, []byte(rosterTOML), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	roster, err := config.Load(rosterPath)
	if err != nil {
		t.Fatalf("load roster: %v", err)
	}

	db, err := journal.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	store := journal.NewStore(db)
	h := hub.New(store, 0)

	seedProbeRecords(t, store, roster, o)

	hn := &Harness{
		T:          t,
		Store:      store,
		DB:         db,
		Hub:        h,
		Clock:      NewFakeClock(o.clockStart),
		RosterPath: rosterPath,
		Adapters:   map[string]*fake.Adapter{},
		gated:      map[string]*gatedAdapter{},
		done:       make(chan struct{}),
	}
	for _, p := range roster.Participants {
		fa := fake.New(scripts[p.ID], fake.Options{DefaultReportedModel: DefaultReportedModel, CancelUnconfirmed: o.unconfirmed, BlockingCancel: o.blockingCancel})
		hn.Adapters[p.ID] = fa
		hn.gated[p.ID] = &gatedAdapter{inner: fa, dead: &hn.dead}
	}

	eng, err := engine.New(engine.Options{
		Store:         store,
		Hub:           h,
		RosterPath:    rosterPath,
		EngineVersion: EngineVersion,
		Clock:         crashClock{inner: hn.Clock, dead: &hn.dead},
		// The harness seeds the records it wants; start-time probing would
		// replace the states SkipProbe sets up.
		NoStartupProbes:    true,
		AuthorChangeLines:  o.changeLines,
		BeforeToolEffect:   o.beforeEffect,
		AfterWalkComponent: o.afterWalk,
		BeforeApply:        o.beforeApply,
		SearchProviders:    o.providers,
		SearchNetwork:      o.searchNet,
		Secrets:            o.secrets,
		Exec:               o.exec,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			a, ok := hn.gated[p.ID]
			if !ok {
				return nil, "", errors.New("enginetest: no adapter for participant " + p.ID)
			}
			return a, AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	hn.Engine = eng

	if !o.noStart {
		if err := eng.Start(); err != nil {
			t.Fatalf("engine.Start: %v", err)
		}
	}
	hn.subscribe()
	t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			t.Errorf("engine.Stop: %v", err)
		}
	})
	return hn
}

func seedProbeRecords(t *testing.T, store *journal.Store, roster *config.Roster, o *options) {
	t.Helper()
	seeded := map[[2]string]bool{}
	type target struct{ id, endpoint, model string }
	var targets []target
	for _, p := range roster.Participants {
		if o.skip[p.ID] {
			continue
		}
		targets = append(targets, target{p.ID, p.Endpoint, p.Model})
	}
	for key := range o.modelToolCalls {
		targets = append(targets, target{"", key[0], key[1]})
	}
	for _, p := range targets {
		key := [2]string{p.endpoint, p.model}
		if seeded[key] {
			continue
		}
		seeded[key] = true

		var profile config.EndpointProfile
		for _, ep := range roster.Endpoints {
			if ep.Name == p.endpoint {
				profile = ep
			}
		}
		export := evidence.Export{
			Redacted:                 true,
			RedactionRule:            "redaction/v1",
			EndpointName:             profile.Name,
			DeclaredLocation:         profile.DeclaredLocation,
			DeclaredExecution:        profile.DeclaredExecution,
			DeclaredBilling:          profile.DeclaredBilling,
			NetworkDependency:        profile.NetworkDependency,
			Model:                    p.model,
			AuthMode:                 "none",
			Identity:                 transport.NewUnknownIdentity(),
			ReportedModel:            DefaultReportedModel,
			Streaming:                transport.TriUnknown,
			CancelObservable:         transport.TriUnknown,
			UsageAvailable:           transport.TriUnknown,
			ModelsEndpoint:           transport.TriUnknown,
			ModelListed:              transport.TriUnknown,
			SystemRoleHonored:        transport.TriUnknown,
			EligibleRequiredReviewer: transport.TriUnknown,
			ToolCalls:                transport.TriYes,
			AdapterVersion:           AdapterVersion,
			ObservedAt:               o.clockStart,
		}
		bound := DefaultContextBound
		if v, ok := o.contextBound[p.id]; ok {
			bound = v
		}
		if bound > 0 {
			export.ContextBound = &bound
		}
		if v, ok := o.outputBound[p.id]; ok {
			export.OutputBound = &v
		}
		if v, ok := o.eligible[p.id]; ok {
			export.EligibleRequiredReviewer = v
		}
		if v, ok := o.toolCalls[p.id]; ok {
			export.ToolCalls = v
		}
		if v, ok := o.modelToolCalls[key]; ok {
			export.ToolCalls = v
		}
		bytesPerToken := DefaultCalibration
		if v, ok := o.calibration[p.id]; ok {
			bytesPerToken = v
		}
		calibration := transport.Calibration{}
		if bytesPerToken > 0 {
			calibration = transport.Calibration{Available: true, BytesPerToken: bytesPerToken}
		}
		pm, err := evidence.NewProbeManifest(export, profile, p.model, AdapterVersion, calibration)
		if err != nil {
			t.Fatalf("probe manifest for %s: %v", p.id, err)
		}
		data, err := json.Marshal(pm)
		if err != nil {
			t.Fatalf("marshal probe manifest: %v", err)
		}
		if _, _, err := store.CreateProbeRun(data); err != nil {
			t.Fatalf("seed probe record for %s: %v", p.id, err)
		}
	}
}

func (h *Harness) subscribe() {
	sub, _, err := h.Hub.Subscribe(0)
	if err != nil {
		h.T.Fatalf("subscribe: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		for {
			ev, err := sub.Next(ctx)
			if err != nil {
				return
			}
			h.mu.Lock()
			h.events = append(h.events, decode(ev))
			h.mu.Unlock()
		}
	}()
	h.T.Cleanup(func() {
		h.cancel()
		<-h.done
		sub.Close()
	})
}

// Events returns every event committed so far, decoded, in journal order. It
// waits for the subscription to catch up with the journal first.
func (h *Harness) Events() []Event {
	h.T.Helper()
	target, err := h.Store.MaxSeq()
	if err != nil {
		h.T.Fatalf("MaxSeq: %v", err)
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		h.mu.Lock()
		caughtUp := len(h.events) > 0 && h.events[len(h.events)-1].Seq >= target
		h.mu.Unlock()
		if caughtUp || target == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.events...)
}

// TurnStates returns the turn_state states seen so far for the turn.
func (h *Harness) TurnStates(turnID string) []string {
	var states []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvTurnState); ok && p.TurnID == turnID {
			states = append(states, p.State)
		}
	}
	return states
}

// WaitTurnState blocks until a turn_state event with the state has been seen
// for the turn, and fails the test if none arrives.
func (h *Harness) WaitTurnState(turnID, state string) {
	h.T.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		for _, s := range h.TurnStates(turnID) {
			if s == state {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.T.Fatalf("turn %s never reached state %q; saw %v", turnID, state, h.TurnStates(turnID))
}

// Hold makes the participant's next Complete call block until the returned
// function is called, so a turn can be observed while open.
func (h *Harness) Hold(participantID string) (release func()) {
	g, ok := h.gated[participantID]
	if !ok {
		h.T.Fatalf("no adapter for participant %q", participantID)
	}
	release = g.hold()
	h.T.Cleanup(release)
	return release
}

// ReleaseBlockingCancel releases blocking Cancel calls on all adapters.
// Only call this if BlockingCancel() was used.
func (h *Harness) ReleaseBlockingCancel() {
	for _, a := range h.Adapters {
		a.ReleaseBlockingCancel()
	}
}

// Crash makes the engine inert as a killed process is: its timers never fire
// and its adapters never answer again, so it writes nothing more to the
// journal. Nothing is stopped and nothing is journalled; the engine's own
// context still ends at cleanup. Use it before starting a second engine on the
// same journal.
func (h *Harness) Crash() {
	h.dead.Store(true)
}
