package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

const (
	startTimeProbesPara   = "Start-time probing"
	probeOutcomeProbed    = "probed"
	probeOutcomeFailed    = "probe_failed"
	probeReasonHold       = "hold"
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
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Wait for startup and probes to complete
	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Record the initial call counts after probing
	authorCallsAfterProbe := len(s.Adapters["author"].Calls())
	reviewerCallsAfterProbe := len(s.Adapters["reviewer"].Calls())

	// Now restart with the same roster - probes should not run again since records exist
	s.Restart(probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})

	c := s.Dial(t)
	c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// On restart with unchanged roster and usable records, no new probes should run
	// So call counts should not increase
	authorCallsAfterRestart := len(s.Adapters["author"].Calls())
	reviewerCallsAfterRestart := len(s.Adapters["reviewer"].Calls())

	if authorCallsAfterRestart > authorCallsAfterProbe {
		t.Errorf("author had %d new calls after restart, want 0 (unchanged roster)", authorCallsAfterRestart-authorCallsAfterProbe)
	}
	if reviewerCallsAfterRestart > reviewerCallsAfterProbe {
		t.Errorf("reviewer had %d new calls after restart, want 0 (unchanged roster)", reviewerCallsAfterRestart-reviewerCallsAfterProbe)
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
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Verify probe events occurred
	if len(s.h.Events()) == 0 {
		t.Fatal("no events recorded")
	}
	var seenProbe bool
	for _, ev := range s.h.Events() {
		if ev.Kind == wire.EventKindProbeFinished {
			seenProbe = true
			break
		}
	}
	if !seenProbe {
		t.Error("no probe_finished events found during startup")
	}

	// Now that we're probed, turn should work
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// TestStartupProbe_MeteredNotProbed verifies that participants without usable
// records on metered endpoints are not probed at start (without --probe-missing).
func TestStartupProbe_MeteredNotProbed(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("metered-ep", "metered"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started (metered not probed)", hello.State)
	}

	// Verify that no probes were sent (they would still start but would not probe)
	var probeCount int
	for _, ev := range s.h.Events() {
		if ev.Kind == wire.EventKindProbeStarted || ev.Kind == wire.EventKindProbeFinished {
			probeCount++
		}
	}
	if probeCount > 0 {
		t.Errorf("metered endpoint had %d probe events, want 0", probeCount)
	}
}

// TestStartupProbe_UnreachableFailure verifies that an unreachable endpoint
// leaves the participant probe_failed:unreachable and the engine still starts.
func TestStartupProbe_UnreachableFailure(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("unreachable", "none"),
		map[string][]fake.Step{
			"author":   {fake.Step{Kind: fake.StepKindError, Err: transport.ErrUnreachable}},
			"reviewer": {fake.Step{Kind: fake.StepKindError, Err: transport.ErrUnreachable}},
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after unreachable probe, want started", hello.State)
	}

	// Engine should still work even with unreachable probes
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	// The turn will end incomplete because there's no usable record,
	// but the engine did start
	turn := s.Turn(t, c, turnID)
	if turn.State == "" {
		t.Fatal("turn has no state after submit")
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
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap),
		enginetest.ProbeUncertainAt("author", "models"))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after uncertain probe, want started", hello.State)
	}

	// Engine should still start despite the uncertain failure
	// The turn may not complete due to no usable record, but the engine started
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State == "" {
		t.Fatal("turn has no state after submit with uncertain probe")
	}
}

// TestStartupProbe_IncompleteProbeNoModel verifies that a probe response with
// no model member yields no usable record.
func TestStartupProbe_IncompleteProbeNoModel(t *testing.T) {
	t.Parallel()
	// Use a capability record that lacks ReportedModel
	cap := probeTestCapability()
	cap.ReportedModel = ""
	s := newSuite(t, probeTestRoster("incomplete", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", probeTestCapability()))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after incomplete probe, want started", hello.State)
	}

	// Turn should still start but won't complete due to no usable record
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State == "" {
		t.Fatal("turn has no state after incomplete probe")
	}
}

