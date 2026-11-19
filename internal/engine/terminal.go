package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Capacity event states and the cancellation states of an attempt.
const (
	capacityUncertain = "uncertain"
	capacityReleased  = "released"

	cancellationRequested = "requested"
	cancellationConfirmed = "confirmed"
	cancellationUnknown   = "unknown"

	releaseServerConfirmed = "server_confirmed"
	releaseLateResult      = "late_result"
	releaseOperator        = "operator"
	releaseOperatorTimeout = "operator_timeout"

	reasonUncertainTimeout = "uncertain_release_after"
)

// hold is the in-memory mirror of an unreleased capacity_holds row.
type hold struct {
	attemptID    string
	pool         string   // original pool name from the hold record (for orphaned holds)
	appliedPools []string // all pools this hold applies to in the new roster
	participant  string
	since        time.Time
	group        []wire.GroupMember
	// calling is true while the attempt's Complete call has not returned, in
	// which case its pool slot is already counted as running.
	calling bool
	stop    func() bool
}

// terminal is what the terminal Tx decided, for the engine to apply once the
// Tx has committed.
type terminal struct {
	holds []*hold
}

// occupied is the number of slots of the pool in use: requests in flight plus
// holds whose request is no longer in flight. e.mu is held.
func (e *Engine) occupied(name string) int {
	n := e.poolState(name).running
	for _, h := range e.holds {
		if !h.calling {
			for _, pool := range h.appliedPools {
				if pool == name {
					n++
					break
				}
			}
		}
	}
	return n
}

// poolGroup is every {endpoint_name, model} of the pool's members in the
// current roster.
func (e *Engine) poolGroup(pool string) []wire.GroupMember {
	seen := map[wire.GroupMember]bool{}
	var group []wire.GroupMember
	for _, p := range e.roster.Participants {
		if poolName(p) != pool {
			continue
		}
		m := wire.GroupMember{EndpointName: p.Endpoint, Model: p.Model}
		if !seen[m] {
			seen[m] = true
			group = append(group, m)
		}
	}
	sort.Slice(group, func(i, j int) bool {
		if group[i].EndpointName != group[j].EndpointName {
			return group[i].EndpointName < group[j].EndpointName
		}
		return group[i].Model < group[j].Model
	})
	return group
}

// holdIn opens the capacity hold of an attempt whose server-side outcome is
// unknown and announces it, inside the caller's Tx. The caller installs the
// returned hold once the Tx has committed. e.mu is held.
func (e *Engine) holdIn(tx *journal.Tx, emit hub.Emit, attemptID, participant string, now time.Time) (*hold, error) {
	p := e.participantConfig(participant)
	pool := poolName(p)
	group := e.poolGroup(pool)
	members := make([]journal.GroupMember, len(group))
	for i, m := range group {
		members[i] = journal.GroupMember{EndpointName: m.EndpointName, Model: m.Model}
	}
	if err := tx.OpenHold(journal.Hold{
		AttemptID:    attemptID,
		Pool:         pool,
		EndpointName: p.Endpoint,
		Model:        p.Model,
		Group:        members,
		HeldSince:    now,
	}); err != nil {
		return nil, err
	}
	if err := emit("", wire.EventKindCapacity, wire.EvCapacity{
		Pool:          pool,
		HeldByAttempt: attemptID,
		State:         capacityUncertain,
	}); err != nil {
		return nil, err
	}
	return &hold{attemptID: attemptID, pool: pool, appliedPools: []string{pool}, participant: participant, since: now, group: group}, nil
}

