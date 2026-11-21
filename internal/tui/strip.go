package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// renderStrip returns the status strip line. It is a single line showing:
// [preset] [quick:<name>?] [turn state] [elapsed / turn_deadline]
// [est. tokens / limit] author:<id> <state> reviewers: <id> <state> … identity: unknown(name-matched)
// At 80 columns with five reviewers the strip is at most 80 cells wide and ends with …
// when needed, dropping segments from the right in whole segments. Uses terminal display width.
func renderStrip(s State, width int, now time.Time) string {
	switch s.ConnState {
	case ConnStateConnecting:
		return "engine: connecting"
	}

	// No turn open: show idle state.
	if s.CurrentTurn == "" {
		segments := []string{}
		if s.Status != nil {
			segments = append(segments, fmt.Sprintf("[%s]", s.Status.DefaultPreset))
			if s.NextPreset != "" {
				segments = append(segments, fmt.Sprintf("[next: %s]", s.NextPreset))
			}
			segments = append(segments, "[idle]")
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
	if s.NextPreset != "" {
		segments = append(segments, fmt.Sprintf("[next: %s]", s.NextPreset))
	}

	// [quick:<name>] or [quick:<name> (mode set by /mode)] when one governs
	if ti.QuickProfile != nil && *ti.QuickProfile != "" {
		if ti.ModeOverride {
			segments = append(segments, fmt.Sprintf("[quick:%s (mode set by /mode)]", *ti.QuickProfile))
		} else {
			segments = append(segments, fmt.Sprintf("[quick:%s]", *ti.QuickProfile))
		}
	}

	// [turn state]
	segments = append(segments, fmt.Sprintf("[%s]", ti.State))

	// [elapsed / turn deadline]
	if !ti.SubmittedAt.IsZero() {
		elapsed := formatDuration(max(now.Sub(ti.SubmittedAt), 0))
		switch {
		case ti.DeadlineDur > 0:
			segments = append(segments, fmt.Sprintf("[%s / %s]", elapsed, formatDuration(ti.DeadlineDur)))
		case ti.TurnDeadline != nil:
			segments = append(segments, fmt.Sprintf("[%s / %s]", elapsed, formatDuration(ti.TurnDeadline.Sub(ti.SubmittedAt))))
		default:
			segments = append(segments, fmt.Sprintf("[%s]", elapsed))
		}
	}

	// [est. tokens / limit]
	switch {
	case ti.LimitTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d / %d]", ti.EstimatedTokens, ti.LimitTokens))
	case ti.EstimatedTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d]", ti.EstimatedTokens))
	}

	// author:<id> <state>
	if ti.Author != "" {
		segments = append(segments, fmt.Sprintf("author:%s %s", ti.Author, ti.AuthorState))
	}

	// reviewers: <id> <state> … (in order from Reviewers list, not map iteration)
	if len(ti.Reviewers) > 0 {
		reviewerParts := []string{"reviewers:"}
		for _, id := range ti.Reviewers {
			state := ti.ReviewerStates[id]
			reviewerParts = append(reviewerParts, fmt.Sprintf("%s %s", id, state))
		}
		segments = append(segments, strings.Join(reviewerParts, " "))
	}

	// identity: unknown(name-matched)
	segments = append(segments, "identity: unknown(name-matched)")

	line := strings.Join(segments, " ")

	// If line fits (measured by display width, not bytes), return it.
	displayWidth := runewidth.StringWidth(line)
	if displayWidth <= width {
		return line
	}

	// Drop segments from the right, adding … at the end.
	ellipsis := "…"
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	for len(segments) > 0 {
		segments = segments[:len(segments)-1]
		line = strings.Join(segments, " ")
		lineWidth := runewidth.StringWidth(line)
		if lineWidth+ellipsisWidth <= width {
			return line + ellipsis
		}
	}

	return ellipsis
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

	if s.UnapprovedProfile != nil && *s.UnapprovedProfile != "" {
		banners = append(banners, fmt.Sprintf("quick: unapproved %s", *s.UnapprovedProfile))
	}

	return banners
}
