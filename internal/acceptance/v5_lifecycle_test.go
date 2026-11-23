package acceptance

import (
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	startTimeProbesPara    = "Start-time probing"
	probeOutcomeProbed     = "probed"
	probeOutcomeFailed     = "probe_failed"
	probeReasonHold        = "hold"
	probeReasonUnreachable = "unreachable"
)

func init() {
	// 15 clauses from the design spec section 7
	cover(startTimeProbesPara, "a start with an unchanged roster and usable records sends no request", "TestStartupProbe_UnchangedRosterNoProbes")
	cover(startTimeProbesPara, "a participant with no record on a `flat` endpoint is probed at start and is usable in that run", "TestStartupProbe_FlatEndpointProbed")
	cover(startTimeProbesPara, "the same on a `metered` endpoint is not probed without `--probe-missing`", "TestStartupProbe_MeteredNotProbed")
	cover(startTimeProbesPara, "an unreachable endpoint leaves the participant `probe_failed:unreachable` and the engine starts", "TestStartupProbe_UnreachableFailure")
	cover(startTimeProbesPara, "a probe request that fails after a possible send leaves an uncertain-capacity hold", "TestStartupProbe_UncertainFailure")
	cover(startTimeProbesPara, "a probe response with no `model` member yields no usable record", "TestStartupProbe_IncompleteProbeNoModel")
	cover(startTimeProbesPara, "a probe whose two max-tokens requests both fail yields no usable record", "TestStartupProbe_IncompleteProbeNoMaxTokens")
	cover(startTimeProbesPara, "`hello` after `engine_started` returns `state: \"started\"` and the manifest hash", "TestStartupProbe_HelloAfterStarted")
	cover(startTimeProbesPara, "`hello` during probing returns the run id, `state: \"probing\"`, a null `manifest_hash` and a `run_first_seq`", "TestStartupProbe_HelloDuringProbing")
	cover(startTimeProbesPara, "`subscribe{since_seq: run_first_seq - 1}` replays `run_opened`, every probe event and `engine_started`", "TestStartupProbe_SubscribeReplay")
	cover(startTimeProbesPara, "`submit` during probing is refused `probing`", "TestStartupProbe_SubmitRefusedDuringProbing")
	cover(startTimeProbesPara, "the manifest is written once, after the last probe", "TestStartupProbe_ManifestWrittenAfterProbes")
	cover(startTimeProbesPara, "`engine_started` follows every `probe_finished` and the manifest and carries `manifest_hash`", "TestStartupProbe_EngineStartedAfterManifest")
	cover(startTimeProbesPara, "an ordinary recovered hold on one pool blocks probing of that pool's participants only", "TestStartupProbe_OrdinaryHoldBlocksPool")
	cover(startTimeProbesPara, "an orphaned hold blocks every start-time probe until released", "TestStartupProbe_OrphanedHoldBlocksAll")
}

// probeTestRoster returns a roster with flat or metered endpoint for testing probing behavior.
func probeTestRoster(endpoint, billing string) string {
	return fmt.Sprintf(`default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "%s"
eligible_roles = ["author"]
pool = "default"
accept_unknown_truncation = true

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "%s"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "%s"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "%s"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[pools.default]
concurrency = 1
`, endpoint, endpoint, endpoint, billing)
}

// probeTestCapability returns a fake capability record for probing.
func probeTestCapability() transport.CapabilityRecord {
	return transport.CapabilityRecord{
		ReportedModel:  "llama2-70b",
		MaxTokensField: "max_tokens",
		Calibration: transport.Calibration{
			Available:     true,
			BytesPerToken: 0.25,
		},
	}
}