// endIn is the one terminal path, run inside the Tx that ends the turn:
// bump the epoch, drop the turn's queued dispatches, request cancellation of
// and hold capacity for every in-flight attempt, record the phase for a
// cancel, end the turn and emit turn_state. e.mu is held.
func (e *Engine) endIn(tx *journal.Tx, emit hub.Emit, t *openTurn, state, reason string) (*terminal, error) {
	if _, err := tx.BumpEpoch(t.id); err != nil {
		return nil, err
	}

	// Queued dispatches have no rows, so only their participants change.
	var queued []string
	for _, p := range e.pools {
		for _, q := range p.queue {
			if q.t == t {
				queued = append(queued, q.participant)
			}
		}
	}
	sort.Strings(queued)
	for _, id := range queued {
		next := e.specialState(id)
		if next == "" {
			next = ParticipantIdle
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: id, State: next}); err != nil {
			return nil, err
		}
	}

	attempts, err := tx.ListAttempts(t.id)
	if err != nil {
		return nil, err
	}
	term := &terminal{}
	now := e.clock.Now().UTC()
	cancelling := map[string]bool{}
	for _, a := range attempts {
		if a.State != "pending" {
			continue
		}
		if err := tx.SetCancellation(a.ID, cancellationRequested); err != nil {
			return nil, err
		}
		h, err := e.holdIn(tx, emit, a.ID, a.Participant, now)
		if err != nil {
			return nil, err
		}
		if !cancelling[a.Participant] {
			cancelling[a.Participant] = true
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
				Participant: a.Participant,
				State:       ParticipantCancelling,
			}); err != nil {
				return nil, err
			}
		}
		term.holds = append(term.holds, h)
	}

	if state == StateCancelled {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return nil, err
		}
		phase := phaseAuthor
		if turn.CandidateID != "" {
			phase = phaseReview
		}
		if err := tx.SetPhaseAtCancel(t.id, phase); err != nil {
			return nil, err
		}
	}

	if err := tx.EndTurn(t.id, state, reason); err != nil {
		return nil, err
	}
	if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: state, Reason: &reason}); err != nil {
		return nil, err
	}
	return term, nil
}

// commitTerminal applies a committed terminal Tx to memory: the holds count
// against their pools, the turn is no longer open, and nothing it queued
// starts. e.mu is held.
func (e *Engine) commitTerminal(t *openTurn, term *terminal) {
	if term != nil {
		for _, h := range term.holds {
			h.calling = e.calling[h.attemptID]
			if h.calling {
				e.cancelled[h.attemptID] = true
			}
			e.holds[h.attemptID] = h
			e.cancelling[h.participant]++
			e.pstate[h.participant] = ParticipantCancelling
			e.armHold(h)
		}
	}
	if e.open == t {
		e.open = nil
		e.turnEnded(t)
	}
}

// launchCancels calls Adapter.Cancel for the terminal's attempts on a
// goroutine, for callers that hold e.mu. Stop waits for it.
func (e *Engine) launchCancels(term *terminal) {
	if term == nil || len(term.holds) == 0 {
		return
	}
	e.cancelers++
	go func() {
		_ = e.cancelAttempts(term.holds)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.cancelers--; e.cancelers == 0 {
			e.cancelersOut.Broadcast()
		}
	}()
}

// endLocked runs the terminal path in a Tx of its own, after pre if given,
// and applies it once committed. It returns nil when the Tx did not commit.
// e.mu is held.
func (e *Engine) endLocked(t *openTurn, state, reason string, pre func(tx *journal.Tx, emit hub.Emit) error) *terminal {
	var term *terminal
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		term = nil
		if pre != nil {
			if err := pre(tx, emit); err != nil {
				return err
			}
		}
		var err error
		term, err = e.endIn(tx, emit, t, state, reason)
		return err
	})
	if err != nil {
		return nil
	}
	e.commitTerminal(t, term)
	e.deliverTerminal(t, state, reason)
	return term
}

// finishLocked ends the turn through the terminal path and starts the
// adapter cancellations without waiting for them. e.mu is held.
func (e *Engine) finishLocked(t *openTurn, state, reason string) {
	e.launchCancels(e.endLocked(t, state, reason, nil))
}

