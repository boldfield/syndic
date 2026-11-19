package engine

import (
	"log"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Terminal reasons of a stopping engine. Their turns deliver nothing.
const (
	reasonEngineStop    = "engine_stop"
	reasonEngineRestart = "engine_restart"

	// statusNotSentBudget is the transport status of a delivery the budget
	// refused.
	statusNotSentBudget = statusNotSentPrefix + "budget"

	attemptPending = "pending"
)

// deliveryOff is set by tests only.
var deliveryOff bool

// deliverable reports whether a turn that ended in state with reason sends
// terminal deliveries. The turn must have a frozen candidate (t.candidateHash)
// and be a review turn.
func (e *Engine) deliverable(t *openTurn, state, reason string) bool {
	if e.noDelivery || t.mode == modeSolo || t.candidateHash == "" {
		return false
	}
	if reason == reasonEngineStop || reason == reasonEngineRestart {
		return false
	}
	switch state {
	case StateApproved, StateIncomplete, StateOverridden:
		return true
	}
	return false
}

// deliverTerminal is the one entry point of terminal delivery. Once the turn's
// terminal state has committed it hands one delivery per participant that took
// part to the scheduler, then announces verdicts_delivered. Nothing is sent
// for a turn that is not deliverable. A delivery never changes the turn: if
// the journal cannot be read here nothing is sent, and a failed write loses
// only that delivery's record. e.mu is held.
func (e *Engine) deliverTerminal(t *openTurn, state, reason string) {
	if !e.deliverable(t, state, reason) {
		return
	}
	recipients, in, ok := e.deliveryInput(t, state, reason)
	if !ok {
		return
	}
	msgs := prompt.Delivery(in)
	t.delivering = true
	t.deliveryState = state
	e.deliveries = append(e.deliveries, t)
	for _, id := range recipients {
		// A scheduling failure drops that one delivery and no other.
		if err := e.schedule(t, id, func() { e.startDelivery(t, id, msgs) }); err != nil {
			log.Printf("engine: turn %s: delivery to %s not scheduled: %v", t.id, id, err)
		}
	}
	e.pruneDeliveries()
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit(t.id, wire.EventKindVerdictsDelivered, wire.EvVerdictsDelivered{TurnID: t.id, Recipients: recipients})
	})
	if err != nil {
		log.Printf("engine: turn %s: verdicts_delivered not recorded: %v", t.id, err)
	}
}

// deliveryInput returns the recipients in dispatch order, the author first,
// and what each is sent: the terminal record, each reviewer attempt's output
// in dispatch order, and the turn's open blocks.
func (e *Engine) deliveryInput(t *openTurn, state, reason string) ([]string, prompt.DeliveryInput, bool) {
	attempts, err := e.store.ListAttempts(t.id)
	if err != nil {
		log.Printf("engine: turn %s: no delivery, attempts unreadable: %v", t.id, err)
		return nil, prompt.DeliveryInput{}, false
	}
	blocks, err := e.store.OpenBlocks(t.id)
	if err != nil {
		log.Printf("engine: turn %s: no delivery, open blocks unreadable: %v", t.id, err)
		return nil, prompt.DeliveryInput{}, false
	}
	in := prompt.DeliveryInput{TerminalState: state, TerminalReason: reason, CandidateHash: t.candidateHash}
	seen := map[string]bool{}
	var recipients []string
	for _, a := range attempts {
		if !seen[a.Participant] {
			seen[a.Participant] = true
			recipients = append(recipients, a.Participant)
		}
		if a.Phase != phaseReview {
			continue
		}
		if text, ok := t.outputs[a.ID]; ok {
			in.ReviewerOutputs = append(in.ReviewerOutputs, prompt.ReviewerOutput{Participant: a.Participant, Output: text})
		}
	}
	for _, b := range blocks {
		in.OpenBlocks = append(in.OpenBlocks, prompt.OpenBlock{BlockID: b.ID, Severity: b.Severity, Evidence: b.Evidence, Remedy: b.Remedy})
	}
	return recipients, in, true
}