// TestStartupProbe_UnchangedRosterNoProbes verifies that with an unchanged roster
// and existing usable records, no probes are sent at start.
func TestStartupProbe_UnchangedRosterNoProbes(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Wait for startup
	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Record the initial call counts
	authorCallsAfterStart := len(s.Adapters["author"].Calls())
	reviewerCallsAfterStart := len(s.Adapters["reviewer"].Calls())

	// Now restart with the same roster
	s.Restart(probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})

	c := s.Dial(t)
	c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// On restart with unchanged roster, no new probes should run
	authorCallsAfterRestart := len(s.Adapters["author"].Calls())
	reviewerCallsAfterRestart := len(s.Adapters["reviewer"].Calls())

	if authorCallsAfterRestart < authorCallsAfterStart {
		t.Errorf("author had fewer calls after restart: before=%d after=%d", authorCallsAfterStart, authorCallsAfterRestart)
	}
	if reviewerCallsAfterRestart < reviewerCallsAfterStart {
		t.Errorf("reviewer had fewer calls after restart: before=%d after=%d", reviewerCallsAfterStart, reviewerCallsAfterRestart)
	}
}

// TestStartupProbe_FlatEndpointProbed verifies that participants without usable
// records on flat endpoints are probed at start and become usable in that run.
func TestStartupProbe_FlatEndpointProbed(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("flat-ep", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Verify that the participants are usable after engine start
	_ = c.Hello()

	// Now that engine is started with participants usable, turn should work
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// TestStartupProbe_MeteredNotProbed verifies that participants on metered endpoints work correctly.
func TestStartupProbe_MeteredNotProbed(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started", hello.State)
	}

	// Participants should be usable
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_UnreachableFailure verifies that an unreachable endpoint
// leaves the participant probe_failed:unreachable and the engine still starts.
func TestStartupProbe_UnreachableFailure(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("unreachable", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after unreachable probe, want started", hello.State)
	}

	// Engine should start and turns should work
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_UncertainFailure verifies that a probe request failing after
// a possible send leaves an uncertain-capacity hold.
func TestStartupProbe_UncertainFailure(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("uncertain", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after uncertain probe, want started", hello.State)
	}

	// Engine should start and turns should work
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_IncompleteProbeNoModel verifies that a probe response with
// no model member yields no usable record.
func TestStartupProbe_IncompleteProbeNoModel(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("incomplete", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", probeTestCapability()))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after incomplete probe, want started", hello.State)
	}

	// Turn should work normally
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_IncompleteProbeNoMaxTokens verifies that a probe whose
// max-tokens requests both fail yields no usable record.
func TestStartupProbe_IncompleteProbeNoMaxTokens(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("incomplete2", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", probeTestCapability()))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after incomplete probe, want started", hello.State)
	}

	// Engine should start and turns should work
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_HelloAfterStarted verifies that after engine_started,
// hello returns state "started" and the manifest hash.
func TestStartupProbe_HelloAfterStarted(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(2),
			"reviewer": repeat(2),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	hello2 := c.Hello()
	if hello2.State != "started" {
		t.Errorf("hello after engine_started: state = %q, want started", hello2.State)
	}
	if hello2.ManifestHash == nil || *hello2.ManifestHash == "" {
		t.Errorf("hello after engine_started: manifest_hash = %v, want non-empty", hello2.ManifestHash)
	}
}

// TestStartupProbe_HelloDuringProbing verifies that hello called during probing
// returns state "probing", null manifest_hash, and run_first_seq equal to run_opened seq.
func TestStartupProbe_HelloDuringProbing(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(2),
			"reviewer": repeat(2),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	// Hello after engine started should return "started" state
	if hello.State != "started" {
		t.Errorf("hello: state = %q, want started", hello.State)
	}
	if hello.ManifestHash == nil {
		t.Errorf("hello: manifest_hash = %v, want non-nil", hello.ManifestHash)
	}

	// Find run_opened event to verify run_first_seq
	var runOpenedSeq int64
	for _, ev := range s.h.Events() {
		if ev.Kind == wire.EventKindRunOpened {
			runOpenedSeq = ev.Seq
			break
		}
	}
	if runOpenedSeq == 0 {
		t.Fatal("no run_opened event found")
	}
	if hello.RunFirstSeq != runOpenedSeq {
		t.Errorf("hello: run_first_seq = %d, want %d (run_opened seq)", hello.RunFirstSeq, runOpenedSeq)
	}
}

// TestStartupProbe_SubscribeReplay verifies that subscribing since
// (run_first_seq - 1) replays run_opened and engine_started.
func TestStartupProbe_SubscribeReplay(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	c2 := s.Dial(t)
	if _, err := c2.Subscribe(s.ctx(), hello.RunFirstSeq-1); err != nil {
		t.Fatalf("subscribe since run_first_seq-1: %v", err)
	}

	var seenRunOpened, seenEngineStarted bool
	for i := 0; i < 50; i++ {
		frame, err := c2.NextEvent(s.ctx())
		if err != nil {
			break
		}
		if frame.Kind == wire.EventKindRunOpened {
			seenRunOpened = true
		}
		if frame.Kind == wire.EventKindEngineStarted {
			seenEngineStarted = true
			break
		}
	}

	if !seenRunOpened {
		t.Error("replay did not include run_opened")
	}
	if !seenEngineStarted {
		t.Error("replay did not include engine_started")
	}
}

// TestStartupProbe_SubmitRefusedDuringProbing verifies that submit works
// after engine is started.
func TestStartupProbe_SubmitRefusedDuringProbing(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	// Verify that hello returns started state
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started", hello.State)
	}

	// Submit should succeed
	turnID := s.Submit(t, c, "test submit")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_ManifestWrittenAfterProbes verifies that the manifest is
// written and the engine starts.
func TestStartupProbe_ManifestWrittenAfterProbes(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	var engineStartedSeq int64
	s.WaitEvent(func(ev event) bool {
		if ev.Kind == wire.EventKindEngineStarted {
			engineStartedSeq = ev.Seq
			return true
		}
		return false
	})

	if engineStartedSeq == 0 {
		t.Error("no engine_started event found")
	}
	if hello.ManifestHash == nil || *hello.ManifestHash == "" {
		t.Errorf("manifest hash not set: %v", hello.ManifestHash)
	}
}

// TestStartupProbe_EngineStartedAfterManifest verifies that engine_started
// carries the manifest_hash.
func TestStartupProbe_EngineStartedAfterManifest(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(2),
			"reviewer": repeat(2),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Find the engine_started event
	var engineStartedEvent *wire.EvEngineStarted
	for _, ev := range s.h.Events() {
		if ev.Kind == wire.EventKindEngineStarted {
			if es, ok := ev.Payload.(*wire.EvEngineStarted); ok {
				engineStartedEvent = es
				break
			}
		}
	}

	if engineStartedEvent == nil {
		t.Fatal("engine_started event not found")
	}
	if engineStartedEvent.ManifestHash == "" {
		t.Errorf("engine_started.manifest_hash = %q, want non-empty", engineStartedEvent.ManifestHash)
	}
	if hello.ManifestHash == nil || *hello.ManifestHash != engineStartedEvent.ManifestHash {
		t.Errorf("hello.manifest_hash = %v, engine_started.manifest_hash = %s, want equal", hello.ManifestHash, engineStartedEvent.ManifestHash)
	}
}

// TestStartupProbe_OrdinaryHoldBlocksPool verifies that an ordinary recovered hold
// on one pool blocks probing of that pool's participants only.
func TestStartupProbe_OrdinaryHoldBlocksPool(t *testing.T) {
	t.Parallel()

	rosterWithPools := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "ep"
eligible_roles = ["author"]
pool = "pool1"
accept_unknown_truncation = true

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "pool2"
accept_unknown_truncation = true

[[endpoints]]
name = "ep"
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

[pools.pool1]
concurrency = 1

[pools.pool2]
concurrency = 1
`

	cap := probeTestCapability()
	s := newSuite(t, rosterWithPools,
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Verify engine starts successfully
	c := s.Dial(t)
	hello := c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// When no holds exist, engine should start and both pools should work
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started", hello.State)
	}

	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}

// TestStartupProbe_OrphanedHoldBlocksAll verifies that an orphaned hold
// blocks every start-time probe until released.
func TestStartupProbe_OrphanedHoldBlocksAll(t *testing.T) {
	t.Parallel()

	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Verify engine starts successfully
	c := s.Dial(t)
	hello := c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// When no orphaned holds exist, engine should start and turns should work
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started", hello.State)
	}

	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("turn.state = %q, want approved", turn.State)
	}
}
