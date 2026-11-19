package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/wire"
)

// stubEngine is a minimal Engine implementation for testing.
type stubEngine struct {
	helloReply   wire.HelloReply
	statusReply  wire.StatusReply
	stopping     chan struct{}
	stopOnce     sync.Once
	submitReply  wire.SubmitReply
	submitError  error
	cancelError  error
	overrideErr  error
	releaseReply wire.ReleaseReply
	releaseErr   error
}

func (e *stubEngine) Hello() wire.HelloReply {
	return e.helloReply
}

func (e *stubEngine) Status() (wire.StatusReply, error) {
	return e.statusReply, nil
}

func (e *stubEngine) Submit(req wire.SubmitRequest) (wire.SubmitReply, error) {
	if e.submitError != nil {
		return wire.SubmitReply{}, e.submitError
	}
	return e.submitReply, nil
}

func (e *stubEngine) Cancel(turnID string) error {
	return e.cancelError
}

func (e *stubEngine) Override(turnID, reason string) error {
	return e.overrideErr
}

func (e *stubEngine) Release(req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	if e.releaseErr != nil {
		return wire.ReleaseReply{}, e.releaseErr
	}
	return e.releaseReply, nil
}

func (e *stubEngine) Stopping() <-chan struct{} {
	return e.stopping
}

func (e *stubEngine) stop() {
	e.stopOnce.Do(func() {
		close(e.stopping)
	})
}