// cancelAttempts calls Adapter.Cancel for each held attempt, after the
// terminal Tx committed, and records what came back. It must run without
// e.mu. The first Tx error is returned; the hold stays in that case.
func (e *Engine) cancelAttempts(holds []*hold) error {
	var first error
	for _, h := range holds {
		if e.crashHook != nil && e.crashHook() {
			return nil
		}
		status, err := e.adapterFor(h.participant).Cancel(context.Background(), h.attemptID)
		confirmed := err == nil && status.ServerConfirmed == transport.TriYes
		if err := e.afterCancel(h, confirmed); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// afterCancel records the outcome of one Adapter.Cancel call.
func (e *Engine) afterCancel(h *hold, confirmed bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancelling[h.participant] > 0 {
		e.cancelling[h.participant]--
	}
	released := false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		released = false
		if !confirmed {
			return tx.SetCancellation(h.attemptID, cancellationUnknown)
		}
		if err := tx.SetCancellation(h.attemptID, cancellationConfirmed); err != nil {
			return err
		}
		if e.holds[h.attemptID] != h {
			// A late result already released it.
			return nil
		}
		released = true
		return releaseHoldIn(tx, emit, h, releaseServerConfirmed, "")
	})
	if err == nil && released {
		e.dropHold(h)
	}
	e.syncParticipant(h.participant)
	return err
}

// releaseHoldIn releases the hold's row and emits the capacity event, inside
// the caller's Tx.
func releaseHoldIn(tx *journal.Tx, emit hub.Emit, h *hold, how, reason string) error {
	if err := tx.ReleaseHold(h.attemptID, how, reason); err != nil {
		return err
	}
	ev := wire.EvCapacity{Pool: h.pool, HeldByAttempt: h.attemptID, State: capacityReleased, How: &how}
	if reason != "" {
		ev.Reason = &reason
	}
	return emit("", wire.EventKindCapacity, ev)
}

// dropHold forgets a hold whose row was released and lets its pool's queue
// move. e.mu is held.
func (e *Engine) dropHold(h *hold) {
	if e.holds[h.attemptID] != h {
		return
	}
	delete(e.holds, h.attemptID)
	if h.stop != nil {
		h.stop()
	}
	e.kick(h.pool)
}

// armHold starts the operator-configured release timer of the hold's pool.
// Zero means the hold is never released on its own. e.mu is held.
func (e *Engine) armHold(h *hold) {
	_, after := e.roster.PoolFor(h.pool)
	if after <= 0 {
		return
	}
	d := h.since.Add(after).Sub(e.clock.Now())
	h.stop = e.clock.AfterFunc(d, func() { e.onHoldTimeout(h) })
}

// onHoldTimeout releases the hold with how operator_timeout. If the Tx fails
// the hold stays, which is the conservative outcome.
func (e *Engine) onHoldTimeout(h *hold) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.holds[h.attemptID] != h {
		return
	}
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return releaseHoldIn(tx, emit, h, releaseOperatorTimeout, reasonUncertainTimeout)
	})
	if err != nil {
		return
	}
	e.dropHold(h)
	e.syncParticipant(h.participant)
}

// lateEvidence reports whether a result that arrived after the attempt was
// cancelled shows the server finished. A result the client itself cut short,
// or a Go error, does not.
func lateEvidence(res transport.CompleteResult) bool {
	return res.Status != transport.StatusInterrupted && !strings.HasPrefix(res.TransportStatus, "adapter_error")
}

// releaseLateIn releases the hold of a quarantined attempt, in the Tx that
// stores its output, when the result is evidence the server finished.
func (e *Engine) releaseLateIn(tx *journal.Tx, emit hub.Emit, attemptID string, res transport.CompleteResult) error {
	h := e.holds[attemptID]
	if h == nil || !lateEvidence(res) {
		return nil
	}
	return releaseHoldIn(tx, emit, h, releaseLateResult, "")
}

// lateCommitted applies releaseLateIn once its Tx committed. The participant
// is reported when its slot is given back, which follows. e.mu is held.
func (e *Engine) lateCommitted(attemptID string, res transport.CompleteResult) {
	if h := e.holds[attemptID]; h != nil && lateEvidence(res) {
		e.dropHold(h)
	}
}

// specialState is cancelling or unknown when either applies to the
// participant, else empty. e.mu is held.
func (e *Engine) specialState(id string) string {
	if e.cancelling[id] > 0 {
		return ParticipantCancelling
	}
	for _, h := range e.holds {
		if h.participant == id {
			return ParticipantUnknown
		}
	}
	return ""
}

