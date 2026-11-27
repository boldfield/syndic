package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/x/ansi"
)

// ReviewsDrawer holds the state of the reviews drawer.
type ReviewsDrawer struct {
	open          bool     // whether the drawer is open
	selectedTurn  int      // index in turns list (not turn ID)
	selectedRound int      // index in rounds list
	turns         []string // turn IDs to display, oldest first
	reviewsByTurn map[string]*drawerReviews
	follow        Follow   // where the drawer stands: at the bottom, or holding a place by content
	roster        []string // participant IDs in roster order
	drawerHeight  int      // allocated height for rendering
	animating     bool     // a spring is still moving animPos toward its target
	animPos       float64  // how much of the drawer shows while animating, 0 to 1
	animVel       float64  // the spring's velocity

	rowsFn func() ([]string, []FollowRow) // tests only: stands in for the content's rows
}

// drawerSpring moves the drawer open and shut in a few tenths of a second
// without overshooting; a frame is one drawerFrameInterval.
var drawerSpring = harmonica.NewSpring(harmonica.FPS(60), 22, 1)

// drawerFrameInterval is the time between animation frames.
const drawerFrameInterval = time.Second / 60

// drawerSettle is how close to its target the spring must be to stop.
const drawerSettle = 0.02

// drawerReviews holds the reviews for one turn, grouped by round.
type drawerReviews struct {
	items  []wire.ReviewItem // all items for this turn
	rounds map[int][]int     // round -> indices into items
	err    string            // why the reviews could not be read, if they could not
}

// NewReviewsDrawer returns a new closed drawer.
func NewReviewsDrawer() *ReviewsDrawer {
	return &ReviewsDrawer{
		reviewsByTurn: make(map[string]*drawerReviews),
		roster:        []string{},
	}
}

// SetTurns updates the drawer's turn list.
// The selected turn stays selected when it is still listed, and the view keeps
// its place then; otherwise the selection is clamped and the view goes to the bottom.
func (d *ReviewsDrawer) SetTurns(turns []string) {
	prev := d.selectedTurnID()
	d.turns = turns
	if prev != "" && d.selectedTurn < len(d.turns) && d.turns[d.selectedTurn] == prev {
		return
	}
	for i, id := range d.turns {
		if id == prev {
			d.selectedTurn = i
			return
		}
	}
	if d.selectedTurn >= len(d.turns) {
		d.selectedTurn = max(0, len(d.turns)-1)
	}
	d.follow.End()
}

// SetRoster sets the participant ordering for the drawer from a list in roster order.
func (d *ReviewsDrawer) SetRoster(participants []string) {
	d.roster = append([]string{}, participants...)
}

// SetDrawerHeight sets the allocated height for the drawer.
func (d *ReviewsDrawer) SetDrawerHeight(height int) {
	d.drawerHeight = height
}

// Open opens the drawer on the given turn index, at the bottom of its content. It
// slides in unless plainMode is set, when it is whole at once.
func (d *ReviewsDrawer) Open(turnIdx int, plainMode bool) {
	if turnIdx < 0 || turnIdx >= len(d.turns) {
		return
	}
	from := d.progress()
	d.open = true
	d.selectedTurn = turnIdx
	d.selectedRound = 0
	d.follow.End()
	d.animPos, d.animVel = from, d.animVel*boolFloat(d.animating)
	d.animating = !plainMode && from < 1
}

// Close closes the drawer. It slides out unless plainMode is set, when it is
// gone at once; the keyboard is released either way.
func (d *ReviewsDrawer) Close(plainMode bool) {
	from := d.progress()
	d.open = false
	d.follow.End()
	d.animPos, d.animVel = from, d.animVel*boolFloat(d.animating)
	d.animating = !plainMode && from > 0
}

