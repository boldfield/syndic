package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// runTurn hands the turn's author dispatch to the scheduler. The dispatch
// runs at once when the author's pool has a free slot and queues otherwise.
func (e *Engine) runTurn(t *openTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		return
	}
	if err := e.schedule(t, t.author, func() { e.startAuthor(t) }); err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
	}
}

// startAuthor begins the author's attempt and calls the adapter on its own
// goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startAuthor(t *openTurn) {
	req, attemptID, err := e.dispatchAuthor(t)
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
			e.releaseLocked(t.author)
			return
		}
		// Nothing was written for the dispatch; end the turn so the engine
		// does not stay busy.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		e.releaseLocked(t.author)
		return
	}
	go e.runAuthor(t, req, attemptID)
}

// runAuthor calls the author, handles its result and gives back the pool slot
// before anything else is dispatched.
func (e *Engine) runAuthor(t *openTurn, req transport.CompleteRequest, attemptID string) {
	res, goErr := e.completeWithRetry(t.author, req, attemptID, t.id, 0, "author")
	if t.mode != modeSolo {
		proceed := e.completeAuthorReview(t, attemptID, req, res, goErr)
		e.release(t.author)
		if proceed {
			e.runReviewers(t)
		}
		return
	}
	e.completeAuthor(t, attemptID, req, res, goErr)
	e.release(t.author)
}

// complete calls the participant's adapter and returns both result and error.
// The original error is preserved for dispatch failure mapping.
func (e *Engine) complete(participant string, req transport.CompleteRequest) (transport.CompleteResult, error) {
	res, err := e.adapterFor(participant).Complete(context.Background(), req)
	e.returned(req.Envelope.AttemptID)
	return res, err
}

// returned records that the attempt's Complete call has returned. A hold on
// the attempt then occupies a slot of its own.
func (e *Engine) returned(attemptID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.calling, attemptID)
	if h := e.holds[attemptID]; h != nil {
		h.calling = false
	}
}

func (e *Engine) adapterFor(id string) transport.Adapter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.adapters[id]
}

// dispatchAuthor records the request message and the attempt, and moves the
// turn to author_dispatched, all in one transaction. e.mu is held.
func (e *Engine) dispatchAuthor(t *openTurn) (transport.CompleteRequest, string, error) {
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == t.author {
			p = candidate
			break
		}
	}

	msgs := prompt.Author(t.input)
	maxOut := budget.ClampOutput(*t.eff.Budgets.AuthorMaxOutputTokens, e.built.Participants[t.author].OutputBound)
	deadline := e.attemptDeadline(t)

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		estimated, err := e.reserve(tx, t, t.author, msgs, maxOut)
		if err != nil {
			return err
		}
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseAuthor, senderUser, []string{t.author}, []byte(t.input), nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseAuthor,
			Sender:  senderUser,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           t.author,
			Phase:                 phaseAuthor,
			Epoch:                 turn.Epoch,
			Deadline:              &deadline,
			MaxOutputTokens:       maxOut,
			EstimatedPromptTokens: estimated,
		})
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           t.author,
			Phase:                 phaseAuthor,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantRunning,
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: StateAuthorDispatched}); err != nil {
			return err
		}
		req = transport.CompleteRequest{
			Envelope: transport.AttemptEnvelope{
				Participant: t.author,
				TurnID:      t.id,
				Phase:       phaseAuthor,
				Epoch:       turn.Epoch,
				AttemptID:   attemptID,
			},
			Model:           p.Model,
			Messages:        msgs,
			MaxOutputTokens: maxOut,
			Deadline:        deadline,
		}
		return nil
	})
	if err != nil {
		return transport.CompleteRequest{}, "", err
	}
	e.running[t.author] = true
	e.armAttempt(t, attemptID, deadline)
	return req, attemptID, nil
}

