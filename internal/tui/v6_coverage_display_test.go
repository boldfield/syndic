package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// These tests close v6 section 1, section 6 and section 8 (Display) clauses
// that the older tests prove only in part. Fake data only; every one runs
// under -short.

// forceColourProfile makes styles emit escape sequences whatever the test
// process's terminal, so a missing suppression shows up.
func forceColourProfile(t *testing.T, p termenv.Profile) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

var colourCaps = TerminalCapabilities{Color: true, AltScreen: true}

// The transcript tab is unformatted safe text whatever /raw says, while the
// chat tab renders the same text as Markdown.
func TestV6TranscriptTabIsAlwaysUnformatted(t *testing.T) {
	const md = "**bold** and `code` and [docs](https://e.io/x)\n- item\n```go\nx := 1\n```"
	a := transcriptApp(100, 40)
	a.terminalCapabilities = colourCaps
	if a.rawMode {
		t.Fatal("rawMode is on at start; the test needs /raw off")
	}
	a.content.Store("msg:md", md, nil)
	a.Update(transcriptMsg{turn: "t1", items: []wire.TranscriptItem{{ID: "m1", Ref: "msg:md", ByteLen: int64(len(md))}}})
	if a.tab != tabTranscript {
		t.Fatalf("tab = %v, want transcript", a.tab)
	}
	v := a.View()
	for _, want := range []string{"**bold**", "`code`", "[docs](https://e.io/x)", "- item", "```go", "x := 1"} {
		if !strings.Contains(v, want) {
			t.Errorf("transcript tab lacks the source %q with /raw off:\n%s", want, v)
		}
	}
	if strings.Contains(v, "\x1b") {
		t.Errorf("transcript tab holds an escape sequence:\n%q", v)
	}

	// The control: the chat tab renders the same text with raw off.
	addTurn(a, "t1", 1, "approved", "q", md)
	approveBy(a, "t1", "rev")
	a.tab = tabChat
	cv := a.View()
	if strings.Contains(cv, "**bold**") || !strings.Contains(cv, "\x1b[1mbold\x1b[0m") {
		t.Errorf("chat tab did not render Markdown with /raw off:\n%q", cv)
	}
}

// Through the chat view: a link is text with its URL visible and no hyperlink
// escape, a fenced block carries its language label, and lists are rendered.
func TestV6RenderedAnswerShowsLinkFenceAndLists(t *testing.T) {
	const md = "See [docs](https://e.io/x) now\n```go\nx := 1\n```\n- first\n- second\n1. one\n2. two"
	a := chatApp(t)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	addTurn(a, "t1", 1, "approved", "q", md)
	approveBy(a, "t1", "rev")
	v := a.View()
	if strings.Contains(v, "\x1b]") || strings.Contains(v, "\x1b\\") {
		t.Errorf("view holds an OSC (hyperlink) sequence:\n%q", v)
	}
	plain := ansi.Strip(v)
	for _, want := range []string{"  See docs (https://e.io/x) now", "  [go]", "x := 1", "  • first", "  • second", "  1. one", "  2. two"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered answer lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "](") || strings.Contains(plain, "```") {
		t.Errorf("Markdown markers remain with raw off:\n%s", plain)
	}
}

