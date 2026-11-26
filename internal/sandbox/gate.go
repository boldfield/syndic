package sandbox

import (
	"errors"
	"sync"
)

// ErrGateClosed is returned, wrapped in a *GateClosedError, by Enter on a
// closed gate.
var ErrGateClosed = errors.New("sandbox: execution gate closed")

// GateClosedError is the error of an Enter refused by a closed gate.
type GateClosedError struct {
	// Reason is the reason given to the Close that closed the gate.
	Reason string
}

func (e *GateClosedError) Error() string {
	return ErrGateClosed.Error() + ": " + e.Reason
}

// Unwrap makes errors.Is(err, ErrGateClosed) hold.
func (e *GateClosedError) Unwrap() error { return ErrGateClosed }

// Gate is the run's execution gate (v7 delta, section 3, "Containment
// unverified"). The engine holds one per run, closes it when a call's
// containment is unverified, and calls Enter immediately before starting
// any command's process. Enter and Close are mutually exclusive, so a
// command either entered before the gate closed or does not start. The zero
// value is an open gate. A Gate is safe for concurrent use.
type Gate struct {
	mu     sync.Mutex
	closed bool
	reason string
}

// Enter returns nil while the gate is open. Once a Close has returned, every
// later Enter fails with a *GateClosedError naming the reason.
func (g *Gate) Enter() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return &GateClosedError{Reason: g.reason}
	}
	return nil
}

// Close closes the gate for the rest of the run. The first reason is kept;
// closing a closed gate changes nothing.
func (g *Gate) Close(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed, g.reason = true, reason
}

// Closed reports whether the gate is closed and, if so, why.
func (g *Gate) Closed() (reason string, closed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason, g.closed
}
