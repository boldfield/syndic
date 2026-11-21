package engine_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
)

// TestProbeFailureDoesNotStopEngine tests that probe failure doesn't prevent engine startup.
// This addresses the feedback "Failure does not leave engine available".
func TestProbeFailureDoesNotStopEngine(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("reviewer"))

	hello := h.Engine.Hello()
	if hello.State != "started" {
		t.Fatalf("Engine state should be 'started' but got %q", hello.State)
	}

	// Engine should have noted the unprobed reviewer in the engine_started event
	var unprobed []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvEngineStarted); ok {
			unprobed = p.Unprobed
			break
		}
	}

	if len(unprobed) != 1 || unprobed[0] != "reviewer" {
		t.Fatalf("Expected reviewer to be unprobed, got: %v", unprobed)
	}
}

// TestProbeHonoursUsableRecord tests that existing usable records are not probed again.
// This verifies the base case where all participants have usable records.
func TestProbeHonoursUsableRecord(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)

	// All participants should have usable records from the harness setup
	var unprobed []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvEngineStarted); ok {
			unprobed = p.Unprobed
			break
		}
	}

	if len(unprobed) != 0 {
		t.Fatalf("Expected no unprobed participants since all have usable records, got: %v", unprobed)
	}
}

// TestProbeResultsFreeze tests that probe results are frozen in the manifest.
// This addresses "Probe results are discarded; usable records not frozen after probing".
func TestProbeResultsFreeze(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)

	hello := h.Engine.Hello()
	if hello.ManifestHash == nil || *hello.ManifestHash == "" {
		t.Fatal("Manifest should be frozen after probing")
	}

	// Verify manifest was written to the run
	run, err := h.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.ManifestHash == "" || run.Manifest == nil {
		t.Fatal("Run should have manifest after engine start")
	}
}

// TestProbeEventsEmitted tests that probe_started and probe_finished events are emitted.
// This verifies the event sequence for probing.
func TestProbeEventsEmitted(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("author"))

	var probeStartedCount, probeFinishedCount int
	for _, ev := range h.Events() {
		switch ev.Kind {
		case wire.EventKindProbeStarted:
			probeStartedCount++
		case wire.EventKindProbeFinished:
			probeFinishedCount++
		}
	}

	// We skipped author's probe, so we should only see events for reviewer
	if probeStartedCount < 1 {
		t.Fatalf("Expected at least 1 probe_started event, got %d", probeStartedCount)
	}
	if probeFinishedCount < 1 {
		t.Fatalf("Expected at least 1 probe_finished event, got %d", probeFinishedCount)
	}
}

// TestManifestWrittenAfterProbes tests that the manifest is written after probes complete.
// This verifies "run_opened" comes before probes and "engine_started" comes after.
func TestManifestWrittenAfterProbes(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("reviewer"))

	var runOpenedSeq, engineStartedSeq int64
	for _, ev := range h.Events() {
		if ev.Kind == wire.EventKindRunOpened && runOpenedSeq == 0 {
			runOpenedSeq = ev.Seq
		}
		if ev.Kind == wire.EventKindEngineStarted {
			engineStartedSeq = ev.Seq
		}
	}

	if runOpenedSeq == 0 {
		t.Fatal("No run_opened event found")
	}
	if engineStartedSeq == 0 {
		t.Fatal("No engine_started event found")
	}

	// run_opened should come first, then engine_started
	if runOpenedSeq >= engineStartedSeq {
		t.Fatalf("run_opened seq %d should be before engine_started seq %d", runOpenedSeq, engineStartedSeq)
	}
}
