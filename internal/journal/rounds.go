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
		CurrentRound:      0,
		MaxRoundsInEffect: 0,
		MaxRoundsCeiling:  5,
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
	} else {
		// Default to configured value if not set on turn
		info.MaxRoundsCeiling = 5
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

// LatestCandidate retrieves the latest candidate for a turn.
func (tx *Tx) LatestCandidate(turnID string) (string, int, error) {
	var candidateID sql.NullString
	var round int

	if err := tx.tx.QueryRow(`
		SELECT id, round FROM candidates WHERE turn_id = ? ORDER BY round DESC LIMIT 1
	`, turnID).Scan(&candidateID, &round); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("query latest candidate: %w", err)
		}
		return "", 0, nil
	}

	if !candidateID.Valid {
		return "", 0, nil
	}

	return candidateID.String, round, nil
}

// AdvanceRound atomically advances a turn to the next round and bumps epoch.
// This rejects late previous-round results by advancing the epoch.
// It creates the new candidate (round+1 superseding current), updates turns.candidate_id,
// and bumps the epoch all in one transaction.
func (s *Store) AdvanceRound(turnID string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.AdvanceRound(turnID)
	})
}

// AdvanceRound atomically advances a turn to the next round and bumps epoch.
// Creates a new candidate for round+1 that supersedes the current candidate,
// updates turns.candidate_id to the new candidate, and increments the epoch.
func (tx *Tx) AdvanceRound(turnID string) error {
	var maxRoundsInEffect sql.NullInt64
	var currentCandidateID sql.NullString
	var currentRound int

	if err := tx.tx.QueryRow(`
		SELECT max_rounds_in_effect, candidate_id FROM turns WHERE id = ?
	`, turnID).Scan(&maxRoundsInEffect, &currentCandidateID); err != nil {
		return fmt.Errorf("query turn: %w", err)
	}

	if !currentCandidateID.Valid {
		return fmt.Errorf("cannot advance round: turn has no current candidate")
	}

	if err := tx.tx.QueryRow(`
		SELECT round FROM candidates WHERE id = ?
	`, currentCandidateID.String).Scan(&currentRound); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("cannot advance round: current candidate does not exist")
		}
		return fmt.Errorf("query current candidate: %w", err)
	}

	nextRound := currentRound + 1
	if maxRoundsInEffect.Valid && nextRound > int(maxRoundsInEffect.Int64) {
		return fmt.Errorf("cannot advance: round %d exceeds max_rounds_in_effect %d", nextRound, maxRoundsInEffect.Int64)
	}

	newCandidateID, err := generateID()
	if err != nil {
		return fmt.Errorf("generate candidate id: %w", err)
	}

	emptyContent := ""
	if _, err := tx.tx.Exec(`
		INSERT INTO candidates (id, turn_id, content, content_hash, round, supersedes)
		VALUES (?, ?, ?, ?, ?, ?)
	`, newCandidateID, turnID, emptyContent, "", nextRound, currentCandidateID.String); err != nil {
		return fmt.Errorf("insert new candidate: %w", err)
	}

	if _, err := tx.tx.Exec(`
		UPDATE turns SET candidate_id = ?, epoch = epoch + 1 WHERE id = ?
	`, newCandidateID, turnID); err != nil {
		return fmt.Errorf("update turn: %w", err)
	}

	return nil
}
