package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mouseApp is a chat of three approved turns laid out at 80x24 with colour and
// the mouse on. Turn t1 has 2 reviews, t2 has 3 and t3 none.
func mouseApp(t *testing.T) *App {
	t.Helper()
	a := chatApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: true}
	a.mouseEnabled = true
	for i, id := range []string{"t1", "t2", "t3"} {
		addTurn(a, id, int64(i+1), "approved", "question "+id, "answer "+id)
		approveBy(a, id, "rev")
	}
	two, three := 2, 3
	a.reviewCounts["t1"], a.reviewCounts["t2"] = &two, &three
	return a
}

// screen is the rows of the view with their styling removed.
func screen(a *App) []string {
	return strings.Split(ansi.Strip(a.View()), "\n")
}

// cellOf is the screen cell where sub starts in row, or -1.
func cellOf(row, sub string) int {
	i := strings.Index(row, sub)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(row[:i])
}

// where finds sub on the screen and returns its cell and row.
func where(t *testing.T, a *App, sub string) (x, y int) {
	t.Helper()
	rows := screen(a)
	for y, r := range rows {
		if x := cellOf(r, sub); x >= 0 {
			return x, y
		}
	}
	t.Fatalf("%q is not on the screen:\n%s", sub, strings.Join(rows, "\n"))
	return 0, 0
}

func click(a *App, x, y int) tea.Cmd {
	_, cmd := a.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: x, Y: y})
	return cmd
}

// settle sends drawer frames until the slide ends, returning how many it took.
func settle(t *testing.T, a *App) int {
	t.Helper()
	for n := 0; n < 200; n++ {
		if !a.reviewsDrawer.IsAnimating() {
			return n
		}
		a.Update(drawerFrameMsg{})
	}
	t.Fatal("the drawer is still sliding after 200 frames")
	return 0
}

// hasHeader reports whether the drawer's header for turn n is on the screen.
func hasHeader(a *App, turn int) bool {
	return strings.Contains(strings.Join(screen(a), "\n"), fmt.Sprintf("Reviews · Turn %d", turn))
}

func TestMouseOffByDefault(t *testing.T) {
	a := chatApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: true}
	if a.mouseEnabled {
		t.Fatal("the mouse is on without --mouse or /mouse on")
	}
	addTurn(a, "t1", 1, "approved", "q", "a")
	x, _ := where(t, a, "transcript")
	click(a, x, 0)
	if a.tab != tabChat {
		t.Errorf("a click switched to tab %v with the mouse off", a.tab)
	}
}

func TestMouseTabClickSwitchesToTheNamedTab(t *testing.T) {
	a := mouseApp(t)
	x, y := where(t, a, "transcript")
	if y != 0 {
		t.Fatalf("tab bar is on row %d", y)
	}
	click(a, x+3, 0)
	if a.tab != tabTranscript || !strings.Contains(screen(a)[0], "[transcript]") {
		t.Fatalf("clicking transcript: tab = %v, bar = %q", a.tab, screen(a)[0])
	}
	// The bar re-flowed: participants now starts further right than before.
	x, _ = where(t, a, "participants")
	click(a, x, 0)
	if a.tab != tabParticipants || !strings.Contains(screen(a)[0], "[participants]") {
		t.Fatalf("clicking participants: tab = %v, bar = %q", a.tab, screen(a)[0])
	}
	// The gap between names and the space past the last one are not tabs.
	end := cellOf(screen(a)[0], "[participants]") + len("[participants]")
	click(a, end+5, 0)
	click(a, 30, 0)
	if a.tab != tabParticipants {
		t.Errorf("a click past the last tab switched to %v", a.tab)
	}
	click(a, 0, 0)
	if a.tab != tabChat {
		t.Errorf("clicking chat: tab = %v", a.tab)
	}
}

func TestMouseTabClickClosesTheDrawerLikeTab(t *testing.T) {
	a := mouseApp(t)
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	settle(t, a)
	if !a.reviewsDrawer.IsOpen() || !hasHeader(a, 3) {
		t.Fatal("ctrl+o did not open the drawer")
	}
	x, _ := where(t, a, "transcript")
	click(a, x, 0)
	if a.reviewsDrawer.Visible() || a.want.Drawer || hasHeader(a, 3) {
		t.Errorf("the drawer stays on the transcript tab:\n%s", a.View())
	}
	// With the drawer gone the keyboard reaches the input again.
	click(a, 0, 0)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if a.input.Value() != "x" {
		t.Errorf("input = %q after the drawer closed, want x", a.input.Value())
	}
}

