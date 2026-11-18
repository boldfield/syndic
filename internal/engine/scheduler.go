package engine

import (
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/wire"
)

// queuedDispatch represents a dispatch waiting in a pool queue.
type queuedDispatch struct {
	participant string
	turnID      string
	dispatchFn  func() error
}

// queuedReviewer represents a queued reviewer for later dispatch.
type queuedReviewer struct {
	participant   string
	turnID        string
	emitTurnState bool
}

// poolScheduler manages a single resource pool.
type poolScheduler struct {
	name            string
	concurrency     int
	running         int
	queue           []queuedDispatch
	queuedReviewers []queuedReviewer
}

// Scheduler manages pools and participant states for the engine.
type Scheduler struct {
	mu               sync.Mutex
	pools            map[string]*poolScheduler
	participantState map[string]string      // participant id -> state
	attemptDeadlines map[string]func() bool // attempt id -> cancel function
	turnDeadline     func() bool            // turn-level cancel function
}

// NewScheduler creates a new scheduler with pools configured from the roster.
func NewScheduler(roster interface {
	PoolFor(name string) (concurrency int, uncertainReleaseAfter time.Duration)
}, participants interface {
	//Participant returns the participant config
}, poolNames []string) *Scheduler {
	s := &Scheduler{
		pools:            make(map[string]*poolScheduler),
		participantState: make(map[string]string),
		attemptDeadlines: make(map[string]func() bool),
	}

	// Initialize pools with their concurrency limits.
	seenPools := make(map[string]bool)
	for _, poolName := range poolNames {
		if seenPools[poolName] {
			continue
		}
		seenPools[poolName] = true

		concurrency, _ := roster.PoolFor(poolName)
		s.pools[poolName] = &poolScheduler{
			name:        poolName,
			concurrency: concurrency,
		}
	}
	// Always have a default pool
	if !seenPools["default"] {
		s.pools["default"] = &poolScheduler{
			name:        "default",
			concurrency: 1,
		}
	}
	return s
}

// PoolFor returns the concurrency and queue depth for a named pool.
func (s *Scheduler) PoolFor(poolName string) (concurrency int, queued int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}
	p, ok := s.pools[poolName]
	if !ok {
		return 1, 0
	}
	return p.concurrency, len(p.queue)
}

// GetParticipantState returns the current state of a participant.
func (s *Scheduler) GetParticipantState(participant string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.participantState[participant]
	if !ok {
		return ParticipantIdle
	}
	return state
}

// SetParticipantState sets the participant state and emits an event if the state changed.
func (s *Scheduler) SetParticipantState(participant, state string, emit hub.Emit) error {
	s.mu.Lock()
	oldState, ok := s.participantState[participant]
	if ok && oldState == state {
		s.mu.Unlock()
		return nil
	}
	s.participantState[participant] = state
	s.mu.Unlock()

	return emit("", wire.EventKindParticipantState, wire.EvParticipantState{
		Participant: participant,
		State:       state,
	})
}

// TryDispatch attempts to dispatch a participant to its pool, or queues it.
// Returns true if the participant was dispatched, false if queued.
func (s *Scheduler) TryDispatch(participant string, poolName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}

	p, ok := s.pools[poolName]
	if !ok {
		return false
	}

	if p.running < p.concurrency {
		p.running++
		return true
	}
	return false
}

// Queue queues a dispatch that could not start immediately.
func (s *Scheduler) Queue(participant, turnID string, poolName string, dispatchFn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}

	p, ok := s.pools[poolName]
	if !ok {
		return
	}

	p.queue = append(p.queue, queuedDispatch{
		participant: participant,
		turnID:      turnID,
		dispatchFn:  dispatchFn,
	})
}

// Dequeue removes and returns the next queued dispatch from a pool.
func (s *Scheduler) Dequeue(poolName string) (queuedDispatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}

	p, ok := s.pools[poolName]
	if !ok || len(p.queue) == 0 {
		return queuedDispatch{}, false
	}

	d := p.queue[0]
	p.queue = p.queue[1:]
	p.running++
	return d, true
}

// ReleaseSlot releases a concurrency slot in a pool, allowing the next queued
// dispatch to run if any.
func (s *Scheduler) ReleaseSlot(poolName string) {
	s.mu.Lock()
	p, ok := s.pools[poolName]
	if ok && p.running > 0 {
		p.running--
	}
	s.mu.Unlock()
}

// SetAttemptDeadlineTimer stores the cancel function for an attempt deadline.
func (s *Scheduler) SetAttemptDeadlineTimer(attemptID string, cancel func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptDeadlines[attemptID] = cancel
}

// SetTurnDeadlineTimer stores the cancel function for a turn deadline.
func (s *Scheduler) SetTurnDeadlineTimer(cancel func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnDeadline = cancel
}

// ClearDeadlineTimer removes an attempt deadline timer.
func (s *Scheduler) ClearDeadlineTimer(attemptID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.attemptDeadlines, attemptID)
}

// ClearTurnDeadlineTimer removes the turn deadline timer.
func (s *Scheduler) ClearTurnDeadlineTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.turnDeadline = nil
}

// ClearAllDeadlines removes all timers (called when a turn ends).
func (s *Scheduler) ClearAllDeadlines() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cancel := range s.attemptDeadlines {
		if cancel != nil {
			cancel()
		}
	}
	if s.turnDeadline != nil {
		s.turnDeadline()
	}
	s.attemptDeadlines = make(map[string]func() bool)
	s.turnDeadline = nil
}

// TurnDeadlineTimer returns the turn deadline timer if set.
func (s *Scheduler) TurnDeadlineTimer() func() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnDeadline
}

// ClearTurnQueues removes all queued dispatches for a given turn.
func (s *Scheduler) ClearTurnQueues(turnID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := 0
	for _, p := range s.pools {
		var filtered []queuedDispatch
		for _, d := range p.queue {
			if d.turnID != turnID {
				filtered = append(filtered, d)
			} else {
				total++
			}
		}
		p.queue = filtered

		var filteredReviewers []queuedReviewer
		for _, r := range p.queuedReviewers {
			if r.turnID != turnID {
				filteredReviewers = append(filteredReviewers, r)
			} else {
				total++
			}
		}
		p.queuedReviewers = filteredReviewers
	}
	return total
}

// QueueReviewer queues a reviewer for later dispatch.
func (s *Scheduler) QueueReviewer(participant, turnID string, poolName string, emitTurnState bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}

	p, ok := s.pools[poolName]
	if !ok {
		return
	}

	p.queuedReviewers = append(p.queuedReviewers, queuedReviewer{
		participant:   participant,
		turnID:        turnID,
		emitTurnState: emitTurnState,
	})
}

// DequeueReviewer returns the next queued reviewer from a pool, if any.
func (s *Scheduler) DequeueReviewer(poolName string) (queuedReviewer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if poolName == "" {
		poolName = "default"
	}

	p, ok := s.pools[poolName]
	if !ok || len(p.queuedReviewers) == 0 {
		return queuedReviewer{}, false
	}

	r := p.queuedReviewers[0]
	p.queuedReviewers = p.queuedReviewers[1:]
	return r, true
}
