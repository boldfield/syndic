package engine

import (
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Terminal reasons of a deliberate turn (v8 delta section 2, "Failure").
const (
	reasonProposalFailed = "proposal_failed:"
	// reasonPhaseUnsupported ends a deliberate turn that reaches a phase no
	// task has built yet; it carries the phase's name.
	reasonPhaseUnsupported = "deliberate_phase_unsupported:"
)

// startProposals begins a deliberate turn: it journals proposals_dispatched,
// sets the phase and hands one proposal dispatch per proposer to the
// scheduler, in roster order. Each runs when its pool has room and queues
// otherwise. e.mu is held.
func (e *Engine) startProposals(t *openTurn) {
	t.proposers = t.eff.Preset.DeliberateProposers()
	t.proposals = make([]string, len(t.proposers))
	t.proposalAttempts = map[string]string{}
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		if err := tx.SetDeliberationPhase(t.id, journal.PhasePropose); err != nil {
			return err
		}
		return tx.EmitDeliberationEvent(t.id, journal.EventProposalsDispatched)
	})
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonProposalFailed+t.proposers[0]+":error")
		return
	}
	for i, id := range t.proposers {
		if e.open != t {
			return
		}
		if err := e.schedule(t, id, func() { e.startProposer(t, i) }); err != nil {
			e.finishLocked(t, StateIncomplete, reasonProposalFailed+id)
			return
		}
	}
}

// startProposer begins the proposer's attempt and calls the adapter on its
// own goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startProposer(t *openTurn, i int) {
	id := t.proposers[i]
	req, err := e.dispatchProposer(t, id)
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
		} else {
			e.finishLocked(t, StateIncomplete, reasonProposalFailed+id)
		}
		e.releaseLocked(id)
		return
	}
	go e.runProposer(t, i, req)
}

// runProposer calls the proposer, handles its result and gives back the pool
// slot.
func (e *Engine) runProposer(t *openTurn, i int, req transport.CompleteRequest) {
	res, fail := e.complete(t, t.proposers[i], req)
	e.completeProposal(t, i, req, res, fail)
	e.release(t.proposers[i])
}

// dispatchProposer records the request message and the proposer's attempt,
// reserves its budget and emits its state, in one transaction. The request is
// the proposal prompt and offers no tool definitions. e.mu is held.
func (e *Engine) dispatchProposer(t *openTurn, id string) (transport.CompleteRequest, error) {
	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.ProposerMaxOutputTokens, e.built.Participants[id].OutputBound)
	deadline := e.attemptDeadline(t)
	msgs := prompt.Proposal(t.history, t.input)

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		estimated, err := e.reserve(tx, t, id, msgs, nil, maxOut)
		if err != nil {
			return err
		}
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseProposal, senderUser, []string{id}, []byte(t.input), nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseProposal,
			Sender:  senderUser,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           id,
			Phase:                 phaseProposal,
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
			Participant:           id,
			Phase:                 phaseProposal,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: id,
			State:       ParticipantRunning,
		}); err != nil {
			return err
		}
		req = transport.CompleteRequest{
			Envelope: transport.AttemptEnvelope{
				Participant: id,
				TurnID:      t.id,
				Phase:       phaseProposal,
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
		return transport.CompleteRequest{}, err
	}
	t.proposalAttempts[attemptID] = id
	e.markRunning(id)
	e.armAttempt(t, attemptID, deadline)
	return req, nil
}

// completeProposal runs accept and gate for a proposer's result and records
// its text as the proposal. The first reply that carries no tool call
// completes it: no verdict parser runs on it, no envelope is expected and
// there is no repair. A result that fails acceptance or the gate, does not
// complete or carries a tool call ends the turn proposal_failed. When every
// proposal is recorded, proposals_complete is journaled.
func (e *Engine) completeProposal(t *openTurn, i int, req transport.CompleteRequest, res transport.CompleteResult, fail *failure) {
	e.mu.Lock()
	defer e.mu.Unlock()

	id := t.proposers[i]
	attemptID := req.Envelope.AttemptID
	gate := transport.CheckResult(req, res, e.built.Participants[id].Expected)
	// The request offered no tool definitions, so a call in the reply is not
	// a completion.
	failed := gate != nil || fail != nil || transport.IsToolStep(res)

	var quarantined, exceeded, complete bool
	var term *terminal
	var held *hold
	var endReason string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, exceeded, complete, term, held, endReason = false, false, false, nil, nil, ""
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			if exceeded, err = e.settleQuarantined(tx, emit, t.id, attemptID, res); err != nil {
				return err
			}
			return emitCompleted(emit, t.id, attemptID, res, nil)
		}
		if exceeded, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
		}
		if err := recordCompletion(tx, emit, t.id, id, attemptID, res, gate, fail, e.lastInflight(id)); err != nil {
			return err
		}
		if held, err = e.failedIn(tx, emit, id, attemptID, fail); err != nil {
			return err
		}
		if failed {
			endReason = reasonProposalFailed + id
			term, err = e.endIn(tx, emit, t, StateIncomplete, endReason)
			return err
		}
		if err := tx.SetAttemptText(attemptID, res.Text); err != nil {
			return err
		}
		if _, err := tx.RecordProposal(journal.DeliberationItemInput{
			TurnID:         t.id,
			Participant:    id,
			RosterPosition: i,
			AttemptID:      attemptID,
			Label:          fmt.Sprintf("proposal %d", i+1),
			Content:        []byte(res.Text),
		}); err != nil {
			return err
		}
		if t.proposalsDone+1 < len(t.proposers) {
			return nil
		}
		complete = true
		if err := tx.EmitDeliberationEvent(t.id, journal.EventProposalsComplete); err != nil {
			return err
		}
		// The critique phase is built by a later task.
		endReason = reasonPhaseUnsupported + journal.PhaseCritique
		term, err = e.endIn(tx, emit, t, StateIncomplete, endReason)
		return err
	})
	e.attemptDone(t, attemptID)
	if err == nil && exceeded {
		e.markExceeded(id)
	}
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonProposalFailed+id)
		return
	}
	if quarantined {
		e.lateCommitted(attemptID, res)
		return
	}
	e.failedCommitted(id, fail, held)
	if e.lastInflight(id) {
		delete(e.running, id)
	}
	if !failed {
		t.proposals[i] = res.Text
		t.proposalsDone++
	}
	if failed || complete {
		e.commitTerminal(t, term)
		e.launchCancels(term)
	}
}