func TestMouseReviewCountClickOpensThatTurn(t *testing.T) {
	a := mouseApp(t)
	// Turn 2's count is the one reading "3 reviews".
	x, y := where(t, a, "3 reviews")
	click(a, x+2, y)
	if !a.reviewsDrawer.IsOpen() || a.reviewsDrawer.SelectedTurnIndex() != 1 {
		t.Fatalf("drawer open = %v on turn index %d, want turn index 1", a.reviewsDrawer.IsOpen(), a.reviewsDrawer.SelectedTurnIndex())
	}
	settle(t, a)
	if !hasHeader(a, 2) {
		t.Fatalf("the drawer does not show turn 2:\n%s", a.View())
	}

	// Turn 1's count opens turn 1.
	x, y = where(t, a, "2 reviews")
	click(a, x, y)
	settle(t, a)
	if !hasHeader(a, 1) || hasHeader(a, 2) {
		t.Fatalf("the drawer does not show turn 1:\n%s", a.View())
	}
}

func TestMouseOnlyTheReviewCountOpensTheDrawer(t *testing.T) {
	a := mouseApp(t)
	x, y := where(t, a, "3 reviews")
	row := screen(a)[y]
	// Another cell of the same line, the turn's text, and the line's end.
	for _, cx := range []int{0, 2, x - 4, x + len("3 reviews")} {
		click(a, cx, y)
		if a.reviewsDrawer.IsOpen() {
			t.Fatalf("a click at cell %d of %q opened the drawer", cx, row)
		}
	}
	// A turn with no review count, the conversation header and the tab bar.
	_, y3 := where(t, a, "answer t3")
	click(a, 5, y3)
	click(a, 5, 1)
	if a.reviewsDrawer.IsOpen() {
		t.Fatalf("a click off a review count opened the drawer:\n%s", a.View())
	}
	// A banner is below the body, so rows are counted from the screen's top.
	a.tab = tabTranscript
	click(a, x, y)
	if a.reviewsDrawer.IsOpen() {
		t.Fatal("a review count is clickable off the chat tab")
	}
}

func TestMouseReviewCountClickFollowsScroll(t *testing.T) {
	a := mouseApp(t)
	for i := 4; i <= 12; i++ {
		id := fmt.Sprintf("t%d", i)
		addTurn(a, id, int64(i), "approved", "question "+id, "answer "+id)
		approveBy(a, id, "rev")
	}
	nine := 9
	a.reviewCounts["t12"] = &nine
	// Following the newest turn, the chat has scrolled: turn 12 is on screen.
	x, y := where(t, a, "9 reviews")
	click(a, x, y)
	settle(t, a)
	if !hasHeader(a, 12) {
		t.Fatalf("the drawer is not on turn 12:\n%s", a.View())
	}
}

func openRounds(t *testing.T, a *App, n int) {
	t.Helper()
	x, y := where(t, a, "3 reviews")
	click(a, x, y)
	items := make([]wire.ReviewItem, n)
	for i := range items {
		items[i] = wire.ReviewItem{Round: i, Participant: "rev", Kind: "approve"}
	}
	a.Update(reviewsMsg{turnID: "t2", items: items})
	settle(t, a)
}

func TestMouseRoundHeaderClickSelectsThatRound(t *testing.T) {
	a := mouseApp(t)
	openRounds(t, a, 3)
	if !strings.Contains(strings.Join(screen(a), "\n"), "[*1* 2 3]") {
		t.Fatalf("the drawer does not list three rounds:\n%s", a.View())
	}
	// The header holds "·" characters, two bytes and one cell each: the click
	// is by cell, so it lands on the digit drawn there.
	x, y := where(t, a, "[*1* 2 3]")
	click(a, x+len("[*1* ")+2, y)
	if got := screen(a)[y]; !strings.Contains(got, "[1 2 *3*]") {
		t.Fatalf("clicking round 3: header = %q", got)
	}
	click(a, x+len("[1 "), y)
	if got := screen(a)[y]; !strings.Contains(got, "[1 *2* 3]") {
		t.Fatalf("clicking round 2: header = %q", got)
	}
	// The brackets, the space between numbers and the text before the list are not rounds.
	for _, cx := range []int{x, x + len("[1"), 3, x + len("[1 2 3]") + 4} {
		click(a, cx, y)
		if got := screen(a)[y]; !strings.Contains(got, "[1 *2* 3]") {
			t.Errorf("a click at cell %d changed the selection: %q", cx, got)
		}
	}
}