// completeAuthor runs accept, gate and the terminal path for the author's
// result in one transaction.
func (e *Engine) completeAuthor(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult, goErr error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	// Check for transport failures or Go errors that might need a hold
	var openHold bool
	var newStatus, terminalReason string
	var retryCount int
	if goErr != nil || (res.TransportStatus != "" && res.TransportStatus != transport.TransportStatusOK) {
		// Get the retry count to determine if this is a retry
		if attempt, err := e.getAttempt(attemptID); err == nil && attempt != nil && attempt.RetryCount != nil {
			retryCount = *attempt.RetryCount
		}
		_, openHold, newStatus, terminalReason = decideDispatchFailure("author", t.author, retryCount, res.TransportStatus, goErr)
	}

	// Use newStatus if set, otherwise use the result's status
	finalStatus := string(res.Status)
	if newStatus != "" {
		finalStatus = newStatus
	}

	// Determine if we should update participant state based on the failure type
	participantState := ""
	var participantDetail *string
	if res.TransportStatus == transport.TransportStatusUnreachable && retryCount > 0 {
		participantState = ParticipantUnreachable
	} else if res.TransportStatus == transport.TransportStatusUnauthorized {
		participantState = ParticipantUnauthorized
	} else if res.TransportStatus == transport.TransportStatusModelUnavailable {
		participantState = ParticipantUnavailable
	}

	quarantined, exceeded := false, false
	var term *terminal
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, exceeded, term = false, false, nil
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", journal.AttemptOutput{
			RawScrubbed:     res.RawOutput,
			ScrubCount:      res.ScrubCount,
			Identity:        identityString(res.Identity),
			Status:          finalStatus,
			NativeFinish:    res.NativeFinish,
			TransportStatus: res.TransportStatus,
		})
		if err != nil {
			return err
		}
		if !accepted {
			// A late result for a superseded epoch or a terminal turn is
			// quarantined and nothing further happens.
			quarantined = true
			exceeded, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}

		var usage *wire.ReportedUsage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
			promptTokens, completionTokens := int64(res.Usage.Prompt), int64(res.Usage.Completion)
			usage = &wire.ReportedUsage{PromptTokens: &promptTokens, CompletionTokens: &completionTokens}
		}
		if exceeded, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
		}

		// Open hold if needed
		if openHold {
			p := e.participantConfig(t.author)
			pool := poolName(p)
			group := e.poolGroup(pool)
			members := make([]journal.GroupMember, len(group))
			for i, m := range group {
				members[i] = journal.GroupMember{EndpointName: m.EndpointName, Model: m.Model}
			}
			now := e.clock.Now().UTC()
			if err := tx.OpenHold(journal.Hold{
				AttemptID:    attemptID,
				Pool:         pool,
				EndpointName: p.Endpoint,
				Model:        p.Model,
				Group:        members,
				HeldSince:    now,
			}); err != nil {
				return err
			}
		}

		if gate != nil {
			if err := tx.SetAttemptGate(attemptID, gate.Error()); err != nil {
				return err
			}
		}
		if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          finalStatus,
			ScrubCount:      res.ScrubCount,
			TransportStatus: res.TransportStatus,
			Usage:           usage,
		}); err != nil {
			return err
		}
		if e.lastInflight(t.author) {
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
				Participant: t.author,
				State:       ParticipantIdle,
			}); err != nil {
				return err
			}
		}

		// Emit participant state if needed
		if participantState != "" {
			detail := ""
			if participantDetail != nil {
				detail = *participantDetail
			}
			if err := e.emitParticipantState(emit, t.author, participantState, detail); err != nil {
				return err
			}
		}

		// Determine final turn state
		if terminalReason != "" {
			term, err = e.endIn(tx, emit, t, StateIncomplete, reasonAuthorFailed+terminalReason)
		} else if gate == nil {
			term, err = e.endIn(tx, emit, t, StateSoloComplete, reasonSoloOK)
		} else {
			term, err = e.endIn(tx, emit, t, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
		}
		return err
	})
	e.attemptDone(t, attemptID)
	if err == nil && exceeded {
		e.markExceeded(t.author)
	}
	if quarantined {
		if err == nil {
			e.lateCommitted(attemptID, res)
		}
		return
	}
	if err != nil {
		// The transaction left no durable change. End the turn on its own so
		// the engine does not stay busy; if that fails too, the turn stays
		// open in memory exactly as it is in the journal.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}
	if e.lastInflight(t.author) {
		delete(e.running, t.author)
	}
	e.commitTerminal(t, term)
	e.launchCancels(term)
}

