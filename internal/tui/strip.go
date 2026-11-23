package tui

import (
	"fmt"
	"sort"
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
	return renderStripFor(s, width, now, false)
}

// renderStripFor is renderStrip; compact also drops the preset when the line
// is too wide, so the open turn's state keeps its room beside the key hint.
func renderStripFor(s State, width int, now time.Time, compact bool) string {
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
		return safeLine(strings.Join(segments, " "))
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

	// [round n/m] while revising
	if ti.Round > 0 && ti.MaxRounds > 0 {
		segments = append(segments, fmt.Sprintf("[round %d/%d]", ti.Round, ti.MaxRounds))
	}

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

	// [est. tokens / limit]; shed before participant state when the line is tight
	estIdx := len(segments)
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

	// Presets, profile names, states and participant ids come from the engine
	// and the workspace: escape each segment before it is measured.
	for i, seg := range segments {
		segments[i] = safeLine(seg)
	}
	line := strings.Join(segments, " ")

	// If line fits (measured by display width, not bytes), return it.
	displayWidth := runewidth.StringWidth(line)
	if displayWidth <= width {
		return line
	}

	// Token estimate and participant state are valuable; drop token estimate first,
	// then drop segments from the right.
	kept := segments
	if estIdx < len(kept) && strings.HasPrefix(kept[estIdx], "[est.") {
		kept = append(append([]string{}, kept[:estIdx]...), kept[estIdx+1:]...)
		if line = strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
	}
	segments = kept

	// Next the preset goes: the running turn's state matters more.
	if compact && len(segments) > 0 && segments[0] == safeLine(fmt.Sprintf("[%s]", ti.Preset)) {
		segments = segments[1:]
		if line = strings.Join(segments, " "); runewidth.StringWidth(line) <= width {
			return line
		}
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
// Order: identity warning (always first), deadlines: provisional, roster file changed, engine: reconnecting, quick: unapproved.
// If height constraint would leave the tab area with fewer than 5 lines (height - banners - strip - input),
// only the first banner (identity warning if present) is shown with a "+N more" marker.
func renderBanners(s State) []string {
	var banners []string

	// Identity warning always goes first and is never folded
	var unknownParticipants []string
	for id, p := range s.Participants {
		if p.Identity == "" || p.Identity == "unknown" {
			unknownParticipants = append(unknownParticipants, id)
		}
	}
	if len(unknownParticipants) > 0 {
		sort.Strings(unknownParticipants)
		names := strings.Join(unknownParticipants, ", ")
		banners = append(banners, fmt.Sprintf("identity: unknown(name-matched) %s", names))
	}

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
	if s.QuickInvalid != "" {
		banners = append(banners, s.QuickInvalid)
	}

	// Banners carry paths and profile names from the workspace; escape them
	// here so no setter can bypass the boundary.
	for i, b := range banners {
		banners[i] = safeLine(b)
	}
	return banners
}

// bannerHeightDivisor sets how much of the screen banners may take: at most
// height/bannerHeightDivisor lines, which is three at 24 rows. A fourth banner
// at 80x24 therefore collapses the list, so the strip, tab and input keep
// their room however many conditions are active.
const bannerHeightDivisor = 8

// capBannersForHeight returns the banners that fit above the strip. When they
// exceed the allowance of height/bannerHeightDivisor lines, or would leave the
// tab area fewer than minTabHeight lines, only the first banner shows (identity
// warning, if present, is always kept), followed by a "+N more" marker.
func capBannersForHeight(banners []string, height, minTabHeight int) []string {
	room := height - tabBarHeight - stripHeight - inputHeight - minTabHeight
	allowed := min(room, height/bannerHeightDivisor)
	if len(banners) <= 1 || len(banners) <= allowed {
		return banners
	}
	// Keep the first banner (which is identity warning if present) and fold the rest
	return []string{banners[0], fmt.Sprintf("+%d more", len(banners)-1)}
}
