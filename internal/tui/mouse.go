package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Mouse input is opt-in: --mouse at start, or /mouse on|off while running. It
// is never captured under TERM=dumb or with colour off. A left click on a tab
// name switches to it, on a turn's review count opens the reviews drawer on
// that turn, and on a round number in the drawer's header selects that round.
// Every click is mapped from the lines View draws, using the same layout, so
// a click lands on what is on screen; nothing here is a second keyboard.

// drawerFrameMsg is one frame of the reviews drawer sliding open or shut.
type drawerFrameMsg struct{}

// mouseAllowed reports whether the mouse may be captured at all.
func (a *App) mouseAllowed() bool {
	return a.terminalCapabilities.Color
}

// updateMouse handles a mouse event: only a left press does anything, and
// only when the mouse is on.
func (a *App) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !a.mouseEnabled || a.plainMode() || a.isTerminalTooSmall() {
		return a, nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return a, nil
	}
	return a, a.click(msg.X, msg.Y)
}

// click acts on a click at cell (x, y) of the screen View draws.
func (a *App) click(x, y int) tea.Cmd {
	hs := a.layout()
	switch {
	case y < hs.TabBar:
		for i, sp := range a.tabSpans() {
			if x >= sp.start && x < sp.end {
				if tab(i) != a.tab {
					return a.setTab(tab(i))
				}
				return nil
			}
		}
	case y == hs.TabBar+hs.Body+hs.Banners && hs.Drawer > 0 && a.reviewsDrawer.IsOpen():
		if i, ok := a.reviewsDrawer.RoundAt(x, a.width); ok {
			a.reviewsDrawer.SelectRound(i)
		}
	default:
		if id, ok := a.reviewCountAt(x, y, hs); ok {
			if i := slices.Index(chatTurns(a.state), id); i >= 0 {
				return a.openDrawer(i)
			}
		}
	}
	return nil
}

// tabSpans is the cells each tab's name takes in the tab bar.
func (a *App) tabSpans() []span {
	names := tabNames()
	spans := make([]span, len(names))
	pos := 0
	for i, n := range names {
		w := len(n)
		if tab(i) == a.tab {
			w += 2 // its brackets
		}
		spans[i] = span{pos, pos + w}
		pos += w + len(tabGap)
	}
	return spans
}

// reviewCountAt returns the turn whose review count is drawn at cell (x, y):
// the turn's outcome line ends with "· N reviews", and only those cells count.
func (a *App) reviewCountAt(x, y int, hs heights) (string, bool) {
	if a.tab != tabChat || len(a.panel) > 0 {
		return "", false
	}
	lines, lastTurn, starts := chatLinesWithStarts(a.state, a.chatIn())
	// Below the tab bar the chat's first line names the conversation.
	row := y - hs.TabBar - 1
	if row < 0 || row >= a.answerHeight() {
		return "", false
	}
	idx := a.chatStart(len(lines), lastTurn) + row
	if idx >= len(lines) {
		return "", false
	}
	ids := chatTurns(a.state)
	turn := -1
	for i, s := range starts {
		if s <= idx {
			turn = i
		}
	}
	if turn < 0 || turn >= len(ids) {
		return "", false
	}
	count := a.reviewCounts[ids[turn]]
	if count == nil || *count < 0 {
		return "", false
	}
	line := ansi.Strip(lines[idx])
	label := fmt.Sprintf("%d reviews", *count)
	at := strings.LastIndex(line, " · "+label)
	if at < 0 {
		return "", false
	}
	start := ansi.StringWidth(line[:at+len(" · ")])
	if x < start || x >= start+len(label) {
		return "", false
	}
	return ids[turn], true
}

// setTab switches to tab t as Tab does: the panel and the drawer go, and the
// chat follows its newest turn again.
func (a *App) setTab(t tab) tea.Cmd {
	a.panel = nil
	a.tab = t
	a.answerScrollRow, a.answerFollow = 0, true
	if a.tab != tabChat {
		a.reviewsDrawer.ForceClose()
		a.syncDrawerWant()
	}
	a.updateDrawerTurns()
	return a.ensureTick()
}

// syncDrawerWant makes the layout reserve the drawer's lines for as long as
// it shows, which includes the frames of its closing slide.
func (a *App) syncDrawerWant() {
	a.want.Drawer = a.reviewsDrawer.Visible()
}

// openDrawer opens the reviews drawer on the turn at index i of the chat's
// turns, and fetches its reviews.
func (a *App) openDrawer(i int) tea.Cmd {
	turns := chatTurns(a.state)
	if i < 0 || i >= len(turns) || a.width < regionMinWidth || a.height < regionMinHeight {
		return nil
	}
	a.updateDrawerTurns()
	a.closeCommandList()
	a.showShortHelp = false
	a.reviewsDrawer.Open(i, a.plainMode())
	a.syncDrawerWant()
	a.reviewsDrawer.SetDrawerHeight(a.layout().Drawer)
	return tea.Batch(a.fetchReviewsForTurn(turns[i]), a.animateDrawer())
}

// closeDrawer closes the reviews drawer; its lines stay reserved while it slides out.
func (a *App) closeDrawer() tea.Cmd {
	a.reviewsDrawer.Close(a.plainMode())
	a.syncDrawerWant()
	return a.animateDrawer()
}

// animateDrawer schedules the next frame of the drawer's slide, if it is
// sliding and no frame is already on its way. A frame only redraws the
// drawer: it carries no key and sends nothing.
func (a *App) animateDrawer() tea.Cmd {
	if !a.reviewsDrawer.IsAnimating() || a.drawerAnimating {
		return nil
	}
	a.drawerAnimating = true
	return tea.Tick(drawerFrameInterval, func(time.Time) tea.Msg { return drawerFrameMsg{} })
}
