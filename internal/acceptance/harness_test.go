package acceptance

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// frameLimit is the size an encoded engine frame must stay under.
const frameLimit = wire.MaxFrameBytes

// waitLimit bounds how long a wait on the engine may take in real time. It is
// a guard against a hung test, never a way to pass the time: the engine's
// time is the fake clock.
const waitLimit = 20 * time.Second

// suite is a running engine behind its control socket.
type suite struct {
	t *testing.T

	Engine     *engine.Engine
	Store      *journal.Store
	DB         *journal.DB
	Hub        *hub.Hub
	Clock      *enginetest.FakeClock
	Adapters   map[string]*fake.Adapter
	RosterPath string

	paths lockfile.Paths

	// largestFrame is the largest encoded frame any server of the suite has
	// written.
	largestFrame atomic.Int64

	srv     *server.Server
	cancel  context.CancelFunc
	served  chan struct{}
	events  *eventStream
	engines []*engine.Engine
	h       *enginetest.Harness
}

// newSuite starts an engine on rosterTOML and scripts with enginetest.New,
// puts the hub and a server on a Unix socket in a temporary directory, and
// subscribes the suite's own client to every event from seq 0.
func newSuite(t *testing.T, rosterTOML string, scripts map[string][]fake.Step, opts ...enginetest.Option) *suite {
	t.Helper()
	h := enginetest.New(t, rosterTOML, scripts, opts...)
	s := &suite{
		t:          t,
		h:          h,
		Engine:     h.Engine,
		Store:      h.Store,
		DB:         h.DB,
		Hub:        h.Hub,
		Clock:      h.Clock,
		Adapters:   h.Adapters,
		RosterPath: h.RosterPath,
	}

	// A socket path must fit in 108 bytes; the test's temp directory may not.
	dir, err := os.MkdirTemp("", "acc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	s.paths = lockfile.Paths{Dir: dir, Socket: filepath.Join(dir, "engine.sock"), Lock: filepath.Join(dir, "engine.lock")}

	// Cleanups run last in, first out: the frame check runs after the server
	// has stopped, and the server stops after the clients have closed.
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if n, bad := s.oversizedFrame(); bad {
			t.Errorf("an engine frame measured %d bytes encoded, want under %d", n, frameLimit)
		}
	})
	s.startServer()
	s.events = s.subscribe()
	return s
}

// observeFrame is the hook on the server's writer: it records the largest
// encoded frame written.
func (s *suite) observeFrame(n int) {
	for {
		cur := s.largestFrame.Load()
		if int64(n) <= cur || s.largestFrame.CompareAndSwap(cur, int64(n)) {
			return
		}
	}
}

// oversizedFrame is the check run when the test ends: it returns the largest
// frame recorded and whether that is too large. A frame must measure under
// frameLimit, so exactly frameLimit is too large.
func (s *suite) oversizedFrame() (largest int64, tooLarge bool) {
	largest = s.largestFrame.Load()
	return largest, largest >= frameLimit
}

