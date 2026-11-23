package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
	scrollOffset  int      // scroll position within the drawer content
	contentLines  int      // cached line count for paging calculations
	roster        []string // participant IDs in roster order
	drawerHeight  int      // allocated height for rendering
}

// drawerReviews holds the reviews for one turn, grouped by round.
type drawerReviews struct {
	items  []wire.ReviewItem // all items for this turn
	rounds map[int][]int     // round -> indices into items
}

// NewReviewsDrawer returns a new closed drawer.
func NewReviewsDrawer() *ReviewsDrawer {
	return &ReviewsDrawer{
		reviewsByTurn: make(map[string]*drawerReviews),
		roster:        []string{},
	}
}

// SetTurns updates the drawer's turn list.
func (d *ReviewsDrawer) SetTurns(turns []string) {
	d.turns = turns
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

// Open opens the drawer on the given turn index.
func (d *ReviewsDrawer) Open(turnIdx int) {
	if turnIdx < 0 || turnIdx >= len(d.turns) {
		return
	}
	d.open = true
	d.selectedTurn = turnIdx
	d.selectedRound = 0
	d.scrollOffset = 0
}

// Close closes the drawer.
func (d *ReviewsDrawer) Close() {
	d.open = false
	d.scrollOffset = 0
}

// IsOpen reports whether the drawer is open.
func (d *ReviewsDrawer) IsOpen() bool {
	return d.open
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

// ContentLines returns the total number of lines in the drawer content for paging.
func (d *ReviewsDrawer) ContentLines() int {
	return d.contentLines
}

// SelectedTurnIndex returns the index of the currently selected turn.
func (d *ReviewsDrawer) SelectedTurnIndex() int {
	return d.selectedTurn
}

// render renders the drawer at a given width and height, using content from cache.
func (d *ReviewsDrawer) render(w int, cc *ContentCache) string {
	if !d.open {
		return ""
	}
	turnID := d.selectedTurnID()
	if turnID == "" {
		return strings.Repeat("─", w)
	}
	rev := d.reviewsByTurn[turnID]
	if rev == nil {
		return strings.Repeat("─", w)
	}

	rounds := d.getRounds()
	if len(rounds) == 0 {
		return strings.Repeat("─", w)
	}
	if d.selectedRound >= len(rounds) {
		d.selectedRound = 0
	}

	selectedRoundNum := rounds[d.selectedRound]
	itemIndices := rev.rounds[selectedRoundNum]

	// Build header
	header := fmt.Sprintf("Reviews · Turn %d · Round %d", d.selectedTurn+1, selectedRoundNum+1)
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
	header = ansi.Truncate(header, w, "")

	var lines []string
	lines = append(lines, header)

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
	// Add any participants not in roster
	for p := range itemsByParticipant {
		if !slices.Contains(sortedParticipants, p) {
			sortedParticipants = append(sortedParticipants, p)
		}
	}

	// Render each participant's verdict
	for _, participant := range sortedParticipants {
		item := itemsByParticipant[participant]
		itemLine := fmt.Sprintf("  %s (%s)", safeLine(participant), item.Kind)
		lines = append(lines, ansi.Truncate(itemLine, w, ""))

		// Handle oversized record
		if item.Oversized && item.Record != nil {
			ci := cc.Get(item.Record.Ref)
			if ci.Ready && ci.Err == nil {
				var fullItem wire.ReviewItem
				if err := json.Unmarshal([]byte(ci.Data), &fullItem); err == nil {
					item = &fullItem
				}
			}
		}

		// Show blocks
		for _, block := range item.Blocks {
			blockID := safeLine(block.BlockID)
			blockLine := fmt.Sprintf("    • %s: %s", blockID, block.State)
			lines = append(lines, ansi.Truncate(blockLine, w, ""))

			// Show evidence if present
			if block.Evidence != nil {
				ci := cc.Get(block.Evidence.Ref)
				if ci.Ready && ci.Err == nil {
					lines = append(lines, ansi.Truncate("      evidence:", w, ""))
					for _, line := range SafeTextWithPrefix(ci.Data, w-8) {
						lines = append(lines, ansi.Truncate(line, w, ""))
					}
				}
			}

			// Show remedy if present
			if block.Remedy != nil {
				ci := cc.Get(block.Remedy.Ref)
				if ci.Ready && ci.Err == nil {
					lines = append(lines, ansi.Truncate("      remedy:", w, ""))
					for _, line := range SafeTextWithPrefix(ci.Data, w-8) {
						lines = append(lines, ansi.Truncate(line, w, ""))
					}
				}
			}
		}

		// Show commentary if present
		if item.Commentary != nil {
			ci := cc.Get(item.Commentary.Ref)
			if ci.Ready && ci.Err == nil {
				lines = append(lines, ansi.Truncate("    commentary:", w, ""))
				for _, line := range SafeTextWithPrefix(ci.Data, w-6) {
					lines = append(lines, ansi.Truncate(line, w, ""))
				}
			}
		}
	}

	d.contentLines = len(lines)

	// Apply scroll offset and return
	if d.scrollOffset < len(lines) {
		lines = lines[d.scrollOffset:]
	} else {
		lines = nil
	}

	return strings.Join(lines, "\n")
}
