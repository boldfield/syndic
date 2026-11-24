package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

const noticeText = "reconnected: turn ended while detached"

// fakeEngine answers hello, status and subscribe on a unix socket; the tests
// feed the event frames to the App themselves. liveFrom is the live_from_seq
// the next subscribe reports.
type fakeEngine struct {
	socket   string
	liveFrom atomic.Int64
}

func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "e.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	e := &fakeEngine{socket: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				r, w := wire.NewReader(c), wire.NewWriter(c)
				for {
					f, err := r.Read()
					if err != nil {
						return
					}
					var body any
					switch f.Type {
					case wire.RequestHello:
						body = wire.HelloReply{Protocol: 1, RunID: "run-1"}
					case wire.RequestStatus:
						body = wire.StatusReply{}
					case wire.RequestSubscribe:
						body = wire.SubscribeReply{OK: true, LiveFromSeq: e.liveFrom.Load()}
					default:
						return
					}
					reply, _ := wire.NewReply(f.ID, body)
					_ = w.Write(reply)
				}
			}()
		}
	}()
	return e
}

// notifyHarness drives an App through connect, frames and connection loss.
type notifyHarness struct {
	t     *testing.T
	eng   *fakeEngine
	app   *App
	bells atomic.Int32
}

func newNotifyHarness(t *testing.T, notify bool) *notifyHarness {
	t.Helper()
	h := &notifyHarness{t: t, eng: newFakeEngine(t)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.app = NewApp(ctx, h.eng.socket, WithNotify(notify), WithBellEmitter(func() { h.bells.Add(1) }))
	h.app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

// connect runs the App's own connect command for its current generation, with
// the engine reporting liveFrom, and hands the result to Update.
func (h *notifyHarness) connect(liveFrom int64) {
	h.t.Helper()
	h.eng.liveFrom.Store(liveFrom)
	msg := connectCmd(h.app.ctx, h.app.gen, h.eng.socket, h.app.state.LastSeq)()
	cm, ok := msg.(connectedMsg)
	if !ok {
		h.t.Fatalf("connect returned %T", msg)
	}
	h.app.Update(cm)
	// Frames are fed by hand: close the stream so the armed reads end at once.
	_ = cm.cl.Close()
}

// lose ends the connection as a stream error does, and reconnects after the
// last seq seen.
func (h *notifyHarness) lose(liveFrom int64) {
	h.t.Helper()
	_, cmd := h.app.Update(eventMsg{gen: h.app.gen, err: errors.New("connection lost")})
	if cmd == nil {
		h.t.Fatal("no reconnect command")
	}
	h.eng.liveFrom.Store(liveFrom)
	cm, ok := cmd().(connectedMsg)
	if !ok {
		h.t.Fatal("reconnect did not connect")
	}
	h.app.Update(cm)
	_ = cm.cl.Close()
}

// frame feeds one event and runs the commands it returns, so a bell command
// reaches the emitter.
func (h *notifyHarness) frame(f wire.Frame) {
	h.t.Helper()
	_, cmd := h.app.Update(eventMsg{gen: h.app.gen, frame: f})
	var wg sync.WaitGroup
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, ok := c().(tea.BatchMsg); ok {
				for _, sub := range b {
					run(sub)
				}
			}
		}()
	}
	run(cmd)
	wg.Wait()
}

func (h *notifyHarness) notices() int {
	return strings.Count(h.app.View(), noticeText)
}

func submittedFrame(turnID string, seq int64) wire.Frame {
	b, _ := json.Marshal(wire.EvTurnSubmitted{TurnID: turnID, SubmissionID: "sub-" + turnID, Preset: "solo"})
	return wire.Frame{Seq: seq, Kind: wire.EventKindTurnSubmitted, At: "2024-01-01T00:00:00Z", Body: b}
}

func terminalEventFrame(turnID string, seq int64) wire.Frame {
	b, _ := json.Marshal(wire.EvTurnState{TurnID: turnID, State: "approved"})
	return wire.Frame{Seq: seq, Kind: wire.EventKindTurnState, At: fmt.Sprintf("2024-01-01T00:00:%02dZ", seq%60), Body: b}
}

