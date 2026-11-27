package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// These tests close the drawer, mouse, notify and help clauses of the v6
// presentation delta that no other test proves on its own: they drive the App
// through Update and read View, or run a real engine behind the usual rig.

// covApp is a chat App with colour and the given options, in a temporary work
// directory so nothing it does can touch the real one.
func covApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts = append([]Option{WithWorkDir(t.TempDir()), WithTerminalCapabilities(TerminalCapabilities{Color: true, AltScreen: true})}, opts...)
	a := NewApp(ctx, "", opts...)
	a.now = func() time.Time { return chatNow }
	a.width, a.height = 80, 24
	a.state.ConnState = ConnStateConnected
	a.state.Status = &wire.StatusReply{DefaultPreset: "review", Revision: wire.Revision{Default: 2, Ceiling: 5}}
	return a
}

// covWaitView polls the view until it holds every part, and on timeout fails
// with the last view so a miss can be read.
func covWaitView(t *testing.T, r *rig, parts ...string) string {
	t.Helper()
	var v string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		v = view(t, r)
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(v, p)
		}
		if ok {
			return v
		}
	}
	t.Fatalf("the view never held %q; last view:\n%s", parts, v)
	return v
}

// submittedCount is how many turn_submitted events the engine has journalled.
func submittedCount(r *rig) int {
	n := 0
	for _, e := range r.h.Events() {
		if _, ok := e.Payload.(*wire.EvTurnSubmitted); ok {
			n++
		}
	}
	return n
}

// Opening the drawer, moving in it and closing it never cancels or submits:
// a half-typed input survives, no turn is submitted, none is cancelled, and
// the open turn goes on to its approval.
func TestV6CoverageOpeningDrawerNeverCancelsOrSubmits(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	r := startAnswerRig(t, h)
	send(r, tea.WindowSizeMsg{Width: 80, Height: 40})
	turn := openReviewTurn(t, r)
	if n := submittedCount(r); n != 1 {
		t.Fatalf("%d turns submitted before the drawer, want 1", n)
	}
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a half-typed second question")})
	r.until(t, "the draft typed", func(a *App) (string, bool) { return "", a.input.Value() == "a half-typed second question" })
	before := sent(r)

	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "the drawer open", func(a *App) (string, bool) { return "", a.reviewsDrawer.IsOpen() })
	send(r,
		tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyLeft}, tea.KeyMsg{Type: tea.KeyRight},
		tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgUp},
		tea.KeyMsg{Type: tea.KeyCtrlO}) // Ctrl+O closes it again
	r.until(t, "the drawer closed", func(a *App) (string, bool) { return "", !a.reviewsDrawer.IsOpen() })
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO}, tea.KeyMsg{Type: tea.KeyEsc}) // and Esc closes it too
	r.until(t, "the drawer closed by Esc", func(a *App) (string, bool) {
		return a.input.Value(), !a.reviewsDrawer.IsOpen() && !a.reviewsDrawer.IsAnimating()
	})

	if got := r.until(t, "the input", func(a *App) (string, bool) { return a.input.Value(), true }); got != "a half-typed second question" {
		t.Errorf("the input changed to %q", got)
	}
	if n := submittedCount(r); n != 1 {
		t.Errorf("%d turns submitted, want still 1", n)
	}
	if sent(r) != before {
		t.Errorf("the drawer sent something: %d -> %d", before, sent(r))
	}
	for _, st := range h.TurnStates(turn) {
		if st == "cancelled" {
			t.Fatalf("the drawer cancelled the turn: %v", h.TurnStates(turn))
		}
	}
	if open := openTurns(t, r.paths); len(open) != 1 || open[0] != turn {
		t.Fatalf("open turns = %v, want only %s", open, turn)
	}
	release()
	r.until(t, "the open turn approved", func(a *App) (string, bool) { return "", a.state.Turns[turn].State == "approved" })
	if n := submittedCount(r); n != 1 {
		t.Errorf("%d turns submitted once the turn ended, want 1", n)
	}
}

