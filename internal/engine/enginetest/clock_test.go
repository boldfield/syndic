package enginetest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeClockAdvanceFiresDueTimersInOrder(t *testing.T) {
	c := NewFakeClock(time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC))
	var fired []string
	c.AfterFunc(2*time.Second, func() { fired = append(fired, "two") })
	c.AfterFunc(time.Second, func() { fired = append(fired, "one") })
	stop := c.AfterFunc(time.Second, func() { fired = append(fired, "stopped") })
	c.AfterFunc(time.Hour, func() { fired = append(fired, "late") })

	if !stop() {
		t.Fatal("stop reported the timer already done")
	}
	if stop() {
		t.Fatal("second stop reported pending")
	}
	start := c.Now()
	c.Advance(2 * time.Second)
	if got := c.Now().Sub(start); got != 2*time.Second {
		t.Fatalf("advanced %v", got)
	}
	if len(fired) != 2 || fired[0] != "one" || fired[1] != "two" {
		t.Fatalf("fired = %v", fired)
	}
}

func TestFakeClockAdvanceRunsTimersScheduledByTimers(t *testing.T) {
	c := NewFakeClock(time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC))
	start := c.Now()
	var ticks []time.Duration
	var tick func()
	tick = func() {
		ticks = append(ticks, c.Now().Sub(start))
		c.AfterFunc(time.Second, tick)
	}
	c.AfterFunc(time.Second, tick)
	c.Advance(3500 * time.Millisecond)
	if len(ticks) != 3 || ticks[2] != 3*time.Second {
		t.Fatalf("ticks = %v", ticks)
	}
	if got := c.Now().Sub(start); got != 3500*time.Millisecond {
		t.Fatalf("now = %v", got)
	}
}

func TestFakeClockWaitTimer(t *testing.T) {
	c := NewFakeClock(time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A timer already pending is found at once.
	c.AfterFunc(time.Second, func() {})
	if err := c.WaitTimer(ctx, time.Second); err != nil {
		t.Fatal(err)
	}

	// A timer scheduled later wakes the wait.
	got := make(chan error, 1)
	go func() { got <- c.WaitTimer(ctx, 3*time.Second) }()
	c.AfterFunc(2*time.Second, func() {})
	c.AfterFunc(3*time.Second, func() {})
	if err := <-got; err != nil {
		t.Fatal(err)
	}

	// With no such timer, the wait ends with the context.
	cancel()
	if err := c.WaitTimer(ctx, 9*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
