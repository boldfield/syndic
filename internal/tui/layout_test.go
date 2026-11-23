package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAllocateDrawerHeights(t *testing.T) {
	for _, tc := range []struct {
		w, h, want int
	}{
		{80, 24, 7}, {80, 30, 7}, {80, 31, 9}, {120, 50, 9},
		{79, 24, 0}, {80, 23, 0}, {79, 40, 0}, {40, 10, 0},
	} {
		got := allocate(layoutIn{Height: tc.h, Width: tc.w, Banners: 2, InputLines: 3, Want: regionWant{Drawer: true}})
		if got.Drawer != tc.want {
			t.Errorf("%dx%d: drawer = %d lines, want %d", tc.w, tc.h, got.Drawer, tc.want)
		}
		if got.total() != tc.h {
			t.Errorf("%dx%d: regions sum to %d lines, want %d", tc.w, tc.h, got.total(), tc.h)
		}
		if got.Body < minBodyLines {
			t.Errorf("%dx%d: body = %d lines, want at least %d", tc.w, tc.h, got.Body, minBodyLines)
		}
	}
}

func TestAllocateDrawerAndListAreExclusive(t *testing.T) {
	both := allocate(layoutIn{Height: 40, Width: 100, Want: regionWant{Drawer: true, ListLines: 5, Help: true}})
	if both.Drawer == 0 || both.List != 0 || both.Help != 0 {
		t.Errorf("drawer open: %+v; want the list closed and help hidden", both)
	}
	list := allocate(layoutIn{Height: 40, Width: 100, Want: regionWant{ListLines: 9, Help: true}})
	if list.Drawer != 0 || list.List != maxListLines || list.Help != 1 {
		t.Errorf("list open: %+v; want a 5-line list, no drawer, help shown", list)
	}
	small := allocate(layoutIn{Height: 23, Width: 100, Want: regionWant{ListLines: 3, Help: true}})
	if small.List != 0 || small.Help != 0 {
		t.Errorf("below 80x24: %+v; want no list", small)
	}
}

func TestAllocateShrinkOrder(t *testing.T) {
	// Roomy: everything at its wanted size.
	h := allocate(layoutIn{Height: 24, Width: 80, Banners: 2, InputLines: 6})
	if h.Input != 6 || h.Banners != 2 || h.Body < minBodyLines {
		t.Errorf("roomy: %+v", h)
	}
	// Seven lines of text still show six.
	if h := allocate(layoutIn{Height: 24, Width: 80, InputLines: 30}); h.Input != maxInputViewport {
		t.Errorf("input = %d lines, want %d", h.Input, maxInputViewport)
	}
	// The drawer opens at 80x24 with a full input, two banners and an attached
	// line: they just fit, leaving the chat its five lines.
	h = allocate(layoutIn{Height: 24, Width: 80, Banners: 2, InputLines: 6, Want: regionWant{Drawer: true, Attached: true}})
	if h.Drawer != drawerLines || h.Input != 6 || h.Banners != 2 || h.Attached != 1 || h.Body != minBodyLines {
		t.Errorf("crowded drawer: %+v", h)
	}
	// Short of room, the input viewport shrinks to one line first and the
	// banners keep their two.
	h = allocate(layoutIn{Height: 14, Width: 80, Banners: 2, InputLines: 6, Want: regionWant{Attached: true}})
	if h.Input != 1 || h.Banners != 2 || h.Body < minBodyLines {
		t.Errorf("input shrink: %+v", h)
	}
	// Tighter still: the banners go to one line once the input is at one.
	h = allocate(layoutIn{Height: 11, Width: 80, Banners: 2, InputLines: 6, Want: regionWant{Attached: true}})
	if h.Input != 1 || h.Banners != 1 || h.Body < minBodyLines {
		t.Errorf("banner shrink: %+v", h)
	}
	// A shorter input shrinks only as far as it must.
	h = allocate(layoutIn{Height: 14, Width: 80, Banners: 2, InputLines: 3, Want: regionWant{Attached: true}})
	if h.Input != 3 || h.Banners != 2 || h.Body != minBodyLines {
		t.Errorf("short input: %+v", h)
	}
}

