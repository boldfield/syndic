package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestDialSuccess(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		frame, _ := r.Read()
		if frame.Type != wire.RequestHello {
			t.Fatalf("expected hello request")
		}

		reply, _ := wire.NewReply("", wire.HelloReply{
			EngineVersion: "1.0",
			Protocol:      1,
			RunID:         "run-1",
			RunFirstSeq:   0,
			RosterHash:    "hash1",
			ManifestHash:  "hash2",
		})
		_ = w.Write(reply)
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	hello := client.Hello()
	if hello.RunID != "run-1" {
		t.Fatalf("expected run-1, got %s", hello.RunID)
	}
}

func TestDialErrorReply(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		_, _ = r.Read()
		errReply, _ := wire.NewError("", &wire.Error{Code: "protocol"})
		_ = w.Write(errReply)
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if client != nil {
		_ = client.Close()
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var wireErr *wire.Error
	if !errors.As(err, &wireErr) || wireErr.Code != "protocol" {
		t.Fatalf("expected protocol error, got %v", err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		frame, _ := r.Read()
		if frame.Type == wire.RequestHello {
			reply, _ := wire.NewReply("", wire.HelloReply{
				EngineVersion: "1.0",
				Protocol:      1,
				RunID:         "run-1",
				RunFirstSeq:   0,
				RosterHash:    "hash1",
				ManifestHash:  "hash2",
			})
			_ = w.Write(reply)
		}

		// For other requests, send reply in reverse order
		frames := make([]wire.Frame, 2)
		frames[0], _ = r.Read()
		frames[1], _ = r.Read()

		// Send replies in reverse order
		reply2, _ := wire.NewReply(frames[1].ID, wire.StatusReply{RunID: "from-frame-1"})
		_ = w.Write(reply2)

		reply1, _ := wire.NewReply(frames[0].ID, wire.StatusReply{RunID: "from-frame-0"})
		_ = w.Write(reply1)

		<-done
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	var wg sync.WaitGroup
	results := make([]string, 2)
	var mu sync.Mutex

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			reply, err := client.Status(ctx)
			if err != nil {
				t.Errorf("Status failed: %v", err)
				return
			}
			mu.Lock()
			results[idx] = reply.RunID
			mu.Unlock()
		}(i)
	}

	wg.Wait()
	done <- true

	// Each goroutine should have gotten a reply (doesn't matter which one based on IDs)
	if results[0] == "" || results[1] == "" {
		t.Fatalf("both goroutines should get replies, got %v", results)
	}
	// The key point is that each got one of the two expected values
	if !((results[0] == "from-frame-0" && results[1] == "from-frame-1") ||
		(results[0] == "from-frame-1" && results[1] == "from-frame-0")) {
		t.Fatalf("unexpected results: %v", results)
	}
}

func TestErrorReply(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			errReply, _ := wire.NewError(frame.ID, &wire.Error{
				Code:   "busy",
				TurnID: strPtr("turn-123"),
			})
			_ = w.Write(errReply)

			<-done
			return
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Status(ctx)
	done <- true

	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var wireErr *wire.Error
	if !errors.As(err, &wireErr) || wireErr.Code != "busy" {
		t.Fatalf("expected busy error, got %v", err)
	}
	if wireErr.TurnID == nil || *wireErr.TurnID != "turn-123" {
		t.Fatalf("expected turn-123, got %v", wireErr.TurnID)
	}
}

func TestContent(t *testing.T) {
	content := make([]byte, 700000)
	for i := range content {
		content[i] = byte(i % 256)
	}

	hash := sha256.Sum256(content)
	hashStr := fmt.Sprintf("%x", hash)

	done := make(chan bool, 1)

	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, _ := r.Read()
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestContent {
				var req wire.ContentRequest
				_ = json.Unmarshal(frame.Body, &req)

				offset := int(req.Offset)
				limit := int(req.Limit)
				if limit > len(content)-offset {
					limit = len(content) - offset
				}

				page := content[offset : offset+limit]

				reply, _ := wire.NewReply(frame.ID, wire.ContentReply{
					Ref:    req.Ref,
					Offset: req.Offset,
					Data:   wire.EncodePage(page),
					Total:  int64(len(content)),
					SHA256: hashStr,
				})
				_ = w.Write(reply)
			}

			select {
			case <-done:
				return
			default:
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.Content(ctx, "test-ref")
	done <- true

	if err != nil {
		t.Fatalf("Content failed: %v", err)
	}

	if len(result) != len(content) {
		t.Fatalf("expected %d bytes, got %d", len(content), len(result))
	}

	if !bytesEqual(result, content) {
		t.Fatalf("content mismatch")
	}
}

func TestContentSHA256Mismatch(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		frame, _ := r.Read()
		if frame.Type == wire.RequestHello {
			reply, _ := wire.NewReply("", wire.HelloReply{
				EngineVersion: "1.0",
				Protocol:      1,
				RunID:         "run-1",
				RunFirstSeq:   0,
				RosterHash:    "hash1",
				ManifestHash:  "hash2",
			})
			_ = w.Write(reply)
			return
		}

		reply, _ := wire.NewReply(frame.ID, wire.ContentReply{
			Ref:    "test-ref",
			Offset: 0,
			Data:   wire.EncodePage([]byte("hello")),
			Total:  5,
			SHA256: "wronghash",
		})
		_ = w.Write(reply)
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Content(ctx, "test-ref")
	if err == nil {
		t.Fatalf("expected error for sha256 mismatch")
	}
}

func TestContentShortFinalPage(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestContent {
				var req wire.ContentRequest
				_ = json.Unmarshal(frame.Body, &req)

				// Always send 0 bytes, saying total is 100 - causes infinite loop
				hash := sha256.Sum256([]byte{})
				hashStr := fmt.Sprintf("%x", hash)

				reply, _ := wire.NewReply(frame.ID, wire.ContentReply{
					Ref:    req.Ref,
					Offset: req.Offset,
					Data:   wire.EncodePage([]byte{}),
					Total:  100,
					SHA256: hashStr,
				})
				_ = w.Write(reply)
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Content(ctx, "test-ref")
	if err == nil {
		t.Fatalf("expected error for zero-length page before reaching total")
	}
}

func TestSubscribeAndNextEvent(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestSubscribe {
				reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{
					OK:          true,
					LiveFromSeq: 1,
				})
				_ = w.Write(reply)

				// Send three events
				for i := 1; i <= 3; i++ {
					body, _ := json.Marshal(wire.EvEngineStarted{RunID: "run-1"})
					event := wire.NewEvent(int64(i), wire.EventKindEngineStarted, time.Now(), body)
					_ = w.Write(event)
				}

				<-done
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	liveFrom, err := client.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if liveFrom != 1 {
		t.Fatalf("expected live_from_seq=1, got %d", liveFrom)
	}

	for i := 1; i <= 3; i++ {
		frame, err := client.NextEvent(ctx)
		if err != nil {
			t.Fatalf("NextEvent failed: %v", err)
		}
		if frame.Seq != int64(i) {
			t.Fatalf("expected seq=%d, got %d", i, frame.Seq)
		}
		if frame.Kind != wire.EventKindEngineStarted {
			t.Fatalf("expected EventKindEngineStarted, got %s", frame.Kind)
		}
	}

	if lastSeq := client.LastSeq(); lastSeq != 3 {
		t.Fatalf("expected LastSeq=3, got %d", lastSeq)
	}

	done <- true
}

func TestEventBetweenRequestAndReply(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestSubscribe {
				reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{
					OK:          true,
					LiveFromSeq: 1,
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestStatus {
				// Send an event before the reply
				body, _ := json.Marshal(wire.EvEngineStarted{RunID: "run-1"})
				event := wire.NewEvent(1, wire.EventKindEngineStarted, time.Now(), body)
				_ = w.Write(event)

				// Now send the status reply
				reply, _ := wire.NewReply(frame.ID, wire.StatusReply{RunID: "run-1"})
				_ = w.Write(reply)

				<-done
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Make a status request
	statusDone := make(chan error, 1)
	go func() {
		_, err := client.Status(ctx)
		statusDone <- err
	}()

	// Should receive event
	frame, err := client.NextEvent(ctx)
	if err != nil {
		t.Fatalf("NextEvent failed: %v", err)
	}
	if frame.Kind != wire.EventKindEngineStarted {
		t.Fatalf("expected EventKindEngineStarted, got %s", frame.Kind)
	}

	// Status request should complete
	if err := <-statusDone; err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	done <- true
}

func TestErrorFrameWithNoID(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		subscribed := false
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestSubscribe {
				reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{
					OK:          true,
					LiveFromSeq: 1,
				})
				_ = w.Write(reply)
				subscribed = true
				continue
			}

			if frame.Type == wire.RequestStatus && subscribed {
				// Send error with no ID (lagging notice)
				errReply, _ := wire.NewError("", &wire.Error{
					Code:    "lagging",
					LastSeq: int64Ptr(7),
				})
				_ = w.Write(errReply)

				<-done
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Make a status request but lagging error arrives
	statusErr := make(chan error, 1)
	go func() {
		_, err := client.Status(ctx)
		statusErr <- err
	}()

	// Should receive lagging error via NextEvent
	_, err = client.NextEvent(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var wireErr *wire.Error
	if !errors.As(err, &wireErr) || wireErr.Code != "lagging" {
		t.Fatalf("expected lagging error, got %v", err)
	}

	// Status should also get error
	if err := <-statusErr; err == nil {
		t.Fatalf("expected error for status request, got nil")
	}

	done <- true
}

func TestConnectionCloseWithWaitingRequest(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		frame, _ := r.Read()
		if frame.Type == wire.RequestHello {
			reply, _ := wire.NewReply("", wire.HelloReply{
				EngineVersion: "1.0",
				Protocol:      1,
				RunID:         "run-1",
				RunFirstSeq:   0,
				RosterHash:    "hash1",
				ManifestHash:  "hash2",
			})
			_ = w.Write(reply)
		}
		<-done
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Make a request that will hang
	statusErr := make(chan error, 1)
	go func() {
		_, err := client.Status(ctx)
		statusErr <- err
	}()

	// Give the request time to reach the server
	time.Sleep(50 * time.Millisecond)

	// Close server without waiting
	done <- true
	time.Sleep(100 * time.Millisecond)

	// Request should get an error (either write error or ErrClosed)
	if err := <-statusErr; err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestConnectionCloseWithWaitingNextEvent(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
			} else if frame.Type == wire.RequestSubscribe {
				reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{
					OK:          true,
					LiveFromSeq: 1,
				})
				_ = w.Write(reply)
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// NextEvent should get ErrClosed when server closes
	_, err = client.NextEvent(ctx)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}
			// Don't send reply to Status, so request hangs until context is cancelled
			select {}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx2, cancel2 := context.WithCancel(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel2()
	}()

	_, err = client.Status(ctx2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	cancel()
}

func TestAlreadyCancelledContextThenSuccessfulRequest(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}
			reply, _ := wire.NewReply(frame.ID, wire.StatusReply{RunID: "run-1"})
			_ = w.Write(reply)
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// First request with cancelled context
	ctx2, cancel2 := context.WithCancel(ctx)
	cancel2()

	_, err = client.Status(ctx2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Second request with fresh context should work
	reply, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if reply.RunID != "run-1" {
		t.Fatalf("expected run-1, got %s", reply.RunID)
	}
}

func TestUnknownEventKind(t *testing.T) {
	done := make(chan bool, 1)
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  "hash2",
				})
				_ = w.Write(reply)
				continue
			}

			if frame.Type == wire.RequestSubscribe {
				reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{
					OK:          true,
					LiveFromSeq: 1,
				})
				_ = w.Write(reply)

				// Send unknown event kind
				event := wire.NewEvent(1, "unknown_kind", time.Now(), []byte(`{"data":"test"}`))
				_ = w.Write(event)
				<-done
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	frame, err := client.NextEvent(ctx)
	if err != nil {
		t.Fatalf("NextEvent failed: %v", err)
	}
	if frame.Kind != "unknown_kind" {
		t.Fatalf("expected unknown_kind, got %s", frame.Kind)
	}

	done <- true
}

// Helpers

type testServer struct {
	addr       string
	listener   net.Listener
	dirToClean string
}

func startTestServer(t *testing.T, handler func(*wire.Reader, *wire.Writer)) *testServer {
	dir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	sockPath := filepath.Join(dir, "test.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to listen: %v", err)
	}

	server := &testServer{
		addr:       sockPath,
		listener:   listener,
		dirToClean: dir,
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := wire.NewReader(c)
				w := wire.NewWriter(c)
				handler(r, w)
			}(conn)
		}
	}()

	return server
}

func (s *testServer) Close() {
	_ = s.listener.Close()
	_ = os.RemoveAll(s.dirToClean)
}

func strPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
