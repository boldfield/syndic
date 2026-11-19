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

const (
	phaseDelivery = "terminal_delivery"
	phaseRepair   = "repair"
)

// deliveryDispatch is one begun delivery attempt.
type deliveryDispatch struct {
	participant string
	attemptID   string
	req         transport.CompleteRequest
}

// deliveryRecipients gathers all participants who should receive terminal delivery:
// the author and all reviewers who were dispatched, in that order. e.mu is held.
func (e *Engine) deliveryRecipients(t *openTurn) ([]string, error) {
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
		// Include both review and repair attempts
		if (a.Phase == phaseReview || a.Phase == phaseRepair) && a.RawOutput != nil && *a.RawOutput != "" {
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

	recipients, err := e.deliveryRecipients(t)
	if err != nil {
		return err
	}

	for i, id := range recipients {
		// Mark the first delivery so it emits verdicts_delivered
		isFirst := i == 0
		if err := e.schedule(t, id, func() { e.startDelivery(t, id, isFirst, recipients) }); err != nil {
			return err
		}
	}

	return nil
}

// startDelivery begins the delivery attempt and calls the adapter on its own
// goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startDelivery(t *openTurn, id string, emitVerdictsDelivered bool, recipients []string) {
	d, notSent, err := e.beginDelivery(t, id, emitVerdictsDelivered, recipients)
	if notSent {
		// Budget refusal: delivery was recorded but not sent
		e.releaseLocked(id)
		return
	}
	if err != nil {
		// Error recording the attempt
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
// It performs the budget check and records not_sent:budget if the delivery does not fit.
// If emitVerdictsDelivered is true, it also emits the verdicts_delivered event.
// Returns (dispatch, notSent, error). notSent indicates the attempt was recorded as not_sent:budget.
func (e *Engine) beginDelivery(t *openTurn, id string, emitVerdictsDelivered bool, recipients []string) (deliveryDispatch, bool, error) {
	turnRow, err := e.store.GetTurn(t.id)
	if err != nil {
		return deliveryDispatch{}, false, err
	}

	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.DeliveryMaxTokens, e.built.Participants[id].OutputBound)
	deadline := e.clock.Now().UTC().Add(t.eff.Deadlines.Delivery.Duration)

	reviewerOuts, err := e.reviewerOutputs(t)
	if err != nil {
		return deliveryDispatch{}, false, err
	}

	blocks, err := e.openBlocks(t.id)
	if err != nil {
		return deliveryDispatch{}, false, err
	}

	deliveryInput := prompt.DeliveryInput{
		TerminalState:   turnRow.TerminalState,
		TerminalReason:  turnRow.TerminalReason,
		CandidateHash:   t.candidateHash,
		ReviewerOutputs: reviewerOuts,
		OpenBlocks:      blocks,
	}

	msgs := prompt.Delivery(deliveryInput)

	var d deliveryDispatch
	var notSent bool
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		notSent = false
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}

		// Emit verdicts_delivered on the first delivery
		if emitVerdictsDelivered && len(recipients) > 0 {
			if err := emit(t.id, wire.EventKindVerdictsDelivered, wire.EvVerdictsDelivered{
				TurnID:     t.id,
				Recipients: recipients,
			}); err != nil {
				return err
			}
		}

		// Check if turn budget is exhausted
		budgetRec, hasBudget, err := readBudget(turn)
		if err != nil {
			return err
		}
		if hasBudget && budgetRec.Exhausted {
			// Turn budget exhausted: record delivery as not_sent:budget without checking context
			attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
				TurnID:      t.id,
				Participant: id,
				Phase:       phaseDelivery,
				Epoch:       turn.Epoch,
			})
			if err != nil {
				return err
			}
			if err := tx.CompleteAttempt(attemptID, "", 0, "", "error", "", "not_sent:budget"); err != nil {
				return err
			}
			notSent = true
			return nil
		}

		// Check budget constraints within the transaction
		pb, missing := e.budgetFor(t.eff, id)
		if missing != "" {
			// Missing budget constraint: record as not_sent:budget
			attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
				TurnID:      t.id,
				Participant: id,
				Phase:       phaseDelivery,
				Epoch:       turn.Epoch,
			})
			if err != nil {
				return err
			}
			if err := tx.CompleteAttempt(attemptID, "", 0, "", "error", "", "not_sent:budget"); err != nil {
				return err
			}
			notSent = true
			return nil
		}

		// Estimate the payload size and check context bound
		estimatedPromptTokens, err := e.estimateDelivery(msgs, pb)
		if err != nil {
			return err
		}

		admissionTokens := budget.AdmissionTokens(msgs, pb.tokensPerByteMax, pb.overheads)
		usableBound := int(float64(pb.contextBound) * (1 - pb.margin))

		// Check if delivery fits in recipient's context
		if admissionTokens+maxOut > usableBound {
			// Does not fit: record as not_sent:budget
			attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
				TurnID:      t.id,
				Participant: id,
				Phase:       phaseDelivery,
				Epoch:       turn.Epoch,
			})
			if err != nil {
				return err
			}
			if err := tx.CompleteAttempt(attemptID, "", 0, "", "error", "", "not_sent:budget"); err != nil {
				return err
			}
			notSent = true
			return nil
		}

		// Record the attempt for sending
		attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           id,
			Phase:                 phaseDelivery,
			Epoch:                 turn.Epoch,
			Deadline:              &deadline,
			MaxOutputTokens:       maxOut,
			EstimatedPromptTokens: estimatedPromptTokens,
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
			EstimatedPromptTokens: int64(estimatedPromptTokens),
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
		return deliveryDispatch{}, notSent, err
	}

	if notSent {
		return deliveryDispatch{}, true, nil
	}

	e.markRunning(id)
	e.armAttempt(t, d.attemptID, d.req.Deadline)
	return d, false, nil
}

// estimateDelivery estimates the prompt tokens for a delivery payload.
func (e *Engine) estimateDelivery(msgs []transport.Message, pb partBudget) (int, error) {
	var totalBytes int
	for _, msg := range msgs {
		totalBytes += len(msg.Content)
	}
	estimated := int(float64(totalBytes)/pb.bytesPerToken) + pb.overheads.RequestTokens
	if estimated < 0 {
		estimated = 0
	}
	return estimated, nil
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

		// Determine status and transport status
		status, transportStatus := e.deliveryStatus(res, fail)

		// Charge usage to the turn budget (for accounting purposes)
		if _, err := e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
		}

		// Record the completion without retries or holds
		return tx.CompleteAttempt(attemptID, res.Text, 0, "", status, "", transportStatus)
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

// deliveryStatus determines the status and transport status for a delivery result.
func (e *Engine) deliveryStatus(res transport.CompleteResult, fail *failure) (status, transportStatus string) {
	if fail != nil {
		status = string(fail.attemptStatus)
		transportStatus = fail.transportStatus
		return
	}

	status = "complete"
	if res.Status == transport.StatusInterrupted {
		status = "interrupted"
	}
	transportStatus = res.TransportStatus
	return
}
