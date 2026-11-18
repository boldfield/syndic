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

// runTurn carries a submitted turn to a terminal state. It runs on its own
// goroutine and holds the engine mutex only around journal transactions and
// the in-memory state they change, never across the adapter call.
func (e *Engine) runTurn(t *openTurn) {
	if t.mode != modeSolo {
		// Review mode is the next task; until then it ends right after
		// turn_state{submitted}.
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"not_implemented")
		return
	}

	req, attemptID, err := e.dispatchAuthor(t)
	if err != nil {
		// Nothing was written for the dispatch; end the turn so the engine
		// does not stay busy.
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}

	res, err := e.adapterFor(t.author).Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{
			Envelope:        req.Envelope,
			Status:          transport.StatusError,
			TransportStatus: "adapter_error:" + err.Error(),
		}
	}
	e.completeAuthor(t, attemptID, req, res)
}

func (e *Engine) adapterFor(id string) transport.Adapter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.adapters[id]
}

// dispatchAuthor records the request message and the attempt, and moves the
// turn to author_dispatched, all in one transaction.
func (e *Engine) dispatchAuthor(t *openTurn) (transport.CompleteRequest, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	mp := e.built.Participants[t.author]
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == t.author {
			p = candidate
			break
		}
	}

	msgs := prompt.Author(t.input)
	maxOut := budget.ClampOutput(*t.eff.Budgets.AuthorMaxOutputTokens, mp.OutputBound)
	deadline := e.clock.Now().UTC().Add(t.attemptDeadline)
	if t.turnDeadline.Before(deadline) {
		deadline = t.turnDeadline
	}

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
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
			TurnID:          t.id,
			Participant:     t.author,
			Phase:           phaseAuthor,
			Epoch:           turn.Epoch,
			Deadline:        &deadline,
			MaxOutputTokens: maxOut,
		})
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:       attemptID,
			Participant:     t.author,
			Phase:           phaseAuthor,
			Epoch:           int64(turn.Epoch),
			Deadline:        deadline.Format(time.RFC3339Nano),
			MaxOutputTokens: int64(maxOut),
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
	return req, attemptID, nil
}

// completeAuthor runs accept, gate and the terminal path for the author's
// result in one transaction.
func (e *Engine) completeAuthor(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	quarantined := false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", journal.AttemptOutput{
			RawScrubbed:     res.RawOutput,
			ScrubCount:      res.ScrubCount,
			Identity:        identityString(res.Identity),
			Status:          string(res.Status),
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
			return nil
		}

		var usage *wire.ReportedUsage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
			promptTokens, completionTokens := int64(res.Usage.Prompt), int64(res.Usage.Completion)
			usage = &wire.ReportedUsage{PromptTokens: &promptTokens, CompletionTokens: &completionTokens}
		}
		if gate != nil {
			if err := tx.SetAttemptGate(attemptID, gate.Error()); err != nil {
				return err
			}
		}
		if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          string(res.Status),
			ScrubCount:      res.ScrubCount,
			TransportStatus: res.TransportStatus,
			Usage:           usage,
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantIdle,
		}); err != nil {
			return err
		}
		if gate == nil {
			return endTurnIn(tx, emit, t.id, StateSoloComplete, reasonSoloOK)
		}
		return endTurnIn(tx, emit, t.id, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
	})
	if quarantined {
		return
	}
	if err != nil {
		// The transaction left no durable change. End the turn on its own so
		// the engine does not stay busy; if that fails too, the turn stays
		// open in memory exactly as it is in the journal.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}
	delete(e.running, t.author)
	e.open = nil
}

// finishTurn ends a turn that has no result to record.
func (e *Engine) finishTurn(t *openTurn, state, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.finishLocked(t, state, reason)
}

// finishLocked is finishTurn with e.mu held.
func (e *Engine) finishLocked(t *openTurn, state, reason string) {
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return endTurnIn(tx, emit, t.id, state, reason)
	})
	if err != nil {
		return
	}
	delete(e.running, t.author)
	e.open = nil
}

// endTurnIn is the terminal path: record the state and reason, bump the
// epoch so no later result is accepted, and emit turn_state.
func endTurnIn(tx *journal.Tx, emit hub.Emit, turnID, state, reason string) error {
	if err := tx.EndTurn(turnID, state, reason); err != nil {
		return err
	}
	if _, err := tx.BumpEpoch(turnID); err != nil {
		return err
	}
	return emit(turnID, wire.EventKindTurnState, wire.EvTurnState{TurnID: turnID, State: state, Reason: &reason})
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
