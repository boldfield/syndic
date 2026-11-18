package enginetest

import (
	"context"
	"sync"

	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// gatedAdapter wraps a fake adapter so a test can hold its next Complete
// call until released.
type gatedAdapter struct {
	inner *fake.Adapter

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
	if gate != nil {
		<-gate
	}
	return a.inner.Complete(ctx, req)
}

func (a *gatedAdapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	return a.inner.Cancel(ctx, attemptID)
}

func (a *gatedAdapter) Usage(ctx context.Context) (*transport.Usage, error) {
	return a.inner.Usage(ctx)
}
