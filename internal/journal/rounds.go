package journal

import (
	"database/sql"
	"errors"
	"fmt"
)

// RoundInfo holds information about a turn's round state.
type RoundInfo struct {
	CurrentRound      int
	MaxRoundsInEffect int
	MaxRoundsCeiling  int
}

// GetRoundInfo retrieves the current round state for a turn.
func (s *Store) GetRoundInfo(turnID string) (*RoundInfo, error) {
	var info *RoundInfo
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		info, txErr = tx.GetRoundInfo(turnID)
		return txErr
	})
	return info, err
}

// GetRoundInfo retrieves the current round state for a turn.
func (tx *Tx) GetRoundInfo(turnID string) (*RoundInfo, error) {
	var currentCandidateID sql.NullString
	var maxRoundsInEffect sql.NullInt64
	var maxRoundsCeiling sql.NullInt64

	if err := tx.tx.QueryRow(`
		SELECT candidate_id, max_rounds_in_effect, max_rounds_ceiling FROM turns WHERE id = ?
	`, turnID).Scan(&currentCandidateID, &maxRoundsInEffect, &maxRoundsCeiling); err != nil {
		return nil, fmt.Errorf("query turn: %w", err)
	}

	info := &RoundInfo{
		CurrentRound: 0,
	}

	if currentCandidateID.Valid {
		var round int
		if err := tx.tx.QueryRow(`
			SELECT round FROM candidates WHERE id = ?
		`, currentCandidateID.String).Scan(&round); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("query current candidate round: %w", err)
			}
		} else {
			info.CurrentRound = round
		}
	}

	if maxRoundsInEffect.Valid {
		info.MaxRoundsInEffect = int(maxRoundsInEffect.Int64)
	}

	if maxRoundsCeiling.Valid {
		info.MaxRoundsCeiling = int(maxRoundsCeiling.Int64)
	}

	return info, nil
}

// CanAdvanceRound checks if a new round can be started for a turn.
// Returns true if the current round is less than max_rounds_in_effect.
func (s *Store) CanAdvanceRound(turnID string) (bool, error) {
	var canAdvance bool
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		canAdvance, txErr = tx.CanAdvanceRound(turnID)
		return txErr
	})
	return canAdvance, err
}

// CanAdvanceRound checks if a new round can be started for a turn.
func (tx *Tx) CanAdvanceRound(turnID string) (bool, error) {
	info, err := tx.GetRoundInfo(turnID)
	if err != nil {
		return false, err
	}

	return info.CurrentRound < info.MaxRoundsInEffect, nil
}

// BlocksForRound retrieves all blocks that are open for a turn at a specific round.
// A block is considered open for a round if it was opened in that round (candidate_hash matches the round's candidate).
func (s *Store) BlocksForRound(turnID string, round int) ([]Block, error) {
	var blocks []Block
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.BlocksForRound(turnID, round)
		return txErr
	})
	return blocks, err
}

