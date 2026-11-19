package engine

import (
	"context"
	"errors"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// stopRun is the first Stop call. Later calls wait on done while it runs.
type stopRun struct {
	done     chan struct{}
	err      error
	finished bool // guarded by Engine.mu
	waiters  int  // calls waiting on done, guarded by Engine.mu
}

// errStopTerminal is returned by Stop when the transaction that ends the open
// turn did not commit. The engine keeps refusing work and Stop may be called
// again.
var errStopTerminal = errors.New("engine: stop: terminal transaction failed")

// Stop stops the engine in an orderly way: Submit, Cancel, Override and
// Release refuse from now on, the open turn ends as incomplete with reason
// engine_stop, deliveries of earlier turns are dropped or cancelled, and the
// adapter cancellations get stop_grace, measured with the engine's Clock, to
// return. engine_stopping is emitted, and Stopping() closed, before the
// cancellations start; what they confirm within the grace period is recorded
// after it, and after Stop returns nothing more is written to the journal.
//
// A call made while another runs waits for it and returns its result; a call
// made after it finished returns nil. If ctx ends first Stop returns its
// error at once, leaving what was committed, which is what a crash would.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if r := e.stop; r != nil {
		finished := r.finished
		if !finished {
			r.waiters++
		}
		e.mu.Unlock()
		if finished {
			return nil
		}
		select {
		case <-r.done:
			return r.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r := &stopRun{done: make(chan struct{})}
	e.stop = r
	e.stopping = true
	e.mu.Unlock()

	err := e.doStop(ctx)

	e.mu.Lock()
	r.err = err
	r.finished = true
	if errors.Is(err, errStopTerminal) {
		e.stop = nil
	}
	e.mu.Unlock()
	close(r.done)
	return err
}

// doStop runs the stop. e.mu is not held on entry or return.
func (e *Engine) doStop(ctx context.Context) error {
	e.mu.Lock()
	if !e.started {
		e.closeJournal()
		close(e.stoppingCh)
		e.mu.Unlock()
		return nil
	}

	// The grace period starts here, before any cancellation can be asked
	// for, so a test that advances the clock once a Cancel has begun is
	// sure to find the timer.
	graceDone := make(chan struct{})
	stopGrace := e.clock.AfterFunc(e.roster.Engine.StopGrace.Duration, func() { close(graceDone) })
	defer stopGrace()

	term, deliveries, err := e.stopTerminals()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	// engine_stopping goes out before the adapter cancellations start, so it
	// precedes everything they record and Stopping() is closed for the whole
	// grace period.
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit("", wire.EventKindEngineStopping, wire.EvEngineStopping{RunID: e.runID})
	})
	if err != nil {
		e.closeJournal()
		e.mu.Unlock()
		return err
	}
	close(e.stoppingCh)
	e.launchCancels(term)
	e.launchCancels(deliveries)
	e.mu.Unlock()

	// Wait for the cancellations, for at most the grace period. What they
	// confirm meanwhile is recorded as usual.
	var ctxErr error
	for {
		e.mu.Lock()
		if e.cancelers == 0 {
			break
		}
		idle := e.cancelersIdle
		e.mu.Unlock()
		select {
		case <-idle:
			continue
		case <-graceDone:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
		e.mu.Lock()
		break
	}
	// The journal closes under the same lock as the decision to return:
	// nothing is written after Stop returns. A context that ended leaves
	// what was committed, as a crash would.
	e.closeJournal()
	e.mu.Unlock()
	return ctxErr
}

// closeJournal stops the engine writing to the journal. e.mu is held.
func (e *Engine) closeJournal() {
	e.gate.Lock()
	e.closed = true
	e.gate.Unlock()
}

// stopTerminals ends the open turn with engine_stop and requests
// cancellation of the in-flight deliveries of earlier turns, dropping the
// queued ones, in one Tx. It returns the open turn's terminal and the
// deliveries' holds, whose adapter cancellations the caller starts. e.mu is
// held.
func (e *Engine) stopTerminals() (*terminal, *terminal, error) {
	var deliveries *terminal
	pre := func(tx *journal.Tx, emit hub.Emit) error {
		deliveries = &terminal{}
		return e.stopDeliveriesIn(tx, emit, deliveries)
	}
	var term *terminal
	t := e.open
	if t != nil {
		if term = e.endLocked(t, StateIncomplete, reasonEngineStop, pre); term == nil {
			// Not committed: unless the journal already has the turn ended,
			// as when a later engine recovered it, Stop has not ended it.
			if row, err := e.store.GetTurn(t.id); err != nil || row.TerminalState == "" {
				return nil, nil, errStopTerminal
			}
			e.open = nil
			e.turnEnded(t)
			t = nil
		}
	}
	if t == nil {
		if err := e.tx(pre); err != nil {
			return nil, nil, errStopTerminal
		}
	}
	e.applyHolds(deliveries)
	for _, t := range e.deliveries {
		for id := range t.pending {
			e.attemptDone(t, id)
		}
		e.dropQueued(t)
	}
	return term, deliveries, nil
}

// stopDeliveriesIn requests cancellation of and holds capacity for the
// deliveries in flight, and reports the participants of the queued ones as
// no longer queued, inside the caller's Tx. The turns they belong to are not
// touched. e.mu is held.
func (e *Engine) stopDeliveriesIn(tx *journal.Tx, emit hub.Emit, term *terminal) error {
	for _, t := range e.deliveries {
		if err := e.reportDroppedIn(emit, t); err != nil {
			return err
		}
		attempts, err := tx.ListAttempts(t.id)
		if err != nil {
			return err
		}
		if err := e.holdPendingIn(tx, emit, term, attempts, phaseDelivery); err != nil {
			return err
		}
	}
	return nil
}

// Stopping returns a channel that is closed once engine_stopping is in the
// journal, so the server can close client connections at the right moment.
func (e *Engine) Stopping() <-chan struct{} {
	return e.stoppingCh
}
