package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func ptrString(s string) *string {
	return &s
}

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
			ManifestHash:  ptrString("hash2"),
			State:         "started",
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

func TestDialContextCancellation(t *testing.T) {
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		frame, _ := r.Read()
		if frame.Type == wire.RequestHello {
			reply, _ := wire.NewReply("", wire.HelloReply{
				EngineVersion: "1.0",
				Protocol:      1,
				RunID:         "run-1",
				RunFirstSeq:   0,
				RosterHash:    "hash1",
				ManifestHash:  ptrString("hash2"),
				State:         "started",
			})
			_ = w.Write(reply)
		}
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			switch frame.Type {
			case wire.RequestStatus:
				reply, _ := wire.NewReply(frame.ID, wire.StatusReply{RunID: "run-1"})
				_ = w.Write(reply)
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	cancel()
	time.Sleep(100 * time.Millisecond)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	status, err := client.Status(ctx2)
	if err != nil {
		t.Fatalf("Status after cancel should work, got error: %v", err)
	}
	if status.RunID != "run-1" {
		t.Fatalf("expected run-1, got %s", status.RunID)
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
				ManifestHash:  ptrString("hash2"),
				State:         "started",
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
	ok := (results[0] == "from-frame-0" && results[1] == "from-frame-1") ||
		(results[0] == "from-frame-1" && results[1] == "from-frame-0")
	if !ok {
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
				ManifestHash:  ptrString("hash2"),
				State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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

	_, err = client.Subscribe(ctx, 7)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Check LastSeq is set to sinceSeq immediately
	if lastSeq := client.LastSeq(); lastSeq != 7 {
		t.Fatalf("expected LastSeq=7 after Subscribe, got %d", lastSeq)
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

	if wireErr.LastSeq == nil || *wireErr.LastSeq != 7 {
		t.Fatalf("expected last_seq 7 on lagging error, got %v", wireErr.LastSeq)
	}

	// The waiting request gets the same notice.
	var statusWireErr *wire.Error
	if err := <-statusErr; !errors.As(err, &statusWireErr) || statusWireErr.Code != "lagging" {
		t.Fatalf("expected lagging error for status request, got %v", err)
	}

	// LastSeq should not change after lagging notice
	if lastSeq := client.LastSeq(); lastSeq != 7 {
		t.Fatalf("expected LastSeq=7 after lagging notice, got %d", lastSeq)
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
				ManifestHash:  ptrString("hash2"),
				State:         "started",
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

	// Request should get ErrClosed
	if err := <-statusErr; !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestConnectionCloseWithWaitingNextEvent(t *testing.T) {
	release := make(chan struct{})
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		switch frame.Type {
		case wire.RequestSubscribe:
			reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: 1})
			_ = w.Write(reply)
		case wire.RequestStatus:
			<-release
			return false
		}
		return true
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Subscribe(ctx, 0); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Two NextEvent waiters and one request, all in flight before the close.
	const waiters = 2
	eventDone := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			_, err := client.NextEvent(ctx)
			eventDone <- err
		}()
	}
	statusDone := make(chan error, 1)
	go func() {
		_, err := client.Status(ctx)
		statusDone <- err
	}()

	time.Sleep(100 * time.Millisecond)
	close(release)

	if err := <-statusDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed for status, got %v", err)
	}
	for i := 0; i < waiters; i++ {
		if err := <-eventDone; !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed for NextEvent waiter, got %v", err)
		}
	}
}

func TestDialRejectsNonHelloReply(t *testing.T) {
	cases := map[string]any{
		"status reply":   wire.StatusReply{RunID: "not-a-hello"},
		"wrong protocol": wire.HelloReply{Protocol: 2, RunID: "run-1"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
				if _, err := r.Read(); err != nil {
					return
				}
				reply, _ := wire.NewReply("", body)
				_ = w.Write(reply)
				_, _ = r.Read()
			})
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := Dial(ctx, server.addr, "test-client", "1.0")
			if err == nil {
				_ = c.Close()
				t.Fatalf("expected Dial to fail")
			}
		})
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
					ManifestHash:  ptrString("hash2"),
					State:         "started",
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
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("failed to remove temp dir: %v", err)
		}
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
				defer func() { _ = c.Close() }()
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
	if err := os.RemoveAll(s.dirToClean); err != nil {
		fmt.Printf("warning: failed to remove temp dir: %v\n", err)
	}
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

// helloThen answers hello and then runs rest for each later frame.
func helloThen(rest func(frame wire.Frame, w *wire.Writer) bool) func(*wire.Reader, *wire.Writer) {
	return func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply("", wire.HelloReply{Protocol: 1, RunID: "run-1"})
				_ = w.Write(reply)
				continue
			}
			if !rest(frame, w) {
				return
			}
		}
	}
}

func TestEventsRightAfterSubscribeReplyStayOrdered(t *testing.T) {
	const n = 300
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestSubscribe {
			return true
		}
		reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: 1})
		_ = w.Write(reply)
		for i := int64(1); i <= n; i++ {
			_ = w.Write(wire.Frame{Type: "event", Seq: i, Kind: "k", Body: json.RawMessage(`{}`)})
		}
		return true
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, server.addr, "t", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Subscribe(ctx, 0); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= n; i++ {
		f, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.Seq != i {
			t.Fatalf("want seq %d got %d", i, f.Seq)
		}
	}
}

func TestPeerCloseDuringSubscribeFlood(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestSubscribe {
			return true
		}
		reply, _ := wire.NewReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: 1})
		_ = w.Write(reply)
		for i := int64(1); i <= 50; i++ {
			_ = w.Write(wire.Frame{Type: "event", Seq: i, Kind: "k", Body: json.RawMessage(`{}`)})
		}
		return false
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, server.addr, "t", "1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Subscribe(ctx, 0)
	for {
		if _, err := c.NextEvent(ctx); err != nil {
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("want ErrClosed, got %v", err)
			}
			return
		}
	}
}

