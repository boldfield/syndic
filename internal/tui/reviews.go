package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/x/ansi"
)

// ReviewsDrawer holds the state of the reviews drawer.
type ReviewsDrawer struct {
	open          bool     // whether the drawer is open
	selectedTurn  int      // index in turns list (not turn ID)
	selectedRound int      // index in rounds list
	turns         []string // turn IDs to display, oldest first
	reviewsByTurn map[string]*drawerReviews
	scrollOffset  int           // scroll position within the drawer content
	roster        []string      // participant IDs in roster order
	drawerHeight  int           // allocated height for rendering
	animStart     time.Time     // when animation started
	animDuration  time.Duration // how long animation should take
	animating     bool          // whether animation is in progress
	closing       bool          // whether we're animating a close
}

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

// Open opens the drawer on the given turn index with animation (unless in plain mode).
func (d *ReviewsDrawer) Open(turnIdx int, plainMode bool) {
	if turnIdx < 0 || turnIdx >= len(d.turns) {
		return
	}
	d.open = true
	d.selectedTurn = turnIdx
	d.selectedRound = 0
	d.scrollOffset = 0
	if !plainMode {
		d.animating = true
		d.animStart = time.Now()
		d.animDuration = 250 * time.Millisecond
	}
	d.closing = false
}

// Close closes the drawer with animation (unless in plain mode).
func (d *ReviewsDrawer) Close(plainMode bool) {
	d.open = false
	d.scrollOffset = 0
	if !plainMode {
		if !d.animating {
			d.animating = true
			d.animStart = time.Now()
			d.animDuration = 150 * time.Millisecond
			d.closing = true
		} else {
			// If already animating (open), continue and mark as closing
			d.closing = true
		}
	}
}

// IsOpen reports whether the drawer is open.
func (d *ReviewsDrawer) IsOpen() bool {
	return d.open
}

// AnimationTick advances the animation by checking elapsed time.
// Returns true if the animation is still in progress.
func (d *ReviewsDrawer) AnimationTick() bool {
	if !d.animating {
		return false
	}

	elapsed := time.Since(d.animStart)
	if elapsed >= d.animDuration {
		d.animating = false
		return false
	}

	return true
}

// getAnimProgress returns the current animation progress (0 to 1) based on elapsed time.
func (d *ReviewsDrawer) getAnimProgress() float64 {
	if !d.animating {
		if d.closing {
			return 0
		}
		if d.open {
			return 1
		}
		return 0
	}

	elapsed := time.Since(d.animStart)
	progress := float64(elapsed) / float64(d.animDuration)
	if progress > 1 {
		progress = 1
	}

	// Use cubic ease-in-out for smooth animation
	eased := easeInOutCubic(progress)

	if d.closing {
		// For close animation, reverse it
		return 1 - eased
	}
	return eased
}

// easeInOutCubic is a cubic easing function for smooth animation.
func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	t = 1 - t
	return 1 - (t * t * t / 2)
}

// IsAnimating reports whether the drawer is currently animating.
func (d *ReviewsDrawer) IsAnimating() bool {
	return d.animating
}

// AnimProgress returns the current animation progress (0 to 1) for testing.
func (d *ReviewsDrawer) AnimProgress() float64 {
	return d.getAnimProgress()
}

// setAnimProgressForTest advances the animation to a specific progress (0 to 1) for testing.
// This is only for testing purposes.
func (d *ReviewsDrawer) setAnimProgressForTest(progress float64) {
	if progress >= 1.0 {
		d.animating = false
	} else if progress <= 0 {
		d.animating = false
		d.closing = false
	} else {
		elapsed := time.Duration(float64(d.animDuration) * progress)
		d.animStart = time.Now().Add(-elapsed)
	}
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

// RoundClickPositions returns the x (cell) positions where each round's number starts in the header,
// for use in click handling. Returns a map from round index to x cell position.
func (d *ReviewsDrawer) RoundClickPositions(headerLine string) map[int]int {
	rounds := d.getRounds()
	if len(rounds) <= 1 {
		return nil
	}

	positions := make(map[int]int)

	pos := strings.Index(headerLine, "[")
	if pos < 0 {
		return positions
	}
	pos++ // Move past the opening bracket

	for i, r := range rounds {
		roundStr := fmt.Sprintf("%d", r+1)
		idx := strings.Index(headerLine[pos:], roundStr)
		if idx >= 0 {
			// Convert byte position to cell width by counting display width up to that point
			cellPos := ansi.StringWidth(headerLine[:pos+idx])
			positions[i] = cellPos
		}
	}

	return positions
}

// SelectRoundByClick selects the round at the given x coordinate, using the provided header line.
func (d *ReviewsDrawer) SelectRoundByClick(headerLine string, x int) {
	positions := d.RoundClickPositions(headerLine)
	if len(positions) == 0 {
		return
	}

	var selectedIdx int
	closestDist := 999
	for idx, xPos := range positions {
		dist := x - xPos
		if dist >= 0 && dist < closestDist {
			selectedIdx = idx
			closestDist = dist
		}
	}

	if closestDist < 999 {
		d.selectedRound = selectedIdx
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
// content from the cache, with animation effects (unless in plain mode).
func (d *ReviewsDrawer) render(w int, cc *ContentCache, plainMode bool) string {
	progress := d.getAnimProgress()

	// Don't render if fully closed
	if !d.open && progress <= 0 {
		return ""
	}

	lines := d.lines(w, cc)
	if d.scrollOffset < len(lines) {
		lines = lines[d.scrollOffset:]
	} else {
		lines = nil
	}

	// Apply animation: show only a portion of lines during animation (skip in plain mode)
	if !plainMode && d.animating && d.drawerHeight > 0 {
		visibleLines := int(float64(d.drawerHeight) * progress)
		if visibleLines < len(lines) {
			lines = lines[:visibleLines]
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
		roundStr := "["
		for i, r := range rounds {
			if i > 0 {
				roundStr += " "
			}
			if i == d.selectedRound {
				roundStr += fmt.Sprintf("*%d*", r+1)
			} else {
				roundStr += fmt.Sprintf("%d", r+1)
			}
		}
		roundStr += "]"
		header = fmt.Sprintf("Reviews · Turn %d · %s", d.selectedTurn+1, roundStr)
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
