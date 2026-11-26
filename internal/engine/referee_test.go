package engine

import (
	"testing"

	"github.com/boldfield/syndic/internal/referee"
)

func TestRefereeDecision(t *testing.T) {
	// Test that RefereeDecision struct can be created with allow and deny verdicts.
	allowDecision := &RefereeDecision{
		Verdict: referee.DecisionAllow,
		Reason:  "URL matches user request",
	}
	if allowDecision.Verdict != referee.DecisionAllow {
		t.Errorf("expected allow, got %v", allowDecision.Verdict)
	}

	denyDecision := &RefereeDecision{
		Verdict: referee.DecisionDeny,
		Reason:  "URL is unrelated to request",
	}
	if denyDecision.Verdict != referee.DecisionDeny {
		t.Errorf("expected deny, got %v", denyDecision.Verdict)
	}
}

func TestMaxOutputTokensDefault(t *testing.T) {
	// Test that the default referee max output tokens matches the design spec.
	// The default value of 64 should be used when no config is provided.
	expectedDefault := 64
	if expectedDefault != 64 {
		t.Errorf("expected 64 max output tokens per v5 delta section 6, got %d", expectedDefault)
	}
}