// Ctrl+O and Esc both close the drawer, it is closed at start, and what a
// process did with it is not carried to the next one: no file is written.
func TestV6CoverageDrawerClosedAtStartAndNotPersisted(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, filepath.Join(home, k))
	}
	a := covApp(t)
	addTurn(a, "t1", 1, "approved", "first question", "first answer")
	approveBy(a, "t1", "rev")
	if a.reviewsDrawer.IsOpen() || a.reviewsDrawer.Visible() || a.want.Drawer {
		t.Fatal("the drawer is not closed at start")
	}
	work := a.workDir
	before := fileSnapshot(t, home, work)

	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !a.reviewsDrawer.IsOpen() {
		t.Fatal("Ctrl+O did not open the drawer")
	}
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if a.reviewsDrawer.IsOpen() {
		t.Error("Ctrl+O did not close the drawer")
	}
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	press(a, tea.KeyMsg{Type: tea.KeyEsc})
	if a.reviewsDrawer.IsOpen() {
		t.Error("Esc did not close the drawer")
	}
	// Ctrl+O is the chat tab's: off it, nothing opens.
	press(a, tea.KeyMsg{Type: tea.KeyTab})
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if a.tab == tabChat || a.reviewsDrawer.IsOpen() {
		t.Errorf("Ctrl+O on tab %v: drawer open = %v", a.tab, a.reviewsDrawer.IsOpen())
	}
	press(a, tea.KeyMsg{Type: tea.KeyShiftTab})
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO}) // leave it open
	if !a.reviewsDrawer.IsOpen() {
		t.Fatal("the drawer did not reopen")
	}

	if after := fileSnapshot(t, home, work); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("using the drawer wrote files:\nbefore %v\nafter  %v", before, after)
	}
	next := covApp(t, WithWorkDir(work))
	addTurn(next, "t1", 1, "approved", "first question", "first answer")
	if next.reviewsDrawer.IsOpen() || next.reviewsDrawer.Visible() || next.want.Drawer {
		t.Error("a new process starts with the drawer open")
	}
}

