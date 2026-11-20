package acceptance

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// gap marks a test as skipped with a message explaining why it's incomplete.
// The spec requires that the message describe an exact observed engine behavior,
// not "not yet implemented". A complete test that fails because the engine
// does something other than the design says calls gap as its first statement.
// nolint:unused
func gap(t *testing.T, reason string) {
	t.Helper()
	t.Skipf("gap: %s", reason)
}

const (
	waitTimeout = 10 * time.Second
)

// Suite is a running engine with server, client library, and journal.
type Suite struct {
	t      *testing.T
	engine *enginetest.Harness
	server *server.Server
	dir    string
	paths  lockfile.Paths

	maxFrameBytes int64
	mu            sync.Mutex
}

// newSuite starts the engine, server and subscribes to events on a Unix socket
// in a temp directory, and returns a harness.
func newSuite(t *testing.T, rosterTOML string, scripts map[string][]fake.Step, opts ...enginetest.Option) *Suite {
	t.Helper()
	dir := t.TempDir()
	paths := lockfile.Paths{
		Dir:    dir,
		Lock:   filepath.Join(dir, "engine.lock"),
		Socket: filepath.Join(dir, "engine.sock"),
	}

	eng := enginetest.New(t, rosterTOML, scripts, opts...)
	h := &Suite{
		t:      t,
		engine: eng,
		dir:    dir,
		paths:  paths,
	}

	h.startServer()
	t.Cleanup(func() {
		h.maxFrameSize()
	})
	return h
}

func (h *Suite) startServer() {
	h.t.Helper()
	h.server = server.New(server.Options{
		Paths:  h.paths,
		Engine: h.engine.Engine,
		Store:  h.engine.Store,
		Hub:    h.engine.Hub,
		FrameObserver: func(len int) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if int64(len) > h.maxFrameBytes {
				h.maxFrameBytes = int64(len)
			}
		},
	})

	if err := h.server.Listen(); err != nil {
		h.t.Fatalf("server.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		if err := h.server.Serve(ctx); err != nil && err != net.ErrClosed {
			serveErr <- err
		}
		close(serveErr)
	}()
	h.t.Cleanup(func() {
		cancel()
		//nolint:errcheck
		<-serveErr
	})
}

func (h *Suite) maxFrameSize() {
	h.t.Helper()
	const maxFrameBytes = 1 << 20 // 1 MiB
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.maxFrameBytes >= int64(maxFrameBytes) {
		h.t.Errorf("frame %d bytes >= %d byte limit", h.maxFrameBytes, maxFrameBytes)
	}
}

// Dial returns a connected client on the socket.
func (h *Suite) Dial(t *testing.T) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	c, err := client.Dial(ctx, h.paths.Socket, "test", "1.0")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
	})
	return c
}

// RawConn returns a bare Unix socket connection to the server.
func (h *Suite) RawConn(t *testing.T) net.Conn {
	t.Helper()
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "unix", h.paths.Socket)
	if err != nil {
		t.Fatalf("dial raw conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

// WaitTurnState waits for a turn to reach a given state via the client subscription.
func (h *Suite) WaitTurnState(t *testing.T, c *client.Client, turnID string, state string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	for {
		frame, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatalf("NextEvent: %v", err)
		}
		if frame.Type == "turn_state" {
			var ev struct {
				TurnID string `json:"turn_id"`
				State  string `json:"state"`
			}
			if err := json.Unmarshal(frame.Body, &ev); err != nil {
				t.Fatalf("unmarshal turn_state: %v", err)
			}
			if ev.TurnID == turnID && ev.State == state {
				return
			}
		}
	}
}

// WaitEvent waits for an event matching a predicate via the client subscription.
func (h *Suite) WaitEvent(t *testing.T, c *client.Client, match func(frame wire.Frame) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	for {
		frame, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatalf("NextEvent: %v", err)
		}
		if match(frame) {
			return
		}
	}
}

// Restart closes the server and engine without graceful stop (simulating a crash)
// and starts new ones on the same journal.
// If newRosterTOML is not empty, the roster file is rewritten first.
func (h *Suite) Restart(t *testing.T, newRosterTOML string) {
	t.Helper()

	// Close server without gracefully stopping (crash simulation).
	_ = h.server.Close()

	if newRosterTOML != "" {
		if err := os.WriteFile(h.engine.RosterPath, []byte(newRosterTOML), 0o600); err != nil {
			t.Fatalf("rewrite roster: %v", err)
		}
	}

	// Read current roster (or the new one if rewritten)
	rosterTOML := func() string {
		b, err := os.ReadFile(h.engine.RosterPath)
		if err != nil {
			t.Fatalf("read roster: %v", err)
		}
		return string(b)
	}()

	// Create new engine on same DB, store, hub, and clock (reuse the same state directory)
	newEng, err := enginetest.NewOnExisting(t, h.engine, rosterTOML)
	if err != nil {
		t.Fatalf("restart engine: %v", err)
	}
	h.engine = newEng
	h.startServer()
}

// StopGracefully calls engine.Stop to end any open turn with reason engine_stop.
func (h *Suite) StopGracefully(t *testing.T) {
	t.Helper()
	if err := h.engine.Engine.Stop(context.Background()); err != nil {
		t.Errorf("engine.Stop: %v", err)
	}
}

// AdvanceClock advances the fake clock until a participant has been called n times.
// This is how retry tests release the engine's retry wait. It advances the clock
// in small increments without sleeping or polling wall-clock time.
func (h *Suite) AdvanceClock(t *testing.T, participant string, n int) {
	t.Helper()
	const maxSteps = 1000000
	for step := 0; step < maxSteps; step++ {
		if len(h.engine.Adapters[participant].Calls()) >= n {
			return
		}
		h.engine.Clock.Advance(time.Millisecond)
	}
	t.Fatalf("%s called %d times after max advances, want %d", participant, len(h.engine.Adapters[participant].Calls()), n)
}