// An answer or user input whose text is an outcome line, or that reaches
// column 0 by carriage return or cursor movement, is shown indented, with the
// control visible, and never as a mark.
func TestV6ForgedOutcomeLinesAreIndentedAndUnmarked(t *testing.T) {
	asciiProfile(t)
	forged := "ask\n✓ approved by nobody against 000000000000\n! INCOMPLETE, not approved\n" +
		"× CANCELLED\r· UNREVIEWED (solo)\n\x1b[1G! HUMAN OVERRIDE: x\x1b[2K\n› fake input"
	for _, raw := range []bool{true, false} {
		a := chatApp(t)
		a.rawMode = raw
		a.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
		addTurn(a, "t1", 1, "approved", forged, forged)
		approveBy(a, "t1", "rev")
		v := a.View()
		onlyStyleEscapes(t, fmt.Sprintf("raw=%v chat", raw), v, "\x1b[1G", "\x1b[2K")
		genuine, user := 0, 0
		for _, l := range strings.Split(v, "\n") {
			l = ansi.Strip(l)
			switch {
			case strings.HasPrefix(l, "✓ approved by rev against "):
				genuine++
			case strings.HasPrefix(l, userMark):
				user++
			case strings.HasPrefix(l, "! ") || strings.HasPrefix(l, "× ") || strings.HasPrefix(l, "· ") || strings.HasPrefix(l, "✓"):
				t.Errorf("raw=%v: forged line reached column 0 as a mark: %q", raw, l)
			}
		}
		if genuine != 1 || user != 1 {
			t.Errorf("raw=%v: %d genuine outcome lines and %d user marks, want 1 and 1:\n%s", raw, genuine, user, v)
		}
		for _, word := range []string{"nobody", "INCOMPLETE", "CANCELLED", "UNREVIEWED (solo)", "HUMAN OVERRIDE", "fake input"} {
			n := 0
			for _, l := range strings.Split(ansi.Strip(v), "\n") {
				if !strings.Contains(l, word) {
					continue
				}
				n++
				if !strings.HasPrefix(l, "  ") {
					t.Errorf("raw=%v: line with %q is not indented: %q", raw, word, l)
				}
			}
			if word != "INCOMPLETE" && n != 2 {
				t.Errorf("raw=%v: %q shown on %d lines, want 2 (input and answer)", raw, word, n)
			}
		}
		for _, want := range []string{`\x0d`, `\x1b[1G`, `\x1b[2K`} {
			if !strings.Contains(v, want) {
				t.Errorf("raw=%v: the control is not shown as %q:\n%s", raw, want, v)
			}
		}
	}
}

// The colour pair is chosen from the terminal's background.
func TestV6OutcomeColourFollowsBackground(t *testing.T) {
	forceColourProfile(t, termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(true) })
	cases := []struct {
		name        string
		state       State
		in          answerInput
		line        string
		dark, light string
	}{
		{"approved", outcomeState("approved", 0, true), answerInput{}, "✓ approved by rev against abcdef123456", "38;5;42", "38;5;28"},
		{"incomplete", outcomeState("incomplete", 0, true), answerInput{}, "! INCOMPLETE, not approved", "38;5;214", "38;5;130"},
		{"cancelled", outcomeState("cancelled", 0, true), answerInput{}, "× CANCELLED, not approved", "38;5;203", "38;5;124"},
		{"solo", outcomeState("solo_complete", 0, false), answerInput{}, "· UNREVIEWED (solo)", "38;5;245", "38;5;240"},
	}
	for _, c := range cases {
		c.in.width, c.in.content = 80, NewContentCache()
		lipgloss.SetHasDarkBackground(true)
		dark := renderAnswer(c.state, "t1", c.in)
		lipgloss.SetHasDarkBackground(false)
		light := renderAnswer(c.state, "t1", c.in)
		if !strings.Contains(dark, c.dark) || strings.Contains(dark, c.light) {
			t.Errorf("%s on a dark background: want colour %s, not %s: %q", c.name, c.dark, c.light, dark)
		}
		if !strings.Contains(light, c.light) || strings.Contains(light, c.dark) {
			t.Errorf("%s on a light background: want colour %s, not %s: %q", c.name, c.light, c.dark, light)
		}
		for _, v := range []string{dark, light} {
			if !strings.Contains(ansi.Strip(v), c.line) {
				t.Errorf("%s: coloured line lost its mark or words: %q", c.name, ansi.Strip(v))
			}
		}
	}
}

