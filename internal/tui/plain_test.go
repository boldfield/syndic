package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
)

// These tests start the App the way cmd/syndic does, with the options the
// detected capabilities allow, and inspect what the program writes.

var (
	sgrSeq     = regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	altScreen  = "\x1b[?1049h"
	mouseModes = []string{"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1003h", "\x1b[?1006h"}
)

// plainRig is a rig whose program output is kept for inspection.
type plainRig struct {
	*rig
	out bytes.Buffer
	src io.Reader
}

// startPlainRig runs the App under the environment the caller has set with
// t.Setenv, at the given size. Styling is forced on first, so a missing
// suppression shows up as escape sequences rather than hiding behind the
// test process having no terminal.
func startPlainRig(t *testing.T, h *enginetest.Harness, width, height int) *plainRig {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	caps := DetectTerminalCapabilities()
	ApplyTerminalCapabilities(caps)

	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket, WithTerminalCapabilities(caps))
	// teatest builds the program itself and takes no program options, so the
	// alternate screen the capabilities allow is entered from Init; the
	// options cmd/syndic passes are checked in TestProgramOptions.
	var enter tea.Cmd
	if len(ProgramOptions(caps)) > 0 {
		enter = tea.EnterAltScreen
	}
	r.tm = teatest.NewTestModel(t, &probe{app: r.app, enter: enter},
		teatest.WithInitialTermSize(width, height))
	t.Cleanup(func() { _ = r.tm.Quit() })
	pr := &plainRig{rig: r}
	pr.src = io.TeeReader(r.tm.Output(), &pr.out)
	r.waitConnected(t)
	return pr
}

// waitOutput waits for the program to write want.
func (p *plainRig) waitOutput(t *testing.T, want string) {
	t.Helper()
	teatest.WaitFor(t, p.src, func(b []byte) bool { return bytes.Contains(b, []byte(want)) },
		teatest.WithDuration(10*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

// captured is everything the program has written, once it has quit.
func (p *plainRig) captured(t *testing.T) string {
	t.Helper()
	p.final(t)
	_, _ = io.Copy(io.Discard, p.src)
	return p.out.String()
}

func assertNoColor(t *testing.T, out string) {
	t.Helper()
	if m := sgrSeq.FindString(out); m != "" {
		t.Errorf("output holds a styling sequence %q:\n%q", m, out)
	}
}

func assertFits(t *testing.T, what, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Errorf("%s: %d lines, want at most %d:\n%s", what, len(lines), height, view)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s: line %d is %d cells, want at most %d: %q", what, i, w, width, l)
		}
	}
}

func TestDumbTerminalSoloTurn(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "")
	h := scripted(t, enginetest.Reply(answerText), approve())
	p := startPlainRig(t, h, 80, 24)
	solo := "solo"
	submit(t, p.rig, wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &solo})
	turnIn(t, p.rig, "solo_complete")
	p.waitOutput(t, answerText)

	out := p.captured(t)
	assertNoColor(t, out)
	for _, seq := range append([]string{altScreen}, mouseModes...) {
		if strings.Contains(out, seq) {
			t.Errorf("TERM=dumb output holds %q", seq)
		}
	}
}

func TestNoColorKeepsAltScreen(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	p := startPlainRig(t, h, 80, 24)
	p.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	p.typeLine("question")
	p.waitOutput(t, "!! UNREVIEWED DRAFT !!")

	out := p.captured(t)
	assertNoColor(t, out)
	if !strings.Contains(out, altScreen) {
		t.Errorf("NO_COLOR output does not enter the alternate screen:\n%q", out)
	}
}

// TestColorIsEmittedWithColor shows the scans above can fail: with colour on,
// the draft banner is styled.
func TestColorIsEmittedWithColor(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	p := startPlainRig(t, h, 80, 24)
	p.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	p.typeLine("question")
	p.waitOutput(t, "UNREVIEWED DRAFT")
	if out := p.captured(t); !sgrSeq.MatchString(out) {
		t.Errorf("no styling sequence with colour on:\n%q", out)
	}
}

func TestPlainDraftBannerInView(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "")
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	p := startPlainRig(t, h, 80, 24)
	p.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	p.typeLine("question")
	v := untilView(t, p.rig, "!! UNREVIEWED DRAFT !!", answerText)
	assertNoColor(t, v)
}

