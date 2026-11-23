package tui

import (
	"testing"
)

func TestCalcBounds80x24(t *testing.T) {
	// At 80x24, the minimum viable full-featured size, layout should:
	// - Reserve 1 line for tab bar, 1 for status = 2
	// - Reserve at least 1 for input, 2 for banners = 3
	// - Leave remaining for content (at least 5)
	b := calcBounds(24, 80)
	if b.TabBar != 1 {
		t.Errorf("TabBar = %d, want 1", b.TabBar)
	}
	if b.Status != 1 {
		t.Errorf("Status = %d, want 1", b.Status)
	}
	if b.Input < 1 || b.Input > 6 {
		t.Errorf("Input = %d, want 1-6", b.Input)
	}
	if b.Banners != 2 {
		t.Errorf("Banners = %d, want 2 at 80x24", b.Banners)
	}
	if b.Content < 5 {
		t.Errorf("Content = %d, want at least 5", b.Content)
	}

	// In D-07, drawer and list are closed by default, so they should be 0.
	if b.Drawer != 0 {
		t.Errorf("Drawer = %d, want 0 (closed by default)", b.Drawer)
	}
	if b.List != 0 {
		t.Errorf("List = %d, want 0 (closed by default)", b.List)
	}
}

func TestCalcBoundsBelow80x24(t *testing.T) {
	// Below 80x24, drawer and lists do not open.
	testCases := []struct {
		h, w int
		name string
	}{
		{24, 79, "79x24"},
		{23, 80, "80x23"},
		{20, 60, "60x20"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := calcBounds(tc.h, tc.w)
			if b.Drawer != 0 || b.List != 0 {
				t.Errorf("At %dx%d: drawer=%d list=%d, want both 0", tc.w, tc.h, b.Drawer, b.List)
			}
		})
	}
}

func TestCalcBoundsSmallTerminal(t *testing.T) {
	// Very small terminal (less than tab+status+1 line of content)
	b := calcBounds(2, 40)
	if b.TabBar+b.Status+b.Content > b.Height {
		t.Errorf("At 40x2: total allocated = %d + %d + %d = %d, exceeds height %d",
			b.TabBar, b.Status, b.Content, b.TabBar+b.Status+b.Content, b.Height)
	}
}

func TestCalcBoundsLargeTerminal(t *testing.T) {
	// At 30+ rows, drawer should be 9 lines (not applicable in D-07 since drawer
	// is always closed, but we reserve the space calculation).
	b := calcBounds(40, 120)
	if b.TabBar != 1 || b.Status != 1 {
		t.Errorf("At 120x40: TabBar=%d Status=%d, want 1 each", b.TabBar, b.Status)
	}
	// Content should be substantial
	if b.Content < 5 {
		t.Errorf("At 120x40: Content = %d, want at least 5", b.Content)
	}
}

func TestCalcScrollPosition(t *testing.T) {
	testCases := []struct {
		total, visible, firstLine int
		wantAbove                 int
		wantPercentage            int
		name                      string
	}{
		// All content fits: no scroll needed
		{10, 20, 0, 0, 0, "content fits in view"},
		// At the beginning
		{100, 10, 0, 0, 0, "at start of 100 lines"},
		// At the end
		{100, 10, 90, 90, 100, "at end of 100 lines"},
		// Middle
		{100, 10, 45, 45, 50, "middle of 100 lines"},
		// Edge case: single line visible
		{100, 1, 50, 50, 50, "single line at 50/100"},
		// Edge case: empty content
		{0, 10, 0, 0, 0, "zero total lines"},
		// Percentage rounding
		{100, 10, 1, 1, 1, "1/99 ≈ 1%"},
		{100, 10, 10, 10, 11, "10/90 ≈ 11%"},
		// Clamped firstLine
		{100, 10, 150, 90, 100, "firstLine clamped to max"},
		{100, 10, -5, 0, 0, "negative firstLine clamped to 0"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := calcScrollPosition(tc.total, tc.visible, tc.firstLine)
			if got.Above != tc.wantAbove {
				t.Errorf("Above = %d, want %d", got.Above, tc.wantAbove)
			}
			if got.Percentage != tc.wantPercentage {
				t.Errorf("Percentage = %d, want %d", got.Percentage, tc.wantPercentage)
			}
		})
	}
}