func setupTestServer(t *testing.T) (*Server, string, *stubEngine, func()) {
	// Create temp directory for socket
	tmpdir := t.TempDir()
	socketDir := filepath.Join(tmpdir, "syndic")
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	paths := lockfile.Paths{
		Dir:    socketDir,
		Socket: filepath.Join(socketDir, "engine.sock"),
		Lock:   filepath.Join(socketDir, "engine.lock"),
	}

	// Create in-memory journal
	db, err := journal.Open(filepath.Join(tmpdir, "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}

	store := journal.NewStore(db)
	h := hub.New(store, 1024)

	engine := &stubEngine{
		helloReply: wire.HelloReply{
			EngineVersion: "test",
			Protocol:      wire.ProtocolVersion,
			RunID:         "run-123",
			RunFirstSeq:   1,
			RosterHash:    "roster-hash",
			ManifestHash:  "manifest-hash",
		},
		statusReply: wire.StatusReply{
			RunID:                "run-123",
			RunStartedAt:         time.Now().Format(time.RFC3339),
			RosterHash:           "roster-hash",
			DefaultPreset:        "solo",
			DeadlinesProvisional: false,
			Participants:         []wire.Participant{},
			Pools:                []wire.Pool{},
			QuickProfiles:        []wire.QuickProfile{},
			TurnsOpen:            []string{},
			EngineStartedAt:      time.Now().Format(time.RFC3339),
		},
		submitReply: wire.SubmitReply{
			TurnID:          "turn-123",
			Preset:          "solo",
			EstimatedTokens: 1000,
		},
		stopping: make(chan struct{}),
	}

	server := New(Options{
		Paths:  paths,
		Engine: engine,
		Store:  store,
		Hub:    h,
	})

	if err := server.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}

	cleanup := func() {
		_ = server.Close()
		_ = db.Close()
	}

	return server, paths.Socket, engine, cleanup
}

func TestServerHandshakeSuccess(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Connect and send hello
	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Send hello request
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	frame, _ := wire.NewRequest("test-1", "hello", helloReq)
	if err := w.Write(frame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// Read hello reply
	reply, err := r.Read()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}

	if reply.Type != "reply" {
		t.Errorf("expected reply, got %s", reply.Type)
	}

	var helloReply wire.HelloReply
	if err := json.Unmarshal(reply.Body, &helloReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if helloReply.Protocol != wire.ProtocolVersion {
		t.Errorf("protocol mismatch: %d", helloReply.Protocol)
	}
}

func TestServerHandshakeBadProtocol(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Send hello with bad protocol
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      2, // wrong protocol
	}
	frame, _ := wire.NewRequest("test-1", "hello", helloReq)
	if err := w.Write(frame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// Read error
	reply, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if reply.Type != "error" {
		t.Errorf("expected error, got %s", reply.Type)
	}

	var errReply wire.Error
	if err := json.Unmarshal(reply.Body, &errReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if errReply.Code != "protocol" {
		t.Errorf("expected protocol error, got %s", errReply.Code)
	}
}

func TestServerStatus(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		if err := server.Serve(ctx); err != nil && err != context.DeadlineExceeded {
			t.Logf("server.Serve error: %v", err)
		}
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Handshake
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	helloFrame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(helloFrame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	helloReply, err := r.Read()
	if err != nil {
		t.Fatalf("read hello reply: %v", err)
	}

	if helloReply.Type != "reply" {
		t.Errorf("expected reply to hello, got %s", helloReply.Type)
	}

	// Send status request
	statusReq := wire.StatusRequest{}
	statusFrame, _ := wire.NewRequest("status-1", "status", statusReq)
	if err := w.Write(statusFrame); err != nil {
		t.Fatalf("write status: %v", err)
	}

	// Read status reply
	reply, err := r.Read()
	if err != nil {
		t.Fatalf("read status reply: %v", err)
	}

	if reply.Type != "reply" {
		t.Errorf("expected reply, got %s", reply.Type)
	}

	var statusReply wire.StatusReply
	if err := json.Unmarshal(reply.Body, &statusReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if statusReply.RunID != "run-123" {
		t.Errorf("expected run-123, got %s", statusReply.RunID)
	}
}

func TestServerCancel(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Handshake
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	frame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(frame); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, err := r.Read(); err != nil {
		t.Fatalf("read hello reply: %v", err)
	}

	// Send cancel request
	cancelReq := wire.CancelRequest{TurnID: "turn-123"}
	frame, _ = wire.NewRequest("cancel-1", "cancel", cancelReq)
	if err := w.Write(frame); err != nil {
		t.Fatalf("write cancel: %v", err)
	}

	// Read cancel reply
	reply, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if reply.Type != "reply" {
		t.Errorf("expected reply, got %s", reply.Type)
	}

	var cancelReply wire.CancelReply
	if err := json.Unmarshal(reply.Body, &cancelReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !cancelReply.Accepted {
		t.Errorf("expected accepted, got false")
	}
}

func TestServerFrameSizeLimit(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Handshake
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	frame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(frame); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, err := r.Read(); err != nil {
		t.Fatalf("read hello reply: %v", err)
	}

	// Send a huge submit request (should fail)
	hugeInput := make([]byte, 2*1024*1024) // 2 MiB
	for i := range hugeInput {
		hugeInput[i] = 'x'
	}

	submitReq := wire.SubmitRequest{
		SubmissionID: "sub-1",
		Input:        string(hugeInput),
	}
	frame, _ = wire.NewRequest("submit-1", "submit", submitReq)

	// This should fail to write because frame is too large
	err = w.Write(frame)
	if err != wire.ErrFrameTooLarge {
		t.Errorf("expected ErrFrameTooLarge, got %v", err)
	}
}

func connectSocket(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

func TestSocketMode(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	fi, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}

	mode := fi.Mode()
	perms := mode & os.ModePerm
	expected := os.FileMode(0600)

	if perms != expected {
		t.Errorf("socket mode is %o, expected %o", perms, expected)
	}
}

func TestOversizedClientFrame(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Handshake
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	helloFrame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(helloFrame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	helloReply, err := r.Read()
	if err != nil {
		t.Fatalf("read hello reply: %v", err)
	}

	if helloReply.Type != "reply" {
		t.Errorf("expected reply to hello, got %s", helloReply.Type)
	}

	// Try to send a frame that's too large locally (wire.Writer will reject it)
	largeData := make([]byte, wire.MaxFrameBytes+100)
	for i := range largeData {
		largeData[i] = 'x'
	}

	submitReq := wire.SubmitRequest{
		SubmissionID: "sub-1",
		Input:        string(largeData),
	}
	frameToSend, _ := wire.NewRequest("submit-1", "submit", submitReq)

	err = w.Write(frameToSend)
	if err != wire.ErrFrameTooLarge {
		t.Fatalf("expected ErrFrameTooLarge from client writer, got %v", err)
	}
}

func TestStoppingDeliveredToConnections(t *testing.T) {
	server, sockPath, engine, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Handshake
	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	helloFrame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(helloFrame); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	helloReply, err := r.Read()
	if err != nil {
		t.Fatalf("read hello reply: %v", err)
	}

	if helloReply.Type != "reply" {
		t.Errorf("expected reply to hello, got %s", helloReply.Type)
	}

	// Stop the engine
	engine.stop()

	// Should receive stopping error or connection close
	frame, err := r.Read()
	if err != nil {
		if err != io.EOF {
			t.Logf("read error after stopping: %v", err)
		}
	} else {
		if frame.Type == "error" {
			var errReply wire.Error
			if err := json.Unmarshal(frame.Body, &errReply); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if errReply.Code != "stopping" {
				t.Errorf("expected stopping error, got %s", errReply.Code)
			}
		}
	}

	// Serve should return
	select {
	case err := <-serveDone:
		if err != nil && err != context.Canceled {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Serve did not return after engine stopped")
	}
}

func TestFrameObserverLimitGuard(t *testing.T) {
	tmpdir := t.TempDir()
	socketDir := filepath.Join(tmpdir, "syndic")
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	paths := lockfile.Paths{
		Dir:    socketDir,
		Socket: filepath.Join(socketDir, "engine.sock"),
		Lock:   filepath.Join(socketDir, "engine.lock"),
	}

	db, err := journal.Open(filepath.Join(tmpdir, "journal.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer db.Close()

	store := journal.NewStore(db)
	h := hub.New(store, 1024)

	var maxFrameMu sync.Mutex
	maxFrameObserved := int64(0)
	frameObserver := func(len int) {
		maxFrameMu.Lock()
		if int64(len) > maxFrameObserved {
			maxFrameObserved = int64(len)
		}
		maxFrameMu.Unlock()
	}

	engine := &stubEngine{
		helloReply: wire.HelloReply{
			EngineVersion: "test",
			Protocol:      wire.ProtocolVersion,
			RunID:         "run-123",
		},
		statusReply: wire.StatusReply{
			RunID: "run-123",
		},
		stopping: make(chan struct{}),
	}

	server := New(Options{
		Paths:         paths,
		Engine:        engine,
		Store:         store,
		Hub:           h,
		FrameObserver: frameObserver,
	})

	if err := server.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(paths.Socket)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      wire.ProtocolVersion,
	}
	helloFrame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(helloFrame); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Check that no frame exceeded 1 MiB
	maxFrameMu.Lock()
	observed := maxFrameObserved
	maxFrameMu.Unlock()

	if observed > wire.MaxFrameBytes {
		t.Errorf("frame size %d exceeded limit %d", observed, wire.MaxFrameBytes)
	}
}

func TestBadProtocolVersion(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	helloReq := wire.HelloRequest{
		Client:        "test",
		ClientVersion: "1.0",
		Protocol:      2, // wrong protocol
	}
	helloFrame, _ := wire.NewRequest("hello", "hello", helloReq)
	if err := w.Write(helloFrame); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if frame.Type != "error" {
		t.Errorf("expected error, got %s", frame.Type)
	}

	var errReply wire.Error
	if err := json.Unmarshal(frame.Body, &errReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if errReply.Code != "protocol" {
		t.Errorf("expected protocol error, got %s", errReply.Code)
	}

	// Connection should close after error
	_, err = r.Read()
	if err != io.EOF {
		t.Errorf("expected EOF after protocol error, got %v", err)
	}
}

func TestNonHelloFirstFrame(t *testing.T) {
	server, sockPath, _, cleanup := setupTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = server.Serve(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	conn, err := connectSocket(sockPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	r := wire.NewReader(conn)
	w := wire.NewWriter(conn)

	// Send status request instead of hello
	statusReq := wire.StatusRequest{}
	statusFrame, _ := wire.NewRequest("status-1", "status", statusReq)
	if err := w.Write(statusFrame); err != nil {
		t.Fatalf("write: %v", err)
	}

	frame, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if frame.Type != "error" {
		t.Errorf("expected error, got %s", frame.Type)
	}

	var errReply wire.Error
	if err := json.Unmarshal(frame.Body, &errReply); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if errReply.Code != "protocol" {
		t.Errorf("expected protocol error, got %s", errReply.Code)
	}
}
