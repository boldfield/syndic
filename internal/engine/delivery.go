package engine

import (
	"sort"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

const phaseDelivery = "terminal_delivery"

// computePayloadTokens estimates tokens for a payload using the given overheads.
func computePayloadTokens(msgs []transport.Message, msgOverhead, reqOverhead *int) int {
	total := *reqOverhead
	for _, m := range msgs {
		total += len(m.Content) + *msgOverhead
	}
	return total
}

// canFitContext reports whether a payload with output fits the context bound.
func canFitContext(payloadTokens, maxOutput int, contextBound *int, margin *float64) bool {
	if contextBound == nil {
		return true
	}
	usable := int(float64(*contextBound) * (1 - *margin))
	return payloadTokens+maxOutput <= usable
}

// deliveryRecipients gathers all participants who should receive terminal delivery:
// the author and all reviewers who were dispatched. e.mu is held.
func (e *Engine) deliveryRecipients(t *openTurn, turnRow *journal.TurnRow) ([]string, error) {
	recipients := map[string]bool{t.author: true}

	attempts, err := e.store.ListAttempts(t.id)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		if a.Phase == phaseReview {
			recipients[a.Participant] = true
		}
	}

	var list []string
	for id := range recipients {
		list = append(list, id)
	}
	sort.Strings(list)
	return list, nil
}

// reviewerOutputs gathers all reviewer outputs (text + commentary) in dispatch order.
// e.mu is held.
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

// dispatchDeliveries sends terminal delivery to all turn participants. Called
// after the turn's terminal transaction has committed. e.mu is held.
func (e *Engine) dispatchDeliveries(t *openTurn) error {
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

	reviewerOuts, err := e.reviewerOutputs(t)
	if err != nil {
		return err
	}

	blocks, err := e.openBlocks(t.id)
	if err != nil {
		return err
	}

	deliveryInput := prompt.DeliveryInput{
		TerminalState:   turnRow.TerminalState,
		TerminalReason:  turnRow.TerminalReason,
		CandidateHash:   turnRow.CandidateID,
		ReviewerOutputs: reviewerOuts,
		OpenBlocks:      blocks,
	}

	var attemptByParticipant map[string]string = make(map[string]string)

	for _, participant := range recipients {
		msgs := prompt.Delivery(deliveryInput)
		maxOut := budget.ClampOutput(*t.eff.Budgets.DeliveryMaxTokens, e.built.Participants[participant].OutputBound)
		deadline := e.clock.Now().UTC().Add(t.eff.Deadlines.Delivery.Duration)

		payloadTokens := computePayloadTokens(msgs, t.eff.Budgets.MessageOverheadTokens, t.eff.Budgets.RequestOverheadTokens)
		if !canFitContext(payloadTokens, maxOut, e.built.Participants[participant].ContextBound, t.eff.Budgets.ContextMargin) {
			var attemptID string
			err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				turn, err := tx.GetTurn(t.id)
				if err != nil {
					return err
				}

				var txErr error
				attemptID, txErr = tx.BeginAttemptWith(journal.AttemptParams{
					TurnID:                t.id,
					Participant:           participant,
					Phase:                 phaseDelivery,
					Epoch:                 turn.Epoch,
					Deadline:              &deadline,
					MaxOutputTokens:       maxOut,
					EstimatedPromptTokens: 0,
				})
				if txErr != nil {
					return txErr
				}
				if err := tx.CompleteAttempt(attemptID, "", 0, "", "error", "", "not_sent:budget"); err != nil {
					return err
				}
				if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
					AttemptID:             attemptID,
					Participant:           participant,
					Phase:                 phaseDelivery,
					Epoch:                 int64(turn.Epoch),
					Deadline:              deadline.Format(time.RFC3339Nano),
					MaxOutputTokens:       int64(maxOut),
					EstimatedPromptTokens: 0,
				}); err != nil {
					return err
				}
				if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
					AttemptID:       attemptID,
					Status:          "error",
					ScrubCount:      0,
					TransportStatus: "not_sent:budget",
				}); err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				return err
			}
			attemptByParticipant[participant] = attemptID
			continue
		}

		var attemptID string
		err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			turn, err := tx.GetTurn(t.id)
			if err != nil {
				return err
			}

			var txErr error
			attemptID, txErr = tx.BeginAttemptWith(journal.AttemptParams{
				TurnID:                t.id,
				Participant:           participant,
				Phase:                 phaseDelivery,
				Epoch:                 turn.Epoch,
				Deadline:              &deadline,
				MaxOutputTokens:       maxOut,
				EstimatedPromptTokens: 0,
			})
			if txErr != nil {
				return txErr
			}
			if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
				AttemptID:             attemptID,
				Participant:           participant,
				Phase:                 phaseDelivery,
				Epoch:                 int64(turn.Epoch),
				Deadline:              deadline.Format(time.RFC3339Nano),
				MaxOutputTokens:       int64(maxOut),
				EstimatedPromptTokens: 0,
			}); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if attemptID != "" {
			attemptByParticipant[participant] = attemptID
		}
	}

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

	for _, participant := range recipients {
		if attemptID, ok := attemptByParticipant[participant]; ok {
			req := transport.CompleteRequest{
				Messages:        prompt.Delivery(deliveryInput),
				MaxOutputTokens: budget.ClampOutput(*t.eff.Budgets.DeliveryMaxTokens, e.built.Participants[participant].OutputBound),
			}

			e.wg.Add(1)
			go func(participant string, attemptID string) {
				defer e.wg.Done()
				res, fail := e.complete(t, participant, req)
				e.completeDelivery(t, attemptID, participant, req, res, fail)
			}(participant, attemptID)
		}
	}

	return nil
}

// completeDelivery handles the result of a delivery attempt. The result is never
// parsed and delivery failures are recorded but not retried.
func (e *Engine) completeDelivery(t *openTurn, attemptID, participant string, req transport.CompleteRequest, res transport.CompleteResult, fail *failure) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var quarantined bool
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined = false
		accepted, err := tx.AcceptResult(attemptID, 0, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			_, err := e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}
		_, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true)
		if err != nil {
			return err
		}

		return tx.CompleteAttempt(attemptID, res.Text, 0, "", "complete", "", res.TransportStatus)
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