func TestCalcBounds80x24CrowdedLayout(t *testing.T) {
	// Acceptance test: 80x24 crowded layout retains five chat lines and identity warning.
	// This checks the specific constraint from the task.
	b := calcBounds(24, 80)

	// At 80x24:
	// - Tab bar: 1 line
	// - Status: 1 line
	// - Input: at minimum 1 line (spec says 1-6)
	// - Banners: 2 lines (identity warning is always shown when it applies)
	// - Content: at least 5 lines (the acceptance criterion)
	// Total: 1 + 1 + 1 + 2 + 5 = 10 lines minimum

	if b.Content < 5 {
		t.Errorf("At 80x24: Content = %d, want at least 5 for identity warning + chat", b.Content)
	}

	// Verify no drawer/list are consuming space
	if b.Drawer != 0 || b.List != 0 {
		t.Errorf("At 80x24: drawer=%d list=%d, want both 0 (closed by default)", b.Drawer, b.List)
	}

	// Content + Input + Banners + TabBar + Status should equal height
	total := b.TabBar + b.Status + b.Input + b.Banners + b.Content
	if total > b.Height {
		t.Errorf("At 80x24: total allocated = %d, exceeds height %d", total, b.Height)
	}
}

func TestInputRowsConstraint(t *testing.T) {
	// The input can be 1-6 lines, but the actual allocation happens in inputRows().
	// This test verifies the bounds structure doesn't violate that.
	for h := 10; h <= 50; h++ {
		for w := 40; w <= 200; w += 20 {
			b := calcBounds(h, w)
			if b.Input > 6 {
				t.Errorf("At %dx%d: Input = %d, exceeds max 6", w, h, b.Input)
			}
		}
	}
}

// TestBoundsAllocation verifies that bounds allocation satisfies the design constraints.
func TestBoundsAllocation(t *testing.T) {
	testCases := []struct {
		h, w            int
		name            string
		minContent      int
		expectDrawer    int
		expectList      int
		expectShortHelp int
	}{
		{80, 24, "80x24 crowded", 5, 0, 0, 0},
		{80, 31, "80x31 large", 5, 0, 0, 0},
		{79, 24, "79x24 too narrow", 0, 0, 0, 0},
		{80, 23, "80x23 too short", 0, 0, 0, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := calcBounds(tc.h, tc.w)

			// Check drawer and list allocation
			if b.Drawer != tc.expectDrawer {
				t.Errorf("Drawer = %d, want %d", b.Drawer, tc.expectDrawer)
			}
			if b.List != tc.expectList {
				t.Errorf("List = %d, want %d", b.List, tc.expectList)
			}
			if b.ShortHelp != tc.expectShortHelp {
				t.Errorf("ShortHelp = %d, want %d", b.ShortHelp, tc.expectShortHelp)
			}

			// Check content minimum when at 80x24+
			if tc.w >= 80 && tc.h >= 24 && b.Content < tc.minContent {
				t.Errorf("At %dx%d: Content = %d, want at least %d", tc.w, tc.h, b.Content, tc.minContent)
			}

			// Verify bounds don't exceed terminal size
			total := b.TabBar + b.Status + b.Content + b.Input + b.Banners + b.Drawer + b.List + b.ShortHelp + b.Attached
			if total > b.Height {
				t.Errorf("At %dx%d: total allocated %d exceeds height %d", tc.w, tc.h, total, b.Height)
			}
		})
	}
}

// TestTurnLineMapCounting verifies correct turn boundary tracking.
func TestTurnLineMapCounting(t *testing.T) {
	testCases := []struct {
		name       string
		turnStarts []int
		lineIndex  int
		wantTurns  int
	}{
		{"line in first turn", []int{0, 5, 10}, 2, 0},
		{"line at second turn", []int{0, 5, 10}, 5, 1},
		{"line in second turn", []int{0, 5, 10}, 7, 1},
		{"line at third turn", []int{0, 5, 10}, 10, 2},
		{"line in third turn", []int{0, 5, 10}, 12, 2},
		{"empty turns", []int{}, 0, 0},
		{"single turn", []int{0}, 0, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &TurnLineMap{
				turnStarts: tc.turnStarts,
				totalLines: 100,
			}
			got := m.countEarlierTurns(tc.lineIndex)
			if got != tc.wantTurns {
				t.Errorf("countEarlierTurns(%d) = %d, want %d", tc.lineIndex, got, tc.wantTurns)
			}
		})
	}
}

// TestScrollPositionByTurns verifies scroll position calculation using turns.
func TestScrollPositionByTurns(t *testing.T) {
	turns := &TurnLineMap{
		turnStarts: []int{0, 10, 20, 30},
		totalLines: 40,
	}

	testCases := []struct {
		name        string
		total       int
		visible     int
		firstLine   int
		wantAbove   int
		wantPercent int
	}{
		{"at start", 40, 10, 0, 0, 0},
		{"in middle", 40, 10, 15, 1, 50}, // 15 * 100 / (40 - 10) = 50
		{"at end", 40, 10, 30, 3, 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := calcScrollPositionByTurns(tc.total, tc.visible, tc.firstLine, turns)
			if got.Above != tc.wantAbove {
				t.Errorf("Above = %d, want %d", got.Above, tc.wantAbove)
			}
			if got.Percentage != tc.wantPercent {
				t.Errorf("Percentage = %d, want %d", got.Percentage, tc.wantPercent)
			}
		})
	}
}