// BlocksForRound retrieves all blocks that are open for a turn at a specific round.
func (tx *Tx) BlocksForRound(turnID string, round int) ([]Block, error) {
	rows, err := tx.tx.Query(`
		SELECT b.id, b.verdict_id, b.candidate_hash, b.severity, b.evidence, b.remedy, b.state, b.reviewer_block_id
		FROM blocks b
		JOIN verdicts v ON b.verdict_id = v.id
		JOIN attempts a ON v.attempt_id = a.id
		JOIN candidates c ON b.candidate_hash = c.content_hash AND c.turn_id = ? AND c.round = ?
		WHERE a.turn_id = ? AND b.state = 'open'
		ORDER BY b.id
	`, turnID, round, turnID)
	if err != nil {
		return nil, fmt.Errorf("query blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var blocks []Block
	for rows.Next() {
		var block Block
		var reviewerBlockID sql.NullString
		if err := rows.Scan(&block.ID, &block.VerdictID, &block.CandidateHash, &block.Severity, &block.Evidence, &block.Remedy, &block.State, &reviewerBlockID); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		if reviewerBlockID.Valid {
			block.ReviewerBlockID = reviewerBlockID.String
		}
		blocks = append(blocks, block)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return blocks, nil
}

// CandidateForRound retrieves the candidate for a specific round of a turn.
func (s *Store) CandidateForRound(turnID string, round int) (candidateID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		candidateID, txErr = tx.CandidateForRound(turnID, round)
		return txErr
	})
	return candidateID, err
}

// CandidateForRound retrieves the candidate for a specific round of a turn.
func (tx *Tx) CandidateForRound(turnID string, round int) (string, error) {
	var candidateID sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT id FROM candidates WHERE turn_id = ? AND round = ?
	`, turnID, round).Scan(&candidateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query candidate: %w", err)
	}

	if !candidateID.Valid {
		return "", nil
	}

	return candidateID.String, nil
}

// LatestCandidate retrieves the latest candidate for a turn.
func (s *Store) LatestCandidate(turnID string) (candidateID string, round int, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		candidateID, round, txErr = tx.LatestCandidate(turnID)
		return txErr
	})
	return candidateID, round, err
}

// LatestCandidate retrieves the turn's current candidate through the
// authoritative turns.candidate_id pointer. Candidate rows that exist without
// the pointer moving to them are not current. Returns "" when none is set.
func (tx *Tx) LatestCandidate(turnID string) (string, int, error) {
	var candidateID string
	var round int
	err := tx.tx.QueryRow(`
		SELECT c.id, c.round FROM turns t
		JOIN candidates c ON c.id = t.candidate_id
		WHERE t.id = ?
	`, turnID).Scan(&candidateID, &round)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("query latest candidate: %w", err)
	}
	return candidateID, round, nil
}

// ApprovedParticipants returns the participants holding a valid approve verdict
// on the turn's current candidate hash. Approvals recorded against an earlier
// round's candidate never count, so they do not transfer across a round advance.
func (s *Store) ApprovedParticipants(turnID string) ([]string, error) {
	var ids []string
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		ids, txErr = tx.ApprovedParticipants(turnID)
		return txErr
	})
	return ids, err
}

// ApprovedParticipants returns the participants holding a valid approve verdict
// on the turn's current candidate hash.
func (tx *Tx) ApprovedParticipants(turnID string) ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT DISTINCT a.participant
		FROM turns t
		JOIN candidates c ON c.id = t.candidate_id
		JOIN attempts a ON a.turn_id = t.id
		JOIN verdicts v ON v.attempt_id = a.id AND v.candidate_hash = c.content_hash
		WHERE t.id = ? AND v.parsed_verdict = 'approve' AND v.validation_status = 'valid'
		ORDER BY a.participant
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}
	return ids, nil
}

// AllApproved reports whether every required participant approves the current
// candidate and no open block remains on the turn.
func (tx *Tx) AllApproved(turnID string, required []string) (bool, error) {
	approved, err := tx.ApprovedParticipants(turnID)
	if err != nil {
		return false, err
	}
	have := make(map[string]bool, len(approved))
	for _, id := range approved {
		have[id] = true
	}
	for _, id := range required {
		if !have[id] {
			return false, nil
		}
	}
	open, err := tx.GetBlocksByState(turnID, "open")
	if err != nil {
		return false, err
	}
	return len(open) == 0, nil
}

// TurnInputRef returns the content ref of the first message the user sent in
// the turn, which is what they typed, or "" when none is journaled yet.
func (s *Store) TurnInputRef(turnID string) (string, error) {
	var id string
	err := s.Tx(func(tx *Tx) error {
		err := tx.tx.QueryRow(`
			SELECT id FROM messages WHERE turn_id = ? AND sender = 'user' AND phase = 'author' ORDER BY sequence LIMIT 1
		`, turnID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("query turn input: %w", err)
	}
	if id == "" {
		return "", nil
	}
	return "msg:" + id, nil
}
