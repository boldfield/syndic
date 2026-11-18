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
