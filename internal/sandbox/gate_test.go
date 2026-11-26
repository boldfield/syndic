package sandbox

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGateCloses(t *testing.T) {
	var g Gate
	if err := g.Enter(); err != nil {
		t.Fatalf("Enter on an open gate: %v", err)
	}
	if _, closed := g.Closed(); closed {
		t.Fatal("new gate is closed")
	}
	g.Close("containment unverified")
	g.Close("second reason")
	for i := 0; i < 3; i++ {
		err := g.Enter()
		var ce *GateClosedError
		if !errors.Is(err, ErrGateClosed) || !errors.As(err, &ce) || ce.Reason != "containment unverified" {
			t.Fatalf("Enter after Close: %v", err)
		}
	}
	if reason, closed := g.Closed(); !closed || reason != "containment unverified" {
		t.Errorf("Closed() = %q, %v", reason, closed)
	}
}

// TestGateConcurrent checks, under the race detector, that no Enter begun
// after Close returned succeeds.
func TestGateConcurrent(t *testing.T) {
	for round := 0; round < 200; round++ {
		var g Gate
		var closeReturned atomic.Bool
		var late atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for j := 0; j < 50; j++ {
					after := closeReturned.Load()
					if g.Enter() == nil && after {
						late.Add(1)
					}
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			g.Close("unverified")
			closeReturned.Store(true)
		}()
		close(start)
		wg.Wait()
		if n := late.Load(); n != 0 {
			t.Fatalf("round %d: %d Enter calls succeeded after Close returned", round, n)
		}
		if g.Enter() == nil {
			t.Fatalf("round %d: Enter succeeded on a closed gate", round)
		}
	}
}