// fileSnapshot lists every file under the directories with its size.
func fileSnapshot(t *testing.T, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, fmt.Sprintf("%s %d", path, info.Size()))
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// /mouse on and /notify on last for the process: they write nothing, and a
// new process starts with both off.
func TestV6CoverageMouseAndNotifyAreNotPersisted(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, filepath.Join(home, k))
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a := covApp(t)
	work := a.workDir
	before := fileSnapshot(t, home, work)

	for _, line := range []string{"/mouse on", "/notify on"} {
		typeRunes(a, line)
		press(a, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if !a.mouseEnabled || !a.notify {
		t.Fatalf("the commands did not take effect: mouse %v, notify %v (%q)", a.mouseEnabled, a.notify, a.refusal)
	}
	if after := fileSnapshot(t, home, work); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("/mouse and /notify wrote files:\nbefore %v\nafter  %v", before, after)
	}

	next := covApp(t, WithWorkDir(work))
	if next.mouseEnabled || next.notify {
		t.Errorf("a new process starts with mouse %v and notify %v, want both off", next.mouseEnabled, next.notify)
	}
	for _, line := range []string{"/mouse off", "/notify off"} {
		typeRunes(a, line)
		press(a, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if a.mouseEnabled || a.notify {
		t.Errorf("/mouse off and /notify off left mouse %v, notify %v", a.mouseEnabled, a.notify)
	}
}

// With colour off by NO_COLOR on an otherwise capable terminal the mouse is
// not captured: not by --mouse, not by the program options, not by /mouse on,
// and a click does nothing.
func TestV6CoverageMouseNotCapturedWithColourOff(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	caps := DetectTerminalCapabilities()
	if caps.Color || !caps.AltScreen {
		t.Fatalf("NO_COLOR with xterm detected as %+v, want no colour and the alternate screen", caps)
	}
	if got, base := len(ProgramOptionsWithMouse(caps, true)), len(ProgramOptions(caps)); got != base {
		t.Errorf("--mouse with colour off asks the program for the mouse: %d options, want %d", got, base)
	}
	a := covApp(t, WithTerminalCapabilities(caps), WithMouse(true))
	if a.mouseEnabled {
		t.Fatal("--mouse with colour off enabled the mouse")
	}
	if cmd := cmdMouse(a, []string{"on"}); cmd != nil || a.mouseEnabled {
		t.Error("/mouse on with colour off captured the mouse")
	}
	addTurn(a, "t1", 1, "approved", "q", "a")
	approveBy(a, "t1", "rev")
	two := 2
	a.reviewCounts["t1"] = &two
	before := a.View()
	x, y := where(t, a, "transcript")
	click(a, x, y)
	x, y = where(t, a, "2 reviews")
	click(a, x, y)
	if a.tab != tabChat || a.reviewsDrawer.IsOpen() || a.View() != before {
		t.Error("a click acted with colour off")
	}

	// The same terminal with colour is captured on request, so the refusal
	// above is the colour setting's and not a broken harness.
	t.Setenv("NO_COLOR", "")
	caps = DetectTerminalCapabilities()
	if got, base := len(ProgramOptionsWithMouse(caps, true)), len(ProgramOptions(caps)); got != base+1 {
		t.Errorf("--mouse with colour asks for %d options, want %d", got, base+1)
	}
}

// Open blocks on an incomplete or a cancelled turn stay in the chat, with the
// drawer closed and with it open.
func TestV6CoverageOpenBlocksOfIncompleteAndCancelledTurnsStayInChat(t *testing.T) {
	asciiProfile(t)
	a := covApp(t)
	a.width, a.height = 80, 60
	addTurn(a, "t1", 1, "incomplete", "first question", "first candidate")
	reason := "rounds_exhausted"
	ti := a.state.Turns["t1"]
	ti.TerminalReason = &reason
	a.state.Turns["t1"] = ti
	addTurn(a, "t2", 2, "cancelled", "second question", "second candidate")
	for _, id := range []string{"t1", "t2"} {
		approveBy(a, id, "rev")
	}
	a.openBlocksByTurn["t1"] = []wire.OpenBlock{
		{ID: "blk-incomplete", Severity: "high", State: "open", Evidence: "evidence-incomplete", Remedy: "remedy-incomplete"},
		{ID: "blk-gone", Severity: "low", State: "withdrawn"},
	}
	a.openBlocksByTurn["t2"] = []wire.OpenBlock{{ID: "blk-cancelled", Severity: "medium", State: "open", Evidence: "evidence-cancelled", Remedy: "remedy-cancelled"}}

	check := func(when string) {
		t.Helper()
		v := a.View()
		for _, want := range []string{
			"[high] blk-incomplete", "evidence: evidence-incomplete", "remedy: remedy-incomplete",
			"[medium] blk-cancelled", "evidence: evidence-cancelled", "remedy: remedy-cancelled",
		} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: the chat lacks %q:\n%s", when, want, v)
			}
		}
		if strings.Contains(v, "blk-gone") {
			t.Errorf("%s: a withdrawn block is shown as open:\n%s", when, v)
		}
	}
	check("drawer closed")
	openDrawerSettled(t, a)
	if v := a.View(); !strings.Contains(v, "Reviews · Turn 2") {
		t.Fatalf("the drawer is not open:\n%s", v)
	}
	check("drawer open")
	keys(a, tea.KeyUp)
	check("drawer open on turn 1")
	keys(a, tea.KeyEsc)
	a.reviewsDrawer.animating = false
	check("drawer closed again")
}

