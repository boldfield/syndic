package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// renderStrip returns the status fields alone, width cells at most: preset,
// the next preset, quick:<name> when one governs, the turn state or idle,
// elapsed/turn_deadline, the budget meter with tokens/limit, and round n/m
// during a revision. Participant states are on the chat's progress line and
// in the participants tab, not here.
func renderStrip(s State, width int, now time.Time) string {
	return statusFieldsOf(s, now).fit(width, false, false)
}

// renderStatusLine is the strip with the right-edge hints, exactly width
// cells when they fit: ctrl+o: reviews when reviews is set, then keyHint.
func renderStatusLine(s State, width int, now time.Time, reviews bool) string {
	return statusFieldsOf(s, now).fit(width, true, reviews)
}

// reviewsHint names the reviews drawer's key; it sits left of keyHint while
// the chat tab is active.
const reviewsHint = "ctrl+o: reviews"

// keyHint sits at the right edge of the status line.
const keyHint = "tab: switch  /help"

// budgetMeterCells is how many cells the budget meter takes.
const budgetMeterCells = 8

// statusFields are the status line's fields, each already escaped. The
// overflow order names them, so none is found by position.
type statusFields struct {
	fixed   string   // a whole line in place of the fields, such as engine: connecting
	lead    []string // preset, next preset and quick profile, bracketed
	state   string   // the turn state or idle, unbracketed; "" when nothing is known
	elapsed string   // [elapsed / deadline], or ""
	meter   string   // the budget meter, or "" without a limit
	tokens  string   // est. tokens / limit, or est. tokens; "" when unknown
	round   string   // [round n/m] during a revision, or ""
}

func statusFieldsOf(s State, now time.Time) statusFields {
	var f statusFields
	if s.ConnState == ConnStateConnecting {
		f.fixed = "engine: connecting"
		return f
	}
	next := func() {
		if s.NextPreset != "" {
			f.lead = append(f.lead, safeLine(fmt.Sprintf("[next: %s]", s.NextPreset)))
		}
	}

	if s.CurrentTurn == "" {
		if s.Status != nil {
			f.lead = append(f.lead, safeLine(fmt.Sprintf("[%s]", s.Status.DefaultPreset)))
			next()
			f.state = "idle"
		}
		return f
	}
	ti, ok := s.Turns[s.CurrentTurn]
	if !ok {
		return f
	}

	f.lead = append(f.lead, safeLine(fmt.Sprintf("[%s]", ti.Preset)))
	next()
	if ti.QuickProfile != nil && *ti.QuickProfile != "" {
		if ti.ModeOverride {
			f.lead = append(f.lead, safeLine(fmt.Sprintf("[quick:%s (mode set by /mode)]", *ti.QuickProfile)))
		} else {
			f.lead = append(f.lead, safeLine(fmt.Sprintf("[quick:%s]", *ti.QuickProfile)))
		}
	}
	f.state = safeLine(ti.State)

	if !ti.SubmittedAt.IsZero() {
		elapsed := formatDuration(max(now.Sub(ti.SubmittedAt), 0))
		switch {
		case ti.DeadlineDur > 0:
			f.elapsed = fmt.Sprintf("[%s / %s]", elapsed, formatDuration(ti.DeadlineDur))
		case ti.TurnDeadline != nil:
			f.elapsed = fmt.Sprintf("[%s / %s]", elapsed, formatDuration(ti.TurnDeadline.Sub(ti.SubmittedAt)))
		default:
			f.elapsed = fmt.Sprintf("[%s]", elapsed)
		}
	}

	switch {
	case ti.LimitTokens > 0:
		f.meter = budgetMeter(ti.EstimatedTokens, ti.LimitTokens)
		f.tokens = fmt.Sprintf("est. %d / %d", ti.EstimatedTokens, ti.LimitTokens)
	case ti.EstimatedTokens > 0:
		f.tokens = fmt.Sprintf("est. %d", ti.EstimatedTokens)
	}

	if ti.Round > 0 && ti.MaxRounds > 0 {
		f.round = fmt.Sprintf("[round %d/%d]", ti.Round, ti.MaxRounds)
	}
	return f
}

// budgetMeter draws how much of the limit the estimate takes, in
// budgetMeterCells cells. The numbers beside it carry the same meaning.
func budgetMeter(est, limit int64) string {
	filled := int(min(max(est, 0)*budgetMeterCells/limit, budgetMeterCells))
	return strings.Repeat("█", filled) + strings.Repeat("░", budgetMeterCells-filled)
}

