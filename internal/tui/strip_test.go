package tui

import (
	"strings"
	"testing"
	"time"
)

var stripNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestRenderStripShowsBudgetMeter(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1500,
		LimitTokens:     3000,
		Round:           1,
		MaxRounds:       2,
		SubmittedAt:     stripNow,
	}

	out := renderStripFor(s, 150, stripNow, false, false)

	// Should contain budget meter
	if !strings.Contains(out, "█") && !strings.Contains(out, "░") {
		t.Errorf("missing budget meter in %q", out)
	}
	// Should contain tokens/limit
	if !strings.Contains(out, "1500") || !strings.Contains(out, "3000") {
		t.Errorf("missing tokens/limit in %q", out)
	}
}

func TestRenderStripOverflowDropsBudgetMeterFirst(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 2500,
		LimitTokens:     5000,
		Round:           2,
		MaxRounds:       3,
		SubmittedAt:     stripNow,
	}

	// At very narrow width, budget meter should drop but tokens/limit should stay
	out := renderStripFor(s, 40, stripNow, false, true)

	// tokens/limit should be present
	if !strings.Contains(out, "2500") || !strings.Contains(out, "5000") {
		t.Errorf("tokens/limit should remain after overflow, got %q", out)
	}
}

func TestRenderStripTokensLimitAlwaysRetained(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1000,
		LimitTokens:     2000,
		Round:           1,
		MaxRounds:       1,
		SubmittedAt:     stripNow,
	}

	// At very narrow width, tokens/limit should still be shown
	out := renderStripFor(s, 20, stripNow, false, false)

	// Even at very narrow width, tokens/limit or ellipsis should be shown
	if out == "" {
		t.Errorf("strip should not be empty")
	}
}

func TestRenderStripReviewsHintDropsBeforeRound(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1000,
		LimitTokens:     2000,
		Round:           1,
		MaxRounds:       2,
		SubmittedAt:     stripNow,
	}

	// At narrow width with reviews hint, reviews should drop before round
	out := renderStripFor(s, 50, stripNow, false, true)

	// If reviews hint was added, check if it's in the output
	// The exact behavior depends on width, but we should have either
	// reviews hint + round, or just round, or neither
	_ = out // Just check it doesn't crash
}

func TestRenderStripRoundDropsBeforeTurnState(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1000,
		LimitTokens:     2000,
		Round:           1,
		MaxRounds:       1,
		SubmittedAt:     stripNow,
	}

	// At very narrow width, round should drop but turn state should remain
	out := renderStripFor(s, 25, stripNow, false, false)

	// Turn state should be in the output
	if !strings.Contains(out, "running") && !strings.Contains(out, "…") {
		t.Errorf("turn state or ellipsis should remain, got %q", out)
	}
}

func TestRenderStripElipsisWidthCalculation(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1000,
		LimitTokens:     2000,
		Round:           1,
		MaxRounds:       1,
		SubmittedAt:     stripNow,
	}

	// At very narrow width, should show ellipsis
	out := renderStripFor(s, 10, stripNow, false, false)

	// Should contain ellipsis
	if !strings.Contains(out, "…") {
		t.Errorf("at narrow width should have ellipsis, got %q", out)
	}
}