func TestMouseRoundHeaderClickWithTwoDigitRounds(t *testing.T) {
	a := mouseApp(t)
	a.width, a.height = 120, 30
	openRounds(t, a, 12)
	x, y := where(t, a, "[*1* 2 ")
	// Round 10 starts after "[*1* 2 3 4 5 6 7 8 9 ": cell offset 5 + 2*8 = 21.
	click(a, x+21+1, y)
	if got := screen(a)[y]; !strings.Contains(got, " *10* ") {
		t.Fatalf("clicking round 10: header = %q", got)
	}
	click(a, x+1, y)
	if got := screen(a)[y]; !strings.Contains(got, "[*1* 2 ") {
		t.Fatalf("clicking round 1: header = %q", got)
	}
}

func TestMouseRoundClickNeedsAnOpenDrawer(t *testing.T) {
	a := mouseApp(t)
	// Rows of the chat, banners, input and status that hold no review count.
	before := a.View()
	for row := 2; row < a.height-2; row++ {
		click(a, 40, row)
	}
	if a.reviewsDrawer.IsOpen() || a.View() != before {
		t.Fatalf("clicks with no review count under them changed the screen:\n%s", a.View())
	}
}

func TestMouseOpenSlidesInFrameByFrame(t *testing.T) {
	a := mouseApp(t)
	a.input.SetValue("draft")
	x, y := where(t, a, "3 reviews")
	if cmd := click(a, x, y); cmd == nil {
		t.Fatal("opening the drawer schedules no frame")
	}
	if len(screen(a)) != a.height {
		t.Fatalf("view is %d rows, terminal is %d", len(screen(a)), a.height)
	}
	if hasHeader(a, 2) {
		t.Fatal("the drawer is whole before its first frame")
	}
	frames, shown := 0, []int{}
	for a.reviewsDrawer.IsAnimating() {
		_, cmd := a.Update(drawerFrameMsg{})
		frames++
		if len(screen(a)) != a.height {
			t.Fatalf("view is %d rows mid-slide, terminal is %d", len(screen(a)), a.height)
		}
		shown = append(shown, int(a.reviewsDrawer.AnimProgress()*100))
		if a.reviewsDrawer.IsAnimating() && cmd == nil {
			t.Fatal("a frame did not schedule the next")
		}
		if frames > 100 {
			t.Fatal("the slide does not end")
		}
	}
	if frames < 3 || frames > 40 {
		t.Errorf("slide took %d frames, want a short slide of several (%v)", frames, shown)
	}
	for i := 1; i < len(shown); i++ {
		if shown[i] < shown[i-1] {
			t.Errorf("the drawer moved back while opening: %v", shown)
		}
	}
	if !hasHeader(a, 2) || a.reviewsDrawer.AnimProgress() != 1 {
		t.Fatalf("the slide ended with no whole drawer:\n%s", a.View())
	}
	if a.input.Value() != "draft" {
		t.Errorf("input = %q after the slide, want draft", a.input.Value())
	}
}

func TestMouseCloseKeepsItsLinesWhileSliding(t *testing.T) {
	a := mouseApp(t)
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	settle(t, a)
	body := a.layout().Body

	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("closing the drawer schedules no frame")
	}
	if a.reviewsDrawer.IsOpen() {
		t.Error("the keyboard is still held by a drawer that is closing")
	}
	if !a.want.Drawer || a.layout().Body != body {
		t.Error("the layout gives up the drawer's lines before it has slid out")
	}
	a.Update(drawerFrameMsg{})
	if !a.reviewsDrawer.IsAnimating() || !a.want.Drawer {
		t.Fatal("the drawer is gone after one frame")
	}
	// Keys type into the input while the drawer slides out.
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	if a.input.Value() != "hi" {
		t.Errorf("input = %q while the drawer closes, want hi", a.input.Value())
	}
	settle(t, a)
	if a.want.Drawer || a.layout().Body <= body || hasHeader(a, 3) {
		t.Errorf("the drawer did not go:\n%s", a.View())
	}
	if a.input.Value() != "hi" {
		t.Errorf("input = %q after the slide, want hi", a.input.Value())
	}
}

func TestAnimationNeverSubmitsOrCancels(t *testing.T) {
	a := mouseApp(t)
	a.input.SetValue("draft")
	a.state.CurrentTurn = "t3"
	for _, m := range []tea.Msg{tea.KeyMsg{Type: tea.KeyCtrlO}, drawerFrameMsg{}, drawerFrameMsg{}, tea.KeyMsg{Type: tea.KeyEsc}, drawerFrameMsg{}} {
		a.Update(m)
		if a.input.Value() != "draft" {
			t.Fatalf("input = %q after %T, want draft", a.input.Value(), m)
		}
	}
	if a.state.CurrentTurn != "t3" {
		t.Errorf("animation touched the open turn: %v", a.state.CurrentTurn)
	}
	// A frame with the drawer already settled does nothing and schedules nothing.
	settle(t, a)
	if _, cmd := a.Update(drawerFrameMsg{}); cmd != nil && a.reviewsDrawer.IsAnimating() {
		t.Error("a settled drawer asks for more frames")
	}
}