// startServer serves the current engine on the socket. The server goroutine is
// started after its cancel is registered, and stopServer waits for it.
func (s *suite) startServer() {
	s.t.Helper()
	srv := server.New(server.Options{
		Paths:         s.paths,
		Engine:        s.Engine,
		Store:         s.Store,
		Hub:           s.Hub,
		FrameObserver: s.observeFrame,
	})
	if err := srv.Listen(); err != nil {
		s.t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	s.srv, s.cancel, s.served = srv, cancel, served
	s.t.Cleanup(s.stopServer)
	go func() {
		defer close(served)
		if err := srv.Serve(ctx); err != nil {
			s.t.Errorf("server.Serve: %v", err)
		}
	}()
}

// stopServer ends the server and every connection to it, and waits for the
// server goroutine to return. It is safe to call twice.
func (s *suite) stopServer() {
	s.cancel()
	<-s.served
	_ = s.srv.Close()
}

// ctx returns a context that ends when the test's deadline nears, after
// waitLimit, or with the test.
func (s *suite) ctx() context.Context {
	s.t.Helper()
	limit := waitLimit
	if dl, ok := s.t.Deadline(); ok {
		if left := time.Until(dl) - time.Second; left < limit {
			limit = left
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	s.t.Cleanup(cancel)
	return ctx
}

// Dial connects a client to the socket. It is closed when the test ends.
func (s *suite) Dial(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.Dial(s.ctx(), s.paths.Socket, "acceptance", "0")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// RawConn connects a bare connection to the socket, for tests of framing.
func (s *suite) RawConn(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: s.paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// event is a frame the engine sent to a subscriber, with its body left raw.
type event struct {
	Seq  int64
	Kind string
	Body json.RawMessage
}

// body decodes the event body into T, reporting whether the event is of kind.
func body[T any](ev event, kind string) (T, bool) {
	var v T
	if ev.Kind != kind {
		return v, false
	}
	if err := json.Unmarshal(ev.Body, &v); err != nil {
		return v, false
	}
	return v, true
}

// eventStream is a client subscribed from seq 0 that keeps what it has read.
type eventStream struct {
	c    *client.Client
	seen []event
}

func (s *suite) subscribe() *eventStream {
	s.t.Helper()
	c := s.Dial(s.t)
	if _, err := c.Subscribe(s.ctx(), 0); err != nil {
		s.t.Fatalf("Subscribe: %v", err)
	}
	return &eventStream{c: c}
}

// WaitEvent blocks on the suite's subscription until an event matching match
// has arrived, and returns it. Events already read are checked first, so it
// finds an event that came before the call. If the wait ends first, the test
// fails with the events seen.
func (s *suite) WaitEvent(match func(event) bool) event {
	s.t.Helper()
	for _, ev := range s.events.seen {
		if match(ev) {
			return ev
		}
	}
	ctx := s.ctx()
	for {
		frame, err := s.events.c.NextEvent(ctx)
		if err != nil {
			s.t.Fatalf("waiting for an event: %v; events seen: %s", err, describe(s.events.seen))
		}
		ev := event{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body}
		s.events.seen = append(s.events.seen, ev)
		if match(ev) {
			return ev
		}
	}
}

// Events returns the events the suite's subscription has read so far.
func (s *suite) Events() []event {
	return append([]event(nil), s.events.seen...)
}

func describe(events []event) string {
	out := "["
	for i, ev := range events {
		if i > 0 {
			out += " "
		}
		out += ev.Kind
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok {
			out += "(" + st.State + ")"
		}
	}
	return out + "]"
}

// WaitTurnState blocks until the turn has announced the state.
func (s *suite) WaitTurnState(turnID, state string) wire.EvTurnState {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == state
	})
	st, _ := body[wire.EvTurnState](ev, wire.EventKindTurnState)
	return st
}

// WaitTerminal blocks until the turn has announced a terminal state and
// returns it.
func (s *suite) WaitTerminal(turnID string) wire.EvTurnState {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && terminal(st.State)
	})
	st, _ := body[wire.EvTurnState](ev, wire.EventKindTurnState)
	return st
}

func terminal(state string) bool {
	switch state {
	case engine.StateApproved, engine.StateSoloComplete, engine.StateIncomplete, engine.StateCancelled, engine.StateOverridden:
		return true
	}
	return false
}

// Restart ends the server and builds a new engine on the same journal, hub,
// clock and state directory, with fresh scripted adapters, as a restart after
// a crash does: the old engine is not stopped, so it writes no engine_stopping
// and ends no turn, and the new engine's recovery finds what was left. The old
// engine is made inert (enginetest.Harness.Crash): its timers never fire and
// its adapters never answer, so it cannot write after the new engine recovers. If
// rosterTOML is not empty it is written over the roster file first. The
// suite's own subscription is renewed, replaying from seq 0.
func (s *suite) Restart(rosterTOML string, scripts map[string][]fake.Step) {
	s.t.Helper()
	s.stopServer()
	s.h.Crash()
	if rosterTOML != "" {
		if err := os.WriteFile(s.RosterPath, []byte(rosterTOML), 0o600); err != nil {
			s.t.Fatalf("write roster: %v", err)
		}
	}
	var mu sync.Mutex
	adapters := map[string]*fake.Adapter{}
	eng, err := engine.New(engine.Options{
		NoStartupProbes: true, // these tests seed the records they need
		Store:           s.Store,
		Hub:             s.Hub,
		RosterPath:      s.RosterPath,
		EngineVersion:   enginetest.EngineVersion,
		Clock:           s.Clock,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			a := fake.New(scripts[p.ID], fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel})
			mu.Lock()
			adapters[p.ID] = a
			mu.Unlock()
			return a, enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		s.t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Start(); err != nil {
		s.t.Fatalf("engine.Start: %v", err)
	}
	s.t.Cleanup(func() {
		if err := eng.Stop(context.Background()); err != nil {
			s.t.Errorf("engine.Stop: %v", err)
		}
	})
	mu.Lock()
	s.Adapters = adapters
	mu.Unlock()
	s.engines = append(s.engines, s.Engine)
	s.Engine = eng
	s.startServer()
	s.events = s.subscribe()
}

// StopGracefully stops the engine as SIGTERM does.
func (s *suite) StopGracefully() {
	s.t.Helper()
	if err := s.Engine.Stop(s.ctx()); err != nil {
		s.t.Fatalf("engine.Stop: %v", err)
	}
}

// gap marks a test whose complete scenario fails because the engine does
// something other than what the design says, and making it pass needs a
// production change. It is the only way a test in this package skips.
// Nothing calls it while every item passes; it is here for the tests that need it.
//
//nolint:unused // kept for the one permitted skip; see TestCoverage_NoBareSkips
func gap(t *testing.T, what string) {
	t.Helper()
	t.Skip(what)
}

// Submit sends a review-preset submission and returns the turn id.
func (s *suite) Submit(t *testing.T, c *client.Client, submissionID string) string {
	t.Helper()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: submissionID, Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("submit %s: %v", submissionID, err)
	}
	return reply.TurnID
}

// SubmitPreset sends a submission naming the preset.
func (s *suite) SubmitPreset(t *testing.T, c *client.Client, submissionID, preset string) string {
	t.Helper()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: submissionID, Input: "what is the answer?", Preset: &preset})
	if err != nil {
		t.Fatalf("submit %s: %v", submissionID, err)
	}
	return reply.TurnID
}

// Turn fetches the turn record through the protocol.
func (s *suite) Turn(t *testing.T, c *client.Client, turnID string) wire.TurnReply {
	t.Helper()
	reply, err := c.Turn(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("turn %s: %v", turnID, err)
	}
	return reply
}

// Status fetches the engine status through the protocol.
func (s *suite) Status(t *testing.T, c *client.Client) wire.StatusReply {
	t.Helper()
	reply, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return reply
}
