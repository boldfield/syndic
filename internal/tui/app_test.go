package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/charmbracelet/x/exp/teatest"
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

// seen records the seq of every event an App applies and tracks turns.
type seen struct {
	mu    sync.Mutex
	seqs  []int64
	turns []string
}

func (s *seen) observe(f wire.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqs = append(s.seqs, f.Seq)
	if f.Kind == wire.EventKindTurnSubmitted {
		var body wire.EvTurnSubmitted
		if err := json.Unmarshal(f.Body, &body); err == nil {
			s.turns = append(s.turns, body.TurnID)
		}
	}
}

func (s *seen) snapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.seqs...)
}

func (s *seen) lastTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return ""
	}
	return s.turns[len(s.turns)-1]
}

func (s *seen) turnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
}

type rig struct {
	h             *enginetest.Harness
	srv           *serving
	paths         lockfile.Paths
	app           *App
	tm            *teatest.TestModel
	seen          *seen
	lastTurnCount int
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
	r.tm = teatest.NewTestModel(t, r.app, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	return r
}

func (r *rig) waitFor(t *testing.T, want string) {
	t.Helper()
	teatest.WaitFor(t, r.tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(want)) },
		teatest.WithDuration(10*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

// waitForEmptyInput waits until the submit has cleared the input line.
// After onSubmit processes the reply, the input field is emptied (app.input.Value() == "").
func (r *rig) waitForEmptyInput(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.app.input.Value() == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("input line never cleared; value = %q", r.app.input.Value())
}

// waitCaughtUp waits for the journal to go quiet and the App to have applied
// everything in it. It polls the event stream and store until they're in sync.
func (r *rig) waitCaughtUp(t *testing.T) int64 {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var prev int64 = -1
	for time.Now().Before(deadline) {
		max, err := r.h.Store.MaxSeq()
		if err != nil {
			t.Fatal(err)
		}
		s := r.seen.snapshot()
		// Check if the journal is quiet (max hasn't changed) and app is caught up.
		if max == prev && len(s) > 0 && s[len(s)-1] == max {
			return max
		}
		prev = max
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the TUI never caught up with the journal; last max=%d, seen %d events", prev, len(r.seen.snapshot()))
	return 0
}

func (r *rig) typeLine(s string) {
	r.tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	// Capture baseline BEFORE sending Enter, so waitForNewTurn can detect if a turn was already submitted
	r.lastTurnCount = r.seen.turnCount()
	r.tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

// waitForNewTurn waits for the next turn to be submitted and returns its ID.
// It observes the effect through the event stream: a TurnSubmitted event arrives and is processed by the app.
// The baseline is captured in typeLine before Enter is sent, to avoid missing fast submissions.
func (r *rig) waitForNewTurn(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	lastCount := r.lastTurnCount

	for time.Now().Before(deadline) {
		current := r.seen.turnCount()
		if current > lastCount {
			return r.seen.lastTurn()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no turn was submitted; saw %d events, last turn: %q", len(r.seen.snapshot()), r.seen.lastTurn())
	return ""
}

// final quits the TUI and returns the finished App.
func (r *rig) final(t *testing.T) *App {
	t.Helper()
	_ = r.tm.Quit()
	return r.tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*App)
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

func TestRegionsAndTabs(t *testing.T) {
	r := newRig(t)
	r.waitFor(t, "answer (placeholder)")
	r.tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	r.waitFor(t, "transcript (placeholder)")
	r.tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	r.waitFor(t, "participants (placeholder)")
	r.tm.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	r.waitFor(t, "transcript (placeholder)")

	app := r.final(t)
	lines := strings.Split(app.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24", len(lines))
	}
	if !strings.Contains(lines[0], "idle") || !strings.HasPrefix(lines[0], "[") {
		t.Errorf("strip region = %q, want [<preset>] [idle] ...", lines[0])
	}
	if !strings.Contains(lines[1], "transcript (placeholder)") {
		t.Errorf("tab region = %q", lines[1])
	}
	if !strings.HasPrefix(lines[23], "> ") {
		t.Errorf("input region = %q", lines[23])
	}
}

func TestSubmitBusyAndEventOrder(t *testing.T) {
	r := newRig(t)
	release := r.h.Hold("author")
	r.waitFor(t, "answer (placeholder)")

	r.typeLine("what is the answer? héllo")
	turn := r.waitForNewTurn(t)
	r.waitForEmptyInput(t)

	// A second submit is refused as busy; the code shows and the text stays.
	r.typeLine("second question")
	r.waitFor(t, "busy")

	release()
	r.h.WaitTurnState(turn, engine.StateApproved)
	calls := r.h.Adapters["author"].Calls()
	authorCalls := 0
	for _, c := range calls {
		if c.Request.Envelope.Phase == "author" {
			authorCalls++
			if authorCalls == 1 {
				if !strings.Contains(c.Request.Messages[len(c.Request.Messages)-1].Content, "what is the answer? héllo") {
					t.Fatalf("first author call content mismatch: %+v", c)
				}
			}
		}
	}
	if authorCalls != 1 {
		t.Fatalf("expected 1 author call with phase=author, got %d; full calls: %+v", authorCalls, calls)
	}

	max := r.waitCaughtUp(t)
	app := r.final(t)
	assertContiguous(t, r.seen.snapshot(), max)
	if app.state.LastSeq != max {
		t.Errorf("LastSeq = %d, want %d", app.state.LastSeq, max)
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

func assertContiguous(t *testing.T, seqs []int64, max int64) {
	t.Helper()
	if int64(len(seqs)) != max {
		t.Fatalf("saw %d events, want %d: %v", len(seqs), max, seqs)
	}
	for i, s := range seqs {
		if s != int64(i)+1 {
			t.Fatalf("event %d has seq %d; want each seq once, in order: %v", i, s, seqs)
		}
	}
}

func TestRestartReplaysEveryMissedEventOnce(t *testing.T) {
	r := newRig(t)
	r.waitFor(t, "answer (placeholder)")

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
	r.waitFor(t, "answer (placeholder)")
	r.typeLine("/resume")
	// Wait for the reconnect effect: gen increments and ConnState becomes connected.
	// Poll gen/ConnState directly until both have changed, with a reasonable timeout.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.app.gen > 0 && r.app.state.ConnState == ConnStateConnected {
			// Once reconnect is complete, give it another 10ms to fully settle.
			time.Sleep(10 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	max, err := r.h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	app := r.final(t)
	assertContiguous(t, r.seen.snapshot(), max)
	if app.gen != 1 || app.state.ConnState != ConnStateConnected {
		t.Errorf("gen = %d, ConnState = %q; want one reconnect, connected", app.gen, app.state.ConnState)
	}
}

func TestQuitLeavesTurnOpen(t *testing.T) {
	r := newRig(t)
	r.h.Hold("author")
	r.waitFor(t, "answer (placeholder)")
	r.typeLine("long question")
	turn := r.waitForNewTurn(t)
	r.typeLine("/quit")
	app := r.final(t)
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
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(80, 24))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("answer (placeholder)")) },
		teatest.WithDuration(10*time.Second))
	_ = tm.Quit()
	got := tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*App)
	if got.CurrentTurn() != reply.TurnID {
		t.Errorf("CurrentTurn = %q, want %q", got.CurrentTurn(), reply.TurnID)
	}
}

func TestUnknownCommandSendsNothing(t *testing.T) {
	r := newRig(t)
	r.waitFor(t, "answer (placeholder)")
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
	r.waitFor(t, "answer (placeholder)")
	r.typeLine("  /quit now  ")
	turn := r.waitForNewTurn(t)
	r.h.WaitTurnState(turn, engine.StateApproved)
	calls := r.h.Adapters["author"].Calls()
	if len(calls) == 0 {
		t.Fatal("the line never reached the engine")
	}
	got := calls[0].Request.Messages[len(calls[0].Request.Messages)-1].Content
	if !strings.Contains(got, "  /quit now  ") {
		t.Errorf("submitted input = %q; want it exactly as typed", got)
	}
	r.final(t)
}
