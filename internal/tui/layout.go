package tui

import "fmt"

// Height limits of the v6 layout (section 1, Heights).
const (
	minBodyLines     = 5 // the active tab never gets fewer, where the screen allows
	maxBannerLines   = 2
	maxListLines     = 5
	drawerLines      = 7
	tallDrawerLines  = 9
	tallDrawerAbove  = 30 // the drawer takes tallDrawerLines above this many rows
	regionMinWidth   = 80 // the drawer and lists open from this size up
	regionMinHeight  = 24
	maxInputViewport = 6 // the input scrolls inside itself beyond this
)

// regionWant is what the rest of the TUI asks the layout for. The zero value
// asks for nothing: the drawer, lists, attached line and help line are other
// tasks' features, and each reserves its lines by setting its field here.
type regionWant struct {
	Drawer    bool // the reviews drawer is open
	ListLines int  // lines a command or file list would take; 0 when closed
	Attached  bool // the attached line has content
	Help      bool // the short help line is wanted
}

// layoutIn is everything the allocator needs.
type layoutIn struct {
	Height, Width int
	Banners       int // banners that apply, before folding
	InputLines    int // screen lines the input's text takes
	Want          regionWant
}

// heights is the number of lines each region gets, top to bottom: tab bar,
// body, banners, drawer or list, attached, help, refusal and input, status.
type heights struct {
	TabBar, Body, Banners, Drawer, List, Attached, Help, Refusal, Input, Status int
}

// total is the sum of every region; it equals the terminal height.
func (h heights) total() int {
	return h.TabBar + h.Body + h.Banners + h.Drawer + h.List + h.Attached + h.Help + h.Refusal + h.Input + h.Status
}

// allocate gives each region a bounded viewport. The tab bar, refusal line and
// status line are fixed; the optional regions take their bounded height; the
// body gets what remains. When that is under minBodyLines the input viewport
// shrinks to one line, then banners to one line, then the body takes what is
// left. Below 80x24 the drawer and lists do not open.
func allocate(in layoutIn) heights {
	h := heights{TabBar: tabBarHeight, Status: stripHeight, Refusal: 1}
	enabled := in.Width >= regionMinWidth && in.Height >= regionMinHeight

	if enabled {
		switch {
		case in.Want.Drawer:
			h.Drawer = drawerLines
			if in.Height > tallDrawerAbove {
				h.Drawer = tallDrawerLines
			}
		case in.Want.ListLines > 0:
			// A list closes when the drawer opens, so it is the else arm.
			h.List = min(in.Want.ListLines, maxListLines)
		}
		// The short help line is hidden while the drawer is open.
		if in.Want.Help && h.Drawer == 0 {
			h.Help = 1
		}
	}
	if in.Want.Attached {
		h.Attached = 1
	}
	h.Banners = min(in.Banners, maxBannerLines)
	h.Input = min(max(in.InputLines, 1), maxInputViewport)

	room := func() int { return in.Height - h.total() }
	if room() < minBodyLines {
		h.Input = 1
	}
	if room() < minBodyLines {
		h.Banners = min(h.Banners, 1)
	}
	if room() < minBodyLines {
		h.Banners = 0
	}
	h.Body = max(room(), 0)
	return h
}

// foldBanners fits banners into lines. When more apply than fit, the last
// shown line ends "+<n> more"; the first banner, which is the identity
// warning when it applies, is always shown.
func foldBanners(banners []string, lines int) []string {
	if lines <= 0 || len(banners) == 0 {
		return nil
	}
	if len(banners) <= lines {
		return banners
	}
	more := fmt.Sprintf("+%d more", len(banners)-1)
	if lines == 1 {
		return []string{banners[0] + " " + more}
	}
	return append(append([]string{}, banners[:lines-1]...), more)
}

// scrollPercent is the scroll position as a percentage: 0 at the first line
// and 100 once the last line is in view, including when everything fits.
func scrollPercent(total, visible, first int) int {
	if total <= visible || first+visible >= total {
		return 100
	}
	return first * 100 / (total - visible)
}
