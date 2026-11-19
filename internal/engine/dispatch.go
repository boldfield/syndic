package engine

import (
	"context"
	"errors"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
)

// Participant states derived from a failed dispatch. They last until the
// participant's next dispatch.
const (
	stateUnreachable      = "unreachable"
	stateUnauthorized     = "unauthorized"
	stateModelUnavailable = "model_unavailable"
)

const (
	statusNotSentPrefix     = "not_sent:"
	statusAdapterErrPrefix  = "adapter_error:"
	statusReset             = transport.TransportStatusReset
	defaultUnreachableDelay = 500 * time.Millisecond
)

// retryDelay is how long an unreachable attempt waits, on the engine's Clock,
// before its one retry. Tests shorten it.
var retryDelay = defaultUnreachableDelay

// failure is what a failed Complete does to its attempt and turn.
type failure struct {
	// attemptStatus and transportStatus are what the attempt records;
	// transportStatus is also the <status> of the turn's terminal reason.
	attemptStatus   transport.CompletionStatus
	transportStatus string
	// retry: no request reached the server, so one retry is allowed.
	retry bool
	// hold: the request may have reached the server, so capacity is held.
	hold bool
	// state is the participant state the failure sets, if any.
	state string
}

// participantState is the state the participant reports once the attempt
// completes.
func (f *failure) participantState() string {
	switch {
	case f == nil:
		return ParticipantIdle
	case f.hold:
		// The held slot makes the participant unknown; skip the idle between.
		return ParticipantUnknown
	case f.state == "":
		return ParticipantIdle
	}
	return f.state
}

// failureRules is the mapping from a transport status to its failure. Statuses
// not listed here and not "ok" are treated like an http_<code>.
var failureRules = map[string]failure{
	transport.TransportStatusUnreachable:      {attemptStatus: transport.StatusError, retry: true, state: stateUnreachable},
	transport.TransportStatusReset:            {attemptStatus: transport.StatusInterrupted, hold: true},
	transport.TransportStatusTimeout:          {attemptStatus: transport.StatusInterrupted, hold: true},
	transport.TransportStatusUnauthorized:     {attemptStatus: transport.StatusError, state: stateUnauthorized},
	transport.TransportStatusModelUnavailable: {attemptStatus: transport.StatusError, state: stateModelUnavailable},
	transport.TransportStatusMalformedBody:    {attemptStatus: transport.StatusError},
	transport.TransportStatusBodyTooLarge:     {attemptStatus: transport.StatusError},
}

// classifyDispatch maps what Complete returned to a failure. It returns nil
// for a transport-ok result, whose completion status is handled as usual.
func classifyDispatch(res transport.CompleteResult, goErr error) *failure {
	if goErr != nil {
		if errors.Is(goErr, transport.ErrNotSent) {
			return &failure{attemptStatus: transport.StatusError, transportStatus: statusNotSentPrefix + goErr.Error()}
		}
		return &failure{attemptStatus: transport.StatusInterrupted, transportStatus: statusAdapterErrPrefix + goErr.Error(), hold: true}
	}
	ts := res.TransportStatus
	if ts == "" || ts == transport.TransportStatusOK {
		return nil
	}
	// A cancel the engine did not make is as uncertain as a reset. callOnce
	// keeps the engine's own cancels away from here.
	if ts == transport.TransportStatusCancelled {
		ts = statusReset
	}
	f, ok := failureRules[ts]
	if !ok {
		// http_<code> and anything unknown: the attempt failed, nothing is
		// known to be running.
		f = failure{attemptStatus: transport.StatusError}
	}
	f.transportStatus = ts
	return &f
}

// applyTo returns res with the failure's statuses recorded.
func (f *failure) applyTo(req transport.CompleteRequest, res transport.CompleteResult) transport.CompleteResult {
	if res.Envelope == (transport.AttemptEnvelope{}) {
		res.Envelope = req.Envelope
	}
	res.Status = f.attemptStatus
	res.TransportStatus = f.transportStatus
	return res
}