// statusLayout says which optional parts a candidate line keeps.
type statusLayout struct {
	meter, reviews, round bool
	noTokens              bool   // leaves the budget out, for the last resort that places it itself
	state                 string // the turn state as shown
}

// left joins the fields the layout keeps.
func (f statusFields) left(l statusLayout) string { return strings.Join(f.segments(l), " ") }

// segments are the fields the layout keeps, in order.
func (f statusFields) segments(l statusLayout) []string {
	segs := append([]string(nil), f.lead...)
	if f.state != "" {
		segs = append(segs, "["+l.state+"]")
	}
	if f.elapsed != "" {
		segs = append(segs, f.elapsed)
	}
	if b := f.budget(l.meter); b != "" && !l.noTokens {
		segs = append(segs, b)
	}
	if l.round && f.round != "" {
		segs = append(segs, f.round)
	}
	return segs
}

func (f statusFields) budget(meter bool) string {
	switch {
	case f.tokens == "":
		return ""
	case meter && f.meter != "":
		return "[" + f.meter + " " + f.tokens + "]"
	default:
		return "[" + f.tokens + "]"
	}
}

// fit lays the fields out in width cells. With hint the line ends with the
// right-edge hints, reviewsHint first when reviews is set. When the line is
// too wide it sheds, in the operator's order: the budget meter, keeping
// tokens/limit; then ctrl+o: reviews; then round n/m; then it shortens the
// turn state with an ellipsis. keyHint and tokens/limit are never shed.
func (f statusFields) fit(width int, hint, reviews bool) string {
	withReviews := hint && reviews
	right := func(r bool) string {
		switch {
		case !hint:
			return ""
		case r:
			return reviewsHint + "  " + keyHint
		default:
			return keyHint
		}
	}
	// room is the width the fields may take beside the hints.
	room := func(r bool) int {
		if !hint {
			return width
		}
		return width - runewidth.StringWidth(right(r)) - 1
	}
	if f.fixed != "" {
		r := withReviews && runewidth.StringWidth(f.fixed) <= room(true)
		return f.join(runewidth.Truncate(f.fixed, max(room(r), 0), "…"), right(r), width)
	}

	steps := []statusLayout{
		{meter: true, reviews: withReviews, round: true},
		{reviews: withReviews, round: true},
		{round: true},
		{},
	}
	for _, l := range steps {
		l.state = f.state
		if line := f.left(l); runewidth.StringWidth(line) <= room(l.reviews) {
			return f.join(line, right(l.reviews), width)
		}
	}

	// Shorten the turn state as far as it must go, down to a lone ellipsis.
	r := room(false)
	if f.state != "" {
		bare := runewidth.StringWidth(f.left(statusLayout{}))
		if avail := r - bare; avail >= 1 {
			return f.join(f.left(statusLayout{state: runewidth.Truncate(f.state, avail, "…")}), right(false), width)
		}
	}

	// Below what the ruling covers: keep tokens/limit whole and drop whole
	// fields from the right of what stands before it, marking the cut with …
	budget := f.budget(false)
	avail := r
	if budget != "" {
		avail -= runewidth.StringWidth(budget) + 1
	}
	if avail < 1 {
		return f.join(runewidth.Truncate(budget, max(r, 0), "…"), right(false), width)
	}
	rest := f.segments(statusLayout{state: "…", noTokens: true})
	for len(rest) > 0 && runewidth.StringWidth(strings.Join(rest, " "))+1 > avail {
		rest = rest[:len(rest)-1]
	}
	line := strings.Join(rest, " ") + "…"
	if budget != "" {
		line += " " + budget
	}
	return f.join(line, right(false), width)
}

// join puts the hints at the right edge of a width-cell line, one cell at
// least from the fields. Without hints it is the fields alone.
func (f statusFields) join(line, right string, width int) string {
	if right == "" {
		return line
	}
	if width < runewidth.StringWidth(right) {
		return runewidth.Truncate(right, max(width, 0), "")
	}
	if line == "" {
		return strings.Repeat(" ", max(width-runewidth.StringWidth(right), 0)) + right
	}
	return line + strings.Repeat(" ", max(width-runewidth.StringWidth(line)-runewidth.StringWidth(right), 1)) + right
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