// Colour off (NO_COLOR) degrades the screen exactly as TERM=dumb does, apart
// from the alternate screen: same view, no animation, no mouse, Markdown off,
// single-line prompts.
func TestV6ColourOffDegradesLikeDumb(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	dumb := DetectTerminalCapabilities()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	noColour := DetectTerminalCapabilities()
	if dumb.Color || dumb.AltScreen || noColour.Color || !noColour.AltScreen {
		t.Fatalf("capabilities: dumb %+v, NO_COLOR %+v", dumb, noColour)
	}

	views := map[string][2]string{}
	for name, caps := range map[string]TerminalCapabilities{"dumb": dumb, "NO_COLOR": noColour} {
		// Markdown off, no meter, no pips, no escapes.
		forceColourProfile(t, termenv.TrueColor)
		ApplyTerminalCapabilities(caps)
		a := runningApp(t, caps, 80, 24)
		addTurn(a, "t0", 0, "approved", "earlier", "**bold** and `code`")
		approveBy(a, "t0", "rev")
		v := a.View()
		if !strings.Contains(v, "**bold** and `code`") || strings.Contains(v, "\x1b") {
			t.Errorf("%s: want unformatted Markdown source and no escape:\n%q", name, v)
		}
		for _, bad := range []string{"█", "░", "●", "○"} {
			if strings.Contains(v, bad) {
				t.Errorf("%s: view holds %q:\n%s", name, bad, v)
			}
		}
		for _, want := range []string{"round 1/3", "100 / 1000", "running r2"} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: view lacks %q:\n%s", name, want, v)
			}
		}
		views[name] = [2]string{v, ""}

		// No mouse.
		a.mouseEnabled = true
		if cmd := cmdMouse(a, []string{"on"}); cmd != nil || a.mouseEnabled {
			t.Errorf("%s: /mouse on captured the mouse", name)
		}
		if len(ProgramOptionsWithMouse(caps, true)) != len(ProgramOptions(caps)) {
			t.Errorf("%s: --mouse added a mouse program option", name)
		}
		if b := NewApp(context.Background(), "", WithTerminalCapabilities(caps), WithMouse(true)); b.mouseEnabled {
			t.Errorf("%s: the app starts with the mouse captured", name)
		}

		// No animation: the drawer opens at once.
		a.reviewsDrawer.SetTurns([]string{"t0"})
		press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
		if !a.reviewsDrawer.IsOpen() || a.reviewsDrawer.IsAnimating() || a.reviewsDrawer.AnimProgress() != 1 {
			t.Errorf("%s: drawer open=%v animating=%v progress=%v", name, a.reviewsDrawer.IsOpen(), a.reviewsDrawer.IsAnimating(), a.reviewsDrawer.AnimProgress())
		}
		press(a, tea.KeyMsg{Type: tea.KeyEsc})

		// Prompts are single-line questions as in v4.
		if cmd := openReasonForm(a, []string{"release?"}, nil); cmd != nil || a.reasonForm != nil || a.input.Prompt != reasonPrompt {
			t.Errorf("%s: reason prompt is not the single-line prompt (cmd=%v form=%v prompt=%q)", name, cmd != nil, a.reasonForm != nil, a.input.Prompt)
		}
		if pv := a.View(); !strings.Contains(pv, reasonPrompt) || strings.Contains(pv, "\x1b") {
			t.Errorf("%s: prompt view:\n%q", name, pv)
		}
	}
	if views["dumb"] != views["NO_COLOR"] {
		t.Errorf("NO_COLOR view differs from the TERM=dumb view:\n--- dumb ---\n%s\n--- NO_COLOR ---\n%s", views["dumb"][0], views["NO_COLOR"][0])
	}
}

