package enginetest

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// gatedAdapter wraps a fake adapter so a test can hold its next Complete
// call until released.
type gatedAdapter struct {
	inner *fake.Adapter
	dead  *atomic.Bool // set by Harness.Crash

	mu   sync.Mutex
	gate chan struct{}
}

func (a *gatedAdapter) hold() (release func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	gate := make(chan struct{})
	a.gate = gate
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (a *gatedAdapter) Probe(ctx context.Context, endpoint, model string) (transport.CapabilityRecord, error) {
	return a.inner.Probe(ctx, endpoint, model)
}

func (a *gatedAdapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	a.mu.Lock()
	gate := a.gate
	a.gate = nil
	a.mu.Unlock()
	if a.dead.Load() {
		return a.inert(ctx)
	}
	if gate != nil {
		<-gate
	}
	if a.dead.Load() {
		return a.inert(ctx)
	}
	return a.inner.Complete(ctx, req)
}

func (a *gatedAdapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	return a.inner.Cancel(ctx, attemptID)
}

func (a *gatedAdapter) Usage(ctx context.Context) (*transport.Usage, error) {
	return a.inner.Usage(ctx)
}

// inert is what a call on a crashed engine's adapter does: it never answers,
// so the dead engine writes nothing, until its own context ends.
func (a *gatedAdapter) inert(ctx context.Context) (transport.CompleteResult, error) {
	<-ctx.Done()
	return transport.CompleteResult{}, ctx.Err()
}

// crashClock wraps a clock so timers set by a crashed engine never fire.
type crashClock struct {
	inner *FakeClock
	dead  *atomic.Bool
}

func (c crashClock) Now() time.Time { return c.inner.Now() }

func (c crashClock) AfterFunc(d time.Duration, f func()) func() bool {
	return c.inner.AfterFunc(d, func() {
		if !c.dead.Load() {
			f()
		}
	})
}
