package engine

import (
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// Apply applies a change set to the workspace when the turn is approved or
// overridden. It enforces that the turn is in the right state and has a change
// set, and returns the per-path outcomes.
func (e *Engine) Apply(turnID string) (wire.ApplyReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return wire.ApplyReply{}, errors.New("engine: not started")
	}
	if e.stopping {
		return wire.ApplyReply{}, refusal(wire.ErrorCodeStopping)
	}

	// Check if another operation is running
	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.ApplyReply{}, busy
	}

	row, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		r := refusal(wire.ErrorCodeNotFound)
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	case err != nil:
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: %w", err)
	}

	// Check turn state: must be approved or overridden
	if row.TerminalState != StateApproved && row.TerminalState != StateOverridden {
		r := refusal(wire.ErrorCodeNotOpen)
		r.Detail = strPtr("turn not approved or overridden")
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	}

	// Check that there is a candidate with a change set
	if row.CandidateID == "" {
		r := refusal(wire.ErrorCodeNotOpen)
		r.Detail = strPtr("no candidate")
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	}

	// Get the change set
	changeSetID, err := e.store.GetCandidateChangeSetID(row.CandidateID)
	if err != nil {
		if errors.Is(err, journal.ErrNoChangeSet) {
			r := refusal(wire.ErrorCodeNotOpen)
			r.Detail = strPtr("no change set")
			r.TurnID = strPtr(turnID)
			return wire.ApplyReply{}, r
		}
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: get change set id: %w", err)
	}

	// Get the conversation to find the workspace
	conv, err := e.store.GetConversation(row.ConversationID)
	if err != nil {
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: get conversation: %w", err)
	}

	workspace := ""
	if conv.Workspace != nil {
		workspace = *conv.Workspace
	}

	// Perform the apply
	result, err := e.store.ApplyChangeSet(workspace, changeSetID, row.TerminalState == StateOverridden)
	if err != nil {
		if errors.Is(err, journal.ErrWorkspaceMissing) {
			r := refusal(wire.ErrorCodeNotOpen)
			r.Detail = strPtr("workspace_missing")
			r.TurnID = strPtr(turnID)
			return wire.ApplyReply{}, r
		}
		if be := (*journal.BaseChangedError)(nil); errors.As(err, &be) {
			r := refusal(wire.ErrorCodePolicy)
			r.Detail = strPtr("base_changed")
			r.TurnID = strPtr(turnID)
			return wire.ApplyReply{}, r
		}
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: %w", err)
	}

	// Build the reply with per-path outcomes
	reply := wire.ApplyReply{State: result.State}
	for _, p := range result.Paths {
		outcome := p.Outcome
		if outcome == "" {
			outcome = journal.PathPending
		}
		reply.Paths = append(reply.Paths, wire.ApplyPathReply{
			Path:    p.Path,
			Outcome: outcome,
		})
	}

	return reply, nil
}

// Discard marks a change set as discarded without deleting evidence or rolling
// back any applied files. The per-path outcomes are preserved.
func (e *Engine) Discard(turnID string) (wire.DiscardReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return wire.DiscardReply{}, errors.New("engine: not started")
	}
	if e.stopping {
		return wire.DiscardReply{}, refusal(wire.ErrorCodeStopping)
	}

	// Check if another operation is running
	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.DiscardReply{}, busy
	}

	row, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		r := refusal(wire.ErrorCodeNotFound)
		r.TurnID = strPtr(turnID)
		return wire.DiscardReply{}, r
	case err != nil:
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: %w", err)
	}

	// Check turn state: must be approved or overridden
	if row.TerminalState != StateApproved && row.TerminalState != StateOverridden {
		r := refusal(wire.ErrorCodeNotOpen)
		r.Detail = strPtr("turn not approved or overridden")
		r.TurnID = strPtr(turnID)
		return wire.DiscardReply{}, r
	}

	// Check that there is a candidate with a change set
	if row.CandidateID == "" {
		r := refusal(wire.ErrorCodeNotOpen)
		r.Detail = strPtr("no candidate")
		r.TurnID = strPtr(turnID)
		return wire.DiscardReply{}, r
	}

	// Get the change set
	changeSetID, err := e.store.GetCandidateChangeSetID(row.CandidateID)
	if err != nil {
		if errors.Is(err, journal.ErrNoChangeSet) {
			r := refusal(wire.ErrorCodeNotOpen)
			r.Detail = strPtr("no change set")
			r.TurnID = strPtr(turnID)
			return wire.DiscardReply{}, r
		}
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: get change set id: %w", err)
	}

	// Mark the change set as discarded
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return tx.DiscardChangeSet(changeSetID)
	})
	if err != nil {
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: %w", err)
	}

	// Get the new state
	cs, err := e.store.GetChangeSet(changeSetID)
	if err != nil {
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: get change set: %w", err)
	}

	return wire.DiscardReply{State: cs.State}, nil
}