// callOnce calls the adapter once and classifies what came back.
// A result the adapter reported as cancelled after the engine itself cancelled
// the attempt (ours reports true) keeps the adapter's own status.
func callOnce(a transport.Adapter, req transport.CompleteRequest, ours func() bool) (transport.CompleteResult, *failure) {
	res, err := a.Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{}
	}
	if err == nil && res.TransportStatus == transport.TransportStatusCancelled && ours() {
		return res, nil
	}
	f := classifyDispatch(res, err)
	if f != nil {
		res = f.applyTo(req, res)
	}
	return res, f
}

// retryRoom reports whether the turn is still open and the delay fits before
// both deadlines, plus the attempt is still pending. e.mu is held.
func (e *Engine) retryRoom(t *openTurn, req transport.CompleteRequest, delay time.Duration) bool {
	if e.open != t {
		return false
	}
	if _, pending := t.pending[req.Envelope.AttemptID]; !pending {
		return false
	}
	due := e.clock.Now().Add(delay)
	return due.Before(t.turnDeadline) && due.Before(req.Deadline)
}

// waitToRetry waits retryDelay on the engine's Clock when a retry still fits.
func (e *Engine) waitToRetry(t *openTurn, req transport.CompleteRequest) bool {
	e.mu.Lock()
	ok := e.retryRoom(t, req, e.retryDelay)
	e.mu.Unlock()
	if !ok {
		return false
	}
	done := make(chan struct{})
	e.clock.AfterFunc(e.retryDelay, func() { close(done) })
	<-done
	return true
}

// recordRetry journals the retry once the wait is over, only if it can still
// be dispatched, so a journaled retry is always a dispatched one.
func (e *Engine) recordRetry(t *openTurn, req transport.CompleteRequest) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.retryRoom(t, req, 0) {
		return false
	}
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		_, err := tx.RecordRetryDispatch(req.Envelope.AttemptID)
		return err
	})
	// A failed journal write skips the retry on purpose: a retry that is not
	// journaled must not be dispatched, so the turn ends with the first result.
	return err == nil
}

// complete calls the participant's adapter, retrying once when no request
// reached the server, and classifies the outcome. The failure is nil for a
// transport-ok result.
func (e *Engine) complete(t *openTurn, participant string, req transport.CompleteRequest) (transport.CompleteResult, *failure) {
	a := e.adapterFor(participant)
	ours := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cancelled[req.Envelope.AttemptID]
	}
	res, f := callOnce(a, req, ours)
	if f != nil && f.retry && e.waitToRetry(t, req) && e.recordRetry(t, req) {
		res, f = callOnce(a, req, ours)
	}
	e.returned(req.Envelope.AttemptID)
	return res, f
}

// failedIn opens the capacity hold a failure needs, in the Tx that records
// the attempt's output. It returns nil when none is needed.
func (e *Engine) failedIn(tx *journal.Tx, emit hub.Emit, participant, attemptID string, f *failure) (*hold, error) {
	if f == nil || !f.hold {
		return nil, nil
	}
	return e.holdIn(tx, emit, attemptID, participant, e.clock.Now().UTC())
}

// failedCommitted applies a committed failure to memory: the hold counts
// against its pool and the participant keeps its failure state until its next
// dispatch. e.mu is held.
func (e *Engine) failedCommitted(participant string, f *failure, h *hold) {
	if h != nil {
		h.calling = e.calling[h.attemptID]
		e.holds[h.attemptID] = h
		e.armHold(h)
		if e.lastInflight(participant) {
			// Already announced by the completion Tx; syncParticipant must not repeat it.
			e.pstate[participant] = ParticipantUnknown
		}
	}
	if f != nil && f.state != "" && e.lastInflight(participant) {
		e.failed[participant] = f.state
	}
}

// markRunning records a begun dispatch; the participant's failure state ends.
// e.mu is held.
func (e *Engine) markRunning(participant string) {
	e.running[participant] = true
	delete(e.failed, participant)
}