func TestEveryTabFitsAt80x24(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	p := startPlainRig(t, h, 80, 24)
	p.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	openReviewTurn(t, p.rig)

	checkTabs := func(phase string) {
		for i := 0; i < int(numTabs); i++ {
			v := view(t, p.rig)
			assertFits(t, fmt.Sprintf("%s, tab %d", phase, i), v, 80, 24)
			send(p.rig, tea.KeyMsg{Type: tea.KeyTab})
		}
	}
	checkTabs("during the turn")
	release()
	turnIn(t, p.rig, "approved")
	checkTabs("after the turn")
}

func TestFourBannersCollapseAt80x24(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	p := startPlainRig(t, newHarness(t), 80, 24)
	p.waitCaughtUp(t)

	var body int
	v := p.until(t, "four banners", func(a *App) (string, bool) {
		a.state.Status.DeadlinesProvisional = true
		a.state.Status.RosterFileChanged = true
		a.state.ConnState = ConnStateReconnecting
		profile := "fast"
		a.state.UnapprovedProfile = &profile
		body = a.bodyHeight()
		return a.View(), true
	})
	if n := len(renderBanners(State{ConnState: ConnStateReconnecting})); n != 1 {
		t.Fatalf("renderBanners sanity check: %d", n)
	}
	assertFits(t, "four banners", v, 80, 24)
	lines := strings.Split(v, "\n")
	// tab bar, tab area, banners, input, status line.
	at := 1 + body
	if !strings.HasPrefix(lines[at], "identity: unknown(name-matched)") || lines[at+1] != "+4 more" {
		t.Errorf("banner region = %q, %q; want the identity banner first and +4 more", lines[at], lines[at+1])
	}
	if body < 5 {
		t.Errorf("tab area has %d lines, want at least 5", body)
	}
	// tab bar, tab area, banners, input and status line fill the frame.
	if want := tabBarHeight + body + 2 + 2 + stripHeight; len(lines) != want {
		t.Errorf("frame has %d lines, want %d", len(lines), want)
	}
}