func TestPlainModeDrawerIsWholeAtOnce(t *testing.T) {
	a := mouseApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: false}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd != nil {
		t.Error("plain mode schedules a frame")
	}
	if a.reviewsDrawer.IsAnimating() || !hasHeader(a, 3) {
		t.Fatalf("the drawer is not whole at once:\n%s", a.View())
	}
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || a.reviewsDrawer.IsAnimating() || a.want.Drawer || hasHeader(a, 3) {
		t.Fatalf("the drawer does not go at once:\n%s", a.View())
	}
}

func TestPlainModeIgnoresTheMouse(t *testing.T) {
	a := mouseApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: false}
	x, y := where(t, a, "transcript")
	click(a, x, y)
	x, y = where(t, a, "3 reviews")
	click(a, x, y)
	if a.tab != tabChat || a.reviewsDrawer.IsOpen() {
		t.Error("the mouse acted in plain mode")
	}
	if cmd := cmdMouse(a, []string{"on"}); cmd != nil || a.mouseEnabled {
		t.Error("/mouse on captured the mouse in plain mode")
	}
	if a.refusal != "mouse: not available in plain mode" {
		t.Errorf("refusal = %q", a.refusal)
	}
}

func TestDumbTerminalNeverCapturesTheMouse(t *testing.T) {
	t.Setenv("TERM", "dumb")
	caps := DetectTerminalCapabilities()
	if len(ProgramOptionsWithMouse(caps, true)) != 0 {
		t.Error("the program captures the mouse under TERM=dumb")
	}
	a := NewApp(t.Context(), "", WithMouse(true), WithTerminalCapabilities(caps))
	if a.mouseEnabled {
		t.Error("--mouse is on under TERM=dumb")
	}
}

func TestMouseCommandTogglesCapture(t *testing.T) {
	a := chatApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: true}

	cmd := cmdMouse(a, []string{"on"})
	if !a.mouseEnabled || a.refusal != "mouse on" || cmd == nil || cmd() != tea.EnableMouseCellMotion() {
		t.Errorf("/mouse on: enabled = %v, refusal = %q, capture cmd = %v", a.mouseEnabled, a.refusal, cmd != nil)
	}
	cmd = cmdMouse(a, []string{"off"})
	if a.mouseEnabled || a.refusal != "mouse off" || cmd == nil || cmd() != tea.DisableMouse() {
		t.Errorf("/mouse off: enabled = %v, refusal = %q, release cmd = %v", a.mouseEnabled, a.refusal, cmd != nil)
	}
	for _, args := range [][]string{{"invalid"}, {}, {"on", "off"}} {
		if cmd := cmdMouse(a, args); cmd != nil || a.refusal != "usage: /mouse on|off" {
			t.Errorf("/mouse %v: refusal = %q", args, a.refusal)
		}
	}
}

func TestMouseIgnoresOtherButtonsAndReleases(t *testing.T) {
	a := mouseApp(t)
	x, y := where(t, a, "transcript")
	for _, m := range []tea.MouseMsg{
		{Button: tea.MouseButtonRight, Action: tea.MouseActionPress, X: x, Y: y},
		{Button: tea.MouseButtonMiddle, Action: tea.MouseActionPress, X: x, Y: y},
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: x, Y: y},
		{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress, X: x, Y: y},
	} {
		a.Update(m)
		if a.tab != tabChat {
			t.Fatalf("%v switched tabs", m)
		}
	}
}

func TestHelpListsMouseAndNotify(t *testing.T) {
	a := chatApp(t)
	cmdHelp(a, nil)
	help := strings.Join(a.panel, "\n")
	for _, want := range []string{"--mouse", "--notify", "/mouse on|off", "/notify on|off"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help does not list %q", want)
		}
	}
}

func TestProgramOptionsWithMouse(t *testing.T) {
	for _, tc := range []struct {
		color, ask bool
		want       int
	}{{true, true, 1}, {false, true, 0}, {true, false, 0}} {
		if got := len(ProgramOptionsWithMouse(TerminalCapabilities{Color: tc.color}, tc.ask)); got != tc.want {
			t.Errorf("color=%v ask=%v: %d options, want %d", tc.color, tc.ask, got, tc.want)
		}
	}
}
