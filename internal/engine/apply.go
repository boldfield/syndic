package engine

import (
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// getTurnAndChangeSet validates a turn and retrieves its change set, returning
// the turn row, change set ID, workspace, and any error. Used by both Apply
// and Discard to avoid duplication.
func (e *Engine) getTurnAndChangeSet(turnID string) (*journal.TurnRow, string, string, error) {
	row, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		return nil, "", "", fmt.Errorf("turn not found")
	case err != nil:
		return nil, "", "", fmt.Errorf("get turn: %w", err)
	}

	// Check turn state: must be approved or overridden
	if row.TerminalState != StateApproved && row.TerminalState != StateOverridden {
		return row, "", "", fmt.Errorf("turn not approved or overridden")
	}

	// Check that there is a candidate with a change set
	if row.CandidateID == "" {
		return row, "", "", fmt.Errorf("no candidate")
	}

	// Get the change set
	changeSetID, err := e.store.GetCandidateChangeSetID(row.CandidateID)
	if err != nil {
		if errors.Is(err, journal.ErrNoChangeSet) {
			return row, "", "", fmt.Errorf("no change set")
		}
		return row, "", "", fmt.Errorf("get change set id: %w", err)
	}

	// Get the conversation to find the workspace
	conv, err := e.store.GetConversation(row.ConversationID)
	if err != nil {
		return row, "", "", fmt.Errorf("get conversation: %w", err)
	}

	workspace := ""
	if conv.Workspace != nil {
		workspace = *conv.Workspace
	}

	return row, changeSetID, workspace, nil
}

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

	// Check if a turn or another apply is running
	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.ApplyReply{}, busy
	}
	if e.applyOpen != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.applyOpen.id)
		return wire.ApplyReply{}, busy
	}

	row, changeSetID, workspace, err := e.getTurnAndChangeSet(turnID)
	if err != nil {
		if err.Error() == "turn not found" {
			r := refusal(wire.ErrorCodeNotFound)
			r.TurnID = strPtr(turnID)
			return wire.ApplyReply{}, r
		}
		r := refusal(wire.ErrorCodeNotOpen)
		r.TurnID = strPtr(turnID)
		r.Detail = strPtr(err.Error())
		return wire.ApplyReply{}, r
	}

	// Mark apply as in progress
	e.applyOpen = &openTurn{id: turnID}

	// Perform the apply
	result, err := e.store.ApplyChangeSet(workspace, changeSetID, row.TerminalState == StateOverridden)
	if err != nil {
		e.applyOpen = nil
		if errors.Is(err, journal.ErrWorkspaceMissing) {
			r := refusal(wire.ErrorCodeWorkspaceMissing)
			r.TurnID = strPtr(turnID)
			return wire.ApplyReply{}, r
		}
		if be := (*journal.BaseChangedError)(nil); errors.As(err, &be) {
			r := refusal(wire.ErrorCodeBaseChanged)
			r.TurnID = strPtr(turnID)
			// Include the paths that conflicted
			for _, path := range be.Paths {
				p := path.Path
				r.Paths = append(r.Paths, &p)
			}
			return wire.ApplyReply{}, r
		}
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: %w", err)
	}

	// Clear apply-in-progress marker
	e.applyOpen = nil

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

	// Check if a turn or an apply is running
	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.DiscardReply{}, busy
	}
	if e.applyOpen != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.applyOpen.id)
		return wire.DiscardReply{}, busy
	}

	_, changeSetID, _, err := e.getTurnAndChangeSet(turnID)
	if err != nil {
		if err.Error() == "turn not found" {
			r := refusal(wire.ErrorCodeNotFound)
			r.TurnID = strPtr(turnID)
			return wire.DiscardReply{}, r
		}
		r := refusal(wire.ErrorCodeNotOpen)
		r.TurnID = strPtr(turnID)
		r.Detail = strPtr(err.Error())
		return wire.DiscardReply{}, r
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