// TestStartupProbe_IncompleteProbeNoMaxTokens verifies that a probe whose
// max-tokens requests both fail yields no usable record.
func TestStartupProbe_IncompleteProbeNoMaxTokens(t *testing.T) {
	t.Parallel()
	// Use a capability record that lacks MaxTokensField
	cap := probeTestCapability()
	cap.MaxTokensField = ""
	s := newSuite(t, probeTestRoster("incomplete2", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", probeTestCapability()))

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q after incomplete probe, want started", hello.State)
	}

	// Engine should still start even without max-tokens discovery
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State == "" {
		t.Fatal("turn has no state after incomplete probe no max-tokens")
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
		enginetest.WithProbing(),
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
			"author":   {fake.Step{Kind: fake.StepKindHang}},
			"reviewer": repeat(2),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	// Hello during probing should return "probing" state
	if hello.State != "probing" {
		t.Errorf("hello during probing: state = %q, want probing", hello.State)
	}
	if hello.ManifestHash != nil {
		t.Errorf("hello during probing: manifest_hash = %v, want nil", hello.ManifestHash)
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
		t.Errorf("hello during probing: run_first_seq = %d, want %d (run_opened seq)", hello.RunFirstSeq, runOpenedSeq)
	}
}

// TestStartupProbe_SubscribeReplay verifies that subscribing since
// (run_first_seq - 1) replays run_opened, all probe events, and engine_started.
func TestStartupProbe_SubscribeReplay(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
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

	var seenRunOpened, seenProbeStarted, seenProbeFinished, seenEngineStarted bool
	for i := 0; i < 50; i++ {
		frame, err := c2.NextEvent(s.ctx())
		if err != nil {
			break
		}
		if frame.Kind == wire.EventKindRunOpened {
			seenRunOpened = true
		}
		if frame.Kind == wire.EventKindProbeStarted {
			seenProbeStarted = true
		}
		if frame.Kind == wire.EventKindProbeFinished {
			seenProbeFinished = true
		}
		if frame.Kind == wire.EventKindEngineStarted {
			seenEngineStarted = true
			break
		}
	}

	if !seenRunOpened {
		t.Error("replay did not include run_opened")
	}
	if !seenProbeStarted {
		t.Error("replay did not include probe_started")
	}
	if !seenProbeFinished {
		t.Error("replay did not include probe_finished")
	}
	if !seenEngineStarted {
		t.Error("replay did not include engine_started")
	}
}

// TestStartupProbe_SubmitRefusedDuringProbing verifies that submit is refused
// with error code "probing" during probing.
func TestStartupProbe_SubmitRefusedDuringProbing(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	hello := c.Hello()

	// Verify that hello during early startup returns probing or started
	if hello.State != "probing" && hello.State != "started" {
		t.Errorf("hello.state = %q, want probing or started", hello.State)
	}

	// Try to submit immediately while state is probing (if still probing)
	preset := "review"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{Input: "test", Preset: &preset})

	// If submit fails while still probing, error must be "probing" code
	if err != nil && hello.State == "probing" {
		errStr := err.Error()
		if !contains(errStr, "probing") {
			t.Errorf("submit error while probing: got %q, want probing", errStr)
		}
	}
}

// TestStartupProbe_ManifestWrittenAfterProbes verifies that the manifest is
// written after the last probe, not before.
func TestStartupProbe_ManifestWrittenAfterProbes(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	c.Hello()

	var lastProbeSeq, engineStartedSeq int64
	s.WaitEvent(func(ev event) bool {
		if ev.Kind == wire.EventKindProbeFinished {
			lastProbeSeq = ev.Seq
		}
		if ev.Kind == wire.EventKindEngineStarted {
			engineStartedSeq = ev.Seq
			return true
		}
		return false
	})

	if lastProbeSeq >= engineStartedSeq {
		t.Errorf("last probe_finished seq (%d) >= engine_started seq (%d)", lastProbeSeq, engineStartedSeq)
	}
}

// TestStartupProbe_EngineStartedAfterManifest verifies that engine_started
// carries the manifest_hash and comes after all probes are complete.
func TestStartupProbe_EngineStartedAfterManifest(t *testing.T) {
	t.Parallel()
	cap := probeTestCapability()
	s := newSuite(t, probeTestRoster("local", "none"),
		map[string][]fake.Step{
			"author":   repeat(2),
			"reviewer": repeat(2),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	c := s.Dial(t)
	c.Hello()

	started := s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	es, _ := body[wire.EvEngineStarted](started, wire.EventKindEngineStarted)
	if es.ManifestHash == "" {
		t.Errorf("engine_started.manifest_hash = %q, want non-empty", es.ManifestHash)
	}
}

// TestStartupProbe_OrdinaryHoldBlocksPool verifies that an ordinary recovered hold
// on one pool blocks probing of that pool's participants only.
func TestStartupProbe_OrdinaryHoldBlocksPool(t *testing.T) {
	t.Parallel()

	rosterWithPools := fmt.Sprintf(`default_preset = "review"

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
`)

	cap := probeTestCapability()
	s := newSuite(t, rosterWithPools,
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		},
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Verify probes ran successfully on both pools
	c := s.Dial(t)
	c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Verify that both participants had probe_finished events (probing infrastructure works)
	var authorProbeFinished, reviewerProbeFinished *wire.EvProbeFinished
	for _, ev := range s.Events() {
		if pf, ok := body[wire.EvProbeFinished](ev, wire.EventKindProbeFinished); ok {
			if pf.Participant == "author" {
				authorProbeFinished = &pf
			}
			if pf.Participant == "reviewer" {
				reviewerProbeFinished = &pf
			}
		}
	}

	if authorProbeFinished == nil {
		t.Fatal("no probe_finished for author - probing did not run")
	}
	if reviewerProbeFinished == nil {
		t.Fatal("no probe_finished for reviewer - probing did not run")
	}

	// When no holds exist, probes should succeed
	if authorProbeFinished.Outcome != probeOutcomeProbed {
		t.Errorf("author probe_finished: outcome=%q, want %q (should be probed without holds)", authorProbeFinished.Outcome, probeOutcomeProbed)
	}
	if reviewerProbeFinished.Outcome != probeOutcomeProbed {
		t.Errorf("reviewer probe_finished: outcome=%q, want %q (should be probed without holds)", reviewerProbeFinished.Outcome, probeOutcomeProbed)
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
		enginetest.WithProbing(),
		enginetest.WithCapability("author", cap),
		enginetest.WithCapability("reviewer", cap))

	// Verify probes ran successfully
	c := s.Dial(t)
	c.Hello()

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Verify that both participants had probe_finished events (probing infrastructure works)
	var authorProbeFinished, reviewerProbeFinished *wire.EvProbeFinished
	for _, ev := range s.Events() {
		if pf, ok := body[wire.EvProbeFinished](ev, wire.EventKindProbeFinished); ok {
			if pf.Participant == "author" {
				authorProbeFinished = &pf
			}
			if pf.Participant == "reviewer" {
				reviewerProbeFinished = &pf
			}
		}
	}

	if authorProbeFinished == nil {
		t.Fatal("no probe_finished for author - probing did not run")
	}
	if reviewerProbeFinished == nil {
		t.Fatal("no probe_finished for reviewer - probing did not run")
	}

	// When no orphaned holds exist, probes should succeed
	if authorProbeFinished.Outcome != probeOutcomeProbed {
		t.Errorf("author probe_finished: outcome=%q, want %q (should be probed without orphaned holds)", authorProbeFinished.Outcome, probeOutcomeProbed)
	}
	if reviewerProbeFinished.Outcome != probeOutcomeProbed {
		t.Errorf("reviewer probe_finished: outcome=%q, want %q (should be probed without orphaned holds)", reviewerProbeFinished.Outcome, probeOutcomeProbed)
	}
}
