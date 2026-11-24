package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

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
	scrollOffset  int      // scroll position within the drawer content
	roster        []string // participant IDs in roster order
	drawerHeight  int      // allocated height for rendering
	animating     bool     // a spring is still moving animPos toward its target
	animPos       float64  // how much of the drawer shows while animating, 0 to 1
	animVel       float64  // the spring's velocity
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
// The selected turn stays selected when it is still listed, and the scroll
// position is kept then; otherwise the selection is clamped and scroll resets.
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
	d.scrollOffset = 0
}

// SetRoster sets the participant ordering for the drawer from a list in roster order.
func (d *ReviewsDrawer) SetRoster(participants []string) {
	d.roster = append([]string{}, participants...)
}

// SetDrawerHeight sets the allocated height for the drawer.
func (d *ReviewsDrawer) SetDrawerHeight(height int) {
	d.drawerHeight = height
}

// Open opens the drawer on the given turn index. It slides in unless plainMode
// is set, when it is whole at once.
func (d *ReviewsDrawer) Open(turnIdx int, plainMode bool) {
	if turnIdx < 0 || turnIdx >= len(d.turns) {
		return
	}
	from := d.progress()
	d.open = true
	d.selectedTurn = turnIdx
	d.selectedRound = 0
	d.scrollOffset = 0
	d.animPos, d.animVel = from, d.animVel*boolFloat(d.animating)
	d.animating = !plainMode && from < 1
}

// Close closes the drawer. It slides out unless plainMode is set, when it is
// gone at once; the keyboard is released either way.
func (d *ReviewsDrawer) Close(plainMode bool) {
	from := d.progress()
	d.open = false
	d.scrollOffset = 0
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
		d.scrollOffset = 0
	}
}

// SelectDown moves selection down one turn.
func (d *ReviewsDrawer) SelectDown() {
	if d.selectedTurn < len(d.turns)-1 {
		d.selectedTurn++
		d.selectedRound = 0
		d.scrollOffset = 0
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
		d.scrollOffset = 0
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
		d.scrollOffset = 0
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
// there. The header scrolls with the content, so it is only there at the top.
func (d *ReviewsDrawer) RoundAt(x, w int) (int, bool) {
	rounds := d.getRounds()
	if len(rounds) <= 1 || d.scrollOffset != 0 || x < 0 || x >= w {
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
		d.scrollOffset = 0
	}
}

// ScrollUp scrolls the drawer content up by one line.
func (d *ReviewsDrawer) ScrollUp(lines int) {
	d.scrollOffset = max(d.scrollOffset-lines, 0)
}

// ScrollDown scrolls the drawer content down by a page.
func (d *ReviewsDrawer) ScrollDown(lines int, maxContent int) {
	viewport := d.drawerHeight
	if viewport <= 0 {
		viewport = lines
	}
	maxScroll := max(0, maxContent-viewport)
	d.scrollOffset = min(d.scrollOffset+lines, maxScroll)
}

// ContentLines returns the number of lines the drawer's content takes at width w.
func (d *ReviewsDrawer) ContentLines(w int, cc *ContentCache) int {
	return len(d.lines(w, cc))
}

// SelectedTurnIndex returns the index of the currently selected turn.
func (d *ReviewsDrawer) SelectedTurnIndex() int {
	return d.selectedTurn
}

// render renders the drawer at width w from its scroll position, using
// content from the cache. While it slides, only the top part of the lines
// shows; plainMode never slides.
func (d *ReviewsDrawer) render(w int, cc *ContentCache, plainMode bool) string {
	if !d.Visible() {
		return ""
	}

	lines := d.lines(w, cc)
	if d.scrollOffset < len(lines) {
		lines = lines[d.scrollOffset:]
	} else {
		lines = nil
	}

	if !plainMode && d.animating && d.drawerHeight > 0 {
		visible := int(d.animPos*float64(d.drawerHeight) + 0.5)
		if visible < len(lines) {
			lines = lines[:visible]
		}
	}

	return strings.Join(lines, "\n")
}

// lines is the drawer's whole content at width w: a header naming the turn
// and its rounds, then each reviewer of the selected round in roster order.
func (d *ReviewsDrawer) lines(w int, cc *ContentCache) []string {
	turnID := d.selectedTurnID()
	if turnID == "" {
		return []string{strings.Repeat("─", w)}
	}
	header := fmt.Sprintf("Reviews · Turn %d", d.selectedTurn+1)
	rev, fetched := d.reviewsByTurn[turnID]
	rounds := d.getRounds()
	switch {
	case !fetched:
		return []string{ansi.Truncate(header, w, ""), "  loading…"}
	case rev != nil && rev.err != "":
		return []string{ansi.Truncate(header, w, ""), ansi.Truncate("  reviews unavailable: "+safeLine(rev.err), w, "")}
	case len(rounds) == 0:
		return []string{ansi.Truncate(header, w, ""), "  no reviews"}
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

	lines := []string{ansi.Truncate(header, w, "")}

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

	// content appends a labelled content reference once it is fetched.
	content := func(label string, ref *wire.ContentReference, indent int) {
		if ref == nil {
			return
		}
		ci := cc.Get(ref.Ref)
		if !ci.Ready || ci.Err != nil {
			return
		}
		lines = append(lines, ansi.Truncate(strings.Repeat(" ", indent)+label+":", w, ""))
		for _, line := range SafeTextWithPrefix(ci.Data, w-indent-2) {
			lines = append(lines, ansi.Truncate(line, w, ""))
		}
	}

	// Render each participant's verdict. Oversized items were already
	// replaced by their verified records when they were fetched.
	for _, participant := range sortedParticipants {
		item := itemsByParticipant[participant]
		itemLine := fmt.Sprintf("  %s (%s)", safeLine(participant), safeLine(item.Kind))
		lines = append(lines, ansi.Truncate(itemLine, w, ""))
		for _, block := range item.Blocks {
			blockLine := fmt.Sprintf("    • %s: %s", safeLine(block.BlockID), safeLine(block.State))
			lines = append(lines, ansi.Truncate(blockLine, w, ""))
			content("evidence", block.Evidence, 6)
			content("remedy", block.Remedy, 6)
		}
		content("commentary", item.Commentary, 4)
	}
	return lines
}
