package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func setupTestDB(t *testing.T) *journal.Store {
	tempDir := t.TempDir()
	db, err := journal.Open(tempDir + "/test.db")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return journal.NewStore(db)
}

func TestTxEmitsAndDeliversEvents(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	events := make([]journal.Event, 0)
	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		if err := emit("", "test", map[string]string{"message": "hello"}); err != nil {
			return err
		}
		if err := emit("", "test", map[string]string{"message": "world"}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	sub, liveFromSeq, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	for i := 0; i < 2; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		events = append(events, event)
	}

	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if events[0].Kind != "test" || events[1].Kind != "test" {
		t.Errorf("unexpected event kinds: %v, %v", events[0].Kind, events[1].Kind)
	}

	if liveFromSeq != 3 {
		t.Errorf("expected liveFromSeq=3, got %d", liveFromSeq)
	}

	sub.Close()
}

func TestReplayAndLive(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	// Emit initial events
	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < 5; i++ {
			if err := emit("", "initial", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	// Subscribe after initial events
	sub, liveFromSeq, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if liveFromSeq != 6 {
		t.Errorf("expected liveFromSeq=6, got %d", liveFromSeq)
	}

	// Emit more events
	err = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		if err := emit("", "live", map[string]int{"n": 100}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second Tx failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Should get replay events
	for i := 0; i < 5; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next failed for replay: %v", err)
		}
		if event.Kind != "initial" {
			t.Errorf("expected 'initial', got %q", event.Kind)
		}
	}

	// Should get live event
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next failed for live: %v", err)
	}
	if event.Kind != "live" {
		t.Errorf("expected 'live', got %q", event.Kind)
	}

	sub.Close()
}

func TestMultipleSubscribers(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	sub1, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe 1 failed: %v", err)
	}

	err = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		return emit("", "event1", map[string]string{"data": "value"})
	})
	if err != nil {
		t.Fatalf("Tx 1 failed: %v", err)
	}

	sub2, _, err := hub.Subscribe(1)
	if err != nil {
		t.Fatalf("Subscribe 2 failed: %v", err)
	}

	err = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		return emit("", "event2", map[string]string{"data": "value"})
	})
	if err != nil {
		t.Fatalf("Tx 2 failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// sub1 should get event1 from replay, then event2 from live
	e1, _ := sub1.Next(ctx)
	if e1.Kind != "event1" {
		t.Errorf("sub1 first event: expected event1, got %q", e1.Kind)
	}
	e2, _ := sub1.Next(ctx)
	if e2.Kind != "event2" {
		t.Errorf("sub1 second event: expected event2, got %q", e2.Kind)
	}

	// sub2 should only get event2 (subscribed after event1 with sinceSeq=1)
	e3, _ := sub2.Next(ctx)
	if e3.Kind != "event2" {
		t.Errorf("sub2 event: expected event2, got %q", e3.Kind)
	}

	sub1.Close()
	sub2.Close()
}

func TestLaggingSubscriber(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 2)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Emit 3 events; queue size is 2, so 3rd event will cause lagging
	err = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < 3; i++ {
			if err := emit("", "event", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Should get first 2 events (they made it into the queue)
	for i := 0; i < 2; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d failed: %v", i, err)
		}
		if event.Kind != "event" {
			t.Errorf("event %d: expected 'event', got %q", i, event.Kind)
		}
	}

	// Now should get LaggingError (3rd event was dropped due to full queue)
	_, err = sub.Next(ctx)
	if !errors.Is(err, ErrLagging) {
		t.Errorf("expected ErrLagging, got %v", err)
	}

	var lagErr *LaggingError
	if !errors.As(err, &lagErr) {
		t.Errorf("expected *LaggingError, got %T", err)
	}
	if lagErr.LastSeq == 0 {
		t.Errorf("LaggingError.LastSeq should be > 0, got %d", lagErr.LastSeq)
	}

	sub.Close()
}

func TestClosedSubscription(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = sub.Next(ctx)
	if !errors.Is(err, ErrClosed) {
		t.Errorf("expected ErrClosed after Close, got %v", err)
	}

	sub.Close()
}

func TestContextCancellation(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = sub.Next(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	sub.Close()
}

func TestEventSequenceOrdering(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < 10; i++ {
			if err := emit("", "event", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var prevSeq int64
	for i := 0; i < 10; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d failed: %v", i, err)
		}
		if event.Seq <= prevSeq {
			t.Errorf("sequence not strictly increasing: %d after %d", event.Seq, prevSeq)
		}
		prevSeq = event.Seq
	}

	sub.Close()
}

func TestTxRollbackDoesntDeliver(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		if err := emit("", "event", map[string]string{"data": "value"}); err != nil {
			return err
		}
		return errors.New("forced error")
	})
	if err == nil {
		t.Fatal("expected error from Tx")
	}

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = sub.Next(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}

	sub.Close()
}