func TestNotifyOffByDefault(t *testing.T) {
	a := NewApp(context.Background(), "x")
	if a.notify {
		t.Fatal("notify must default to off")
	}
}

func TestNewProcessWithHistoryShowsNoNoticeAndNoBell(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(11)
	h.frame(submittedFrame("turn-1", 3))
	h.frame(terminalEventFrame("turn-1", 4))
	if n := h.bells.Load(); n != 0 {
		t.Errorf("replay rang %d bells", n)
	}
	if n := h.notices(); n != 0 {
		t.Errorf("a new process showed %d notices", n)
	}
}

func TestBoundaryEventRingsOnce(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(10)
	h.frame(submittedFrame("turn-1", 9))
	h.frame(terminalEventFrame("turn-1", 10))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("boundary terminal rang %d bells, want 1", n)
	}
	h.frame(terminalEventFrame("turn-1", 10))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("a repeated frame rang again: %d bells", n)
	}
	if n := h.notices(); n != 0 {
		t.Errorf("a live event showed %d notices", n)
	}
}

func TestEmptyReplayThenLiveTerminalRingsOnce(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	if n := h.bells.Load(); n != 0 {
		t.Fatalf("non-terminal rang %d bells", n)
	}
	h.frame(terminalEventFrame("turn-1", 2))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("live terminal rang %d bells, want 1", n)
	}
}

func TestNotifyOffNeverRings(t *testing.T) {
	h := newNotifyHarness(t, false)
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	h.frame(terminalEventFrame("turn-1", 2))
	if n := h.bells.Load(); n != 0 {
		t.Errorf("notify off rang %d bells", n)
	}
}

func TestMissedTerminalWhileDetachedShowsOneNoticeAndNoBell(t *testing.T) {
	for _, notify := range []bool{true, false} {
		t.Run(fmt.Sprintf("notify=%v", notify), func(t *testing.T) {
			h := newNotifyHarness(t, notify)
			h.connect(5)
			h.frame(submittedFrame("turn-1", 5))
			if n := h.notices(); n != 0 {
				t.Fatalf("notice before any reconnect: %d", n)
			}
			// The turn ends while detached; the replay after reconnect carries it.
			h.lose(8)
			h.frame(terminalEventFrame("turn-1", 6))
			view := h.app.View()
			if n := strings.Count(view, noticeText); n != 1 {
				t.Fatalf("got %d notice lines, want 1; view:\n%s", n, view)
			}
			if !strings.Contains(view, "approved at 2024-01-01T00:00:06Z") {
				t.Errorf("notice names no outcome and time; view:\n%s", view)
			}
			if n := h.bells.Load(); n != 0 {
				t.Errorf("replayed terminal rang %d bells", n)
			}

			// Later reconnects, even re-delivering the frame, add nothing.
			for i := 0; i < 2; i++ {
				h.lose(8)
				h.frame(terminalEventFrame("turn-1", 6))
				if n := h.notices(); n != 1 {
					t.Errorf("reconnect %d: %d notice lines, want 1", i+2, n)
				}
				if n := h.bells.Load(); n != 0 {
					t.Errorf("reconnect %d rang %d bells", i+2, n)
				}
			}
		})
	}
}

func TestLiveTerminalAfterReconnectRingsOnce(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(5)
	h.frame(submittedFrame("turn-1", 5))
	h.lose(7)
	h.frame(submittedFrame("turn-2", 7))
	h.frame(terminalEventFrame("turn-2", 8))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("got %d bells, want 1", n)
	}
	h.lose(9)
	h.frame(terminalEventFrame("turn-2", 8))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("a re-delivered live terminal rang again: %d bells", n)
	}
}

func TestRetainedSeqAdvancesWhileNotifyIsOff(t *testing.T) {
	h := newNotifyHarness(t, false)
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	h.frame(terminalEventFrame("turn-1", 2))
	h.app.notify = true // as /notify on does
	h.lose(3)
	h.frame(terminalEventFrame("turn-1", 2))
	if n := h.bells.Load(); n != 0 {
		t.Errorf("an event processed before /notify on rang %d bells", n)
	}
	if n := h.notices(); n != 0 {
		t.Errorf("an event processed before the reconnect gave %d notices", n)
	}
}
