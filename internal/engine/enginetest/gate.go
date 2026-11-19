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

	cancelGate    chan struct{}
	cancelEntered chan struct{}
}

func (a *gatedAdapter) holdCancel() (entered <-chan struct{}, release func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	gate := make(chan struct{})
	en := make(chan struct{})
	a.cancelGate, a.cancelEntered = gate, en
	var once sync.Once
	return en, func() { once.Do(func() { close(gate) }) }
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
	a.mu.Lock()
	gate, entered := a.cancelGate, a.cancelEntered
	a.cancelGate, a.cancelEntered = nil, nil
	a.mu.Unlock()
	if gate != nil {
		close(entered)
		<-gate
	}
	return a.inner.Cancel(ctx, attemptID)
}

func (a *gatedAdapter) Usage(ctx context.Context) (*transport.Usage, error) {
	return a.inner.Usage(ctx)
}
