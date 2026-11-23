package tui

import (
	"strings"
	"testing"
	"time"
)

var progressNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestRenderProgressShowsSpinnerPhaseAndTime(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{
		State:          "running",
		Round:          1,
		MaxRounds:      2,
		Author:         "alice",
		AuthorState:    "working",
		Reviewers:      []string{"bob"},
		ReviewerStates: map[string]string{"bob": "queued"},
		SubmittedAt:    progressNow,
	}
	now := progressNow.Add(90 * time.Second)

	out := renderProgress(s, "t1", now, false)

	// Should contain phase, round, participant marks, and elapsed time
	if !strings.Contains(out, "running") {
		t.Errorf("missing running state in %q", out)
	}
	if !strings.Contains(out, "round 1/2") {
		t.Errorf("missing round 1/2 in %q", out)
	}
	if !strings.Contains(out, "1m30s") {
		t.Errorf("missing elapsed time in %q", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("missing author alice in %q", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("missing reviewer bob in %q", out)
	}
}

func TestRenderProgressShowsParticipantMarks(t *testing.T) {
	tests := []struct {
		name  string
		state string
		mark  string
	}{
		{"done participant", "done", "✓"},
		{"working participant", "working", "◜"},
		{"queued participant", "queued", "○"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := NewState()
			s.Turns["t1"] = TurnInfo{
				State:          "running",
				Round:          1,
				MaxRounds:      1,
				Author:         "alice",
				AuthorState:    test.state,
				Reviewers:      nil,
				ReviewerStates: map[string]string{},
				SubmittedAt:    progressNow,
			}

			out := renderProgress(s, "t1", progressNow, false)

			if !strings.Contains(out, test.mark) {
				t.Errorf("missing mark %q in %q", test.mark, out)
			}
		})
	}
}

func TestRenderProgressShowsPips(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{
		State:          "running",
		Round:          2,
		MaxRounds:      3,
		Author:         "alice",
		AuthorState:    "working",
		Reviewers:      nil,
		ReviewerStates: map[string]string{},
		SubmittedAt:    progressNow,
	}
	now := progressNow.Add(60 * time.Second)

	out := renderProgress(s, "t1", now, false)

	// Should show pips: ● ● ○
	if !strings.Contains(out, "●●○") {
		t.Errorf("missing pip pattern in %q", out)
	}
}

func TestRenderProgressPlainMode(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{
		State:          "running",
		Round:          1,
		MaxRounds:      2,
		Author:         "alice",
		AuthorState:    "working",
		Reviewers:      nil,
		ReviewerStates: map[string]string{},
		SubmittedAt:    progressNow,
	}
	now := progressNow.Add(60 * time.Second)

	out := renderProgress(s, "t1", now, true)

	// In plain mode, no pips
	if strings.Contains(out, "●") || strings.Contains(out, "○") {
		t.Errorf("plain mode should not have pips in %q", out)
	}
	// Should still have round text
	if !strings.Contains(out, "round 1/2") {
		t.Errorf("plain mode should have round text in %q", out)
	}
}

func TestRenderProgressNotRunning(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{
		State:          "approved",
		Round:          1,
		MaxRounds:      1,
		Author:         "alice",
		AuthorState:    "done",
		Reviewers:      nil,
		ReviewerStates: map[string]string{},
		SubmittedAt:    progressNow,
	}
	now := progressNow.Add(60 * time.Second)

	out := renderProgress(s, "t1", now, false)

	// Should be empty for non-running turns
	if out != "" {
		t.Errorf("non-running turn should produce empty progress, got %q", out)
	}
}
