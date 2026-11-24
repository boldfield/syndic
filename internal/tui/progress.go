package tui

import (
	"fmt"
	"strings"
	"time"
)

// The progress line's marks. Each participant mark is a different glyph, so
// no meaning rests on colour or on the spinner's motion.
const (
	markDone   = "✓"
	markQueued = "○"
	pipStarted = "●"
	pipAhead   = "○"
)

// spinnerFrames turn once a second with the clock tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerAt(now time.Time) string {
	if now.IsZero() {
		return spinnerFrames[0]
	}
	return spinnerFrames[int(now.Unix()%int64(len(spinnerFrames)))]
}

// progressLine is the one line a running turn shows: a spinner or state word, the phase,
// round n/m with one pip per allowed round (omitted in plainMode), filled for the rounds started,
// each participant with its mark, and the elapsed time.
func progressLine(s State, turnID string, now time.Time, plainMode bool) string {
	ti := s.Turns[turnID]
	spin := spinnerAt(now)

	var parts []string
	if plainMode {
		// In plain mode, show state word instead of spinner
		parts = []string{ti.State + " turn in progress"}
	} else {
		// Normal mode: show spinner
		parts = []string{spin + " turn in progress: " + ti.State}
	}

	if ti.MaxRounds > 0 {
		if plainMode {
			// In plain mode, omit pips but keep round n/m
			parts = append(parts, fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds))
		} else {
			started := min(max(ti.Round, 0), ti.MaxRounds)
			pips := strings.Repeat(pipStarted, started) + strings.Repeat(pipAhead, ti.MaxRounds-started)
			parts = append(parts, fmt.Sprintf("round %d/%d %s", ti.Round, ti.MaxRounds, pips))
		}
	}
	if ti.Author != "" {
		parts = append(parts, participantMark(s, turnID, ti.Author, true, spin, plainMode)+" "+ti.Author)
	}
	for _, id := range ti.Reviewers {
		parts = append(parts, participantMark(s, turnID, id, false, spin, plainMode)+" "+id)
	}
	if !ti.SubmittedAt.IsZero() && !now.IsZero() {
		parts = append(parts, formatDuration(max(now.Sub(ti.SubmittedAt), 0)))
	}
	return safeLine(strings.Join(parts, ", "))
}

// participantMark is the spinner while the participant works, ✓ once it has
// done its part for the current candidate, and ○ while it is queued or its
// part has not begun. In plainMode, running/cancelling states show a text marker instead of spinner.
// The engine returns a participant to idle both before and after its work, so done comes from the turn's phase and attempts.
func participantMark(s State, turnID, id string, author bool, spin string, plainMode bool) string {
	ti := s.Turns[turnID]
	state := ti.AuthorState
	if !author {
		state = ti.ReviewerStates[id]
	}
	switch state {
	case "running", "cancelling", "unknown":
		if plainMode {
			return "⋯" // text marker for working in plain mode (not a spinner animation)
		}
		return spin
	case "queued":
		return markQueued
	}
	if author && authorDone(s, turnID, id) || !author && reviewerDone(s, turnID, id) {
		return markDone
	}
	return markQueued
}

// authorDone: the author's latest attempt is finished and the turn has moved
// on to its candidate's review.
func authorDone(s State, turnID, id string) bool {
	switch s.Turns[turnID].State {
	case "candidate_frozen", "reviewers_dispatched":
	default:
		return false
	}
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Participant == id && ai.Phase == "author" && attemptDone(ai) {
			return true
		}
	}
	return false
}

// reviewerDone: the reviewer finished a review of the current candidate.
// The first candidate is reviewed at its author attempt's epoch. A revision's
// author attempt runs at the epoch of the reviews it answers, and storing its
// candidate bumps the epoch, so once a revision has started the current
// candidate's reviews are one epoch above the latest author attempt.
func reviewerDone(s State, turnID, id string) bool {
	current, seen := int64(0), false
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "author" && (!seen || ai.Epoch > current) {
			current, seen = ai.Epoch, true
		}
	}
	if s.Turns[turnID].Round > 0 {
		current++
	}
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Participant == id && ai.Phase == "review" && attemptDone(ai) && (!seen || ai.Epoch >= current) {
			return true
		}
	}
	return false
}