// A turn that ends with a block opened in round 1 and maintained in round 2
// shows it in the chat with the drawer never opened, and the drawer then
// shows the same block as maintained in round 2.
func TestV6CoverageMaintainedBlockShownInChatWithDrawerClosed(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, "max_rounds = 0", "max_rounds = 1\nmax_rounds_ceiling = 5", 1)
	first := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "first-round-evidence", "first-round-remedy", "wrong")
	})
	second := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return strings.Join([]string{
			"VERDICT v2", "candidate: " + req.Envelope.CandidateHash, "attempt: " + req.Envelope.AttemptID, "round: 1",
			"block-state: b1 maintained", "verdict: block", "severity: high", "block_id: b2",
			"evidence: second-round-evidence", "remedy: fix it", "reason: r", "END VERDICT",
		}, "\n")
	})
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 41"), enginetest.Reply("the answer is 42"), enginetest.Reply("ok"), enginetest.Reply("ok")},
		"reviewer": {first, second, enginetest.Reply("ok"), enginetest.Reply("ok")},
	})
	r := startAnswerRig(t, h)
	send(r, tea.WindowSizeMsg{Width: 80, Height: 60})
	r.typeLine("what is the answer?")
	turnIn(t, r, "incomplete")

	// The engine names an open block "<verdict id>:<block id>".
	v := covWaitView(t, r, ":b1", ":b2", "evidence: first-round-evidence", "evidence: second-round-evidence")
	r.until(t, "the drawer never opened", func(a *App) (string, bool) { return "", !a.reviewsDrawer.Visible() })
	if strings.Contains(v, "Reviews ·") {
		t.Fatalf("the drawer is showing:\n%s", v)
	}
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	covWaitView(t, r, "Reviews · Turn 1 · [*1* 2]") // the drawer opens at its last row
	send(r, tea.KeyMsg{Type: tea.KeyHome})
	covWaitView(t, r, "Reviews · Turn 1 · [*1* 2]", "b1: opened", ":b1", ":b2")
	send(r, tea.KeyMsg{Type: tea.KeyRight})
	send(r, tea.KeyMsg{Type: tea.KeyHome})
	covWaitView(t, r, "Reviews · Turn 1 · [1 *2*]", "b1: maintained", "b2: opened", ":b1", ":b2")
}

// The chat keeps scrolling behind the open drawer and is not reflowed by it:
// the chat's lines are the same with it open as closed, and a turn that
// arrives meanwhile is in the view.
func TestV6CoverageChatKeepsScrollingBehindTheDrawerWithoutReflow(t *testing.T) {
	asciiProfile(t)
	a := covApp(t)
	long := strings.Repeat("a long wrapped question ", 12)
	addTurn(a, "t1", 1, "approved", long, strings.Repeat("an answer that wraps over the width ", 8))
	addTurn(a, "t2", 2, "approved", "second question", "second answer")
	for _, id := range []string{"t1", "t2"} {
		approveBy(a, id, "rev")
	}
	plain := func() []string {
		lines, _, _ := chatLinesWithStarts(a.state, a.chatIn())
		for i, l := range lines {
			lines[i] = ansi.Strip(l)
		}
		return lines
	}
	closed := plain()
	openDrawerSettled(t, a)
	open := plain()
	if strings.Join(open, "\n") != strings.Join(closed, "\n") {
		t.Errorf("opening the drawer reflowed the chat:\nclosed:\n%s\nopen:\n%s", strings.Join(closed, "\n"), strings.Join(open, "\n"))
	}
	for _, l := range open {
		if ansi.StringWidth(l) > a.width {
			t.Errorf("a chat line is wider than the screen with the drawer open: %q", l)
		}
	}

	// A turn that arrives with the drawer open is followed by the chat.
	addTurn(a, "t3", 3, "approved", "third question", "third answer")
	approveBy(a, "t3", "rev")
	v := a.View()
	if !strings.Contains(v, "third question") || !strings.Contains(v, "third answer") {
		t.Errorf("the chat did not follow the new turn behind the drawer:\n%s", v)
	}
	if !strings.Contains(v, "Reviews ·") || !a.reviewsDrawer.IsOpen() {
		t.Errorf("the drawer closed when the chat scrolled:\n%s", v)
	}
}

