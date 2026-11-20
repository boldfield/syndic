package acceptance

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// SocketHarness wraps an enginetest harness with a socket server and provides
// a client for socket-based testing.
type SocketHarness struct {
	t       *testing.T
	harness *enginetest.Harness
	srv     *server.Server
	paths   lockfile.Paths
	done    chan struct{}
	cancel  context.CancelFunc
}

// New creates a SocketHarness with an enginetest harness and runs a server
// on a Unix socket in a temporary directory.
func New(t *testing.T, rosterTOML string, scripts map[string][]fake.Step, opts ...enginetest.Option) *SocketHarness {
	t.Helper()

	// Create the enginetest harness first
	h := enginetest.New(t, rosterTOML, scripts, opts...)

	// Create temp directory for socket
	dir, err := os.MkdirTemp("", "acceptance")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	paths := lockfile.Paths{
		Dir:    dir,
		Socket: filepath.Join(dir, "engine.sock"),
		Lock:   filepath.Join(dir, "engine.lock"),
	}

	// Create and start the server
	srv := server.New(server.Options{
		Paths:  paths,
		Engine: h.Engine,
		Store:  h.Store,
		Hub:    h.Hub,
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	srvCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		err := srv.Serve(srvCtx)
		t.Logf("server.Serve: %v", err)
		close(done)
	}()

	sh := &SocketHarness{
		t:       t,
		harness: h,
		srv:     srv,
		paths:   paths,
		done:    done,
		cancel:  cancel,
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server.Serve did not return after cancel")
		}
		_ = srv.Close()
	})

	return sh
}

// Dial creates a new client connection to the socket server.
func (h *SocketHarness) Dial(t *testing.T) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, h.paths.Socket, "test-client", "0")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// RawConn dials and returns a raw connection for lower-level testing.
func (h *SocketHarness) RawConn(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Store returns the underlying journal store.
func (h *SocketHarness) Store() *journal.Store {
	return h.harness.Store
}

// Engine returns the underlying engine for direct access when needed.
func (h *SocketHarness) Engine() interface{} {
	return h.harness.Engine
}

// Adapters returns the scripted fake adapters by participant id.
func (h *SocketHarness) Adapters() map[string]*fake.Adapter {
	return h.harness.Adapters
}
