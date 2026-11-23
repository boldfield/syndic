package tui

// RegionState describes which optional regions are currently shown.
type RegionState struct {
	DrawerOpen bool
	ListOpen   bool
	HelpOpen   bool
}

// Bounds describes the allocation of terminal lines to each UI region for a
// given terminal size, respecting v6 height priorities: tab bar, status line,
// content, input, and optional drawer, lists, and banners. All fields are
// non-negative.
type Bounds struct {
	Height    int // terminal height
	Width     int // terminal width
	TabBar    int // tab bar: always 1 line
	Status    int // status line: always 1 line
	Content   int // active tab content: minimum 5 lines when drawer/list open
	Input     int // input viewport: 1 to 6 lines visible (may scroll beyond)
	Banners   int // banners: at most 2 lines, shrinks to 1 when drawer forces
	Attached  int // attached line: 1 line reserved (D-07)
	List      int // command or file list: at most 5 lines (closed when drawer opens)
	ShortHelp int // short help: 1 line (hidden when drawer is open)
	Drawer    int // reviews drawer: 7 lines or 9 above 30 rows (mutually exclusive with List)
}

// calcBounds allocates terminal height to each region according to v6 priority
// and minimum constraints, taking into account which regions are currently open.
// Terminal must be at least 80x24 for drawer/lists; below that they are disabled.
func calcBounds(height, width int, state RegionState) Bounds {
	b := Bounds{Height: height, Width: width, TabBar: 1, Status: 1}

	// Minimum viable terminal: tab bar + status line + at least 1 line of content
	if height < tabBarHeight+stripHeight+1 {
		return b
	}

	// Below 80x24 the drawer and lists do not open (v6 section 1).
	if width < 80 || height < 24 {
		// Minimal layout: just content and input, no drawer or lists.
		availableForContent := height - b.TabBar - b.Status
		// Try to give 1 line of input and the rest to content.
		b.Input = 1
		b.Content = max(availableForContent-b.Input, 0)
		return b
	}

	// At 80x24+, allocate from the fixed regions inward.
	// The order is: TabBar, Status (fixed), Banners, Attached, Drawer|List, Help, Input, Content
	remaining := height - b.TabBar - b.Status

	// Banners: at most 2 lines, but may shrink to 1 to keep content >= 5
	b.Banners = min(2, remaining)
	remaining -= b.Banners

	// Attached line: 1 line reserved for file attachments (D-07 reserved but not rendered)
	b.Attached = 1
	remaining -= b.Attached

	// Drawer and List are mutually exclusive.
	// Determine which one is shown (if any).
	drawerHeight := 0
	listHeight := 0
	if state.DrawerOpen {
		// Drawer: 7 lines, or 9 above 30 rows
		drawerHeight = 7
		if height >= 30 {
			drawerHeight = 9
		}
	} else if state.ListOpen {
		// List: at most 5 lines
		listHeight = 5
	}

	// Short help: 1 line, hidden when drawer is open
	helpHeight := 0
	if state.HelpOpen && !state.DrawerOpen {
		helpHeight = 1
	}

	// Now allocate Input and Content.
	// Content must have at least 5 lines when drawer or list is open.
	minContent := 5
	if !state.DrawerOpen && !state.ListOpen {
		minContent = 1
	}

	// Input: normally 1-6 lines visible, but shrinks when drawer/list force it
	// to preserve content >= 5 lines.
	// Start with maximum input (6 lines).
	inputHeight := 6
	remaining -= drawerHeight + listHeight + helpHeight

	// Adjust input if drawer/list would cause content to drop below minimum
	if remaining-inputHeight < minContent {
		// Shrink input to 1 line to try to preserve content
		inputHeight = 1
		if remaining-inputHeight < minContent && b.Banners > 1 {
			// Still not enough; shrink banners to 1 line
			remaining += b.Banners - 1
			b.Banners = 1
		}
	}

	b.Input = min(inputHeight, max(remaining, 1))
	remaining -= b.Input

	// Drawer and List heights
	b.Drawer = drawerHeight
	b.List = listHeight
	b.ShortHelp = helpHeight

	// Content gets remaining lines, enforcing minimum when drawer/list are open
	b.Content = max(remaining, minContent)

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
