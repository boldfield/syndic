package acceptance

import (
	"context"
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
)

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

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		h.t.Cleanup(cancel)
		if err := h.server.Serve(ctx); err != nil && err != net.ErrClosed {
			h.t.Errorf("server.Serve: %v", err)
		}
	}()
	h.t.Helper()
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

// WaitTurnState waits for a turn to reach a given state via the event stream.
func (h *Suite) WaitTurnState(t *testing.T, turnID string, state string) {
	t.Helper()
	h.engine.WaitTurnState(turnID, state)
}

// WaitEvent waits for an event matching a predicate to appear in the stream.
func (h *Suite) WaitEvent(t *testing.T, match func(*enginetest.Event) bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		for _, ev := range h.engine.Events() {
			if match(&ev) {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("event did not arrive before deadline")
}

// Restart closes the server and engine without graceful stop (simulating a crash)
// and starts new ones on the same journal.
// If newRosterTOML is not empty, the roster file is rewritten first.
func (h *Suite) Restart(t *testing.T, newRosterTOML string) {
	t.Helper()

	// Close server without graceful engine stop (simulating a crash).
	// Leave the old engine running but disconnect clients.
	_ = h.server.Close()

	if newRosterTOML != "" {
		if err := os.WriteFile(h.engine.RosterPath, []byte(newRosterTOML), 0o600); err != nil {
			t.Fatalf("rewrite roster: %v", err)
		}
	}

	// Restart engine and server on the same journal.
	// The old engine's state is left in memory; recovery will handle the journal.
	rosterTOML := func() string {
		b, err := os.ReadFile(h.engine.RosterPath)
		if err != nil {
			t.Fatalf("read roster: %v", err)
		}
		return string(b)
	}()

	// Create new engine on same DB, store, hub, and clock
	oldEngine := h.engine
	newEng, err := enginetest.NewOnExisting(t, oldEngine, rosterTOML)
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
// This is how retry tests release the engine's retry wait.
func (h *Suite) AdvanceClock(t *testing.T, participant string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.engine.Adapters[participant].Calls()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s called %d times, want %d", participant, len(h.engine.Adapters[participant].Calls()), n)
		}
		h.engine.Clock.Advance(time.Millisecond)
		time.Sleep(time.Millisecond)
	}
}