// With TERM=dumb every screen renders with no escape sequence, even with the
// hostile content and the styles that colour would otherwise draw.
func TestV6DumbEveryScreenHasNoEscape(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	caps := DetectTerminalCapabilities()
	if caps.Color || caps.AltScreen {
		t.Fatalf("TERM=dumb capabilities = %+v", caps)
	}
	const hostile = "h \x1b[31mred\x1b[0m \x1b]8;;http://x\x1b\\L\r\u202e **b** `c` [l](http://u)"
	screens := []struct {
		name  string
		build func(a *App)
	}{
		{"chat", func(a *App) {
			addTurn(a, "t1", 1, "approved", hostile, hostile)
			approveBy(a, "t1", "rev")
			a.reviewCounts["t1"] = ptr(1)
			a.tab = tabChat
		}},
		{"outcomes", func(a *App) {
			for i, st := range []string{"incomplete", "overridden", "cancelled", "solo_complete"} {
				addTurn(a, fmt.Sprintf("t%d", i), int64(i+1), st, "q", "answer")
			}
		}},
		{"transcript", func(a *App) {
			a.content.Store("msg:h", hostile, nil)
			a.Update(transcriptMsg{turn: "t1", items: []wire.TranscriptItem{{ID: "m", Ref: "msg:h", ByteLen: int64(len(hostile))}}})
			a.tab = tabTranscript
		}},
		{"participants", func(a *App) {
			a.state.Participants["p1"] = ParticipantInfo{ID: "p1", State: "idle", Identity: "unknown"}
			a.tab = tabParticipants
		}},
		{"drawer", func(a *App) {
			addTurn(a, "t1", 1, "incomplete", "q", "answer")
			a.reviewsDrawer.SetTurns([]string{"t1"})
			a.content.Store("c1", hostile, nil)
			a.Update(reviewsMsg{turnID: "t1", items: []wire.ReviewItem{{Round: 0, Participant: "rev", Kind: "block",
				Blocks:     []wire.ReviewBlock{{BlockID: "b1", State: "opened"}},
				Commentary: &wire.ContentReference{Ref: "c1"}}}})
			press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
			if !a.reviewsDrawer.IsOpen() {
				t.Fatal("the drawer did not open")
			}
		}},
		{"command list", func(a *App) { typeRunes(a, "/") }},
		{"short help", func(a *App) { typeRunes(a, "?") }},
		{"banners", func(a *App) {
			a.state.Participants["p1"] = ParticipantInfo{ID: "p1", Identity: "unknown"}
			a.state.Status.DeadlinesProvisional = true
			a.state.ConnState = ConnStateReconnecting
		}},
		{"reason prompt", func(a *App) { openReasonForm(a, []string{"release?"}, nil) }},
		{"panel", func(a *App) { a.panel = []string{"state: " + hostile} }},
		{"too small", func(a *App) { a.Update(tea.WindowSizeMsg{Width: 30, Height: 8}) }},
	}
	for _, size := range [][2]int{{80, 24}, {100, 40}} {
		for _, sc := range screens {
			forceColourProfile(t, termenv.TrueColor)
			a := chatApp(t)
			a.terminalCapabilities = caps
			ApplyTerminalCapabilities(caps)
			a.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			sc.build(a)
			v := a.View()
			if strings.Contains(v, "\x1b") {
				t.Errorf("%s at %dx%d holds an escape sequence:\n%q", sc.name, size[0], size[1], v)
			}
			if sc.name != "too small" && !strings.HasSuffix(v, keyHint) && !strings.Contains(v, keyHint) {
				t.Errorf("%s at %dx%d: no status line:\n%s", sc.name, size[0], size[1], v)
			}
		}
	}

	// The control: the same chat with colour on does hold escapes.
	forceColourProfile(t, termenv.TrueColor)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "**b**")
	approveBy(a, "t1", "rev")
	if !strings.Contains(a.View(), "\x1b") {
		t.Error("the control chat view holds no escape, so the scans above prove nothing")
	}
}

