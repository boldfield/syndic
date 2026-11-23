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

// progressLine is the one line a running turn shows: a spinner, the phase,
// round n/m with one pip per allowed round, filled for the rounds started,
// each participant with its mark, and the elapsed time.
func progressLine(s State, turnID string, now time.Time) string {
	ti := s.Turns[turnID]
	spin := spinnerAt(now)
	parts := []string{spin + " turn in progress: " + ti.State}
	if ti.MaxRounds > 0 {
		started := min(max(ti.Round, 0), ti.MaxRounds)
		pips := strings.Repeat(pipStarted, started) + strings.Repeat(pipAhead, ti.MaxRounds-started)
		parts = append(parts, fmt.Sprintf("round %d/%d %s", ti.Round, ti.MaxRounds, pips))
	}
	if ti.Author != "" {
		parts = append(parts, participantMark(s, turnID, ti.Author, true, spin)+" "+ti.Author)
	}
	for _, id := range ti.Reviewers {
		parts = append(parts, participantMark(s, turnID, id, false, spin)+" "+id)
	}
	if !ti.SubmittedAt.IsZero() && !now.IsZero() {
		parts = append(parts, formatDuration(max(now.Sub(ti.SubmittedAt), 0)))
	}
	return safeLine(strings.Join(parts, ", "))
}

// participantMark is the spinner while the participant works, ✓ once it has
// done its part for the current candidate, and ○ while it is queued or its
// part has not begun. The engine returns a participant to idle both before
// and after its work, so done comes from the turn's phase and attempts.
func participantMark(s State, turnID, id string, author bool, spin string) string {
	ti := s.Turns[turnID]
	state := ti.AuthorState
	if !author {
		state = ti.ReviewerStates[id]
	}
	switch state {
	case "running", "cancelling", "unknown":
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
// Storing a candidate bumps the turn's epoch, so a review of the current one
// has an epoch above every author attempt's.
func reviewerDone(s State, turnID, id string) bool {
	authorEpoch, seen := int64(0), false
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "author" && (!seen || ai.Epoch > authorEpoch) {
			authorEpoch, seen = ai.Epoch, true
		}
	}
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Participant == id && ai.Phase == "review" && attemptDone(ai) && (!seen || ai.Epoch > authorEpoch) {
			return true
		}
	}
	return false
}
