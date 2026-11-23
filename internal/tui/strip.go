package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// renderStrip returns the status line. It is a single line showing:
// [preset] [quick:<name>?] [turn state] [elapsed / turn_deadline]
// [est. tokens / limit] [round n/m] ctrl+o: reviews tab: switch  /help
// At narrow widths, segments are dropped in order: budget meter, reviews hint, round, then turn state is shortened.
// Uses terminal display width. Participant states are on the progress line.
func renderStrip(s State, width int, now time.Time) string {
	return renderStripFor(s, width, now, false, false)
}

// renderStripFor is renderStrip; compact also drops the preset when the line
// is too wide, so the open turn's state keeps its room beside the key hint.
// includedReviewsHint controls whether to include "ctrl+o: reviews" (true for chat tab).
func renderStripFor(s State, width int, now time.Time, compact bool, includeReviewsHint bool) string {
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

	// [budget meter and tokens/limit]; track position for overflow shedding
	budgetIdx := len(segments)
	switch {
	case ti.LimitTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d / %d]", ti.EstimatedTokens, ti.LimitTokens))
	case ti.EstimatedTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d]", ti.EstimatedTokens))
	}

	// [ctrl+o: reviews] when reviews are present and hint is requested
	reviewsIdx := len(segments)
	if includeReviewsHint {
		segments = append(segments, "ctrl+o: reviews")
	}

	// [round n/m] during revision
	roundIdx := len(segments)
	if ti.Round > 0 && ti.MaxRounds > 0 {
		segments = append(segments, fmt.Sprintf("[round %d/%d]", ti.Round, ti.MaxRounds))
	}

	// Presets, profile names, and states come from the engine
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

	// Overflow strategy per design: drop in order:
	// 1. Budget meter (drop first to keep tokens/limit visible)
	if budgetIdx >= 0 && budgetIdx < len(segments) {
		kept := append(append([]string{}, segments[:budgetIdx]...), segments[budgetIdx+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
		reviewsIdx--
		roundIdx--
	}

	// 2. Reviews hint
	if includeReviewsHint && reviewsIdx >= 0 && reviewsIdx < len(segments) {
		kept := append(append([]string{}, segments[:reviewsIdx]...), segments[reviewsIdx+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
		roundIdx--
	}

	// 3. Round n/m
	if roundIdx >= 0 && roundIdx < len(segments) {
		kept := append(append([]string{}, segments[:roundIdx]...), segments[roundIdx+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
	}

	// Next the preset goes: the running turn's state matters more.
	if compact && len(segments) > 0 && strings.HasPrefix(segments[0], "[") {
		segments = segments[1:]
		if line = strings.Join(segments, " "); runewidth.StringWidth(line) <= width {
			return line
		}
	}

	// 4. Drop segments from the right, adding … at the end.
	// But never drop to fewer than 2 segments to keep turn state and key info visible.
	ellipsis := "…"
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	for len(segments) > 2 {
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
// If height constraint would leave the tab area with fewer than 5 lines (height - banners - strip - input),
// only the first banner is shown with a "+N more" marker.
func renderBanners(s State) []string {
	var banners []string

	// The identity warning sorts first, so the height cap never folds it
	// into a count.
	if b := identityBanner(s); b != "" {
		banners = append(banners, b)
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

// identityBanner names, in sorted order, each participant whose identity the
// engine only matched by name, or "" when none applies. The ids are escaped
// by renderBanners with the rest.
func identityBanner(s State) string {
	var ids []string
	for id, p := range s.Participants {
		if p.Identity == "" || p.Identity == "unknown" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return "identity: unknown(name-matched): " + strings.Join(ids, ", ")
}

// bannerHeightDivisor sets how much of the screen banners may take: at most
// height/bannerHeightDivisor lines, which is three at 24 rows. A fourth banner
// at 80x24 therefore collapses the list, so the strip, tab and input keep
// their room however many conditions are active.
const bannerHeightDivisor = 8

// capBannersForHeight returns the banners that fit above the strip. When they
// exceed the allowance of height/bannerHeightDivisor lines, or would leave the
// tab area fewer than minTabHeight lines, only the first banner shows,
// followed by a "+N more" marker.
func capBannersForHeight(banners []string, height, minTabHeight int) []string {
	room := height - tabBarHeight - stripHeight - inputHeight - minTabHeight
	allowed := min(room, height/bannerHeightDivisor)
	if len(banners) <= 1 || len(banners) <= allowed {
		return banners
	}
	return []string{banners[0], fmt.Sprintf("+%d more", len(banners)-1)}
}