// syncParticipant reports the participant's state when cancelling or unknown
// started or stopped applying. e.mu is held.
func (e *Engine) syncParticipant(id string) {
	want := e.specialState(id)
	if want == e.pstate[id] {
		return
	}
	state := want
	if want == "" {
		delete(e.pstate, id)
		switch {
		case e.running[id]:
			state = ParticipantRunning
		case e.queued[id]:
			state = ParticipantQueued
		default:
			state = ParticipantIdle
		}
	} else {
		e.pstate[id] = want
	}
	_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: id, State: state})
	})
}

// sortedHolds returns the pool's holds, oldest first. e.mu is held.
func (e *Engine) sortedHolds(pool string) []*hold {
	var out []*hold
	for _, h := range e.holds {
		for _, p := range h.appliedPools {
			if p == pool {
				out = append(out, h)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].since.Equal(out[j].since) {
			return out[i].since.Before(out[j].since)
		}
		return out[i].attemptID < out[j].attemptID
	})
	return out
}

// Cancel ends the turn as cancelled at any phase. It is idempotent on a
// cancelled turn.
func (e *Engine) Cancel(turnID string) error {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return errors.New("engine: not started")
	}
	turn, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		e.mu.Unlock()
		missing := refusal(wire.ErrorCodeNotFound)
		missing.TurnID = strPtr(turnID)
		return missing
	case err != nil:
		e.mu.Unlock()
		return err
	case turn.TerminalState == StateCancelled:
		e.mu.Unlock()
		return nil
	case turn.TerminalState != "" || e.open == nil || e.open.id != turnID:
		e.mu.Unlock()
		closed := refusal(wire.ErrorCodeNotOpen)
		closed.TurnID = strPtr(turnID)
		if turn.TerminalState != "" {
			closed.Detail = strPtr(turn.TerminalState)
		}
		return closed
	}
	var failure error
	term := e.endLocked(e.open, StateCancelled, reasonCancelled, nil)
	if term == nil {
		failure = errors.New("engine: cancel: terminal transaction failed")
	}
	e.mu.Unlock()
	if failure != nil {
		return failure
	}
	return e.cancelAttempts(term.holds)
}

// Release releases an uncertain-capacity hold at the operator's request.
func (e *Engine) Release(req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return wire.ReleaseReply{}, errors.New("engine: not started")
	}
	if req.Reason == "" {
		return wire.ReleaseReply{}, refusal(wire.ErrorCodeReasonRequired)
	}

	// First try to find in regular holds
	holds := e.sortedHolds(req.Pool)
	var target *hold
	if req.AttemptID != nil {
		for _, h := range holds {
			if h.attemptID == *req.AttemptID {
				target = h
				break
			}
		}
		if target == nil {
			// Try orphaned holds with the pool name
			if orphanedHold, ok := e.orphanedHolds[*req.AttemptID]; ok && orphanedHold.pool == req.Pool {
				target = orphanedHold
			}
		}
		if target == nil {
			missing := refusal(wire.ErrorCodeNoSuchHold)
			missing.Pool = strPtr(req.Pool)
			missing.AttemptID = strPtr(*req.AttemptID)
			return wire.ReleaseReply{}, missing
		}
	} else {
		switch len(holds) {
		case 0:
			none := refusal(wire.ErrorCodeNoHold)
			none.Pool = strPtr(req.Pool)
			return wire.ReleaseReply{}, none
		case 1:
			target = holds[0]
		default:
			ids := make([]string, len(holds))
			for i, h := range holds {
				ids[i] = h.attemptID
			}
			body, err := json.Marshal(ids)
			if err != nil {
				return wire.ReleaseReply{}, err
			}
			ambiguous := refusal(wire.ErrorCodeAmbiguous)
			ambiguous.Pool = strPtr(req.Pool)
			ambiguous.Holds = body
			return wire.ReleaseReply{}, ambiguous
		}
	}

	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return releaseHoldIn(tx, emit, target, releaseOperator, req.Reason)
	})
	if err != nil {
		return wire.ReleaseReply{}, err
	}

	// Remove from orphaned holds if it was there
	if _, wasOrphaned := e.orphanedHolds[target.attemptID]; wasOrphaned {
		delete(e.orphanedHolds, target.attemptID)
	} else {
		// Release frees all pools the hold applies to
		e.dropHold(target)
	}
	e.syncParticipant(target.participant)
	return wire.ReleaseReply{Released: []string{target.attemptID}}, nil
}