// crowded puts an identity warning, more banners than fit and a long input into the app.
func crowded(t *testing.T, w, h int) *App {
	t.Helper()
	a := chatApp(t)
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	a.state.Participants = map[string]ParticipantInfo{"alpha": {ID: "alpha", Identity: "unknown"}}
	a.state.Status.DeadlinesProvisional = true
	a.state.Status.RosterFileChanged = true
	a.state.ConnState = ConnStateReconnecting
	profile := "fast"
	a.state.UnapprovedProfile = &profile
	a.input.SetValue(strings.Join([]string{"i1", "i2", "i3", "i4", "i5", "i6", "i7", "i8"}, "\n"))
	a.updateInput(nil) // scroll to the cursor, as typing does
	return a
}

// assertCrowdedFrame checks a crowded frame of h lines: the identity warning
// shows, the status line is last and the chat keeps its minimum.
func assertCrowdedFrame(t *testing.T, a *App, h int) []string {
	t.Helper()
	lines := chatView(a)
	if len(lines) != h {
		t.Fatalf("frame has %d lines, want %d:\n%s", len(lines), h, strings.Join(lines, "\n"))
	}
	at := lineOf(t, lines, "identity: unknown(name-matched)")
	if !strings.Contains(lines[len(lines)-1], "tab: switch") {
		t.Errorf("status line is not last: %q", lines[len(lines)-1])
	}
	if body := at - 1; body < minBodyLines {
		t.Errorf("the tab area has %d lines, want at least %d", body, minBodyLines)
	}
	if !strings.HasPrefix(lines[0], "[chat]") {
		t.Errorf("tab bar = %q", lines[0])
	}
	return lines
}

func TestCrowdedChatAt80x24(t *testing.T) {
	a := crowded(t, 80, 24)
	lines := assertCrowdedFrame(t, a, 24)
	// Two banner lines, the identity warning first and the rest folded.
	at := lineOf(t, lines, "identity: unknown(name-matched)")
	if lines[at+1] != "+4 more" {
		t.Errorf("second banner line = %q, want +4 more", lines[at+1])
	}
	// The input shows six lines, scrolled to its cursor: the last line is in view.
	in := strings.Join(lines[at+2:], "\n")
	if !strings.Contains(in, "i8") || strings.Contains(in, "i1\n") {
		t.Errorf("the input did not scroll to its last line:\n%s", in)
	}
	rows := 0
	for _, l := range lines[at+3 : len(lines)-1] {
		if strings.TrimSpace(l) != "" {
			rows++
		}
	}
	if rows != maxInputViewport {
		t.Errorf("input shows %d lines, want %d", rows, maxInputViewport)
	}
}

func TestCrowdedChatWithDrawerAt80x24And80x31(t *testing.T) {
	for _, tc := range []struct{ w, h, drawer int }{{80, 24, 7}, {80, 31, 9}} {
		a := crowded(t, tc.w, tc.h)
		a.want = regionWant{Drawer: true, Attached: true}
		lines := assertCrowdedFrame(t, a, tc.h)
		hs := a.layout()
		if hs.Drawer != tc.drawer {
			t.Errorf("%dx%d: %+v, want a %d-line drawer", tc.w, tc.h, hs, tc.drawer)
		}
		at := lineOf(t, lines, "identity: unknown(name-matched)")
		// banners, drawer, attached line, refusal line, input, status line.
		if got, want := len(lines)-1-at-2, hs.Drawer+hs.Attached+hs.Refusal+hs.Input; got != want {
			t.Errorf("%dx%d: %d lines under the banners, want %d", tc.w, tc.h, got, want)
		}
	}
}

func TestNoDrawerOrListBelow80x24(t *testing.T) {
	for _, size := range [][2]int{{79, 24}, {80, 23}, {60, 30}} {
		a := crowded(t, size[0], size[1])
		a.want = regionWant{Drawer: true, ListLines: 5, Help: true}
		hs := a.layout()
		if hs.Drawer != 0 || hs.List != 0 || hs.Help != 0 {
			t.Errorf("%v: %+v, want no drawer, list or help", size, hs)
		}
		lines := assertCrowdedFrame(t, a, size[1])
		if lineOf(t, lines, "identity: unknown(name-matched)") < 1 {
			t.Errorf("%v: identity warning missing", size)
		}
	}
}

