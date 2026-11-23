package tui

import (
	"fmt"
	"strings"
	"time"
)

// renderProgress returns the progress line for a running turn, showing:
// phase/state, round n/m with pips, participant marks, and elapsed time.
// For compatibility with existing tests and the v5 format, it includes the full
// participant state information while also supporting the new compact marks.
func renderProgress(s State, now time.Time, plainMode bool) string {
	ti, ok := s.Turns[s.CurrentTurn]
	if !ok || s.CurrentTurn == "" {
		return ""
	}

	parts := []string{"turn in progress: " + ti.State}

	// Round n/m
	if ti.MaxRounds > 0 {
		roundStr := fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds)
		if !plainMode {
			roundStr += roundPips(ti.Round, ti.MaxRounds)
		}
		parts = append(parts, roundStr)
	}

	// Elapsed time
	if !ti.SubmittedAt.IsZero() && !now.IsZero() {
		elapsed := formatDuration(max(now.Sub(ti.SubmittedAt), 0))
		parts = append(parts, elapsed)
	}

	// Participant states: author and reviewers
	if ti.Author != "" {
		parts = append(parts, ti.Author+" "+ti.AuthorState)
	}

	for _, id := range ti.Reviewers {
		parts = append(parts, id+" "+ti.ReviewerStates[id])
	}

	line := safeLine(strings.Join(parts, ", "))
	return line
}

// roundPips returns the visual representation of round pips:
// ● for started rounds, ◯ for not started.
// In plain mode, returns empty string since pips are not shown.
func roundPips(round, maxRounds int) string {
	if maxRounds <= 0 {
		return ""
	}
	var pips []string
	for i := 1; i <= maxRounds; i++ {
		if i <= round {
			pips = append(pips, "●")
		} else {
			pips = append(pips, "◯")
		}
	}
	return " " + strings.Join(pips, "")
}
