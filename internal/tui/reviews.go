package tui

import (
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
	scrollOffset  int            // scroll position within the drawer content
	contentLines  int            // cached line count for paging calculations
	roster        map[string]int // participant -> order in roster
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
		roster:        make(map[string]int),
	}
}

// SetTurns updates the drawer's turn list.
func (d *ReviewsDrawer) SetTurns(turns []string) {
	d.turns = turns
	// Keep selectedTurn in bounds
	if d.selectedTurn >= len(d.turns) {
		d.selectedTurn = max(0, len(d.turns)-1)
	}
	d.scrollOffset = 0
}

// SetRoster sets the participant ordering for the drawer.
func (d *ReviewsDrawer) SetRoster(participants []string) {
	d.roster = make(map[string]int)
	for i, p := range participants {
		d.roster[p] = i
	}
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

// UpdateReviews stores reviews for a turn.
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

// ScrollUp scrolls the drawer content up.
func (d *ReviewsDrawer) ScrollUp(lines int) {
	d.scrollOffset = max(d.scrollOffset-lines, 0)
}

// ScrollDown scrolls the drawer content down.
func (d *ReviewsDrawer) ScrollDown(lines int, maxContent int) {
	maxScroll := max(0, maxContent-lines)
	d.scrollOffset = min(d.scrollOffset+lines, maxScroll)
}

// ContentLines returns the total number of lines in the drawer content for paging.
func (d *ReviewsDrawer) ContentLines() int {
	return d.contentLines
}

// renderDrawer renders the drawer at a given width and height.
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

	// Add review items in roster order
	itemsByParticipant := make(map[string]*wire.ReviewItem)
	participants := []string{}
	for i := range itemIndices {
		idx := itemIndices[i]
		item := rev.items[idx]
		itemsByParticipant[item.Participant] = &rev.items[idx]
		if !slices.Contains(participants, item.Participant) {
			participants = append(participants, item.Participant)
		}
	}

	// Sort by roster order
	slices.SortFunc(participants, func(a, b string) int {
		orderA := d.roster[a]
		orderB := d.roster[b]
		return orderA - orderB
	})

	for _, participant := range participants {
		item := itemsByParticipant[participant]
		itemLine := fmt.Sprintf("  %s (%s)", safeText(participant), item.Kind)
		lines = append(lines, ansi.Truncate(itemLine, w, ""))

		// Show blocks
		for _, block := range item.Blocks {
			blockID := safeText(block.BlockID)
			blockLine := fmt.Sprintf("    • %s: %s", blockID, block.State)
			lines = append(lines, ansi.Truncate(blockLine, w, ""))

			// Show evidence and remedy refs if present (fetched content not shown here, just refs)
			if block.Evidence != nil {
				lines = append(lines, ansi.Truncate(fmt.Sprintf("      evidence: %d bytes", block.Evidence.ByteLen), w, ""))
			}
			if block.Remedy != nil {
				lines = append(lines, ansi.Truncate(fmt.Sprintf("      remedy: %d bytes", block.Remedy.ByteLen), w, ""))
			}
		}

		// Show commentary if present
		if item.Commentary != nil {
			ci := cc.Get(item.Commentary.Ref)
			if ci.Ready && ci.Err == nil && ci.Data != "" {
				commentText := safeText(ci.Data)
				commentText = "commentary: " + commentText
				wrapped := wrapText(commentText, w-6)
				for _, line := range wrapped {
					lines = append(lines, ansi.Truncate("      "+line, w, ""))
				}
			} else if !ci.Ready {
				lines = append(lines, ansi.Truncate("      commentary: <loading>", w, ""))
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

// safeText converts content to safe text by replacing control characters and invalid UTF-8 with escapes.
func safeText(s string) string {
	var result strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7F && r != 0x7F {
			result.WriteRune(r)
		} else if r == '\n' || r == '\t' {
			result.WriteRune(r)
		} else {
			fmt.Fprintf(&result, "\\x%02x", r)
		}
	}
	return result.String()
}
