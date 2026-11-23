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

	// Round n/m with pips (pips always shown, even in plain mode as text)
	if ti.MaxRounds > 0 {
		roundStr := fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds)
		pips := roundPips(ti.Round, ti.MaxRounds, plainMode)
		if pips != "" {
			roundStr += " " + pips
		}
		parts = append(parts, roundStr)
	}

	// Participant marks: ✓ for done, spinner for working, ○ for queued
	if ti.Author != "" {
		mark := participantMark(ti.AuthorState, ti.State, true, plainMode)
		parts = append(parts, mark+" "+ti.Author)
	}

	for _, id := range ti.Reviewers {
		state := ti.ReviewerStates[id]
		mark := participantMark(state, ti.State, false, plainMode)
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

// progressSpinner returns a spinner character that changes over time.
// In plain mode, returns the spinner character but it doesn't animate.
func progressSpinner(now time.Time, plainMode bool) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if now.IsZero() {
		return frames[0]
	}
	idx := int(now.UnixNano()/100000000) % len(frames)
	return frames[idx]
}

// participantMark returns the mark for a participant's state.
// ✓ for done, spinner for working, ○ for queued.
// isAuthor indicates if this is the author (done when turn is in reviewers_dispatched or later state).
func participantMark(state, turnState string, isAuthor bool, plainMode bool) string {
	// Determine done state based on participant state and turn state
	isDone := false

	if isAuthor {
		// Author is done when reviewers have been dispatched (turn state >= reviewers_dispatched)
		isDone = turnState == "reviewers_dispatched" || turnState == "incomplete" ||
			turnState == "overridden" || turnState == "cancelled" || turnState == "approved"
	} else {
		// Reviewer is done if they have completed their review (turned idle after running)
		// but we can only infer this from the turn state: a reviewer in revision state is done
		// if the turn is in a state past their round. For now, we check if they're not running.
		isDone = state != "running" && state != "queued"
	}

	// Map engine states to marks
	switch state {
	case "running":
		return "◜"
	case "queued":
		return "○"
	case "idle":
		// Author is done when reviewers dispatched and idle; otherwise idle shows as queued
		if isAuthor && isDone {
			return "✓"
		}
		return "○"
	default:
		return "·"
	}
}

// roundPips returns the visual representation of round pips:
// ● for started rounds, ○ for not started.
// In plain mode, returns empty string to omit visual pips but preserves text representation if needed.
func roundPips(round, maxRounds int, plainMode bool) string {
	if maxRounds <= 0 {
		return ""
	}

	if plainMode {
		// In plain mode, pips are still shown as text representation
		// Use (1) (2) (3) format for started rounds, (?) for not started
		var pips []string
		for i := 1; i <= maxRounds; i++ {
			if i <= round {
				pips = append(pips, fmt.Sprintf("(%d)", i))
			} else {
				pips = append(pips, "(?)")
			}
		}
		return strings.Join(pips, "")
	}

	// Color/graphic mode: use filled and unfilled circles
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
