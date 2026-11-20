package tui

import (
	"fmt"
	"strings"
	"time"
)

// renderStrip returns the status strip line. It is a single line showing:
// [preset] [quick:<name>?] [turn state] [elapsed / turn_deadline]
// [est. tokens / limit] author:<id> <state> reviewers: <id> <state> … identity: unknown(name-matched)
// At 80 columns with five reviewers the strip is at most 80 cells wide and ends with …
// when needed, dropping segments from the right in whole segments.
func renderStrip(s State, width int) string {
	switch s.ConnState {
	case ConnStateReconnecting:
		return "engine: reconnecting"
	case ConnStateConnecting:
		return "engine: connecting"
	}

	// No turn open: show idle state.
	if s.CurrentTurn == "" {
		segments := []string{}
		if s.Status != nil {
			segments = append(segments, fmt.Sprintf("[%s]", s.Status.DefaultPreset))
			if s.Status.DeadlinesProvisional {
				segments = append(segments, "[idle]")
			} else {
				segments = append(segments, "[idle]")
			}
		}
		return strings.Join(segments, " ")
	}

	// Turn is open: build full strip.
	ti, ok := s.Turns[s.CurrentTurn]
	if !ok {
		return ""
	}

	segments := []string{}

	// [preset]
	segments = append(segments, fmt.Sprintf("[%s]", ti.Preset))

	// [quick:<name>] when one governs
	if ti.QuickProfile != nil && *ti.QuickProfile != "" {
		segments = append(segments, fmt.Sprintf("[quick:%s]", *ti.QuickProfile))
	}

	// [turn state]
	segments = append(segments, fmt.Sprintf("[%s]", ti.State))

	// [elapsed / turn deadline]
	if !ti.SubmittedAt.IsZero() {
		elapsed := time.Since(ti.SubmittedAt)
		elapsedStr := formatDuration(elapsed)
		if ti.TurnDeadline != nil && !ti.TurnDeadline.IsZero() {
			remaining := time.Until(*ti.TurnDeadline)
			remainingStr := formatDuration(remaining)
			segments = append(segments, fmt.Sprintf("[%s / %s]", elapsedStr, remainingStr))
		} else {
			segments = append(segments, fmt.Sprintf("[%s]", elapsedStr))
		}
	}

	// [est. tokens / limit]
	if ti.EstimatedTokens > 0 || ti.LimitTokens > 0 {
		segments = append(segments, fmt.Sprintf("[est. %d / %d]", ti.EstimatedTokens, ti.LimitTokens))
	}

	// author:<id> <state>
	if ti.Author != "" {
		segments = append(segments, fmt.Sprintf("author:%s %s", ti.Author, ti.AuthorState))
	}

	// reviewers: <id> <state> …
	if len(ti.Reviewers) > 0 {
		reviewerParts := []string{"reviewers:"}
		for id, state := range ti.Reviewers {
			reviewerParts = append(reviewerParts, fmt.Sprintf("%s %s", id, state))
		}
		segments = append(segments, strings.Join(reviewerParts, " "))
	}

	// identity: unknown(name-matched)
	segments = append(segments, "identity: unknown(name-matched)")

	line := strings.Join(segments, " ")

	// If line fits, return it.
	if len(line) <= width {
		return line
	}

	// Drop segments from the right, adding … at the end.
	for len(segments) > 0 {
		segments = segments[:len(segments)-1]
		line = strings.Join(segments, " ")
		if len(line) <= width-1 {
			return line + "…"
		}
	}

	return "…"
}

// formatDuration returns a human-readable duration string (e.g., "1m23s", "12s").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// renderBanners returns the banners to display above the strip.
// Order: deadlines: provisional, roster file changed, engine: reconnecting, quick: unapproved.
func renderBanners(s State) []string {
	var banners []string

	if s.Status != nil {
		if s.Status.DeadlinesProvisional {
			banners = append(banners, "deadlines: provisional")
		}
		if s.Status.RosterFileChanged {
			banners = append(banners, "roster file changed since start; restart the engine to apply")
		}
	}

	if s.ConnState == ConnStateReconnecting {
		banners = append(banners, "engine: reconnecting")
	}

	return banners
}
