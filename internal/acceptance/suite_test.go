package acceptance

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// TestSuite_RestartAfterCrash proves the harness's Restart does what a crash
// does: the old engine is never stopped, so the journal holds no
// engine_stopping, and the new engine on the same journal finds what was left
// (a capacity hold) and serves new submits with its own adapters.
func TestSuite_RestartAfterCrash(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, failStep(transport.TransportStatusReset, transport.StatusInterrupted))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	held := s.reviewAttempt(t, turnID)
	s.wantHold(t, c, held.ID)
	oldAdapters := s.Adapters
	oldReviewCalls := len(s.calls("reviewer", "review"))
	oldEngine := s.Engine

	s.Restart(enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(4),
		"reviewer": repeat(4),
	})
	if s.Engine == oldEngine || s.Adapters["reviewer"] == oldAdapters["reviewer"] {
		t.Fatal("Restart kept the old engine or its adapters")
	}

	c2 := s.Dial(t)
	hello := c2.Hello()
	started := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq == hello.RunFirstSeq })
	es, _ := body[wire.EvEngineStarted](started, wire.EventKindEngineStarted)
	if es.RunID != hello.RunID || len(es.UncertainCapacity) != 1 || es.UncertainCapacity[0] != "default" {
		t.Fatalf("engine_started = %+v, want the new run carrying the uncertain pool default", es)
	}
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindEngineStopping {
			t.Fatal("the journal holds engine_stopping: the old engine was stopped, not crashed")
		}
	}
	if h := s.holds(t, c2, "default"); len(h) != 1 || h[0].AttemptID != held.ID {
		t.Fatalf("holds after restart = %+v, want the one on %s", h, held.ID)
	}

	if _, err := c2.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "restarted"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	next := s.Submit(t, c2, "s2")
	s.WaitTerminal(next)
	wantTurn(t, s.Turn(t, c2, next), engine.StateApproved, "all_approved")
	if n := len(s.calls("reviewer", "review")); n != 1 {
		t.Fatalf("new reviewer adapter received %d review requests, want 1", n)
	}
	if n := len(oldAdapters["reviewer"].Calls()); n < oldReviewCalls {
		t.Fatalf("old reviewer adapter lost calls: %d", n)
	}
	for _, call := range oldAdapters["reviewer"].Calls() {
		if call.Request.Envelope.TurnID == next {
			t.Fatal("the new turn was dispatched to the old engine's adapter")
		}
	}
}

// TestSuite_StopGracefully proves StopGracefully is Engine.Stop: it announces
// engine_stopping, which a crash does not.
func TestSuite_StopGracefully(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	s.StopGracefully()
	s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStopping })
}
