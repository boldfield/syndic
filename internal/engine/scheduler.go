package engine

import (
	"sort"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// tickInterval is how often a turn reports its remaining time.
const tickInterval = time.Second

// pool is one resource pool: how many requests are in flight and who waits.
type pool struct {
	running int
	queue   []*queuedDispatch
}

// queuedDispatch is a dispatch waiting for a slot. start runs with Engine.mu
// held once the slot is taken, and must hand the slot back through
// releaseLocked if it fails to begin the attempt.
type queuedDispatch struct {
	t           *openTurn
	participant string
	start       func()
}

// poolOf returns the pool a participant dispatches through.
func (e *Engine) poolOf(participant string) string {
	return poolName(e.participantConfig(participant))
}

func (e *Engine) poolState(name string) *pool {
	p := e.pools[name]
	if p == nil {
		p = &pool{}
		e.pools[name] = p
	}
	return p
}

// schedule admits a dispatch to its pool. With a free slot and nothing ahead
// of it, start runs at once; otherwise the dispatch queues, first in first
// out, and the participant is queued. e.mu is held.
func (e *Engine) schedule(t *openTurn, participant string, start func()) error {
	if e.open != t || e.turnExpired(t) {
		return nil
	}
	name := e.poolOf(participant)
	p := e.poolState(name)
	concurrency, _ := e.roster.PoolFor(name)
	if p.running < concurrency && len(p.queue) == 0 {
		p.running++
		e.inflight[participant]++
		start()
		return nil
	}
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: participant, State: ParticipantQueued})
	})
	if err != nil {
		return err
	}
	e.queued[participant] = true
	p.queue = append(p.queue, &queuedDispatch{t: t, participant: participant, start: start})
	return nil
}

// lastInflight reports whether the dispatch completing now is the participant's
// only one holding a slot, so its completion takes the participant to idle.
// Dispatches abandoned at a deadline hold their slot until Complete returns.
func (e *Engine) lastInflight(participant string) bool {
	return e.inflight[participant] <= 1
}

// release gives back the pool slot of an attempt whose Complete call has
// returned.
func (e *Engine) release(participant string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.releaseLocked(participant)
}

// releaseLocked frees the participant's slot and starts the head of the
// queue while slots remain. A participant still marked running here ended
// without reporting idle (its result was never accepted), so it does now.
func (e *Engine) releaseLocked(participant string) {
	name := e.poolOf(participant)
	p := e.poolState(name)
	if p.running > 0 {
		p.running--
	}
	if e.inflight[participant] > 0 {
		e.inflight[participant]--
	}
	// A newer dispatch of the same participant may still be running.
	if e.inflight[participant] == 0 && e.running[participant] {
		delete(e.running, participant)
		_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: participant, State: ParticipantIdle})
		})
	}
	concurrency, _ := e.roster.PoolFor(name)
	for p.running < concurrency && len(p.queue) > 0 {
		head := p.queue[0]
		// A turn whose deadline has passed ends here; dropping its queue
		// takes head out and reports it idle.
		if e.open == head.t && e.turnExpired(head.t) && e.open != head.t {
			continue
		}
		p.queue = p.queue[1:]
		delete(e.queued, head.participant)
		if e.open != head.t {
			continue
		}
		p.running++
		e.inflight[head.participant]++
		head.start()
	}
}

// dropQueued removes the turn's queued dispatches. They never start; their
// participants go back to idle.
func (e *Engine) dropQueued(t *openTurn) {
	var dropped []string
	for _, p := range e.pools {
		kept := p.queue[:0]
		for _, q := range p.queue {
			if q.t == t {
				dropped = append(dropped, q.participant)
				delete(e.queued, q.participant)
				continue
			}
			kept = append(kept, q)
		}
		p.queue = kept
	}
	if len(dropped) == 0 {
		return
	}
	sort.Strings(dropped)
	_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		for _, id := range dropped {
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: id, State: ParticipantIdle}); err != nil {
				return err
			}
		}
		return nil
	})
}

