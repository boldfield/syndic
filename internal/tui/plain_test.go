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
	if len(lines) != 24 || lines[0] != "[chat]  transcript  participants" || !strings.HasPrefix(lines[22], "> ") || !strings.HasPrefix(lines[23], "[") {
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