// The status line is the last line, one line high, at every size and on every
// tab, and it ends with the hint; the tab bar is the first line.
func TestV6StatusLineLastAtEverySizeAndTab(t *testing.T) {
	for _, w := range []int{40, 60, 79, 80, 100, 140} {
		for _, h := range []int{10, 14, 23, 24, 30, 31, 50} {
			for _, tb := range []tab{tabChat, tabTranscript, tabParticipants} {
				a := crowded(t, w, h)
				a.tab = tb
				a.want = regionWant{Drawer: true, Help: true}
				lines := chatView(a)
				label := fmt.Sprintf("%dx%d tab %d", w, h, tb)
				if len(lines) != h {
					t.Fatalf("%s: frame has %d lines, want %d:\n%s", label, len(lines), h, strings.Join(lines, "\n"))
				}
				assertStatusLine(t, w, lines[len(lines)-1])
				if !strings.Contains(lines[0], "chat") || !strings.Contains(lines[0], "participants") {
					t.Errorf("%s: first line is not the tab bar: %q", label, lines[0])
				}
				if hs := a.layout(); hs.TabBar != 1 || hs.Status != 1 || (w >= 80 && h >= 24 && hs.Body < minBodyLines) {
					t.Errorf("%s: %+v", label, hs)
				}
			}
		}
	}
}

// A command list takes at most five lines, below the tab and above the input,
// and closes when the drawer opens.
func TestV6CommandListTakesFiveLinesAndClosesWithDrawer(t *testing.T) {
	for _, h := range []int{24, 40} {
		a := chatApp(t)
		a.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		addTurn(a, "t1", 1, "approved", "q", "a")
		approveBy(a, "t1", "rev")
		a.reviewsDrawer.SetTurns([]string{"t1"})
		typeRunes(a, "/")
		if n := len(commandListMatches("/")); n <= maxListLines {
			t.Fatalf("only %d commands; the test needs more than %d", n, maxListLines)
		}
		listed := func() int {
			n := 0
			for _, l := range chatView(a) {
				if strings.HasPrefix(l, "› /") || strings.HasPrefix(l, "  /") {
					n++
				}
			}
			return n
		}
		lines := chatView(a)
		if !a.commandListOpen() || a.layout().List != maxListLines || listed() != maxListLines || len(lines) != h {
			t.Fatalf("h=%d: open=%v layout=%+v listed=%d lines=%d", h, a.commandListOpen(), a.layout(), listed(), len(lines))
		}
		assertStatusLine(t, 100, lines[len(lines)-1])
		if a.layout().Body < minBodyLines {
			t.Errorf("h=%d: body %d lines", h, a.layout().Body)
		}
		press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
		if !a.reviewsDrawer.IsOpen() || a.commandListOpen() || a.layout().List != 0 || listed() != 0 {
			t.Errorf("h=%d: the list did not close with the drawer: open=%v list=%v layout=%+v listed=%d",
				h, a.reviewsDrawer.IsOpen(), a.commandListOpen(), a.layout(), listed())
		}
		if got := a.input.Value(); got != "/" {
			t.Errorf("h=%d: opening the drawer changed the input to %q", h, got)
		}
	}
}

// The short help line is one line and is hidden while the drawer is open.
func TestV6ShortHelpIsOneLineAndHiddenWithDrawer(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "a")
	approveBy(a, "t1", "rev")
	a.reviewsDrawer.SetTurns([]string{"t1"})
	typeRunes(a, "?")
	if !a.showShortHelp || a.layout().Help != 1 {
		t.Fatalf("short help not shown: %v %+v", a.showShortHelp, a.layout())
	}
	help := a.shortHelpLine()
	lines := chatView(a)
	if len(lines) != 24 || strings.Contains(help, "\n") {
		t.Fatalf("frame has %d lines; help %q", len(lines), help)
	}
	hs := a.layout()
	if got := lines[len(lines)-1-hs.Input-hs.Refusal-hs.Help]; got != help && !strings.HasPrefix(got, help) {
		t.Errorf("the help line is %q, want %q just above the input", got, help)
	}
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !a.reviewsDrawer.IsOpen() || a.layout().Help != 0 || strings.Contains(a.View(), "? hide") {
		t.Errorf("short help shown with the drawer open: %+v", a.layout())
	}
}