func TestStrictlyIncreasingSeqUnderLoad(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	go func() {
		for i := 0; i < 100; i++ {
			_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
				return emit("", "event", map[string]int{"n": i})
			})
		}
	}()

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var prevSeq int64
	eventCount := 0
	for eventCount < 100 {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if event.Seq <= prevSeq {
			t.Errorf("sequence not strictly increasing: %d after %d", event.Seq, prevSeq)
		}
		prevSeq = event.Seq
		eventCount++
	}

	sub.Close()
}

func TestReplayPartialThenLive(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	// Emit 5 events
	for i := 0; i < 5; i++ {
		_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "initial", map[string]int{"n": i})
		})
	}

	// Subscribe with sinceSeq=2, should replay 3, 4, 5
	sub, liveFromSeq, err := hub.Subscribe(2)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if liveFromSeq != 6 {
		t.Errorf("expected liveFromSeq=6, got %d", liveFromSeq)
	}

	// Emit 2 more events
	for i := 5; i < 7; i++ {
		_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "live", map[string]int{"n": i})
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Should replay 3 events (3, 4, 5)
	for i := 0; i < 3; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("replay Next %d failed: %v", i, err)
		}
		if event.Kind != "initial" {
			t.Errorf("expected 'initial', got %q", event.Kind)
		}
	}

	// Should get 2 live events
	for i := 0; i < 2; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("live Next %d failed: %v", i, err)
		}
		if event.Kind != "live" {
			t.Errorf("expected 'live', got %q", event.Kind)
		}
	}

	sub.Close()
}

func TestNonBlockingQueueCheck(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Fill the queue and mark as lagging
	_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < 2; i++ {
			if err := emit("", "event", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Get the queued event
	_, err = sub.Next(ctx)
	if err != nil {
		t.Fatalf("get queued event failed: %v", err)
	}

	// Should get lagging error immediately without blocking
	_, err = sub.Next(ctx)
	if !errors.Is(err, ErrLagging) {
		t.Errorf("expected ErrLagging, got %v", err)
	}

	sub.Close()
}

func TestPayloadMarshaling(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	type TestPayload struct {
		Message string `json:"message"`
		Number  int    `json:"number"`
	}

	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		return emit("", "test", TestPayload{Message: "hello", Number: 42})
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	var payload TestPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if payload.Message != "hello" || payload.Number != 42 {
		t.Errorf("payload mismatch: got %+v", payload)
	}

	sub.Close()
}

func TestLaggingErrorWithResume(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 8)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	err = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < 9; i++ {
			if err := emit("", "event", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var lastEvent journal.Event
	for i := 0; i < 8; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d failed: %v", i, err)
		}
		lastEvent = event
	}

	_, err = sub.Next(ctx)
	if !errors.Is(err, ErrLagging) {
		t.Fatalf("expected ErrLagging, got %v", err)
	}

	var lagErr *LaggingError
	if !errors.As(err, &lagErr) {
		t.Fatalf("expected *LaggingError, got %T", err)
	}

	resumeSeq := lagErr.LastSeq
	if resumeSeq != lastEvent.Seq {
		t.Errorf("LaggingError.LastSeq %d != lastEvent.Seq %d", resumeSeq, lastEvent.Seq)
	}

	sub.Close()

	sub2, _, err := hub.Subscribe(resumeSeq)
	if err != nil {
		t.Fatalf("Subscribe resume failed: %v", err)
	}
	defer sub2.Close()

	event, err := sub2.Next(ctx)
	if err != nil {
		t.Fatalf("Next after resume failed: %v", err)
	}
	if event.Seq != lastEvent.Seq+1 {
		t.Errorf("resume event seq %d, expected %d", event.Seq, lastEvent.Seq+1)
	}
}

func TestConcurrent5000EventHandoff(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	const numEvents = 5000
	const subscribeAfter = 2500

	seenSeqs := make(map[int64]bool)
	seenSeqsMu := &sync.Mutex{}

	done := make(chan int)
	go func() {
		for i := 0; i < numEvents; i++ {
			n := i
			err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
				return emit("", "event", map[string]int{"n": n})
			})
			if err != nil {
				t.Errorf("Tx %d failed: %v", i, err)
				return
			}
		}
		done <- 1
	}()

	time.Sleep(10 * time.Millisecond)

	sub, _, err := hub.Subscribe(subscribeAfter - 1)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var prevSeq int64
	expectedCount := numEvents - subscribeAfter + 1
	for i := 0; i < expectedCount; i++ {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d failed: %v", i, err)
		}
		if event.Seq <= prevSeq {
			t.Errorf("event %d: seq %d not strictly increasing after %d", i, event.Seq, prevSeq)
		}
		seenSeqsMu.Lock()
		if seenSeqs[event.Seq] {
			t.Errorf("seq %d seen twice", event.Seq)
		}
		seenSeqs[event.Seq] = true
		seenSeqsMu.Unlock()
		prevSeq = event.Seq
	}

	if len(seenSeqs) != expectedCount {
		t.Errorf("expected %d unique sequences, got %d", expectedCount, len(seenSeqs))
	}

	<-done
}

