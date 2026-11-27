package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// renderStrip returns the left part of the status line alone, width cells at
// most: preset, the next preset, quick:<name> when one governs, the turn state
// or idle, elapsed/turn_deadline, the budget meter with tokens/limit (meter
// omitted in plainMode), and round n/m during a revision. It is every item,
// cut from the right with … when the line is wider than width. Participant
// states are on the chat's progress line and in the participants tab, not here.
func renderStrip(s State, width int, now time.Time, plainMode bool) string {
	l := statusItemsOf(s, now, plainMode, false).left()
	if cellWidth(l) <= width {
		return l
	}
	return cutCells(l, width-1) + "…"
}

// renderStatusLine is the status line, exactly width cells: the left part, the
// right-edge hints, and the giving way of v6 addendum 1 section 1 between
// them. reviews is set on the chat tab. In plainMode, the meter is omitted but
// tokens/limit are kept.
func renderStatusLine(s State, width int, now time.Time, reviews bool, plainMode bool) string {
	return fitStatusLine(statusItemsOf(s, now, plainMode, reviews), width)
}

// reviewsHint names the reviews drawer's key; it sits left of keyHint while
// the chat tab is active.
const reviewsHint = "ctrl+o: reviews"

// keyHint sits at the right edge of the status line.
const keyHint = "tab: switch  /help"

// budgetMeterCells is how many cells the budget meter takes.
const budgetMeterCells = 8

// statusItemsOf draws the status line's items, each escaped. engine:
// connecting stands whole in place of the preset.
func statusItemsOf(s State, now time.Time, plainMode, chatTab bool) statusItems {
	it := statusItems{chatTab: chatTab}
	if s.ConnState == ConnStateConnecting {
		it.preset = "engine: connecting"
		return it
	}
	next := func() {
		if s.NextPreset != "" {
			it.next = safeLine("next: " + s.NextPreset)
		}
	}

	if s.CurrentTurn == "" {
		if s.Status != nil {
			it.preset = safeLine(s.Status.DefaultPreset)
			next()
			it.state = "idle"
		}
		return it
	}
	ti, ok := s.Turns[s.CurrentTurn]
	if !ok {
		return it
	}

	it.preset = safeLine(ti.Preset)
	next()
	if ti.QuickProfile != nil && *ti.QuickProfile != "" {
		if ti.ModeOverride {
			it.quick = safeLine(fmt.Sprintf("quick:%s (mode set by /mode)", *ti.QuickProfile))
		} else {
			it.quick = safeLine("quick:" + *ti.QuickProfile)
		}
	}
	it.state = safeLine(ti.State)

	if !ti.SubmittedAt.IsZero() {
		elapsed := formatDuration(max(now.Sub(ti.SubmittedAt), 0))
		switch {
		case ti.DeadlineDur > 0:
			it.elapsed = fmt.Sprintf("%s / %s", elapsed, formatDuration(ti.DeadlineDur))
		case ti.TurnDeadline != nil:
			it.elapsed = fmt.Sprintf("%s / %s", elapsed, formatDuration(ti.TurnDeadline.Sub(ti.SubmittedAt)))
		default:
			it.elapsed = elapsed
		}
	}

	switch {
	case ti.LimitTokens > 0:
		if !plainMode {
			it.meter = "[" + budgetMeter(ti.EstimatedTokens, ti.LimitTokens) + "]"
		}
		it.tokens = fmt.Sprintf("est. %d / %d", ti.EstimatedTokens, ti.LimitTokens)
	case ti.EstimatedTokens > 0:
		it.tokens = fmt.Sprintf("est. %d", ti.EstimatedTokens)
	}

	if ti.Round > 0 && ti.MaxRounds > 0 {
		it.round = fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds)
	}
	return it
}

// budgetMeter draws how much of the limit the estimate takes, in
// budgetMeterCells cells. The numbers beside it carry the same meaning.
func budgetMeter(est, limit int64) string {
	filled := int(min(max(est, 0)*budgetMeterCells/limit, budgetMeterCells))
	return strings.Repeat("█", filled) + strings.Repeat("░", budgetMeterCells-filled)
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