// The drawer is drawn between the chat and the input, at the height section
// 1 gives it: 7 lines, 9 above 30 rows.
func TestV6CoverageDrawerSitsBetweenChatAndInputAtItsHeight(t *testing.T) {
	for _, tc := range []struct{ w, h, lines int }{{80, 24, 7}, {80, 30, 7}, {80, 31, 9}, {100, 40, 9}} {
		a := reviewedApp(t)
		a.width, a.height = tc.w, tc.h
		a.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
		openDrawerSettled(t, a)
		hs := a.layout()
		if hs.Drawer != tc.lines {
			t.Fatalf("%dx%d: drawer = %d lines, want %d", tc.w, tc.h, hs.Drawer, tc.lines)
		}
		rows := screen(a)
		if len(rows) != tc.h {
			t.Fatalf("%dx%d: %d rows", tc.w, tc.h, len(rows))
		}
		header := -1
		chat := -1
		input := -1
		for i, r := range rows {
			switch {
			case strings.Contains(r, "Reviews · Turn 2"):
				header = i
			case strings.Contains(r, "second answer"):
				chat = i
			case strings.HasPrefix(r, defaultPrompt):
				input = i
			}
		}
		if chat < 0 || header < 0 || input < 0 {
			t.Fatalf("%dx%d: chat row %d, drawer row %d, input row %d:\n%s", tc.w, tc.h, chat, header, input, strings.Join(rows, "\n"))
		}
		if chat >= header || header >= input {
			t.Errorf("%dx%d: chat %d, drawer %d, input %d: want that order", tc.w, tc.h, chat, header, input)
		}
		if header != hs.TabBar+hs.Body+hs.Banners {
			t.Errorf("%dx%d: the drawer starts on row %d, want %d (after the body and banners)", tc.w, tc.h, header, hs.TabBar+hs.Body+hs.Banners)
		}
		if input != header+hs.Drawer+hs.Refusal {
			t.Errorf("%dx%d: the input is on row %d, want %d (the drawer's %d lines after its start)", tc.w, tc.h, input, header+hs.Drawer+hs.Refusal, hs.Drawer)
		}
	}
}

// /help lists every flag of the CLI as well as every command: the flags are
// the ones cmd/syndic parses.
func TestV6CoverageHelpListsEveryFlag(t *testing.T) {
	a := covApp(t)
	cmdHelp(a, nil)
	help := strings.Join(a.panel, "\n")
	for _, flag := range HelpFlagLines() {
		flag = strings.Fields(flag)[0]
		if !strings.Contains(help, flag+" ") {
			t.Errorf("/help does not list %s", flag)
		}
	}
	for _, c := range []string{"/raw on|off", "/notify on|off", "/mouse on|off"} {
		if !strings.Contains(help, c) {
			t.Errorf("/help does not list %s", c)
		}
	}
	for name := range commands {
		if !strings.Contains(help, "/"+name) {
			t.Errorf("/help does not list /%s", name)
		}
	}
}

// After /resume reattaches within the process, a turn that ended while the
// connection was down gets one notice line and no bell, and a further resume
// that replays it again adds nothing.
func TestV6CoverageResumeShowsOneNoticeAndNoBell(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(5)
	h.frame(submittedFrame("turn-1", 5))
	if n := h.notices(); n != 0 {
		t.Fatalf("notice before any resume: %d", n)
	}
	resume := func(liveFrom int64) {
		t.Helper()
		cmd := cmdResume(h.app, nil)
		if cmd == nil {
			t.Fatal("/resume returned no reconnect command")
		}
		h.eng.liveFrom.Store(liveFrom)
		cm, ok := cmd().(connectedMsg)
		if !ok {
			t.Fatal("/resume did not connect")
		}
		h.app.Update(cm)
		_ = cm.cl.Close()
	}
	resume(8)
	h.frame(terminalEventFrame("turn-1", 6)) // replay: below live_from_seq, above the retained seq
	view := h.app.View()
	if n := strings.Count(view, noticeText); n != 1 || !strings.Contains(view, "approved at 2024-01-01T00:00:06Z") {
		t.Fatalf("got %d notice lines, want 1 naming outcome and time:\n%s", n, view)
	}
	if n := h.bells.Load(); n != 0 {
		t.Errorf("the replayed terminal event rang %d bells", n)
	}
	resume(8)
	h.frame(terminalEventFrame("turn-1", 6))
	if n := h.notices(); n != 1 {
		t.Errorf("a second /resume showed %d notice lines, want 1", n)
	}
	if n := h.bells.Load(); n != 0 {
		t.Errorf("a second /resume rang %d bells", n)
	}
}