func TestResizeBelowMinimumAndBack(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	p := startPlainRig(t, newHarness(t), 80, 24)
	p.waitCaughtUp(t)

	for _, size := range [][2]int{{30, 8}, {10, 8}, {80, 9}, {39, 24}} {
		send(p.rig, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := view(t, p.rig)
		// Narrow terminals get a clipped message, so v is a prefix of the full one.
		if strings.Contains(v, "\n") || v == "" || !strings.HasPrefix("Terminal too small. Resize to 40×10.", v) {
			t.Errorf("%dx%d: want a single line asking for a larger terminal, got %q", size[0], size[1], v)
		}
		assertFits(t, fmt.Sprintf("%dx%d", size[0], size[1]), v, size[0], 1)
	}
	send(p.rig, tea.WindowSizeMsg{Width: 80, Height: 24})
	v := view(t, p.rig)
	assertFits(t, "restored", v, 80, 24)
	lines := strings.Split(v, "\n")
	if len(lines) != 24 || lines[0] != "[chat]  transcript  participants" || !strings.HasPrefix(lines[22], defaultPrompt) || !strings.HasSuffix(lines[23], keyHint) {
		t.Errorf("layout not restored:\n%s", v)
	}
}

func TestWideCandidateStaysWithinWidth(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	cjk := strings.Repeat("测试文本🚀", 20) + " " + strings.Repeat("emoji 😀 mixed 世界 ", 6)
	h := scripted(t, enginetest.Reply(cjk), approve())
	release := h.Hold("reviewer")
	p := startPlainRig(t, h, 80, 24)
	p.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	openReviewTurn(t, p.rig)

	v := untilView(t, p.rig, "UNREVIEWED DRAFT", "测试")
	assertFits(t, "draft", v, 80, 24)
	release()
	v = untilView(t, p.rig, "approved by", "测试")
	assertFits(t, "approved", v, 80, 24)
}

func TestProgramOptions(t *testing.T) {
	for _, c := range []struct {
		name string
		caps TerminalCapabilities
		want int
	}{
		{"dumb", TerminalCapabilities{}, 0},
		{"no colour", TerminalCapabilities{AltScreen: true}, 1},
		{"full", TerminalCapabilities{Color: true, AltScreen: true}, 1},
	} {
		if got := len(ProgramOptions(c.caps)); got != c.want {
			t.Errorf("%s: %d options, want %d", c.name, got, c.want)
		}
	}
}

// TestPlainModeProgressLineNoSpinner verifies that in plain mode, the progress line
// contains no spinner animation, pips are omitted, and state words are used instead.
func TestPlainModeProgressLineNoSpinner(t *testing.T) {
	now := stripT0.Add(42 * time.Second)
	spin := spinnerAt(now)

	// In plain mode, spinner should be omitted and round pips omitted
	s := func() State {
		s := NewState()
		s.CurrentTurn = "t1"
		s.Turns["t1"] = TurnInfo{
			ID: "t1", State: "reviewers_dispatched", Preset: "review",
			SubmittedAt: stripT0, Round: 1, MaxRounds: 3,
			Author: "w", AuthorState: "idle",
			Reviewers:      []string{"r1", "r2"},
			ReviewerStates: map[string]string{"r1": "idle", "r2": "running"},
		}
		s.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: "w", Phase: "author", Epoch: 1, Status: "complete"}
		s.Attempts["v1"] = AttemptInfo{ID: "v1", TurnID: "t1", Participant: "r1", Phase: "review", Epoch: 1, Status: "complete"}
		return s
	}()

	plainModeProgress := progressLine(s, "t1", now, true)
	colorModeProgress := progressLine(s, "t1", now, false)

	// Plain mode should not have spinner glyphs
	if strings.Contains(plainModeProgress, spin) {
		t.Errorf("plain mode progress contains spinner %q: %q", spin, plainModeProgress)
	}

	// Plain mode should not have pips (●○)
	if strings.Contains(plainModeProgress, "●") || strings.Contains(plainModeProgress, "○") {
		t.Errorf("plain mode progress should not have pips: %q", plainModeProgress)
	}

	// Plain mode should still have round n/m
	if !strings.Contains(plainModeProgress, "round 1/3") {
		t.Errorf("plain mode progress missing 'round 1/3': %q", plainModeProgress)
	}

	// Color mode should have pips and spinner
	if !strings.Contains(colorModeProgress, "●") || !strings.Contains(colorModeProgress, "○") {
		t.Errorf("color mode progress should have pips: %q", colorModeProgress)
	}

	// Plain mode should not have escape sequences
	if strings.ContainsRune(plainModeProgress, '\x1b') {
		t.Errorf("plain mode progress contains escape sequence: %q", plainModeProgress)
	}
}

// TestPlainModeStatusLineNoMeter verifies that in plain mode, the status line
// contains no budget meter but keeps tokens/limit.
func TestPlainModeStatusLineNoMeter(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		ID: "t1", State: "author_dispatched", Preset: "review",
		SubmittedAt:     stripT0,
		EstimatedTokens: 100,
		LimitTokens:     1000,
	}

	now := stripT0.Add(5 * time.Second)

	plainStrip := renderStrip(s, 200, now, true)
	colorStrip := renderStrip(s, 200, now, false)

	// Plain mode should have tokens/limit
	if !strings.Contains(plainStrip, "est. 100 / 1000") {
		t.Errorf("plain mode strip missing 'est. 100 / 1000': %q", plainStrip)
	}

	if strings.ContainsAny(plainStrip, "█░") {
		t.Errorf("plain mode strip has a meter: %q", plainStrip)
	}
	if !strings.ContainsAny(colorStrip, "█░") {
		t.Errorf("color mode strip has no meter: %q", colorStrip)
	}

	// Plain mode should not have escape sequences
	if strings.ContainsRune(plainStrip, '\x1b') {
		t.Errorf("plain mode strip contains escape sequence: %q", plainStrip)
	}
}

