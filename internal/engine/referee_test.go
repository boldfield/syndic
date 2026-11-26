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

func TestMaxOutputTokens(t *testing.T) {
	// Test that the referee max output tokens constant matches the design spec.
	if toolsRefereeMaxOutputTokens != 64 {
		t.Errorf("expected 64 max output tokens per v5 delta section 6, got %d", toolsRefereeMaxOutputTokens)
	}
}
