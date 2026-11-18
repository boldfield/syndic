package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// AttemptOutput holds the output of a completed attempt.
type AttemptOutput struct {
	RawScrubbed     string
	ScrubCount      int
	Identity        string
	Status          string
	NativeFinish    string
	TransportStatus string
}

// EndTurn sets the terminal state of a turn with allowed transitions.
// Allowed: any non-terminal → {approved, solo_complete, incomplete, cancelled}
// Also: incomplete → overridden only when candidate_id is set
func (tx *Tx) EndTurn(turnID, state, reason string) error {
	if reason == "" {
		return fmt.Errorf("reason is required")
	}

	var existingState sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT terminal_state FROM turns WHERE id = ?
	`, turnID).Scan(&existingState); err != nil {
		return fmt.Errorf("query turn state: %w", err)
	}

	// Check transition validity
	if existingState.Valid {
		// Already has a terminal state
		if existingState.String == "incomplete" && state == "overridden" {
			// incomplete → overridden is allowed only when candidate_id is set
			var candidateID sql.NullString
			if err := tx.tx.QueryRow(`
				SELECT candidate_id FROM turns WHERE id = ?
			`, turnID).Scan(&candidateID); err != nil {
				return fmt.Errorf("query candidate_id: %w", err)
			}
			if !candidateID.Valid {
				return fmt.Errorf("cannot transition to overridden without a candidate_id")
			}
		} else {
			// All other transitions from terminal states are errors
			return fmt.Errorf("turn already has terminal state: %s", existingState.String)
		}
	} else {
		// From non-terminal, only allow specific states
		allowed := map[string]bool{
			"approved":      true,
			"solo_complete": true,
			"incomplete":    true,
			"cancelled":     true,
		}
		if !allowed[state] {
			return fmt.Errorf("invalid terminal state: %s", state)
		}
	}

	if _, err := tx.tx.Exec(`
		UPDATE turns SET terminal_state = ?, terminal_reason = ? WHERE id = ?
	`, state, reason, turnID); err != nil {
		return fmt.Errorf("update turn state: %w", err)
	}

	return nil
}

// OverrideTurn ends a turn as "overridden" and marks all open blocks as overridden.
// It also emits an override event.
func (tx *Tx) OverrideTurn(turnID, reason string) error {
	if reason == "" {
		return fmt.Errorf("reason is required")
	}

	// Step 1: EndTurn with "overridden" state
	if err := tx.EndTurn(turnID, "overridden", reason); err != nil {
		return err
	}

	// Step 2: Get all open blocks for this turn
	blocks, err := tx.OpenBlocks(turnID)
	if err != nil {
		return fmt.Errorf("get open blocks: %w", err)
	}

	// Step 3: Override each open block
	for _, block := range blocks {
		if err := tx.OverrideBlock(block.ID); err != nil {
			return fmt.Errorf("override block %s: %w", block.ID, err)
		}
	}

	// Step 4: Emit override event
	payload, _ := json.Marshal(map[string]string{
		"turn_id": turnID,
		"reason":  reason,
	})
	if err := tx.AppendEvent("override", payload); err != nil {
		return fmt.Errorf("append override event: %w", err)
	}

	return nil
}

// BumpEpoch increments the turn's epoch and returns the new value.
func (tx *Tx) BumpEpoch(turnID string) (newEpoch int, err error) {
	var currentEpoch int
	if err := tx.tx.QueryRow(`
		SELECT epoch FROM turns WHERE id = ?
	`, turnID).Scan(&currentEpoch); err != nil {
		return 0, fmt.Errorf("query epoch: %w", err)
	}

	newEpoch = currentEpoch + 1

	if _, err := tx.tx.Exec(`
		UPDATE turns SET epoch = ? WHERE id = ?
	`, newEpoch, turnID); err != nil {
		return 0, fmt.Errorf("update epoch: %w", err)
	}

	return newEpoch, nil
}

// FreezeCandidate creates a candidate and sets the turn's candidate_id.
// Returns an error if the turn already has a candidate_id.
func (tx *Tx) FreezeCandidate(turnID, content, hash string) (candidateID string, err error) {
	// Check if turn already has a candidate_id
	var existingCandidateID sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT candidate_id FROM turns WHERE id = ?
	`, turnID).Scan(&existingCandidateID); err != nil {
		return "", fmt.Errorf("query candidate_id: %w", err)
	}

	if existingCandidateID.Valid {
		return "", fmt.Errorf("turn already has a candidate_id")
	}

	// Create the candidate
	candidateID, err = tx.CreateCandidate(turnID, content, hash)
	if err != nil {
		return "", fmt.Errorf("create candidate: %w", err)
	}

	// Set the turn's candidate_id
	if _, err := tx.tx.Exec(`
		UPDATE turns SET candidate_id = ? WHERE id = ?
	`, candidateID, turnID); err != nil {
		return "", fmt.Errorf("update turn candidate_id: %w", err)
	}

	return candidateID, nil
}

// SetPhaseAtCancel stores the phase string on a turn.
func (tx *Tx) SetPhaseAtCancel(turnID, phase string) error {
	if _, err := tx.tx.Exec(`
		UPDATE turns SET phase_at_cancel = ? WHERE id = ?
	`, phase, turnID); err != nil {
		return fmt.Errorf("update phase_at_cancel: %w", err)
	}
	return nil
}

// AcceptResult compares the attempt's epoch and turn's state against expectations.
// If both match, it writes the output as CompleteAttempt and returns true.
// If either fails, it writes as QuarantineLateOutput and returns false.
// Returns an error only for real failures (unknown attempt, invalid state).
func (tx *Tx) AcceptResult(attemptID string, expectedEpoch int, expectedTerminalState string, out AttemptOutput) (accepted bool, err error) {
	// Get the attempt to find its turn
	attempt, err := tx.GetAttempt(attemptID)
	if err != nil {
		return false, fmt.Errorf("get attempt: %w", err)
	}

	// Get the turn's current epoch and terminal_state
	var currentEpoch int
	var terminalState sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT epoch, terminal_state FROM turns WHERE id = ?
	`, attempt.TurnID).Scan(&currentEpoch, &terminalState); err != nil {
		return false, fmt.Errorf("query turn: %w", err)
	}

	// Check if expectations match
	epochMatches := currentEpoch == expectedEpoch
	stateMatches := false

	if expectedTerminalState == "" {
		// When expectedTerminalState is empty, turn's terminal_state must be null
		stateMatches = !terminalState.Valid
	} else {
		// When expectedTerminalState is set, turn's terminal_state must equal it
		stateMatches = terminalState.Valid && terminalState.String == expectedTerminalState
	}

	if epochMatches && stateMatches {
		// Accept the result
		if err := tx.CompleteAttempt(attemptID, out.RawScrubbed, out.ScrubCount, out.Identity, out.Status, out.NativeFinish, out.TransportStatus); err != nil {
			return false, fmt.Errorf("complete attempt: %w", err)
		}
		return true, nil
	} else {
		// Quarantine the result
		if err := tx.QuarantineLateOutput(attemptID, out.RawScrubbed, out.ScrubCount, out.Identity, out.Status, out.NativeFinish, out.TransportStatus); err != nil {
			return false, fmt.Errorf("quarantine late output: %w", err)
		}
		return false, nil
	}
}