// runningApp is an App with a revision turn in flight, at the given size.
func runningApp(t *testing.T, caps TerminalCapabilities, width, height int) *App {
	t.Helper()
	a := chatApp(t)
	a.terminalCapabilities = caps
	a.width, a.height = width, height
	a.input.SetWidth(width)
	addTurn(a, "t1", 1, "reviewers_dispatched", "question", "")
	ti := a.state.Turns["t1"]
	ti.Round, ti.MaxRounds = 1, 3
	ti.Author, ti.AuthorState = "w", "idle"
	ti.Reviewers = []string{"r1", "r2"}
	ti.ReviewerStates = map[string]string{"r1": "idle", "r2": "running"}
	ti.EstimatedTokens, ti.LimitTokens = 100, 1000
	a.state.Turns["t1"] = ti
	a.state.CurrentTurn = "t1"
	return a
}

// TestPlainViewStates drives the App itself through colour-off, dumb and
// below-80x24 states and checks what View draws and what the mouse does.
func TestPlainViewStates(t *testing.T) {
	for _, c := range []struct {
		name          string
		caps          TerminalCapabilities
		width, height int
	}{
		{"colour off", TerminalCapabilities{}, 80, 24},
		{"narrow", TerminalCapabilities{Color: true, AltScreen: true}, 79, 24},
		{"short", TerminalCapabilities{Color: true, AltScreen: true}, 80, 23},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := runningApp(t, c.caps, c.width, c.height)
			view := a.View()
			for _, bad := range []string{"\x1b", "●", "○", "█", "░", "⋯", markQueued} {
				if strings.Contains(view, bad) {
					t.Errorf("view holds %q:\n%s", bad, view)
				}
			}
			for _, want := range []string{"reviewers_dispatched turn in progress", "round 1/3", "running r2", "100 / 1000"} {
				if !strings.Contains(view, want) {
					t.Errorf("view lacks %q:\n%s", want, view)
				}
			}
			assertFits(t, c.name, view, c.width, c.height)

			a.mouseEnabled = true
			x, y := where(t, a, "transcript")
			click(a, x, y)
			if a.tab != tabChat {
				t.Error("the mouse acted in plain mode")
			}
			if cmd := cmdMouse(a, []string{"on"}); cmd != nil || a.mouseEnabled {
				t.Error("/mouse on captured the mouse in plain mode")
			}
			a.input.SetValue("hello")
			if !strings.Contains(a.View(), "hello") {
				t.Error("input is not usable")
			}
		})
	}
}

// TestColourViewKeepsDecoration guards the other side: at 80x24 with colour
// the spinner and pips are drawn.
func TestColourViewKeepsDecoration(t *testing.T) {
	a := runningApp(t, TerminalCapabilities{Color: true, AltScreen: true}, 80, 24)
	view := a.View()
	if !strings.Contains(view, "●") || !strings.Contains(view, "○") {
		t.Errorf("colour view lacks pips:\n%s", view)
	}
}

// TestShrinkingIntoPlainModeReleasesTheMouse: a resize below 80x24 turns the
// mouse off and asks the program to stop capturing.
func TestShrinkingIntoPlainModeReleasesTheMouse(t *testing.T) {
	a := runningApp(t, TerminalCapabilities{Color: true, AltScreen: true}, 80, 24)
	a.mouseEnabled = true
	_, cmd := a.Update(tea.WindowSizeMsg{Width: 79, Height: 24})
	if a.mouseEnabled || cmd == nil {
		t.Errorf("mouseEnabled=%v cmd=%v after shrinking", a.mouseEnabled, cmd)
	}
}

// TestSmallColourTerminalUsesSingleLineReasonPrompt checks that below 80x24 a
// colour terminal gets the plain single-line reason prompt, not the Huh form.
func TestSmallColourTerminalUsesSingleLineReasonPrompt(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
	}{{"narrow", 79, 24}, {"short", 80, 23}} {
		t.Run(c.name, func(t *testing.T) {
			a := runningApp(t, TerminalCapabilities{Color: true, AltScreen: true}, c.width, c.height)
			if cmd := openReasonForm(a, []string{"release?"}, nil); cmd != nil {
				t.Error("the rich form was started in plain mode")
			}
			if a.reasonForm != nil {
				t.Error("reasonForm set in plain mode")
			}
			if view := a.View(); !strings.Contains(view, reasonPrompt) || strings.Contains(view, "\x1b") {
				t.Errorf("view lacks the single-line prompt:\n%s", view)
			}
		})
	}
}
