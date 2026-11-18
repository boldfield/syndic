package enginetest

import (
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