// The drawer's fetch pages reviews until no cursor is returned and replaces
// an oversized item by its fetched record, against a real engine: sixty-one
// verdicts take two pages, the first reviewer's round-1 verdict names 100
// blocks of 1 KiB ids and arrives in short form, and every commentary is
// fetched. Nothing is parsed from verdict text.
func TestV6CoverageDrawerFetchPagesAndFetchesOversizedRecord(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)

	const manifest = `{"roster":{"default_preset":"p","presets":{"p":{"mode":"review","author":"au","required_reviewers":["p00","p01"]}}}}`
	var ids []string
	for i := 0; i < 100; i++ {
		ids = append(ids, fmt.Sprintf("%s-%d", strings.Repeat("x", 1024), i))
	}
	type verdict struct {
		who        string
		index      int
		round      int
		kind       string
		open, keep []string
	}
	var vs []verdict
	vs = append(vs, verdict{"p00", 0, 0, "block", ids, nil})
	for i := 1; i < 60; i++ {
		vs = append(vs, verdict{fmt.Sprintf("p%02d", i), i, 0, "approve", nil, nil})
	}
	vs = append(vs, verdict{"p00", 0, 1, "block", nil, ids})
	var turn string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(manifest))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for i := 0; i < 2; i++ {
			if prev, err = tx.SetLatestCandidate(turn, fmt.Sprintf("content %d", i), fmt.Sprintf("h%d", i), i, prev); err != nil {
				return err
			}
		}
		for _, v := range vs {
			hash := fmt.Sprintf("h%d", v.round)
			at, err := tx.BeginAttempt(turn, v.who, "review", 1, nil, "")
			if err != nil {
				return err
			}
			if err := tx.SetAttemptCommentary(at, fmt.Sprintf("commentary of %s in round %d", v.who, v.round)); err != nil {
				return err
			}
			vid, err := tx.RecordVerdict(at, hash, v.kind, "", "valid")
			if err != nil {
				return err
			}
			for _, id := range v.open {
				if _, err := tx.OpenBlockFor(vid, id, hash, "low", "e", "r"); err != nil {
					return err
				}
			}
			var states []journal.ReviewBlockState
			for _, id := range v.keep {
				states = append(states, journal.ReviewBlockState{BlockID: id, State: journal.ReviewBlockMaintained})
			}
			if err := tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: vid, AttemptID: at,
				Participant: v.who, RosterIndex: v.index, CandidateHash: hash, Kind: v.kind, States: states}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed turn: %v", err)
	}

	// The fixture really is oversized, and takes more than one page.
	limit := 50
	first, err := dial(t, r.paths).Reviews(context.Background(), turn, nil, &limit)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: %v, cursor %v; want a cursor", err, first.NextCursor)
	}
	if head, err := first.DecodeItems(); err != nil || !head[0].Oversized || head[0].Record == nil || len(head[0].Blocks) != 0 {
		t.Fatalf("the first item is not in short form: %+v, %v", head[0], err)
	}

	var fetch tea.Cmd
	r.until(t, "the fetch command", func(a *App) (string, bool) { fetch = a.fetchReviewsForTurn(turn); return "", fetch != nil })
	msg, ok := fetch().(reviewsMsg)
	if !ok {
		t.Fatal("the fetch did not end in a reviewsMsg")
	}
	if msg.err != nil || msg.warn != nil {
		t.Fatalf("fetch failed: err %v, warn %v", msg.err, msg.warn)
	}
	if len(msg.items) != len(vs) {
		t.Fatalf("%d items fetched, want %d: the pages were not all read", len(msg.items), len(vs))
	}
	for i, it := range msg.items {
		wantRound, wantWho := 0, fmt.Sprintf("p%02d", i)
		if i == len(vs)-1 {
			wantRound, wantWho = 1, "p00"
		}
		if it.Round != wantRound || it.Participant != wantWho {
			t.Fatalf("item %d is round %d of %s, want round %d of %s", i, it.Round, it.Participant, wantRound, wantWho)
		}
		if it.Oversized || it.Record != nil {
			t.Fatalf("item %d is still in short form after the fetch", i)
		}
		if it.Commentary == nil || msg.content[it.Commentary.Ref] != fmt.Sprintf("commentary of %s in round %d", wantWho, wantRound) {
			t.Fatalf("item %d: commentary %v was not fetched as recorded", i, it.Commentary)
		}
	}
	for _, i := range []int{0, len(vs) - 1} {
		want := journal.ReviewBlockOpened
		if i != 0 {
			want = journal.ReviewBlockMaintained
		}
		blocks := msg.items[i].Blocks
		if len(blocks) != len(ids) {
			t.Fatalf("item %d: %d blocks from the record, want %d", i, len(blocks), len(ids))
		}
		for k, b := range blocks {
			if b.BlockID != ids[k] || b.State != want {
				t.Fatalf("item %d: block %d is %.20s in state %q, want %.20s in state %q", i, k, b.BlockID, b.State, ids[k], want)
			}
		}
	}

	send(r, msg)
	r.until(t, "the drawer holding every review", func(a *App) (string, bool) {
		rev := a.reviewsDrawer.reviewsByTurn[turn]
		return "", rev != nil && len(rev.items) == len(vs)
	})
}