func TestTenStaggeredSubscribers(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	const totalEvents = 100
	const subscribers = 10

	for i := 0; i < totalEvents; i++ {
		n := i
		_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "event", map[string]int{"n": n})
		})
	}

	subs := make([]*Subscription, subscribers)
	for i := 0; i < subscribers; i++ {
		sinceSeq := int64((i * totalEvents) / subscribers)
		sub, _, err := hub.Subscribe(sinceSeq)
		if err != nil {
			t.Fatalf("Subscribe %d failed: %v", i, err)
		}
		subs[i] = sub
	}
	defer func() {
		for _, sub := range subs {
			sub.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var maxSeqs [subscribers]int64
	for i := 0; i < subscribers; i++ {
		for {
			event, err := subs[i].Next(ctx)
			if err != nil {
				if err == context.DeadlineExceeded {
					break
				}
				t.Fatalf("sub %d Next failed: %v", i, err)
			}
			if event.Seq <= maxSeqs[i] {
				t.Errorf("sub %d: seq %d not strictly increasing after %d", i, event.Seq, maxSeqs[i])
			}
			maxSeqs[i] = event.Seq
		}
	}

	if maxSeqs[0] == 0 {
		t.Fatal("no events received by any subscriber")
	}

	for i := 1; i < subscribers; i++ {
		if maxSeqs[i] != maxSeqs[0] {
			t.Errorf("sub %d final seq %d != sub 0 final seq %d", i, maxSeqs[i], maxSeqs[0])
		}
	}
}

func TestReplayLargeWithStalledSubscriber(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	const totalSeeded = 5000
	for i := 0; i < totalSeeded; i++ {
		n := i
		_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "event", map[string]int{"n": n})
		})
	}

	stalledSub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("stalled subscribe failed: %v", err)
	}
	defer stalledSub.Close()

	readerSub, _, err := hub.Subscribe(2500)
	if err != nil {
		t.Fatalf("reader subscribe failed: %v", err)
	}
	defer readerSub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	txStartTime := time.Now()
	const numTxCalls = 200
	for i := 0; i < numTxCalls; i++ {
		_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "event", map[string]int{"n": totalSeeded + i})
		})
	}
	txEndTime := time.Now()
	txElapsed := txEndTime.Sub(txStartTime)

	var prevSeq int64
	eventCount := 0
	expectedCount := totalSeeded - 2500 + numTxCalls
	for eventCount < expectedCount {
		event, err := readerSub.Next(ctx)
		if err != nil {
			if err == context.DeadlineExceeded {
				break
			}
			t.Fatalf("reader Next failed: %v", err)
		}
		if event.Seq <= prevSeq {
			t.Errorf("seq %d not strictly increasing after %d", event.Seq, prevSeq)
		}
		prevSeq = event.Seq
		eventCount++
	}

	if eventCount != expectedCount {
		t.Errorf("reader got %d events, expected %d", eventCount, expectedCount)
	}

	const txTimeout = 10 * time.Second
	if txElapsed > txTimeout {
		t.Errorf("Tx calls took %v, exceeded timeout %v (stalled subscriber should not block)", txElapsed, txTimeout)
	}
}

func TestCancelationThenReuse(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	ctx, cancel := context.WithCancel(context.Background())

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Close()

	done := make(chan error)
	go func() {
		_, err := sub.Next(ctx)
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	err = <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()

	_ = hub.Tx(func(tx *journal.Tx, emit Emit) error {
		return emit("", "event", map[string]string{"data": "test"})
	})

	event, err := sub.Next(ctx2)
	if err != nil {
		t.Fatalf("Next after cancel failed: %v", err)
	}
	if event.Kind != "event" {
		t.Errorf("expected event, got %q", event.Kind)
	}
}

func TestCloseWhileWaiting(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	done := make(chan error)
	go func() {
		_, err := sub.Next(context.Background())
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	sub.Close()

	err = <-done
	if !errors.Is(err, ErrClosed) {
		t.Errorf("expected ErrClosed, got %v", err)
	}
}