// The drawer takes 7 lines, 9 above 30 rows, in the frame itself.
func TestV6DrawerHeightInFrame(t *testing.T) {
	for _, tc := range []struct{ h, drawer int }{{24, 7}, {30, 7}, {31, 9}, {50, 9}} {
		asciiProfile(t)
		a := chatApp(t)
		a.Update(tea.WindowSizeMsg{Width: 80, Height: tc.h})
		addTurn(a, "t1", 1, "incomplete", "q", "answer")
		a.reviewsDrawer.SetTurns([]string{"t1"})
		a.reviewsDrawer.SetRoster([]string{"alpha"})
		a.Update(reviewsMsg{turnID: "t1", items: []wire.ReviewItem{{Round: 0, Participant: "alpha", Kind: "block",
			Blocks: []wire.ReviewBlock{{BlockID: "b1", State: "opened"}}}}})
		openDrawerSettled(t, a)
		hs := a.layout()
		lines := chatView(a)
		if hs.Drawer != tc.drawer || len(lines) != tc.h || hs.Body < minBodyLines {
			t.Fatalf("%dx80: layout %+v, %d lines", tc.h, hs, len(lines))
		}
		at := lineOf(t, lines, "Reviews · Turn 1")
		if want := len(lines) - 1 - hs.Input - hs.Refusal - hs.Drawer; at != want {
			t.Errorf("h=%d: the drawer starts at line %d, want %d (%d lines above the input)", tc.h, at, want, tc.drawer)
		}
	}
}

// The status line shows, left to right, the preset, the quick profile, the
// turn state, elapsed over deadline, the meter with tokens/limit and round
// n/m, and at the right edge the hints. Participant states are not in it.
func TestV6StatusLineFieldsInOrderWithoutParticipantStates(t *testing.T) {
	a := runningApp(t, colourCaps, 140, 24)
	ti := a.state.Turns["t1"]
	ti.QuickProfile = ptr("fast")
	ti.DeadlineDur = 15 * time.Minute
	ti.EstimatedTokens = 500
	a.state.Turns["t1"] = ti
	a.tab = tabChat
	line := a.statusLine(140)
	assertStatusLine(t, 140, line)
	prev := -1
	for _, part := range []string{"[review]", "[quick:fast]", "[reviewers_dispatched]", "/ 15m0s]", "█", "500 / 1000", "[round 1/3]", reviewsHint, keyHint} {
		i := strings.Index(line, part)
		if i <= prev {
			t.Errorf("%q at %d, want after %d in %q", part, i, prev, line)
		}
		prev = i
	}
	for _, bad := range []string{"r1", "r2", "running"} {
		if strings.Contains(line, bad) {
			t.Errorf("status line holds participant state %q: %q", bad, line)
		}
	}
	a.tab = tabTranscript
	if got := a.statusLine(140); strings.Contains(got, reviewsHint) || !strings.HasSuffix(got, keyHint) {
		t.Errorf("transcript tab status line: %q", got)
	}
	// Before any revision there is no round; with no turn there is idle and no elapsed time.
	ti.Round = 0
	a.state.Turns["t1"] = ti
	if got := a.statusLine(140); strings.Contains(got, "round") {
		t.Errorf("round shown before a revision: %q", got)
	}
	a.state.CurrentTurn = ""
	a.tab = tabChat
	if got := a.statusLine(140); !strings.Contains(got, "[idle]") || strings.Contains(got, " / ") {
		t.Errorf("idle status line: %q", got)
	}
}

// The identity warning is a banner on every tab, sorted first, and stays in
// the participants tab's detail.
func TestV6IdentityWarningBannerAndParticipantsTab(t *testing.T) {
	a := chatApp(t)
	a.state.Participants["zed"] = ParticipantInfo{ID: "zed", Identity: "unknown"}
	a.state.Participants["alpha"] = ParticipantInfo{ID: "alpha", Identity: ""}
	a.state.Participants["known"] = ParticipantInfo{ID: "known", Identity: "model-x"}
	a.state.ConnState = ConnStateReconnecting
	const banner = "identity: unknown(name-matched): alpha, zed"
	for _, tb := range []tab{tabChat, tabTranscript, tabParticipants} {
		a.tab = tb
		lines := chatView(a)
		at := lineOf(t, lines, banner)
		if lines[at+1] != "engine: reconnecting" {
			t.Errorf("tab %d: banner after the identity warning = %q", tb, lines[at+1])
		}
	}
	a.tab = tabParticipants
	if v := a.View(); !strings.Contains(v, "identity: unknown (name-matched)") {
		t.Errorf("the participants tab lost the identity line:\n%s", v)
	}
}

