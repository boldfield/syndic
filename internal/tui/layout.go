package tui

// Bounds describes the allocation of terminal lines to each UI region for a
// given terminal size, respecting v6 height priorities: tab bar, status line,
// content, input, and optional drawer, lists, and banners. All fields are
// non-negative and sum to at most height.
type Bounds struct {
	Height    int // terminal height
	Width     int // terminal width
	TabBar    int // tab bar: always 1 line
	Status    int // status line: always 1 line
	Content   int // active tab content: at least 5 lines if drawer/lists are shown
	Input     int // input viewport: 1 to 6 lines, scrolls internally beyond
	Banners   int // banners: at most 2 lines, may shrink to 1 when drawer forces
	Attached  int // attached line: 1 line if attachments exist (reserved in D-07)
	List      int // command or file list: at most 5 lines (closed when drawer opens)
	ShortHelp int // short help: 1 line (hidden when drawer is open)
	Drawer    int // reviews drawer: 7 lines or 9 above 30 rows (mutually exclusive with List)
}

// calcBounds allocates terminal height to each region according to v6 priority
// and minimum constraints. Terminal must be at least 80x24 for drawer/lists;
// below that they are disabled.
func calcBounds(height, width int) Bounds {
	b := Bounds{Height: height, Width: width, TabBar: 1, Status: 1}

	// Minimum viable terminal: tab bar + status line + at least 1 line of content
	if height < tabBarHeight+stripHeight+1 {
		return b
	}

	// Below 80x24 the drawer and lists do not open (v6 section 1).
	if width < 80 || height < 24 {
		// Minimal layout: just content and input, no drawer or lists.
		availableForContent := height - b.TabBar - b.Status
		// At least try to give 1 line of input and the rest to content.
		b.Input = 1
		b.Content = max(availableForContent-b.Input, 0)
		return b
	}

	// At 80x24+, allocate from the fixed regions inward.
	remaining := height - b.TabBar - b.Status

	// Banners: at most 2 lines
	b.Banners = min(2, remaining)
	remaining -= b.Banners

	// Attached line: 1 line (reserved in D-07, feature not yet present)
	// For now, do not consume space since there are no attachments in D-07.
	// Once attachments are shown, this will be `remaining -= 1` when needed.

	// Drawer and lists are mutually exclusive. They are shown only when there's
	// room for both them and minimum content (5 lines).
	// For D-07, drawer and lists are always closed, so they don't take space.
	// When they open in future tasks, the allocation will ensure content >= 5 lines.
	b.Drawer = 0
	b.List = 0
	b.ShortHelp = 0

	// Input: 1 to 6 lines, allocated last to leave maximum for content.
	// Reserve minimum 1 line for input.
	b.Input = min(6, remaining)
	remaining -= b.Input

	// Content gets remaining lines, but always at least 5 when possible
	b.Content = remaining

	return b
}

// ScrollInfo holds scroll position information for a region: how many lines
// are off-screen before the view, and what percentage of the total that represents.
type ScrollInfo struct {
	Above      int // lines above the view
	Percentage int // 0-100, rounded down; 100 at the last line
}

// calcScrollPosition computes scroll position given total lines, visible lines,
// and the first visible line. It returns the count of lines above and a percentage.
func calcScrollPosition(total, visible, firstLine int) ScrollInfo {
	if total <= 0 {
		return ScrollInfo{0, 0}
	}
	if visible <= 0 {
		return ScrollInfo{0, 0}
	}

	firstLine = max(0, min(firstLine, max(total-visible, 0)))

	percentage := 0
	if total > visible {
		percentage = (firstLine * 100) / (total - visible)
		// Clamp at 100 when at the end
		if firstLine+visible >= total {
			percentage = 100
		}
	}

	return ScrollInfo{firstLine, percentage}
}

// TurnLineMap maps turn indices to their starting line numbers in the rendered chat.
// turnStarts[i] is the line index where turn i begins; len(turnStarts) is the number of turns.
type TurnLineMap struct {
	turnStarts []int // line index where each turn begins
	totalLines int   // total number of lines
}

// countEarlierTurns returns the number of complete turns before the given line index.
func (m *TurnLineMap) countEarlierTurns(lineIndex int) int {
	// Find which turn contains this line
	for i := len(m.turnStarts) - 1; i >= 0; i-- {
		if lineIndex >= m.turnStarts[i] {
			return i
		}
	}
	return 0
}

// calcScrollPositionByTurns computes scroll position by turn count instead of line count.
// It takes the total number of lines, visible lines, the first visible line, and the turn boundaries.
func calcScrollPositionByTurns(total, visible, firstLine int, turns *TurnLineMap) ScrollInfo {
	if total <= 0 || visible <= 0 {
		return ScrollInfo{0, 0}
	}

	firstLine = max(0, min(firstLine, max(total-visible, 0)))

	// Count how many complete turns are above the view
	earlierTurns := turns.countEarlierTurns(firstLine)

	// Calculate percentage based on total lines
	percentage := 0
	if total > visible {
		percentage = (firstLine * 100) / (total - visible)
		// Clamp at 100 when at the end
		if firstLine+visible >= total {
			percentage = 100
		}
	}

	return ScrollInfo{earlierTurns, percentage}
}