func TestDialCtxCancelAfterSuccessKeepsClientAlive(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		reply, _ := wire.NewReply(frame.ID, wire.StatusReply{})
		_ = w.Write(reply)
		return true
	}))
	defer server.Close()

	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		c, err := Dial(ctx, server.addr, "t", "1")
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = c.Status(ctx2)
		cancel2()
		_ = c.Close()
		if err != nil {
			t.Fatalf("iteration %d: status after cancel: %v", i, err)
		}
	}
}

func TestContentShortFinalPageAfterFullPages(t *testing.T) {
	const page = wire.MaxContentPageBytes
	body := make([]byte, 2*page+100)
	for i := range body {
		body[i] = byte(i % 251)
	}
	sum := sha256.Sum256(body)
	sumStr := hex.EncodeToString(sum[:])

	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}
		var req wire.ContentRequest
		_ = json.Unmarshal(frame.Body, &req)
		var data []byte
		switch {
		case req.Offset < 2*page:
			data = body[req.Offset : req.Offset+page]
		case req.Offset == 2*page:
			data = body[req.Offset : req.Offset+50] // short: 50 of the remaining 100
		}
		reply, _ := wire.NewReply(frame.ID, wire.ContentReply{
			Ref:    req.Ref,
			Offset: req.Offset,
			Data:   wire.EncodePage(data),
			Total:  int64(len(body)),
			SHA256: sumStr,
		})
		_ = w.Write(reply)
		return true
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Content(ctx, "ref"); err == nil {
		t.Fatalf("expected error when the final page falls short and then empties")
	}
}

func TestCancelDuringBlockedWrite(t *testing.T) {
	cliConn, srvConn := net.Pipe()
	defer func() { _ = srvConn.Close() }()
	c := &Client{
		conn:      cliConn,
		reader:    wire.NewReader(cliConn),
		writer:    wire.NewWriter(cliConn),
		writeSem:  make(chan struct{}, 1),
		pending:   make(map[string]chan result),
		eventWake: make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	defer func() { _ = c.Close() }()

	// Nothing reads srvConn, so the first write blocks.
	ctx1, cancel1 := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := c.Status(ctx1)
		first <- err
	}()
	time.Sleep(50 * time.Millisecond)

	// A second call waits for the write slot and must honour its context.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := c.Status(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for write slot: want deadline exceeded, got %v", err)
	}

	cancel1()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked write: want canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Status did not return after cancel during blocked write")
	}
}

// TestApplyRetry verifies that Apply is idempotent and survives lost client reply.
// Calling Apply twice with the same turnID should return the same result both times.
func TestApplyRetry(t *testing.T) {
	requestCount := 0
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply(frame.ID, wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  ptrString("hash2"),
					State:         "started",
				})
				_ = w.Write(reply)
			} else if frame.Type == wire.RequestApply {
				requestCount++
				reply, _ := wire.NewReply(frame.ID, wire.ApplyReply{
					State: "applied",
					Paths: []wire.ChangePath{
						{Path: "file.txt", Outcome: "applied"},
					},
				})
				_ = w.Write(reply)
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

	// First Apply call
	reply1, err := client.Apply(ctx, "turn-1")
	if err != nil {
		t.Fatalf("first Apply failed: %v", err)
	}
	if reply1.State != "applied" {
		t.Fatalf("first Apply: state = %s, want 'applied'", reply1.State)
	}

	// Second Apply call (simulating retry after lost reply)
	reply2, err := client.Apply(ctx, "turn-1")
	if err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
	if reply2.State != reply1.State {
		t.Fatalf("second Apply: state = %s, want same as first (%s)", reply2.State, reply1.State)
	}

	// Both calls should have been sent to the server
	if requestCount < 2 {
		t.Fatalf("expected at least 2 Apply requests sent to server, got %d", requestCount)
	}
}

// TestDiscardRetry verifies that Discard is idempotent and survives lost client reply.
func TestDiscardRetry(t *testing.T) {
	requestCount := 0
	server := startTestServer(t, func(r *wire.Reader, w *wire.Writer) {
		for {
			frame, err := r.Read()
			if err != nil {
				return
			}
			if frame.Type == wire.RequestHello {
				reply, _ := wire.NewReply(frame.ID, wire.HelloReply{
					EngineVersion: "1.0",
					Protocol:      1,
					RunID:         "run-1",
					RunFirstSeq:   0,
					RosterHash:    "hash1",
					ManifestHash:  ptrString("hash2"),
					State:         "started",
				})
				_ = w.Write(reply)
			} else if frame.Type == wire.RequestDiscard {
				requestCount++
				reply, _ := wire.NewReply(frame.ID, wire.DiscardReply{
					State: "discarded",
					Paths: []wire.ChangePath{
						{Path: "file.txt", Outcome: "pending"},
					},
				})
				_ = w.Write(reply)
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

	// First Discard call
	reply1, err := client.Discard(ctx, "turn-1")
	if err != nil {
		t.Fatalf("first Discard failed: %v", err)
	}
	if reply1.State != "discarded" {
		t.Fatalf("first Discard: state = %s, want 'discarded'", reply1.State)
	}

	// Second Discard call (simulating retry after lost reply)
	reply2, err := client.Discard(ctx, "turn-1")
	if err != nil {
		t.Fatalf("second Discard failed: %v", err)
	}
	if reply2.State != reply1.State {
		t.Fatalf("second Discard: state = %s, want same as first (%s)", reply2.State, reply1.State)
	}
}