// startDelivery begins the recipient's delivery attempt and calls the adapter
// on its own goroutine. It runs with e.mu held and the pool slot taken. A
// delivery refused by the budget, or one that cannot be begun, ends here and
// hands the slot back.
func (e *Engine) startDelivery(t *openTurn, id string, msgs []transport.Message) {
	d, sent, err := e.beginDelivery(t, id, msgs)
	if err != nil || !sent {
		e.releaseLocked(id)
		e.pruneDeliveries()
		return
	}
	e.markRunning(id)
	e.armAttemptWith(t, d.attemptID, d.req.Deadline, func() { e.onDeliveryDeadline(t, d) })
	go e.runDelivery(t, d)
}

// beginDelivery records the attempt in one Tx. When the budget refuses it the
// attempt is begun and completed in that same Tx as not_sent:budget and sent
// is false. e.mu is held.
func (e *Engine) beginDelivery(t *openTurn, id string, msgs []transport.Message) (d reviewerDispatch, sent bool, err error) {
	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.DeliveryMaxTokens, e.built.Participants[id].OutputBound)
	// The turn's deadline has passed; the delivery has one of its own.
	deadline := e.clock.Now().UTC().Add(t.eff.Deadlines.Delivery.Duration)

	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		d, sent = reviewerDispatch{}, false
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:          t.id,
			Participant:     id,
			Phase:           phaseDelivery,
			Epoch:           turn.Epoch,
			Deadline:        &deadline,
			MaxOutputTokens: maxOut,
		})
		if err != nil {
			return err
		}
		estimated, err := e.reserve(tx, t, id, msgs, maxOut)
		begun := wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           id,
			Phase:                 phaseDelivery,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		}
		if _, refused := asRefusal(err); refused {
			return e.recordNotSent(tx, emit, t, id, begun)
		}
		if err != nil {
			return err
		}
		if err := tx.SetAttemptEstimate(attemptID, estimated); err != nil {
			return err
		}
		content := []byte(msgs[len(msgs)-1].Content)
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseDelivery, senderEngine, []string{id}, content, nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseDelivery,
			Sender:  senderEngine,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, begun); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: id, State: ParticipantRunning}); err != nil {
			return err
		}
		d = reviewerDispatch{
			participant: id,
			attemptID:   attemptID,
			req: transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant:   id,
					TurnID:        t.id,
					Phase:         phaseDelivery,
					Epoch:         turn.Epoch,
					AttemptID:     attemptID,
					CandidateHash: t.candidateHash,
				},
				Model:           p.Model,
				Messages:        msgs,
				MaxOutputTokens: maxOut,
				Deadline:        deadline,
			},
		}
		sent = true
		return nil
	})
	if err != nil {
		return reviewerDispatch{}, false, err
	}
	return d, sent, nil
}

// recordNotSent completes the attempt just begun in tx as an error the
// adapter never saw, and reports the participant free again. e.mu is held.
func (e *Engine) recordNotSent(tx *journal.Tx, emit hub.Emit, t *openTurn, id string, begun wire.EvAttemptBegun) error {
	if err := tx.CompleteAttempt(begun.AttemptID, "", 0, "", string(transport.StatusError), "", statusNotSentBudget); err != nil {
		return err
	}
	if err := emit(t.id, wire.EventKindAttemptBegun, begun); err != nil {
		return err
	}
	if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
		AttemptID:       begun.AttemptID,
		Status:          string(transport.StatusError),
		TransportStatus: statusNotSentBudget,
	}); err != nil {
		return err
	}
	if !e.lastInflight(id) {
		return nil
	}
	state := e.specialState(id)
	if state == "" {
		state = ParticipantIdle
	}
	return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: id, State: state})
}

// runDelivery calls the recipient once and records what came back. The slot is
// given back when Complete has returned and its result is stored. complete
// retries only for a turn that is still open, so a delivery never is.
func (e *Engine) runDelivery(t *openTurn, d reviewerDispatch) {
	res, fail := e.complete(t, d.participant, d.req)
	e.completeDelivery(t, d, res, fail)
	e.release(d.participant)
}