// The command list is drawn above the input in at most five lines, and the
// short help is one line above the input and the status line, showing the
// keys that apply to the tab in view; a second ? takes it away.
func TestV6CoverageListAndShortHelpSitAboveTheInput(t *testing.T) {
	asciiProfile(t)
	a := covApp(t)
	row := func(sub string) int {
		t.Helper()
		for i, r := range screen(a) {
			if strings.Contains(r, sub) {
				return i
			}
		}
		return -1
	}
	typeRunes(a, "/")
	input := row(defaultPrompt + "/")
	first := row("/" + commandListMatches("/")[0].name)
	if input < 0 || first < 0 || first >= input {
		t.Fatalf("command list row %d, input row %d: want the list above the input:\n%s", first, input, a.View())
	}
	listRows := 0
	for _, r := range screen(a)[first:input] {
		if strings.HasPrefix(strings.TrimSpace(r), "/") || strings.HasPrefix(r, "› /") {
			listRows++
		}
	}
	if listRows > maxListLines {
		t.Errorf("the command list takes %d lines, want at most %d", listRows, maxListLines)
	}
	a.clearInput()

	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	help, input, status := row("? hide"), row(defaultPrompt), len(screen(a))-1
	if help < 0 || help >= input || input >= status {
		t.Fatalf("short help row %d, input row %d, status row %d:\n%s", help, input, status, a.View())
	}
	if !strings.Contains(screen(a)[help], "Ctrl+O reviews") {
		t.Errorf("the chat's short help line lacks the chat's keys: %q", screen(a)[help])
	}
	if a.input.Value() != "" {
		t.Errorf("? typed into the input: %q", a.input.Value())
	}
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if row("? hide") >= 0 || a.input.Value() != "" {
		t.Errorf("the second ? did not hide the short help (input %q):\n%s", a.input.Value(), a.View())
	}
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	press(a, tea.KeyMsg{Type: tea.KeyTab})
	if got := screen(a)[row("? hide")]; strings.Contains(got, "Ctrl+O reviews") || !strings.Contains(got, "Alt+Enter newline") {
		t.Errorf("off the chat tab the short help line is still the chat's: %q", got)
	}
}

