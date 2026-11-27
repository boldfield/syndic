package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/mattn/go-runewidth"
)

const answerText = "the answer is 42"

func approve() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

func newHarness(t *testing.T) *enginetest.Harness {
	t.Helper()
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText), enginetest.Reply(answerText)},
		"reviewer": {approve(), approve()},
	}
	return enginetest.New(t, enginetest.RosterTOML, scripts)
}

// serving is a server on a real Unix socket in a temp directory.
type serving struct {
	paths  lockfile.Paths
	cancel context.CancelFunc
	done   chan struct{}
}

func startServer(t *testing.T, paths lockfile.Paths, eng *engine.Engine, h *enginetest.Harness) *serving {
	t.Helper()
	srv := server.New(server.Options{Paths: paths, Engine: eng, Store: h.Store, Hub: h.Hub})
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &serving{paths: paths, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = srv.Serve(ctx)
		close(s.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-s.done
		_ = srv.Close()
	})
	return s
}

func newPaths(t *testing.T) lockfile.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "tui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return lockfile.Paths{Dir: dir, Socket: filepath.Join(dir, "engine.sock"), Lock: filepath.Join(dir, "engine.lock")}
}

// seen records the seq of every event an App applies.
type seen struct {
	mu   sync.Mutex
	seqs []int64
}

func (s *seen) observe(f wire.Frame) {
	s.mu.Lock()
	s.seqs = append(s.seqs, f.Seq)
	s.mu.Unlock()
}

func (s *seen) snapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.seqs...)
}

// probe wraps an App so a test can wait on the App's own state without
// reading it from another goroutine: a condition is registered through the
// program, evaluated on the program's goroutine after every message, and its
// answer handed back on a channel. No polling, no production hook.
type probe struct {
	app     *App
	waiters []*waiter
	enter   tea.Cmd // run at start-up, when the test wants the alternate screen
}

type waiter struct {
	cond func(*App) (string, bool)
	done chan string
}

type waitMsg struct{ w *waiter }

func (p *probe) Init() tea.Cmd { return tea.Batch(p.app.Init(), p.enter) }
func (p *probe) View() string  { return p.app.View() }

func (p *probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if w, ok := msg.(waitMsg); ok {
		p.waiters = append(p.waiters, w.w)
	} else {
		_, cmd = p.app.Update(msg)
	}
	kept := p.waiters[:0]
	for _, w := range p.waiters {
		if v, ok := w.cond(p.app); ok {
			w.done <- v
		} else {
			kept = append(kept, w)
		}
	}
	p.waiters = kept
	return p, cmd
}

type rig struct {
	h     *enginetest.Harness
	srv   *serving
	paths lockfile.Paths
	app   *App
	tm    *teatest.TestModel
	seen  *seen
}

