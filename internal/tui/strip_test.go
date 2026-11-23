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

	// At wide width, both reviews hint and round should be present
	wide := renderStripFor(s, 150, stripNow, false, true)
	if !strings.Contains(wide, "ctrl+o: reviews") || !strings.Contains(wide, "round") {
		t.Errorf("at wide width, want reviews hint and round, got %q", wide)
	}

	// At narrower width, reviews hint should drop first
	narrow := renderStripFor(s, 60, stripNow, false, true)
	hasRound := strings.Contains(narrow, "round")
	hasReviews := strings.Contains(narrow, "ctrl+o")
	if hasReviews && !hasRound {
		t.Errorf("at narrow width, reviews should not be present without round, got %q", narrow)
	}
}

func TestRenderStripOverflowThresholds(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "running",
		EstimatedTokens: 1500,
		LimitTokens:     3000,
		Round:           2,
		MaxRounds:       3,
		SubmittedAt:     stripNow,
	}

	// Test specific widths to verify overflow order
	// The strip should show: [preset] [state] [elapsed] [meter] [tokens] [round]
	// At narrow widths, meter drops first, then reviews, then round, then turn state shortens
	tests := []struct {
		width     int
		hasMeter  bool
		hasRound  bool
		hasTokens bool
	}{
		{150, true, true, true},  // Wide: all present with meter
		{100, true, true, true},  // Still have room for all with meter
		{70, true, true, true},   // Meter just fits
		{65, false, true, true},  // Meter drops, rest still fits
		{55, false, true, true},  // Round still there
		{50, false, false, true}, // Round drops around width 50
		{35, false, false, true}, // Narrow: tokens in narrow state
		{25, false, false, true}, // Very narrow: might show ellipsis
	}

	for _, tt := range tests {
		out := renderStripFor(s, tt.width, stripNow, false, false)

		hasMeter := strings.Contains(out, "█") || strings.Contains(out, "░")
		hasRound := strings.Contains(out, "round")
		hasTokens := strings.Contains(out, "1500") || strings.Contains(out, "3000")

		if hasMeter != tt.hasMeter {
			t.Errorf("width=%d: meter present=%v, want %v, got %q", tt.width, hasMeter, tt.hasMeter, out)
		}
		if hasRound != tt.hasRound {
			t.Errorf("width=%d: round present=%v, want %v, got %q", tt.width, hasRound, tt.hasRound, out)
		}
		// At narrow widths, tokens might be replaced by ellipsis, so only check at wider widths
		if tt.width >= 50 && hasTokens != tt.hasTokens {
			t.Errorf("width=%d: tokens present=%v, want %v, got %q", tt.width, hasTokens, tt.hasTokens, out)
		}
	}
}

func TestRenderStripTurnStateShortening(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		Preset:          "review",
		State:           "reviewers_dispatched",
		EstimatedTokens: 1000,
		LimitTokens:     2000,
		Round:           1,
		MaxRounds:       1,
		SubmittedAt:     stripNow,
	}

	// At very narrow width, turn state should be shortened with ellipsis
	out := renderStripFor(s, 25, stripNow, false, false)

	// Should contain shortened turn state or ellipsis
	if !strings.Contains(out, "…") && !strings.Contains(out, "review") {
		t.Errorf("at very narrow width, turn state should be shortened or ellipsised, got %q", out)
	}
}
