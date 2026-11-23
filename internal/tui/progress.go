package tui

import (
	"fmt"
	"strings"
	"time"
)

// renderProgress returns the progress line for a turn, showing:
// spinner, phase, round n/m with pips, participant marks (✓/spinner/○), and elapsed time.
// It renders the specific turn identified by turnID, not the current turn.
func renderProgress(s State, turnID string, now time.Time, plainMode bool) string {
	ti, ok := s.Turns[turnID]
	if !ok || turnID == "" {
		return ""
	}

	// Terminal states don't show progress
	switch ti.State {
	case "approved", "solo_complete", "incomplete", "overridden", "cancelled":
		return ""
	}

	parts := []string{}

	// Spinner and phase
	spinner := progressSpinner(now, plainMode)
	parts = append(parts, spinner+" "+ti.State)

	// Round n/m with pips
	if ti.MaxRounds > 0 {
		roundStr := fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds)
		if !plainMode {
			roundStr += " " + roundPips(ti.Round, ti.MaxRounds)
		}
		parts = append(parts, roundStr)
	}

	// Participant marks: ✓ for done, spinner for working, ○ for queued
	if ti.Author != "" {
		mark := participantMark(ti.AuthorState, plainMode)
		parts = append(parts, mark+" "+ti.Author)
	}

	for _, id := range ti.Reviewers {
		state := ti.ReviewerStates[id]
		mark := participantMark(state, plainMode)
		parts = append(parts, mark+" "+id)
	}

	// Elapsed time
	if !ti.SubmittedAt.IsZero() && !now.IsZero() {
		elapsed := formatDuration(max(now.Sub(ti.SubmittedAt), 0))
		parts = append(parts, elapsed)
	}

	line := safeLine(strings.Join(parts, " "))
	return line
}

// progressSpinner returns a spinner character that changes over time, or the state word in plain mode.
func progressSpinner(now time.Time, plainMode bool) string {
	if plainMode {
		return "⟳"
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if now.IsZero() {
		return frames[0]
	}
	idx := int(now.UnixNano()/100000000) % len(frames)
	return frames[idx]
}

// participantMark returns the mark for a participant's state: ✓ done, spinner for working, ○ for waiting/queued.
func participantMark(state string, plainMode bool) string {
	switch state {
	case "done":
		return "✓"
	case "working", "running":
		if plainMode {
			return "⟳"
		}
		return "◜"
	case "queued", "idle":
		return "○"
	default:
		return "·"
	}
}

// roundPips returns the visual representation of round pips:
// ● for started rounds, ○ for not started.
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
			pips = append(pips, "○")
		}
	}
	return strings.Join(pips, "")
}
