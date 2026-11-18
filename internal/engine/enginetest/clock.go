package enginetest

import (
	"sync"
	"time"
)

// FakeClock is a Clock whose time only moves when Advance is called.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	nextID int
	timers map[int]*fakeTimer
}

type fakeTimer struct {
	id   int
	at   time.Time
	f    func()
	done bool
}

// NewFakeClock returns a clock reading start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start, timers: make(map[int]*fakeTimer)}
}

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc schedules f for when the clock has advanced by d. The returned
// function stops the timer and reports whether it was still pending.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	t := &fakeTimer{id: c.nextID, at: c.now.Add(d), f: f}
	c.timers[t.id] = t
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.done {
			return false
		}
		t.done = true
		delete(c.timers, t.id)
		return true
	}
}

// Advance moves the clock forward by d, running every timer that comes due
// along the way in order of due time, on the calling goroutine. The clock
// reads each timer's due time while its function runs, so a function that
// schedules another timer inside the window sees it run too.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if t.at.After(target) {
				continue
			}
			if next == nil || t.at.Before(next.at) || (t.at.Equal(next.at) && t.id < next.id) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.done = true
		delete(c.timers, next.id)
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}