// authorFailedStatus follows the design's "Terminal reasons" rule: the
// transport status when it is not ok, else the completion status when it is
// not complete, else the gate error.
func authorFailedStatus(res transport.CompleteResult, gate error) string {
	if res.TransportStatus != "" && res.TransportStatus != transport.TransportStatusOK {
		return res.TransportStatus
	}
	if res.Status != transport.StatusComplete {
		return string(res.Status)
	}
	return fmt.Sprintf("gate:%v", gate)
}

func identityString(id transport.ModelIdentity) string {
	if id.Value == "" {
		return id.Kind
	}
	return id.Kind + ":" + id.Value
}

// getAttempt retrieves an attempt from the store.
func (e *Engine) getAttempt(attemptID string) (*journal.AttemptRow, error) {
	return e.store.GetAttempt(attemptID)
}

// completeWithRetry handles a Complete call with retry logic for unreachable failures.
// It returns the final result and any Go error after retries.
func (e *Engine) completeWithRetry(participant string, req transport.CompleteRequest, attemptID, turnID string, retryCount int, role string) (transport.CompleteResult, error) {
	res, goErr := e.complete(participant, req)

	shouldRetry, _, _, _ := decideDispatchFailure(role, participant, retryCount, res.TransportStatus, goErr)

	// Handle retry for unreachable
	if shouldRetry && res.TransportStatus == transport.TransportStatusUnreachable {
		// Record the retry and check deadlines
		retryable := false
		now := e.clock.Now()
		err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			attempt, err := tx.GetAttempt(attemptID)
			if err != nil {
				return err
			}
			turn, err := tx.GetTurn(turnID)
			if err != nil {
				return err
			}
			// Check if turn is still open and both deadlines allow retry
			if turn.TerminalState != "" {
				return nil
			}
			if attempt.Deadline != nil && now.Add(dispatchRetryDelay).After(*attempt.Deadline) {
				return nil
			}
			if turn.TurnDeadline != nil && now.Add(dispatchRetryDelay).After(*turn.TurnDeadline) {
				return nil
			}
			retryable = true
			_, err = tx.RecordRetryDispatch(attemptID)
			return err
		})
		if err == nil && retryable {
			// Wait for the retry delay using the clock
			if dispatchRetryDelay > 0 {
				done := make(chan struct{})
				e.clock.AfterFunc(dispatchRetryDelay, func() { close(done) })
				<-done
			}
			// Re-gate the retry: check again that turn and deadlines still allow it
			regatePassed := false
			err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				attempt, err := tx.GetAttempt(attemptID)
				if err != nil {
					return err
				}
				turn, err := tx.GetTurn(turnID)
				if err != nil {
					return err
				}
				// Check again if turn is still open and both deadlines still allow retry
				if turn.TerminalState != "" {
					return nil
				}
				now := e.clock.Now()
				if attempt.Deadline != nil && now.After(*attempt.Deadline) {
					return nil
				}
				if turn.TurnDeadline != nil && now.After(*turn.TurnDeadline) {
					return nil
				}
				regatePassed = true
				return nil
			})
			if err == nil && regatePassed {
				// Retry the dispatch
				return e.completeWithRetry(participant, req, attemptID, turnID, retryCount+1, role)
			}
		}
	}

	return res, goErr
}
