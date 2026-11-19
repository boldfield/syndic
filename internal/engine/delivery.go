package engine

import (
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

const phaseDelivery = "terminal_delivery"

// deliveryDispatch is one begun delivery attempt.
type deliveryDispatch struct {
	participant string
	attemptID   string
	req         transport.CompleteRequest
}

// deliveryRecipients gathers all participants who should receive terminal delivery:
// the author and all reviewers who were dispatched, in that order. e.mu is held.
func (e *Engine) deliveryRecipients(t *openTurn, turnRow *journal.TurnRow) ([]string, error) {
	recipients := []string{t.author}
	seen := map[string]bool{t.author: true}

	attempts, err := e.store.ListAttempts(t.id)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		if a.Phase == phaseReview && !seen[a.Participant] {
			recipients = append(recipients, a.Participant)
			seen[a.Participant] = true
		}
	}

	return recipients, nil
}

// reviewerOutputs gathers all reviewer outputs (text + commentary) in dispatch order,
// including repair outputs. e.mu is held.
func (e *Engine) reviewerOutputs(t *openTurn) ([]prompt.ReviewerOutput, error) {
	attempts, err := e.store.ListAttempts(t.id)
	if err != nil {
		return nil, err
	}

	var outputs []prompt.ReviewerOutput
	for _, a := range attempts {
		if a.Phase == phaseReview && a.RawOutput != nil && *a.RawOutput != "" {
			outputs = append(outputs, prompt.ReviewerOutput{
				Participant: a.Participant,
				Output:      *a.RawOutput,
			})
		}
	}
	return outputs, nil
}

// openBlocks gathers all open blocks for the turn. e.mu is held.
func (e *Engine) openBlocks(turnID string) ([]prompt.OpenBlock, error) {
	blocks, err := e.store.OpenBlocks(turnID)
	if err != nil {
		return nil, err
	}

	var out []prompt.OpenBlock
	for _, b := range blocks {
		out = append(out, prompt.OpenBlock{
			BlockID:  b.ID,
			Severity: b.Severity,
			Evidence: b.Evidence,
			Remedy:   b.Remedy,
		})
	}
	return out, nil
}

// runDeliveries hands every terminal delivery to the scheduler, in order. Each
// runs when its pool has room and queues otherwise. e.mu is held.
func (e *Engine) runDeliveries(t *openTurn) error {
	if t.mode == modeSolo || t.candidateHash == "" {
		return nil
	}

	turnRow, err := e.store.GetTurn(t.id)
	if err != nil {
		return err
	}

	if turnRow.TerminalState == StateCancelled ||
		turnRow.TerminalReason == "engine_stop" || turnRow.TerminalReason == "engine_restart" {
		return nil
	}

	recipients, err := e.deliveryRecipients(t, turnRow)
	if err != nil {
		return err
	}

	for _, id := range recipients {
		if err := e.schedule(t, id, func() { e.startDelivery(t, id) }); err != nil {
			return err
		}
	}

	// Emit verdicts_delivered event after all deliveries are queued
	if len(recipients) > 0 {
		err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			return emit(t.id, wire.EventKindVerdictsDelivered, wire.EvVerdictsDelivered{
				TurnID:     t.id,
				Recipients: recipients,
			})
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// startDelivery begins the delivery attempt and calls the adapter on its own
// goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startDelivery(t *openTurn, id string) {
	d, err := e.beginDelivery(t, id)
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		e.releaseLocked(id)
		return
	}
	go e.runDelivery(t, d)
}

// runDelivery calls the adapter for a delivery and handles its result.
func (e *Engine) runDelivery(t *openTurn, d deliveryDispatch) {
	res, fail := e.complete(t, d.participant, d.req)
	e.completeDelivery(t, d.attemptID, d.participant, res, fail)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.releaseLocked(d.participant)
}

// beginDelivery creates a delivery attempt within a transaction. e.mu is held.
func (e *Engine) beginDelivery(t *openTurn, id string) (deliveryDispatch, error) {
	turnRow, err := e.store.GetTurn(t.id)
	if err != nil {
		return deliveryDispatch{}, err
	}

	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.DeliveryMaxTokens, e.built.Participants[id].OutputBound)
	deadline := e.clock.Now().UTC().Add(t.eff.Deadlines.Delivery.Duration)

	reviewerOuts, err := e.reviewerOutputs(t)
	if err != nil {
		return deliveryDispatch{}, err
	}

	blocks, err := e.openBlocks(t.id)
	if err != nil {
		return deliveryDispatch{}, err
	}

	deliveryInput := prompt.DeliveryInput{
		TerminalState:   turnRow.TerminalState,
		TerminalReason:  turnRow.TerminalReason,
		CandidateHash:   turnRow.CandidateID,
		ReviewerOutputs: reviewerOuts,
		OpenBlocks:      blocks,
	}

	msgs := prompt.Delivery(deliveryInput)

	var d deliveryDispatch
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}

		attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           id,
			Phase:                 phaseDelivery,
			Epoch:                 turn.Epoch,
			Deadline:              &deadline,
			MaxOutputTokens:       maxOut,
			EstimatedPromptTokens: 0,
		})
		if err != nil {
			return err
		}

		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           id,
			Phase:                 phaseDelivery,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: 0,
		}); err != nil {
			return err
		}

		d = deliveryDispatch{
			participant: id,
			attemptID:   attemptID,
			req: transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant: id,
					TurnID:      t.id,
					Phase:       phaseDelivery,
					Epoch:       turn.Epoch,
					AttemptID:   attemptID,
				},
				Model:           p.Model,
				Messages:        msgs,
				MaxOutputTokens: maxOut,
				Deadline:        deadline,
			},
		}
		return nil
	})
	if err != nil {
		return deliveryDispatch{}, err
	}

	e.markRunning(id)
	e.armAttempt(t, d.attemptID, d.req.Deadline)
	return d, nil
}

// completeDelivery handles the result of a delivery attempt. The result is
// never parsed and delivery failures are recorded but not retried. e.mu is NOT
// held; acquire it for the transaction.
func (e *Engine) completeDelivery(t *openTurn, attemptID, participant string, res transport.CompleteResult, fail *failure) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var quarantined bool
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined = false
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}

		// Accept result against the terminal epoch and state
		accepted, err := tx.AcceptResult(attemptID, turn.Epoch, turn.TerminalState, outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			_, err := e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}

		// Record the completion with the appropriate status
		status := "complete"
		if fail != nil {
			status = "error"
		}
		if res.Status == transport.StatusInterrupted {
			status = "interrupted"
		}

		if _, err := e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
		}

		return tx.CompleteAttempt(attemptID, res.Text, 0, "", status, "", res.TransportStatus)
	})
	e.attemptDone(t, attemptID)
	if err == nil && quarantined {
		e.lateCommitted(attemptID, res)
		return
	}
	if err != nil {
		return
	}
	if e.lastInflight(participant) {
		delete(e.running, participant)
	}
	e.syncParticipant(participant)
}
