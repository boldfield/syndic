package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
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

// emitN emits n single-event transactions.
func emitN(t *testing.T, h *Hub, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		i := i
		err := h.Tx(func(tx *journal.Tx, emit Emit) error {
			return emit("", "event", map[string]int{"n": i})
		})
		if err != nil {
			t.Errorf("Tx %d failed: %v", i, err)
			return
		}
	}
}

// readTail reads events until seq upTo has been returned, checking that every
// Seq from from+1 to upTo arrives exactly once and in order, then checks that
// nothing further arrives.
func readTail(t *testing.T, sub *Subscription, from, upTo int64) {
	t.Helper()
	// The tail of TestConcurrent5000EventHandoff is thousands of journal
	// transactions; on a loaded machine running every package in parallel
	// they take longer than a minute, so the bound is generous.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	want := from + 1
	for want <= upTo {
		ev, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next failed waiting for seq %d: %v", want, err)
		}
		if ev.Seq != want {
			t.Fatalf("got seq %d, want %d", ev.Seq, want)
		}
		want++
	}
	quiet, qcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer qcancel()
	if ev, err := sub.Next(quiet); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected no further event, got seq %d err %v", ev.Seq, err)
	}
}

func TestLaggingErrorWithResume(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 8)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Close()

	// Nine events with nobody reading: the ninth overflows the queue of 8.
	emitN(t, hub, 9)
	// Later events must not be delivered to the lagging subscriber.
	emitN(t, hub, 5)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var last int64
	for i := 0; i < 8; i++ {
		ev, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d failed: %v", i, err)
		}
		last = ev.Seq
	}
	if last != 8 {
		t.Fatalf("last queued seq %d, want 8", last)
	}

	for i := 0; i < 3; i++ {
		_, err = sub.Next(ctx)
		if !errors.Is(err, ErrLagging) {
			t.Fatalf("call %d: expected ErrLagging, got %v", i, err)
		}
		var lagErr *LaggingError
		if !errors.As(err, &lagErr) {
			t.Fatalf("call %d: expected *LaggingError, got %T", i, err)
		}
		if lagErr.LastSeq != last {
			t.Fatalf("call %d: LastSeq %d, want %d", i, lagErr.LastSeq, last)
		}
	}

	max, err := store.MaxSeq()
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	sub2, liveFrom, err := hub.Subscribe(last)
	if err != nil {
		t.Fatalf("Subscribe resume failed: %v", err)
	}
	defer sub2.Close()
	if liveFrom != max+1 {
		t.Fatalf("liveFromSeq %d, want %d", liveFrom, max+1)
	}
	readTail(t, sub2, last, max)
}

func TestConcurrent5000EventHandoff(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	const numEvents = 5000
	const midway = 2500

	var emitted atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < numEvents; i++ {
			i := i
			err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
				return emit("", "event", map[string]int{"n": i})
			})
			if err != nil {
				t.Errorf("Tx %d failed: %v", i, err)
				return
			}
			emitted.Store(int64(i + 1))
		}
	}()

	// Wait for the writer to pass the midpoint, then resume from a Seq the
	// reader "last saw" while the writer is still going.
	for emitted.Load() < midway {
		time.Sleep(time.Millisecond)
	}
	since := emitted.Load()

	sub, _, err := hub.Subscribe(since)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Close()

	readTail(t, sub, since, numEvents)
	<-done
}

func TestTenStaggeredSubscribers(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 1024)

	const totalEvents = 200
	const subscribers = 10
	const step = totalEvents / subscribers

	type result struct {
		since int64
		seqs  []int64
	}
	subs := make([]*Subscription, 0, subscribers)
	sinces := make([]int64, 0, subscribers)

	// Attach one subscriber every `step` events while emitting; each
	// resumes from a Seq at or before its attach point.
	for i := 0; i < subscribers; i++ {
		emitN(t, hub, step)
		max, err := store.MaxSeq()
		if err != nil {
			t.Fatalf("MaxSeq: %v", err)
		}
		since := max - int64(i) // mix of replayed and live starts
		if since < 0 {
			since = 0
		}
		sub, _, err := hub.Subscribe(since)
		if err != nil {
			t.Fatalf("Subscribe %d failed: %v", i, err)
		}
		defer sub.Close()
		subs = append(subs, sub)
		sinces = append(sinces, since)
	}

	results := make([]result, subscribers)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i, sub := range subs {
		results[i].since = sinces[i]
		for seq := sinces[i] + 1; seq <= totalEvents; seq++ {
			ev, err := sub.Next(ctx)
			if err != nil {
				t.Fatalf("sub %d Next failed: %v", i, err)
			}
			results[i].seqs = append(results[i].seqs, ev.Seq)
			if ev.Seq != seq {
				t.Fatalf("sub %d got seq %d, want %d", i, ev.Seq, seq)
			}
		}
		quiet, qcancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if ev, err := sub.Next(quiet); !errors.Is(err, context.DeadlineExceeded) {
			qcancel()
			t.Fatalf("sub %d: expected no further event, got seq %d err %v", i, ev.Seq, err)
		}
		qcancel()
	}

	// All tails agree where they overlap: each is the suffix of the longest.
	longest := results[0].seqs
	for i, r := range results {
		off := len(longest) - len(r.seqs)
		for j, s := range r.seqs {
			if longest[off+j] != s {
				t.Fatalf("sub %d tail differs at %d: %d vs %d", i, j, s, longest[off+j])
			}
		}
	}
}

func TestReplayLargeWithStalledSubscriber(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 256)

	const totalSeeded = 50000
	err := hub.Tx(func(tx *journal.Tx, emit Emit) error {
		for i := 0; i < totalSeeded; i++ {
			if err := emit("", "event", map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	// Never reads.
	stalled, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("stalled subscribe failed: %v", err)
	}
	defer stalled.Close()

	reader, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("reader subscribe failed: %v", err)
	}
	defer reader.Close()

	const numTxCalls = 200
	txDone := make(chan struct{})
	go func() {
		defer close(txDone)
		emitN(t, hub, numTxCalls)
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		readTail(t, reader, 0, totalSeeded+numTxCalls)
	}()

	select {
	case <-txDone:
	case <-time.After(60 * time.Second):
		t.Fatal("Tx calls did not complete: blocked on a subscriber")
	}
	select {
	case <-readDone:
	case <-time.After(60 * time.Second):
		t.Fatal("reader did not finish replay")
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

func TestCloseWhileWaitingWithTxInFlight(t *testing.T) {
	store := setupTestDB(t)
	hub := New(store, 10)

	sub, _, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	inTx := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- hub.Tx(func(_ *journal.Tx, emit Emit) error {
			close(inTx)
			<-release
			return emit("", "k", map[string]string{})
		})
	}()
	<-inTx

	nextDone := make(chan error, 1)
	go func() {
		_, err := sub.Next(context.Background())
		nextDone <- err
	}()
	time.Sleep(20 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		sub.Close()
		close(closeDone)
	}()
	for {
		sub.mu.Lock()
		c := sub.closed
		sub.mu.Unlock()
		if c {
			break
		}
		time.Sleep(time.Millisecond)
	}

	close(release)
	if err := <-txDone; err != nil {
		t.Fatalf("Tx failed: %v", err)
	}
	<-closeDone

	select {
	case err := <-nextDone:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("expected ErrClosed, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not return")
	}
}