// until blocks until cond holds of the App, evaluated on the program's
// goroutine after every message, and returns the string cond produced. It
// fails the test at its deadline.
func (r *rig) until(t *testing.T, what string, cond func(*App) (string, bool)) string {
	t.Helper()
	w := &waiter{cond: cond, done: make(chan string, 1)}
	r.tm.Send(waitMsg{w})
	select {
	case v := <-w.done:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	h := newHarness(t)
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	return r
}

func (r *rig) waitFor(t *testing.T, want string) {
	t.Helper()
	teatest.WaitFor(t, r.tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(want)) },
		teatest.WithDuration(10*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

// waitConnected waits for the App to be connected to the engine, so a line
// typed afterwards is not refused with "not connected".
func (r *rig) waitConnected(t *testing.T) {
	t.Helper()
	r.until(t, "the TUI to connect", func(a *App) (string, bool) {
		return "", a.state.ConnState == ConnStateConnected
	})
}

// waitCaughtUp waits for the App to be connected and to have applied every
// event in the journal, and returns the last seq.
func (r *rig) waitCaughtUp(t *testing.T) int64 {
	t.Helper()
	var max int64
	r.until(t, "the TUI to catch up with the journal", func(a *App) (string, bool) {
		m, err := r.h.Store.MaxSeq()
		if err != nil {
			t.Error(err)
			return "", true
		}
		max = m
		return "", a.state.ConnState == ConnStateConnected && m > 0 && a.state.LastSeq == m
	})
	return max
}

// waitTurn waits for the App to hold a submitted, open turn with the input
// line already cleared by the submit reply, and returns the turn's id.
func (r *rig) waitTurn(t *testing.T) string {
	t.Helper()
	return r.until(t, "a submitted turn and an empty input", func(a *App) (string, bool) {
		return a.CurrentTurn(), a.CurrentTurn() != "" && a.input.Value() == ""
	})
}

func (r *rig) typeLine(s string) {
	r.tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	r.tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

// final quits the TUI and returns the finished App.
func (r *rig) final(t *testing.T) *App {
	t.Helper()
	_ = r.tm.Quit()
	return r.tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*probe).app
}

func dial(t *testing.T, paths lockfile.Paths) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, paths.Socket, "test", "0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func openTurns(t *testing.T, paths lockfile.Paths) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := dial(t, paths).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st.TurnsOpen
}

// authorCalls is the author-phase calls the author adapter has received;
// terminal delivery to the author is not one of them.
func authorCalls(r *rig) []fake.CompleteCall {
	var out []fake.CompleteCall
	for _, c := range r.h.Adapters["author"].Calls() {
		if c.Request.Envelope.Phase == "author" {
			out = append(out, c)
		}
	}
	return out
}

func TestRegionsAndTabs(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	r.waitFor(t, "no turn yet")
	r.tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	r.waitFor(t, "> author")
	r.tm.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	r.waitFor(t, "no turn yet")

	app := r.final(t)
	lines := strings.Split(app.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24", len(lines))
	}
	if !strings.HasPrefix(lines[0], "chat  [transcript]  participants") || !strings.HasSuffix(lines[0], "100%") {
		t.Errorf("tab bar = %q", lines[0])
	}
	if !strings.Contains(lines[1], "no turn yet") {
		t.Errorf("tab region = %q", lines[1])
	}
	if !strings.HasPrefix(lines[22], defaultPrompt) {
		t.Errorf("input region = %q", lines[22])
	}
	last := lines[23]
	if !strings.HasPrefix(last, "review") || !strings.Contains(last, "idle") || !strings.HasSuffix(last, "tab: switch  /help") {
		t.Errorf("status line = %q, want <preset> · idle ... tab: switch  /help", last)
	}
}

func TestSubmitBusyAndEventOrder(t *testing.T) {
	r := newRig(t)
	release := r.h.Hold("author")
	r.waitConnected(t)

	r.typeLine("what is the answer? héllo")
	turn := r.waitTurn(t) // the submit reply has cleared the input
	if ts := openTurns(t, r.paths); len(ts) != 1 || ts[0] != turn {
		t.Fatalf("turns open = %v, want [%s]", ts, turn)
	}

	// A second submit is refused as busy; the code shows and the text stays.
	r.typeLine("second question")
	r.waitFor(t, "busy")

	release()
	r.h.WaitTurnState(turn, engine.StateApproved)
	calls := authorCalls(r)
	if len(calls) != 1 || !strings.Contains(calls[0].Request.Messages[len(calls[0].Request.Messages)-1].Content, "what is the answer? héllo") {
		t.Fatalf("author-phase calls = %+v; all author adapter calls = %+v", calls, r.h.Adapters["author"].Calls())
	}

	max := r.waitCaughtUp(t)
	app := r.final(t)
	assertContiguous(t, r.seen.snapshot(), max)
	if seqs := r.seen.snapshot(); app.state.LastSeq != int64(len(seqs)) {
		t.Errorf("LastSeq = %d, want %d", app.state.LastSeq, len(seqs))
	}
	if got := app.input.Value(); got != "second question" {
		t.Errorf("input after refusal = %q", got)
	}
	if !strings.HasPrefix(app.refusal, "busy") {
		t.Errorf("refusal = %q", app.refusal)
	}
	if app.CurrentTurn() != "" {
		t.Errorf("CurrentTurn = %q after the turn ended", app.CurrentTurn())
	}
	for _, c := range r.h.Adapters["author"].Calls() {
		if c.Request.Envelope.Phase == "author" && c.Request.Envelope.TurnID != turn {
			t.Errorf("the refused submit reached the engine: %+v", c)
		}
	}
}

// assertContiguous checks the App applied every event up to max, each seq
// once, in order. Events the engine journaled after max (terminal delivery
// may still be running) may also have been applied, but never out of order.
func assertContiguous(t *testing.T, seqs []int64, max int64) {
	t.Helper()
	if int64(len(seqs)) < max {
		t.Fatalf("saw %d events, want at least %d: %v", len(seqs), max, seqs)
	}
	for i, s := range seqs {
		if s != int64(i)+1 {
			t.Fatalf("event %d has seq %d; want each seq once, in order: %v", i, s, seqs)
		}
	}
}

func TestRestartReplaysEveryMissedEventOnce(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	if err := r.h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-r.srv.done
	r.waitFor(t, "engine: reconnecting")

	eng, err := engine.New(engine.Options{
		Store:         r.h.Store,
		Hub:           r.h.Hub,
		RosterPath:    r.h.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         r.h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	startServer(t, r.paths, eng, r.h)

	max := r.waitCaughtUp(t)
	app := r.final(t)
	assertContiguous(t, r.seen.snapshot(), max)
	if app.state.ConnState != ConnStateConnected {
		t.Errorf("ConnState = %q after the engine came back", app.state.ConnState)
	}
	if app.state.Hello.RunID != eng.Hello().RunID {
		t.Errorf("hello run_id = %q, want the new run %q", app.state.Hello.RunID, eng.Hello().RunID)
	}
}

func TestResumeReconnectsOnce(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.typeLine("/resume")
	var max int64
	r.until(t, "one reconnect to complete and the journal to be replayed", func(a *App) (string, bool) {
		m, err := r.h.Store.MaxSeq()
		if err != nil {
			t.Error(err)
			return "", true
		}
		max = m
		return "", a.gen == 1 && a.state.ConnState == ConnStateConnected && a.state.LastSeq == m
	})
	app := r.final(t)
	assertContiguous(t, r.seen.snapshot(), max)
	if app.gen != 1 || app.state.ConnState != ConnStateConnected {
		t.Errorf("gen = %d, ConnState = %q; want one reconnect, connected", app.gen, app.state.ConnState)
	}
}

func TestQuitLeavesTurnOpen(t *testing.T) {
	r := newRig(t)
	r.h.Hold("author")
	r.waitConnected(t)
	r.typeLine("long question")
	turn := r.waitTurn(t)
	r.typeLine("/quit")
	app := r.tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*probe).app
	if app.CurrentTurn() != turn {
		t.Fatalf("CurrentTurn = %q, want %q", app.CurrentTurn(), turn)
	}
	if msg := ExitMessage(app); !strings.Contains(msg, turn) {
		t.Errorf("exit message %q does not name %s", msg, turn)
	}
	if ts := openTurns(t, r.paths); len(ts) != 1 || ts[0] != turn {
		t.Errorf("turns open after quit = %v, want [%s]", ts, turn)
	}
}

func TestReattachFindsOpenTurn(t *testing.T) {
	h := newHarness(t)
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	h.Hold("author")
	c := dial(t, paths)
	reply, err := c.Submit(context.Background(), wire.SubmitRequest{SubmissionID: "pre", Input: "q"})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(context.Background(), paths.Socket)
	r := &rig{h: h, paths: paths}
	r.tm = teatest.NewTestModel(t, &probe{app: app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.until(t, "the open turn to be reattached", func(a *App) (string, bool) {
		return "", a.state.ConnState == ConnStateConnected && a.CurrentTurn() == reply.TurnID
	})
	got := r.final(t)
	if got.CurrentTurn() != reply.TurnID {
		t.Errorf("CurrentTurn = %q, want %q", got.CurrentTurn(), reply.TurnID)
	}
}

func TestUnknownCommandSendsNothing(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	before, err := r.h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	r.typeLine("/bogus")
	r.waitFor(t, "unknown command: /bogus")
	app := r.final(t)
	if len(openTurns(t, r.paths)) != 0 || len(r.h.Adapters["author"].Calls()) != 0 {
		t.Error("an unknown command reached the engine")
	}
	if after, _ := r.h.Store.MaxSeq(); after != before {
		t.Errorf("journal grew from %d to %d", before, after)
	}
	if app.input.Value() != "" {
		t.Errorf("input = %q", app.input.Value())
	}
}

func TestFormatRefusal(t *testing.T) {
	d := "turn t1 is open"
	if got := formatRefusal(&wire.Error{Code: "busy", Detail: &d}); got != "busy: turn t1 is open" {
		t.Errorf("got %q", got)
	}
	if got := formatRefusal(&wire.Error{Code: "busy"}); got != "busy" {
		t.Errorf("got %q", got)
	}
	if got := formatRefusal(errors.New("boom")); got != "boom" {
		t.Errorf("got %q", got)
	}
}

// A line with leading whitespace is a prompt even if it then starts with "/",
// and it is submitted exactly as typed.
func TestSubmitPreservesWhitespaceAndSpacePrefixedSlash(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.typeLine("  /quit now  ")
	// The turn is not held, so it may already be over: wait for it to be approved.
	turn := r.until(t, "the turn to be approved", func(a *App) (string, bool) {
		for id, st := range a.state.TurnState {
			if st == engine.StateApproved {
				return id, true
			}
		}
		return "", false
	})
	r.h.WaitTurnState(turn, engine.StateApproved)
	calls := authorCalls(r)
	if len(calls) != 1 {
		t.Fatalf("author-phase calls = %+v; all author adapter calls = %+v", calls, r.h.Adapters["author"].Calls())
	}
	got := calls[0].Request.Messages[len(calls[0].Request.Messages)-1].Content
	if !strings.Contains(got, "  /quit now  ") {
		t.Errorf("submitted input = %q; want it exactly as typed", got)
	}
	r.final(t)
}

// TestClipToWidth verifies width clipping for wide characters.
func TestClipToWidth(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  string
	}{
		{
			name:  "ASCII within width",
			line:  "hello",
			width: 10,
			want:  "hello",
		},
		{
			name:  "ASCII exceed width",
			line:  "hello world",
			width: 8,
			want:  "hello wo",
		},
		{
			name:  "emoji clipping - fits with space after",
			line:  "test 😀 emoji",
			width: 8,
			want:  "test 😀 ",
		},
		{
			name:  "CJK characters",
			line:  "测试文本",
			width: 8,
			want:  "测试文本",
		},
		{
			name:  "mixed ASCII and CJK",
			line:  "hello 世界 test",
			width: 12,
			want:  "hello 世界 t",
		},
		{
			name:  "width zero",
			line:  "test",
			width: 0,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipToWidth(tt.line, tt.width)
			if got != tt.want {
				t.Errorf("clipToWidth(%q, %d) = %q, want %q", tt.line, tt.width, got, tt.want)
			}
			gotWidth := runewidth.StringWidth(got)
			if gotWidth > tt.width {
				t.Errorf("result width %d exceeds limit %d for %q", gotWidth, tt.width, got)
			}
		})
	}
}

// TestFoldBanners verifies the two-line cap: the first banner shows, and the
// last shown line ends "+N more" when more apply than fit.
func TestFoldBanners(t *testing.T) {
	banners := []string{"deadline: provisional", "roster file changed", "engine: reconnecting", "quick: unapproved profile"}

	got := foldBanners(banners, 2)
	if len(got) != 2 || got[0] != banners[0] || got[1] != "+3 more" {
		t.Errorf("two lines, four banners: %q", got)
	}
	got = foldBanners(banners, 1)
	if len(got) != 1 || got[0] != banners[0]+" +3 more" {
		t.Errorf("one line, four banners: %q", got)
	}
	if got := foldBanners(banners[:2], 2); len(got) != 2 || got[1] != banners[1] {
		t.Errorf("two banners in two lines: %q", got)
	}
	if got := foldBanners(banners, 0); len(got) != 0 {
		t.Errorf("no lines: %q", got)
	}
}

func TestPlainTerminalDetection(t *testing.T) {
	t.Setenv("TERM", "dumb")
	caps := DetectTerminalCapabilities()
	if caps.Color {
		t.Error("Color should be disabled with TERM=dumb")
	}
	if caps.AltScreen {
		t.Error("AltScreen should be disabled with TERM=dumb")
	}
}

// TestNOCOLORWithAltScreen verifies that NO_COLOR disables color but allows alt screen.
func TestNOCOLORWithAltScreen(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	caps := DetectTerminalCapabilities()
	if caps.Color {
		t.Error("Color should be disabled with NO_COLOR")
	}
	if !caps.AltScreen {
		t.Error("AltScreen should be enabled with NO_COLOR and normal TERM")
	}
}

// TestANSIAwareClipping verifies that clipToWidth handles ANSI sequences correctly.
func TestANSIAwareClipping(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
	}{
		{
			name:  "styled text that fits",
			input: "\x1b[1;31m" + strings.Repeat("a", 78) + "\x1b[0m",
			width: 80,
		},
		{
			name:  "styled text that needs clipping",
			input: "\x1b[1;31m" + strings.Repeat("a", 100) + "\x1b[0m",
			width: 80,
		},
		{
			name:  "mixed CJK and styling",
			input: "\x1b[32m" + "测试" + "\x1b[0m",
			width: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := clipToWidth(tt.input, tt.width)
			resultWidth := ansi.StringWidth(result)
			if resultWidth > tt.width {
				t.Errorf("clipToWidth: result width %d exceeds limit %d", resultWidth, tt.width)
			}
		})
	}
}

// TestResizeMessageFitsAtSmallWidth verifies the resize message fits within narrow terminals.
func TestResizeMessageFitsAtSmallWidth(t *testing.T) {
	for width := 10; width <= 40; width += 5 {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			app := NewApp(context.Background(), "/tmp/nonexistent.sock")
			app.width = width
			app.height = 8

			output := app.View()
			lines := strings.Split(output, "\n")

			// Should show some resize message (even if clipped)
			if !strings.Contains(output, "Terminal") {
				t.Errorf("Expected resize message containing 'Terminal', got: %s", output)
			}

			// All lines should fit within width and display as a single line
			if len(lines) != 1 {
				t.Errorf("Expected single line, got %d lines: %v", len(lines), lines)
			}

			for i, line := range lines {
				displayWidth := ansi.StringWidth(line)
				if displayWidth > width {
					t.Errorf("Line %d exceeds width %d: %q (display width %d)", i, width, line, displayWidth)
				}
			}
		})
	}
}
