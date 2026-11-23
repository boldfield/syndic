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
// [budget meter] [est. tokens / limit] [round n/m] [ctrl+o: reviews] tab: switch  /help
// At narrow widths, segments are dropped in order: budget meter, ctrl+o: reviews, round,
// then turn state is shortened. Tokens/limit and the right-edge hint always remain.
// Uses terminal display width. Participant states are on the progress line.
func renderStrip(s State, width int, now time.Time) string {
	return renderStripFor(s, width, now, false, false)
}

// renderStripFor is renderStrip; compact also drops the preset when the line
// is too wide, so the open turn's state keeps its room beside the key hint.
// includeReviewsHint controls whether to include "ctrl+o: reviews" (true for chat tab).
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

	// Build segments with tracking indices for overflow
	segments := []string{}
	indices := struct {
		preset, state, turnStateIdx, budget, tokens, reviews, round int
	}{-1, -1, -1, -1, -1, -1, -1}

	// [preset]
	indices.preset = len(segments)
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

	// [turn state] - track this for ellipsis shortening
	indices.turnStateIdx = len(segments)
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

	// [budget meter] - droppable, only add if meter is not empty
	indices.budget = -1
	if ti.LimitTokens > 0 {
		meter := budgetMeter(ti.EstimatedTokens, ti.LimitTokens)
		if meter != "" {
			indices.budget = len(segments)
			segments = append(segments, meter)
		}
	}

	// [est. tokens / limit] - must stay
	indices.tokens = len(segments)
	switch {
	case ti.LimitTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d / %d]", ti.EstimatedTokens, ti.LimitTokens))
	case ti.EstimatedTokens > 0:
		segments = append(segments, fmt.Sprintf("[est. %d]", ti.EstimatedTokens))
	}

	// [round n/m] during revision - droppable
	indices.round = len(segments)
	if ti.Round > 0 && ti.MaxRounds > 0 {
		segments = append(segments, fmt.Sprintf("[round %d/%d]", ti.Round, ti.MaxRounds))
	}

	// [ctrl+o: reviews] when reviews are requested - droppable
	indices.reviews = len(segments)
	if includeReviewsHint {
		segments = append(segments, "ctrl+o: reviews")
	}

	// Escape all segments
	for i, seg := range segments {
		segments[i] = safeLine(seg)
	}
	line := strings.Join(segments, " ")

	// If line fits (measured by display width, not bytes), return it.
	displayWidth := runewidth.StringWidth(line)
	if displayWidth <= width {
		return line
	}

	// Overflow strategy per design (design §1):
	// Drop in order: budget meter, ctrl+o: reviews, round, then shorten turn state
	// ALWAYS keep: tokens/limit and the right-edge hint (tab: switch /help)
	ellipsis := "…"
	ellipsisWidth := runewidth.StringWidth(ellipsis)

	// 1. Try dropping budget meter (drop first, keep tokens/limit)
	if indices.budget >= 0 && indices.budget < len(segments) && segments[indices.budget] != "" {
		kept := append(append([]string{}, segments[:indices.budget]...), segments[indices.budget+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
		// Adjust indices after removal
		if indices.reviews > indices.budget {
			indices.reviews--
		}
		if indices.round > indices.budget {
			indices.round--
		}
		if indices.tokens > indices.budget {
			indices.tokens--
		}
	}

	// 2. Try dropping ctrl+o: reviews
	if indices.reviews >= 0 && indices.reviews < len(segments) {
		kept := append(append([]string{}, segments[:indices.reviews]...), segments[indices.reviews+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
		// Adjust indices after removal
		if indices.round > indices.reviews {
			indices.round--
		}
		if indices.tokens > indices.reviews {
			indices.tokens--
		}
	}

	// 3. Try dropping round n/m
	if indices.round >= 0 && indices.round < len(segments) {
		kept := append(append([]string{}, segments[:indices.round]...), segments[indices.round+1:]...)
		if line := strings.Join(kept, " "); runewidth.StringWidth(line) <= width {
			return line
		}
		segments = kept
		// Adjust indices after removal
		if indices.tokens > indices.round {
			indices.tokens--
		}
	}

	// 4. Drop the preset if it's the first segment and compact mode
	if compact && len(segments) > 0 && indices.preset == 0 {
		preset := segments[0]
		// Only drop if it's exactly the preset segment [preset_name], not quick profile or next
		if strings.HasPrefix(preset, "[") && strings.HasSuffix(preset, "]") &&
			!strings.Contains(preset, "quick") && !strings.Contains(preset, "next") {
			segments = segments[1:]
			// Adjust indices after removal
			indices.turnStateIdx--
			if indices.budget > 0 {
				indices.budget--
			}
			if indices.tokens > 0 {
				indices.tokens--
			}
			if indices.round > 0 {
				indices.round--
			}
			if indices.reviews > 0 {
				indices.reviews--
			}
			if line := strings.Join(segments, " "); runewidth.StringWidth(line) <= width {
				return line
			}
		}
	}

	// 5. Try shortening turn state with ellipsis
	if indices.turnStateIdx >= 0 && indices.turnStateIdx < len(segments) {
		state := segments[indices.turnStateIdx]
		// Extract the state text from [state] format
		if strings.HasPrefix(state, "[") && strings.HasSuffix(state, "]") {
			inner := state[1 : len(state)-1]
			// Try progressively shorter versions until it fits
			for len(inner) > 1 {
				inner = inner[:len(inner)-1]
				shortened := "[" + inner + "…]"
				segments[indices.turnStateIdx] = shortened
				line := strings.Join(segments, " ")
				if runewidth.StringWidth(line) <= width {
					return line
				}
			}
		}
	}

	// 6. Last resort: drop segments from the right, keeping tokens/limit and hint
	// Keep at least tokens/limit visible at the right edge
	for len(segments) > 1 && indices.tokens >= 0 {
		// Stop if we're about to remove tokens/limit
		if indices.tokens >= len(segments) {
			break
		}
		segments = segments[:len(segments)-1]
		line := strings.Join(segments, " ")
		lineWidth := runewidth.StringWidth(line)
		if lineWidth+ellipsisWidth <= width {
			return line + ellipsis
		}
	}

	// If nothing fits, return just ellipsis
	return ellipsis
}

// budgetMeter returns a visual representation of budget usage as a meter,
// or empty string if there is no limit or no usage to show.
func budgetMeter(estimated, limit int64) string {
	if limit <= 0 {
		return ""
	}
	meterWidth := int64(10)
	filledWidth := (estimated * meterWidth) / limit
	if filledWidth < 0 {
		filledWidth = 0
	}
	if filledWidth > meterWidth {
		filledWidth = meterWidth
	}
	filled := strings.Repeat("█", int(filledWidth))
	empty := strings.Repeat("░", int(meterWidth-filledWidth))
	return "[" + filled + empty + "]"
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