// TestSmallerSizeKeepsTheMinimum runs under -short too: the smallest size the
// TUI draws, with every banner and a long input.
func TestSmallerSizeKeepsTheMinimum(t *testing.T) {
	a := crowded(t, 40, 10)
	lines := chatView(a)
	if len(lines) != 10 || !strings.Contains(lines[len(lines)-1], "tab: switch") && !strings.Contains(lines[len(lines)-1], "idle") && !strings.Contains(lines[len(lines)-1], "engine") {
		t.Fatalf("40x10 frame (%d lines):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if hs := a.layout(); hs.Body < minBodyLines || hs.Drawer != 0 || hs.Input != 1 {
		t.Errorf("40x10: %+v", hs)
	}
}

func TestInputScrollsBeyondSixLines(t *testing.T) {
	a := chatApp(t)
	var rows []string
	for i := 1; i <= 9; i++ {
		rows = append(rows, fmt.Sprintf("row%d", i))
	}
	a.input.SetValue(strings.Join(rows, "\n"))
	a.updateInput(nil)
	lines := chatView(a)
	if hs := a.layout(); hs.Input != maxInputViewport {
		t.Fatalf("input viewport = %d, want %d", hs.Input, maxInputViewport)
	}
	view := strings.Join(lines, "\n")
	if !strings.Contains(view, "row9") || strings.Contains(view, "row1\n") || strings.Contains(view, "row3") {
		t.Errorf("the input did not scroll to the cursor line:\n%s", view)
	}
	if !strings.Contains(lines[len(lines)-1], "tab: switch") {
		t.Errorf("status line is not last: %q", lines[len(lines)-1])
	}
}

func TestChatHeaderCountsEarlierTurnsNotLines(t *testing.T) {
	a := chatApp(t)
	a.workDir, a.conversationID, a.convTurns = "/ws", "0123456789abcdef", 3
	// Turns of different heights: the first answer is long.
	long := strings.TrimSuffix(strings.Repeat("a long answer line\n", 30), "\n")
	addTurn(a, "t1", 1, "approved", "first question", long)
	addTurn(a, "t2", 2, "approved", "second question", "short")
	addTurn(a, "t3", 3, "approved", "third question", strings.TrimSuffix(strings.Repeat("tail line\n", 40), "\n"))

	earlier := func() string {
		h := chatView(a)[1]
		i := strings.Index(h, " earlier")
		if i < 0 {
			t.Fatalf("no scroll info in %q", h)
		}
		return strings.Fields(h[:i])[len(strings.Fields(h[:i]))-1]
	}
	lines, _ := chatLines(a.state, a.chatIn())
	a.scrollAnswer(-len(lines)) // to the top
	if got := earlier(); got != "0" {
		t.Errorf("at the top: %s earlier, want 0", got)
	}
	if h := chatView(a)[1]; !strings.HasSuffix(h, "0%") {
		t.Errorf("at the top: header %q, want 0%%", h)
	}
	_, _, starts := chatLinesWithStarts(a.state, a.chatIn())
	// Scroll until the view starts inside the third turn: two turns lie above.
	a.answerFollow, a.answerScrollRow = false, starts[2]+2
	if got := earlier(); got != "2" {
		t.Errorf("inside turn three: %s earlier, want 2", got)
	}
	// Inside the first turn, no turn lies wholly above, though many lines do.
	a.answerScrollRow = 10
	if got := earlier(); got != "0" {
		t.Errorf("inside turn one: %s earlier, want 0", got)
	}
	// At the end the view reads 100%.
	a.scrollAnswer(len(lines))
	if h := chatView(a)[1]; !strings.HasSuffix(h, "100%") {
		t.Errorf("at the end: header %q, want 100%%", h)
	}
}

func TestChatHeaderKeepsTheWorkspaceWhenScrollInfoDoesNotFit(t *testing.T) {
	a := chatApp(t)
	a.workDir = "/ws/" + strings.Repeat("deep/", 12) + "end"
	header := chatView(a)[1]
	// Only the edge of the screen clips it, never scroll info.
	if !strings.HasPrefix(a.conversationHeader(), header) || strings.Contains(header, "earlier") {
		t.Errorf("header = %q, want the conversation header alone", header)
	}
}

func TestTranscriptTabShowsAPercentage(t *testing.T) {
	a := transcriptApp(80, 24)
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 60)})
	a.Update(tea.KeyMsg{Type: tea.KeyHome})
	if bar := chatView(a)[0]; !strings.HasSuffix(bar, " 0%") {
		t.Errorf("at the top: tab bar %q, want 0%%", bar)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if bar := chatView(a)[0]; !strings.HasSuffix(bar, " 100%") {
		t.Errorf("at the end: tab bar %q, want 100%%", bar)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyHome})
	a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	bar := chatView(a)[0]
	var pct int
	if _, err := fmt.Sscanf(bar[strings.LastIndex(bar, " ")+1:], "%d%%", &pct); err != nil || pct <= 0 || pct >= 100 {
		t.Errorf("part way: tab bar %q", bar)
	}
}