// completeDelivery stores a delivery's result with its status and usage. The
// result is never parsed or gated. A result for an attempt already ended at its
// deadline is late: it only releases the hold when it shows the server
// finished.
func (e *Engine) completeDelivery(t *openTurn, d reviewerDispatch, res transport.CompleteResult, fail *failure) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var late, quarantined, exceeded bool
	var held *hold
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		late, quarantined, exceeded, held = false, false, false, nil
		attempt, err := tx.GetAttempt(d.attemptID)
		if err != nil {
			return err
		}
		if attempt.State != attemptPending {
			late = true
			return e.releaseLateIn(tx, emit, d.attemptID, res)
		}
		accepted, err := tx.AcceptResult(d.attemptID, d.req.Envelope.Epoch, t.deliveryState, outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			if exceeded, err = e.settleQuarantined(tx, emit, t.id, d.attemptID, res); err != nil {
				return err
			}
			return emitCompleted(emit, t.id, d.attemptID, res, nil)
		}
		if exceeded, err = e.settle(tx, emit, t.id, d.attemptID, res.Usage, true); err != nil {
			return err
		}
		if err := recordCompletion(tx, emit, t.id, d.participant, d.attemptID, res, nil, fail, e.lastInflight(d.participant)); err != nil {
			return err
		}
		held, err = e.failedIn(tx, emit, d.participant, d.attemptID, fail)
		return err
	})
	e.attemptDone(t, d.attemptID)
	e.pruneDeliveries()
	if err != nil {
		// Nothing durable changed; the attempt stays pending in the journal.
		return
	}
	if exceeded {
		e.markExceeded(d.participant)
	}
	if late || quarantined {
		e.lateCommitted(d.attemptID, res)
		return
	}
	e.failedCommitted(d.participant, fail, held)
	if e.lastInflight(d.participant) {
		delete(e.running, d.participant)
	}
}

// onDeliveryDeadline ends a delivery that is still pending at its deadline:
// the attempt is recorded interrupted with a capacity hold, and the adapter is
// asked to cancel it. The turn is already terminal and is not touched: only
// the attempt, its budget settlement and the hold are committed.
func (e *Engine) onDeliveryDeadline(t *openTurn, d reviewerDispatch) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, pending := t.pending[d.attemptID]; !pending {
		return
	}
	res := transport.CompleteResult{Envelope: d.req.Envelope, Status: transport.StatusInterrupted, TransportStatus: transport.TransportStatusTimeout}
	fail := classifyDispatch(res, nil)
	res = fail.applyTo(d.req, res)

	var term *terminal
	var exceeded bool
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		term, exceeded = nil, false
		accepted, err := tx.AcceptResult(d.attemptID, d.req.Envelope.Epoch, t.deliveryState, outputOf(res))
		if err != nil || !accepted {
			// Not accepted means the turn's state moved on and the attempt
			// was quarantined by AcceptResult; nothing more to record.
			return err
		}
		if exceeded, err = e.settle(tx, emit, t.id, d.attemptID, nil, true); err != nil {
			return err
		}
		if err := recordCompletion(tx, emit, t.id, d.participant, d.attemptID, res, nil, fail, false); err != nil {
			return err
		}
		h, err := e.failedIn(tx, emit, d.participant, d.attemptID, fail)
		if err != nil {
			return err
		}
		if err := tx.SetCancellation(d.attemptID, cancellationRequested); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: d.participant, State: ParticipantCancelling}); err != nil {
			return err
		}
		term = &terminal{holds: []*hold{h}}
		return nil
	})
	if err != nil {
		return
	}
	e.attemptDone(t, d.attemptID)
	e.pruneDeliveries()
	if exceeded {
		e.markExceeded(d.participant)
	}
	e.commitTerminal(t, term)
	e.launchCancels(term)
}

// pruneDeliveries forgets the turns whose deliveries have all completed,
// failed or been dropped, so e.deliveries holds only turns with delivery work
// still queued or in flight. e.mu is held.
func (e *Engine) pruneDeliveries() {
	kept := e.deliveries[:0]
	for _, t := range e.deliveries {
		if len(t.pending) > 0 || e.hasQueued(t) {
			kept = append(kept, t)
		}
	}
	for i := len(kept); i < len(e.deliveries); i++ {
		e.deliveries[i] = nil
	}
	e.deliveries = kept
}

// hasQueued reports whether the scheduler still holds work of the turn. e.mu
// is held.
func (e *Engine) hasQueued(t *openTurn) bool {
	for _, p := range e.pools {
		for _, q := range p.queue {
			if q.t == t {
				return true
			}
		}
	}
	return false
}