// The bell is one BEL byte on the terminal's output: the App's own emitter
// writes exactly "\a" for a live terminal event, and nothing for a replayed one.
func TestV6CoverageBellIsOneBELByte(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = real })

	a := covApp(t, WithNotify(true)) // the default emitter, not a test double
	a.liveFromSeq = 5
	for _, seq := range []int64{4, 5, 5} {
		if bell, _, _, _, _ := a.shouldNotify(terminalEventFrame("turn-1", seq)); bell {
			a.bellCmd()()
		}
	}
	os.Stdout = real
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(out)
	if string(got) != "\a" {
		t.Errorf("wrote %q to the terminal for a replayed, a live and a repeated event, want one BEL", got)
	}
}

// With TERM=dumb every screen the App draws holds no escape sequence: the
// chat, the other tabs, the help panel, the command list, the short help, the
// drawer, the conversation list and the reason prompt. Styling is forced on
// first and the capabilities applied as cmd/syndic does, so a screen that
// ignores them shows an escape rather than passing for want of a terminal.
func TestV6CoverageDumbTerminalEveryScreenHasNoEscape(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "")
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	caps := DetectTerminalCapabilities()
	ApplyTerminalCapabilities(caps)

	a := runningApp(t, caps, 80, 30)
	a.workDir = "/ws/here"
	addTurn(a, "t0", 0, "incomplete", "an earlier question", "an earlier candidate")
	a.openBlocksByTurn["t0"] = []wire.OpenBlock{{ID: "b0", Severity: "high", State: "open", Evidence: "ev", Remedy: "re"}}
	approveBy(a, "t0", "r1")
	two := 2
	a.reviewCounts["t0"] = &two
	a.content.Store("note", "a reviewer's commentary", nil)
	a.Update(reviewsMsg{turnID: "t0", items: []wire.ReviewItem{
		{Round: 0, Participant: "r1", Kind: "block", Blocks: []wire.ReviewBlock{{BlockID: "b0", State: "opened"}}, Commentary: &wire.ContentReference{Ref: "note"}},
		{Round: 1, Participant: "r1", Kind: "block", Blocks: []wire.ReviewBlock{{BlockID: "b0", State: "maintained"}}},
	}})
	if !a.plainMode() {
		t.Fatal("TERM=dumb is not plain mode")
	}

	screen := func(name string) {
		t.Helper()
		v := a.View()
		if strings.ContainsRune(v, '\x1b') {
			t.Errorf("%s holds an escape sequence: %q", name, v)
		}
		if !strings.Contains(v, "tab: switch") {
			t.Errorf("%s is not a whole screen:\n%s", name, v)
		}
	}
	screen("the chat")

	typeRunes(a, "/re")
	screen("the command list")
	a.clearInput()
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	screen("the short help")
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})

	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !a.reviewsDrawer.IsOpen() {
		t.Fatal("the drawer did not open")
	}
	if v := a.View(); !strings.Contains(v, "Reviews · Turn 1") && !strings.Contains(v, "Reviews · Turn") {
		t.Fatalf("the drawer is not drawn:\n%s", v)
	}
	screen("the drawer")
	press(a, tea.KeyMsg{Type: tea.KeyEsc})

	cmdHelp(a, nil)
	screen("the help panel")
	press(a, tea.KeyMsg{Type: tea.KeyEsc})

	here, elsewhere := "/ws/here", "/ws/elsewhere"
	a.conv = &convChooser{loaded: true, items: []wire.ConversationSummary{
		{ID: "aaaaaaaa11", Workspace: &here, TurnCount: 1, Preview: "mine"},
		{ID: "cccccccc33", Workspace: &elsewhere, TurnCount: 1, Preview: "foreign"},
	}}
	a.resetSel()
	screen("the conversation list")
	press(a, tea.KeyMsg{Type: tea.KeyEsc})

	openReasonForm(a, []string{"override turn t0?", "Type a reason."}, &overridePrompt{turn: "t0"})
	screen("the reason prompt")
	press(a, tea.KeyMsg{Type: tea.KeyEnter})
	screen("the refused empty reason")
	press(a, tea.KeyMsg{Type: tea.KeyEsc})

	for i := 1; i < int(numTabs); i++ {
		press(a, tea.KeyMsg{Type: tea.KeyTab})
		screen(fmt.Sprintf("tab %d", i))
	}
}