// attemptDeadline is the deadline of an attempt dispatched now: the attempt
// duration from now, but never past the turn's.
func (e *Engine) attemptDeadline(t *openTurn) time.Time {
	deadline := e.clock.Now().UTC().Add(t.attemptDeadline)
	if t.turnDeadline.Before(deadline) {
		deadline = t.turnDeadline
	}
	return deadline
}

// turnExpired ends the open turn with deadline:turn when its deadline has
// already passed, so nothing is dispatched after it. e.mu is held.
func (e *Engine) turnExpired(t *openTurn) bool {
	if e.clock.Now().Before(t.turnDeadline) {
		return false
	}
	e.finishLocked(t, StateIncomplete, reasonDeadlineTurn)
	return true
}

// armTurn starts the turn's deadline timer and its once-a-second report. The
// timer fires at the turn's recorded deadline, however long ago that was set;
// a deadline already past ends the turn here. e.mu is held.
func (e *Engine) armTurn(t *openTurn) {
	if e.turnExpired(t) {
		return
	}
	d := t.turnDeadline.Sub(e.clock.Now())
	t.stopTurn = e.clock.AfterFunc(d, func() { e.onTurnDeadline(t) })
	t.stopTick = e.clock.AfterFunc(tickInterval, func() { e.onTick(t) })
}

// armAttempt starts the timer for a begun attempt. e.mu is held.
func (e *Engine) armAttempt(t *openTurn, attemptID string, deadline time.Time) {
	t.pending[attemptID] = deadline
	d := deadline.Sub(e.clock.Now())
	t.stopAttempt[attemptID] = e.clock.AfterFunc(d, func() { e.onAttemptDeadline(t, attemptID) })
}

// attemptDone stops the attempt's timer once its result has been handled.
// e.mu is held.
func (e *Engine) attemptDone(t *openTurn, attemptID string) {
	if stop := t.stopAttempt[attemptID]; stop != nil {
		stop()
	}
	delete(t.stopAttempt, attemptID)
	delete(t.pending, attemptID)
}

// turnEnded is the cleanup every terminal path runs after the terminal state
// committed: no timer of the turn fires and nothing it queued starts. e.mu is
// held.
func (e *Engine) turnEnded(t *openTurn) {
	if t.stopTurn != nil {
		t.stopTurn()
	}
	if t.stopTick != nil {
		t.stopTick()
	}
	for id := range t.stopAttempt {
		e.attemptDone(t, id)
	}
	e.dropQueued(t)
}

func (e *Engine) onTurnDeadline(t *openTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		return
	}
	e.finishLocked(t, StateIncomplete, reasonDeadlineTurn)
}

func (e *Engine) onAttemptDeadline(t *openTurn, attemptID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		return
	}
	if _, pending := t.pending[attemptID]; !pending {
		return
	}
	e.finishLocked(t, StateIncomplete, reasonDeadlineAttempt+attemptID)
}

// onTick reports the remaining time of the turn and of each running attempt,
// then arms the next report.
func (e *Engine) onTick(t *openTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		return
	}
	now := e.clock.Now()
	remaining := func(deadline time.Time) int64 {
		if ms := deadline.Sub(now).Milliseconds(); ms > 0 {
			return ms
		}
		return 0
	}
	ids := make([]string, 0, len(t.pending))
	for id := range t.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		if err := emit(t.id, wire.EventKindDeadline, wire.EvDeadline{
			TurnID:      t.id,
			Kind:        "turn",
			RemainingMs: remaining(t.turnDeadline),
		}); err != nil {
			return err
		}
		for _, id := range ids {
			attemptID := id
			if err := emit(t.id, wire.EventKindDeadline, wire.EvDeadline{
				TurnID:      t.id,
				Kind:        "attempt",
				AttemptID:   &attemptID,
				RemainingMs: remaining(t.pending[id]),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	t.stopTick = e.clock.AfterFunc(tickInterval, func() { e.onTick(t) })
}