// ForceClose closes the drawer with no animation.
func (d *ReviewsDrawer) ForceClose() {
	d.Close(true)
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// IsOpen reports whether the drawer is open.
func (d *ReviewsDrawer) IsOpen() bool {
	return d.open
}

// Visible reports whether the drawer takes any screen lines: it is open, or
// still sliding shut.
func (d *ReviewsDrawer) Visible() bool {
	return d.open || d.animating
}

// IsAnimating reports whether the drawer is sliding.
func (d *ReviewsDrawer) IsAnimating() bool {
	return d.animating
}

// progress is how much of the drawer shows, 0 to 1.
func (d *ReviewsDrawer) progress() float64 {
	if d.animating {
		return d.animPos
	}
	return boolFloat(d.open)
}

// Step advances the slide by one frame and reports whether it is still moving.
// It reads no clock, so frames are the only thing that moves the drawer.
func (d *ReviewsDrawer) Step() bool {
	if !d.animating {
		return false
	}
	target := boolFloat(d.open)
	d.animPos, d.animVel = drawerSpring.Update(d.animPos, d.animVel, target)
	d.animPos = min(max(d.animPos, 0), 1)
	if diff := d.animPos - target; diff < drawerSettle && diff > -drawerSettle && d.animVel < drawerSettle && d.animVel > -drawerSettle {
		d.animating = false
		d.animPos, d.animVel = target, 0
	}
	return d.animating
}

// AnimProgress returns how much of the drawer shows, 0 to 1.
func (d *ReviewsDrawer) AnimProgress() float64 {
	return d.progress()
}

// UpdateReviews stores reviews for a turn, including decoded oversized records.
func (d *ReviewsDrawer) UpdateReviews(turnID string, items []wire.ReviewItem) {
	if len(items) == 0 {
		d.reviewsByTurn[turnID] = nil
		return
	}
	rounds := make(map[int][]int)
	for i, item := range items {
		rounds[item.Round] = append(rounds[item.Round], i)
	}
	d.reviewsByTurn[turnID] = &drawerReviews{
		items:  items,
		rounds: rounds,
	}
}

// SetReviewsError records that the turn's reviews could not be read.
func (d *ReviewsDrawer) SetReviewsError(turnID string, err error) {
	d.reviewsByTurn[turnID] = &drawerReviews{err: err.Error()}
}

// selectedTurnID returns the ID of the currently selected turn, or "".
func (d *ReviewsDrawer) selectedTurnID() string {
	if d.selectedTurn < 0 || d.selectedTurn >= len(d.turns) {
		return ""
	}
	return d.turns[d.selectedTurn]
}

// getRounds returns the sorted round numbers for the selected turn.
func (d *ReviewsDrawer) getRounds() []int {
	turnID := d.selectedTurnID()
	if turnID == "" || d.reviewsByTurn[turnID] == nil {
		return nil
	}
	rev := d.reviewsByTurn[turnID]
	rounds := make([]int, 0, len(rev.rounds))
	for r := range rev.rounds {
		rounds = append(rounds, r)
	}
	slices.Sort(rounds)
	return rounds
}

// SelectUp moves selection up one turn.
func (d *ReviewsDrawer) SelectUp() {
	if d.selectedTurn > 0 {
		d.selectedTurn--
		d.selectedRound = 0
		d.follow.End()
	}
}

// SelectDown moves selection down one turn.
func (d *ReviewsDrawer) SelectDown() {
	if d.selectedTurn < len(d.turns)-1 {
		d.selectedTurn++
		d.selectedRound = 0
		d.follow.End()
	}
}

// SelectRoundLeft moves to the previous round.
func (d *ReviewsDrawer) SelectRoundLeft() {
	rounds := d.getRounds()
	if len(rounds) == 0 {
		return
	}
	if d.selectedRound > 0 {
		d.selectedRound--
		d.follow.End()
	}
}

// SelectRoundRight moves to the next round.
func (d *ReviewsDrawer) SelectRoundRight() {
	rounds := d.getRounds()
	if len(rounds) == 0 {
		return
	}
	if d.selectedRound < len(rounds)-1 {
		d.selectedRound++
		d.follow.End()
	}
}

// span is a run of cells in a line: from start up to, not including, end.
type span struct{ start, end int }

// roundsHeader is the bracketed list of round numbers, the selected one
// starred, with the cells each number takes within it. It is ASCII, so cells
// and bytes agree.
func roundsHeader(rounds []int, selected int) (string, []span) {
	var b strings.Builder
	spans := make([]span, len(rounds))
	b.WriteString("[")
	for i, r := range rounds {
		if i > 0 {
			b.WriteString(" ")
		}
		start := b.Len()
		if i == selected {
			fmt.Fprintf(&b, "*%d*", r+1)
		} else {
			fmt.Fprintf(&b, "%d", r+1)
		}
		spans[i] = span{start, b.Len()}
	}
	b.WriteString("]")
	return b.String(), spans
}

// headerPrefix is the header's text before the round list.
func (d *ReviewsDrawer) headerPrefix() string {
	return fmt.Sprintf("Reviews · Turn %d · ", d.selectedTurn+1)
}

// RoundAt returns the index of the round whose number is under cell x of the
// drawer's header line, if the header lists several rounds and a number is
// there.
func (d *ReviewsDrawer) RoundAt(x, w int) (int, bool) {
	rounds := d.getRounds()
	if len(rounds) <= 1 || x < 0 || x >= w {
		return 0, false
	}
	_, spans := roundsHeader(rounds, d.selectedRound)
	base := ansi.StringWidth(d.headerPrefix())
	for i, sp := range spans {
		if x >= base+sp.start && x < base+sp.end {
			return i, true
		}
	}
	return 0, false
}

// SelectRound selects the round at index i of the turn's rounds.
func (d *ReviewsDrawer) SelectRound(i int) {
	if i >= 0 && i < len(d.getRounds()) {
		d.selectedRound = i
		d.follow.End()
	}
}

// Scroll moves the drawer n rows down, or up when n is negative, through the follow
// model at width w.
func (d *ReviewsDrawer) Scroll(n, w int, cc *ContentCache) {
	d.layout(w, cc)
	d.follow.Scroll(n)
}

// Home moves the drawer to its first row.
func (d *ReviewsDrawer) Home(w int, cc *ContentCache) {
	d.layout(w, cc)
	d.follow.Home()
}

// End puts the drawer at the bottom.
func (d *ReviewsDrawer) End() { d.follow.End() }

// AtBottom reports whether the drawer showed its last row at its last layout.
func (d *ReviewsDrawer) AtBottom() bool { return d.follow.AtBottom() }

// viewRows is how many rows of content the drawer shows: its height less the header line.
func (d *ReviewsDrawer) viewRows() int { return max(d.drawerHeight-1, 1) }

// layout places the view on the content as it is now at width w: the header line, the
// content rows, the first row shown, whether the view is at the bottom, and the count of
// new rows.
func (d *ReviewsDrawer) layout(w int, cc *ContentCache) (header string, body []string, first int, bottom bool, count int) {
	header, body, rows := d.content(w, cc)
	height := d.viewRows()
	if d.drawerHeight <= 0 {
		height = max(len(rows), 1) // no height allotted: every row shows
	}
	first, bottom, count = d.follow.Layout(rows, height)
	return header, body, first, bottom, count
}

// ContentLines returns the number of lines the drawer's content takes at width w.
func (d *ReviewsDrawer) ContentLines(w int, cc *ContentCache) int {
	return len(d.lines(w, cc))
}

// SelectedTurnIndex returns the index of the currently selected turn.
func (d *ReviewsDrawer) SelectedTurnIndex() int {
	return d.selectedTurn
}

// render renders the drawer at width w, using content from the cache: the header line,
// with at its right the count of new rows and where the view stands, then the rows from
// the first one shown. While it slides, only the top part of the lines shows; plainMode
// never slides.
func (d *ReviewsDrawer) render(w int, cc *ContentCache, plainMode bool) string {
	if !d.Visible() {
		return ""
	}

	header, body, first, bottom, count := d.layout(w, cc)
	if d.selectedTurnID() != "" {
		info := fmt.Sprintf("%d%%", scrollPercent(len(body), d.viewRows(), first))
		if !bottom && count > 0 {
			info = fmt.Sprintf("↓ %d new lines · %s", count, info)
		}
		header = withScrollInfo(header, w, info)
	}
	body = body[min(first, len(body)):]
	if d.drawerHeight > 0 {
		body = body[:min(len(body), d.viewRows())]
	}
	lines := append([]string{header}, body...)

	if !plainMode && d.animating && d.drawerHeight > 0 {
		visible := int(d.animPos*float64(d.drawerHeight) + 0.5)
		if visible < len(lines) {
			lines = lines[:visible]
		}
	}

	return strings.Join(lines, "\n")
}

// lines is the drawer's whole content at width w: the header line, then the rows.
func (d *ReviewsDrawer) lines(w int, cc *ContentCache) []string {
	header, body, _ := d.content(w, cc)
	return append([]string{header}, body...)
}

// content is the drawer's content at width w: a header naming the turn and its rounds,
// then the rows, which are each reviewer of the selected round in roster order. For every
// row it also gives the item it belongs to, a reviewer or a note, and the characters of
// that item it shows. A row's characters are its runes, and an empty row counts as one.
func (d *ReviewsDrawer) content(w int, cc *ContentCache) (string, []string, []FollowRow) {
	if d.rowsFn != nil {
		lines, rows := d.rowsFn()
		return "Reviews · Turn 1", lines, rows
	}
	var lines []string
	var rows []FollowRow
	chars := map[string]int{}
	// addText adds a row whose characters are those after the first skip bytes of it: the
	// indentation a wrapped row repeats is not content, so it does not count.
	addText := func(item, line string, skip int) {
		line = ansi.Truncate(line, w, "")
		lines = append(lines, line)
		text := ansi.Strip(line)
		if len(text) >= skip {
			text = text[skip:]
		}
		n := max(utf8.RuneCountInString(text), 1)
		c := chars[item]
		rows = append(rows, FollowRow{Item: item, Char: c, End: c + n})
		chars[item] = c + n
	}
	add := func(item, line string) { addText(item, line, 0) }
	turnID := d.selectedTurnID()
	if turnID == "" {
		return strings.Repeat("─", w), nil, nil
	}
	header := fmt.Sprintf("Reviews · Turn %d", d.selectedTurn+1)
	rev, fetched := d.reviewsByTurn[turnID]
	rounds := d.getRounds()
	switch {
	case !fetched:
		add("note", "  loading…")
		return ansi.Truncate(header, w, ""), lines, rows
	case rev != nil && rev.err != "":
		add("note", "  reviews unavailable: "+safeLine(rev.err))
		return ansi.Truncate(header, w, ""), lines, rows
	case len(rounds) == 0:
		add("note", "  no reviews")
		return ansi.Truncate(header, w, ""), lines, rows
	}
	if d.selectedRound >= len(rounds) {
		d.selectedRound = 0
	}

	selectedRoundNum := rounds[d.selectedRound]
	itemIndices := rev.rounds[selectedRoundNum]

	header += fmt.Sprintf(" · Round %d", selectedRoundNum+1)
	if len(rounds) > 1 {
		list, _ := roundsHeader(rounds, d.selectedRound)
		header = d.headerPrefix() + list
	}
	header = ansi.Truncate(header, w, "")

	// Get items for this round
	itemsByParticipant := make(map[string]*wire.ReviewItem)
	for _, idx := range itemIndices {
		if idx >= 0 && idx < len(rev.items) {
			item := rev.items[idx]
			itemsByParticipant[item.Participant] = &item
		}
	}

	// Sort participants by roster order
	var sortedParticipants []string
	for _, p := range d.roster {
		if _, ok := itemsByParticipant[p]; ok {
			sortedParticipants = append(sortedParticipants, p)
		}
	}
	// Add any participants not in roster, sorted for determinism
	var missing []string
	for p := range itemsByParticipant {
		if !slices.Contains(sortedParticipants, p) {
			missing = append(missing, p)
		}
	}
	slices.Sort(missing)
	sortedParticipants = append(sortedParticipants, missing...)

	// Render each participant's verdict. Oversized items were already
	// replaced by their verified records when they were fetched.
	for _, participant := range sortedParticipants {
		item := itemsByParticipant[participant]
		id := "reviewer:" + participant
		// content appends a labelled content reference once it is fetched.
		content := func(label string, ref *wire.ContentReference, indent int) {
			if ref == nil {
				return
			}
			ci := cc.Get(ref.Ref)
			if !ci.Ready || ci.Err != nil {
				return
			}
			add(id, strings.Repeat(" ", indent)+label+":")
			for _, line := range SafeTextWithPrefix(ci.Data, w-indent-2) {
				addText(id, line, len(contentPrefix))
			}
		}
		add(id, fmt.Sprintf("  %s (%s)", safeLine(participant), safeLine(item.Kind)))
		for _, block := range item.Blocks {
			add(id, fmt.Sprintf("    • %s: %s", safeLine(block.BlockID), safeLine(block.State)))
			content("evidence", block.Evidence, 6)
			content("remedy", block.Remedy, 6)
		}
		content("commentary", item.Commentary, 4)
	}
	return header, lines, rows
}
