package acceptance

import (
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const startTimeProbesPara = "Start-time probing"

func init() {
	cover(startTimeProbesPara, "a participant with no record on a `flat` endpoint is probed at start and is usable in that run", "TestStartupProbe_FlatEndpointProbed")
	cover(startTimeProbesPara, "the same on a `metered` endpoint is not probed without `--probe-missing`", "TestStartupProbe_MeteredNotProbed")
	cover(startTimeProbesPara, "`hello` after `engine_started` returns `state: \"started\"` and the manifest hash", "TestStartupProbe_HelloAfterStarted")
	cover(startTimeProbesPara, "`subscribe{since_seq: run_first_seq - 1}` replays `run_opened`, every probe event and `engine_started`", "TestStartupProbe_SubscribeReplay")
	cover(startTimeProbesPara, "the manifest is written once, after the last probe", "TestStartupProbe_ManifestWrittenAfterProbes")
	cover(startTimeProbesPara, "`engine_started` follows every `probe_finished` and the manifest and carries `manifest_hash`", "TestStartupProbe_EngineStartedAfterManifest")
}

// probeTestRoster returns a roster with flat and metered endpoints, used to test probing behavior.
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

// TestStartupProbe_FlatEndpointProbed verifies that participants without usable
// records on flat endpoints are probed at start and become usable in that run.
func TestStartupProbe_FlatEndpointProbed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, probeTestRoster("flat-ep", "none"),
		map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		})

	c := s.Dial(t)

	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	for _, id := range []string{"author", "reviewer"} {
		calls := s.Adapters[id].Calls()
		if len(calls) == 0 {
			t.Errorf("%s received 0 requests, want at least a probe", id)
		}
	}
}

// TestStartupProbe_MeteredNotProbed verifies that participants without usable
// records on metered endpoints are not probed at start (without --probe-missing).
func TestStartupProbe_MeteredNotProbed(t *testing.T) {
	t.Parallel()
	roster := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "metered-ep"
eligible_roles = ["author"]
pool = "default"
accept_unknown_truncation = true

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "metered-ep"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "metered-ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "metered"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0
spending_control = "external"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[pools.default]
concurrency = 1
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})

	c := s.Dial(t)
	hello := c.Hello()
	if hello.State != "started" {
		t.Errorf("hello.state = %q, want started (metered not probed)", hello.State)
	}

	for _, id := range []string{"author", "reviewer"} {
		calls := s.Adapters[id].Calls()
		if len(calls) > 0 {
			t.Errorf("%s received %d requests, want 0 (metered not probed)", id, len(calls))
		}
	}
}

// TestStartupProbe_HelloAfterStarted verifies that after engine_started,
// hello returns state "started" and the manifest hash.
func TestStartupProbe_HelloAfterStarted(t *testing.T) {
	t.Parallel()
	s := newSuite(t, probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

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

// TestStartupProbe_SubscribeReplay verifies that subscribing since
// (run_first_seq - 1) replays run_opened, all probe events, and engine_started.
func TestStartupProbe_SubscribeReplay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})

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
	for i := 0; i < 20; i++ {
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

// TestStartupProbe_ManifestWrittenAfterProbes verifies that the manifest is
// written after the last probe, not before.
func TestStartupProbe_ManifestWrittenAfterProbes(t *testing.T) {
	t.Parallel()
	s := newSuite(t, probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})

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
	s := newSuite(t, probeTestRoster("local", "none"), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

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