// A real engine returns the answer's bytes unchanged through the transcript
// content reference while /raw on shows the same answer as visible safe text
// without Markdown rendering; the TUI's cache keeps the original bytes too.
func TestV6RawAnswerSafeTextAndTranscriptRefKeepsBytes(t *testing.T) {
	asciiProfile(t)
	const hostile = "line1 \x1b[31mred\x1b[0m\r\u202eover\nline2 **b** `c`"
	h := scripted(t, enginetest.Reply(hostile), approve())
	r := startAnswerRig(t, h)
	r.typeLine("/raw on")
	r.until(t, "raw on", func(a *App) (string, bool) { return "", a.rawMode })
	r.typeLine("question")
	turn := turnIn(t, r, "approved")
	v := untilView(t, r, `line1 \x1b[31mred\x1b[0m\x0d\u202eover`, "line2 **b** `c`")
	onlyControlFree(t, "raw chat", v)
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "line2") && !strings.HasPrefix(l, "  ") {
			t.Errorf("answer line not indented: %q", l)
		}
	}

	cl := dial(t, r.paths)
	reply, err := cl.Transcript(context.Background(), turn, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range reply.Items {
		body, err := cl.Content(context.Background(), it.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(body, []byte(hostile)) {
			found = true
		}
	}
	if !found {
		t.Errorf("no transcript content reference returned the answer's bytes unchanged (%d items)", len(reply.Items))
	}
	r.until(t, "the cache keeps the original bytes", func(a *App) (string, bool) {
		item := a.content.Get("candidate:" + a.candidateIDByTurn[turn])
		return fmt.Sprintf("%q", item.Data), item.Ready && item.Data == hostile
	})
}

// When more banners apply than the strip shows, the participants tab lists
// every one of them.
func TestV6ParticipantsTabListsEveryFoldedBanner(t *testing.T) {
	a := chatApp(t)
	a.state.Participants["alpha"] = ParticipantInfo{ID: "alpha", Identity: "unknown"}
	a.state.ConnState = ConnStateReconnecting
	a.state.Status = &wire.StatusReply{DeadlinesProvisional: true, RosterFileChanged: true}
	quick := "fast"
	a.state.UnapprovedProfile = &quick
	a.tab = tabParticipants
	v := a.View()
	for _, want := range []string{
		"identity: unknown(name-matched): alpha",
		"deadlines: provisional",
		"roster file changed since start; restart the engine to apply",
		"engine: reconnecting",
		"quick: unapproved fast",
	} {
		if n := strings.Count(v, want); n < 1 || (want != "identity: unknown(name-matched): alpha" && n != 1) {
			t.Errorf("banner %q appears %d times in the participants tab:\n%s", want, n, v)
		}
	}
}

// The input's prompt is "┃ " in the chat tab, every wrapped line is indented
// under it, and a literal "┃" typed as content is just content.
func TestV6InputPromptIsABarAndTheAreaIsMultiLine(t *testing.T) {
	if defaultPrompt != "┃ " {
		t.Fatalf("defaultPrompt = %q, want %q", defaultPrompt, "┃ ")
	}
	a := offlineApp(t)
	if got := inputRegion(t, a); len(got) != 1 || got[0] != "┃" {
		t.Errorf("empty input region = %q, want one line with the bar", got)
	}
	typeRunes(a, "one")
	press(a, altEnter)
	typeRunes(a, "two")
	want := []string{"┃ one", "  two"}
	if got := inputRegion(t, a); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("input region = %q, want %q", got, want)
	}
}
