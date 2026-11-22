package journal

import (
	"database/sql"
	"fmt"
)

// BlockState represents the state of a block with withdrawal information.
type BlockState struct {
	ID                   string
	State                string // "open", "withdrawn", or "overridden"
	CandidateHash        string
	WithdrawnAgainstHash *string
	Severity             string
	Evidence             string
	Remedy               string
	ReviewerBlockID      *string
}

// blockStateColumns is the column list scanned by scanBlockStates.
const blockStateColumns = `b.id, b.state, b.candidate_hash, b.withdrawn_against_hash, b.severity, b.evidence, b.remedy, b.reviewer_block_id`

// queryBlockStates runs a query selecting blockStateColumns and scans every row.
func (tx *Tx) queryBlockStates(what, query string, args ...any) ([]BlockState, error) {
	rows, err := tx.tx.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	var blocks []BlockState
	for rows.Next() {
		var b BlockState
		var withdrawnAgainstHash, reviewerBlockID sql.NullString
		if err := rows.Scan(&b.ID, &b.State, &b.CandidateHash, &withdrawnAgainstHash, &b.Severity, &b.Evidence, &b.Remedy, &reviewerBlockID); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}
		if withdrawnAgainstHash.Valid {
			b.WithdrawnAgainstHash = &withdrawnAgainstHash.String
		}
		if reviewerBlockID.Valid {
			b.ReviewerBlockID = &reviewerBlockID.String
		}
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return blocks, nil
}

// GetBlockState retrieves the state of a specific block.
func (s *Store) GetBlockState(blockID string) (*BlockState, error) {
	var state *BlockState
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		state, txErr = tx.GetBlockState(blockID)
		return txErr
	})
	return state, err
}

// GetBlockState retrieves the state of a specific block.
func (tx *Tx) GetBlockState(blockID string) (*BlockState, error) {
	blocks, err := tx.queryBlockStates("block state", `SELECT `+blockStateColumns+` FROM blocks b WHERE b.id = ?`, blockID)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("block not found")
	}
	return &blocks[0], nil
}

const turnBlocksFrom = `
	FROM blocks b
	JOIN verdicts v ON b.verdict_id = v.id
	JOIN attempts a ON v.attempt_id = a.id
`

// GetBlocksByState retrieves all blocks for a turn in a specific state.
func (s *Store) GetBlocksByState(turnID, state string) ([]BlockState, error) {
	var blocks []BlockState
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.GetBlocksByState(turnID, state)
		return txErr
	})
	return blocks, err
}

// GetBlocksByState retrieves all blocks for a turn in a specific state.
func (tx *Tx) GetBlocksByState(turnID, state string) ([]BlockState, error) {
	return tx.queryBlockStates("blocks", `SELECT `+blockStateColumns+turnBlocksFrom+`
		WHERE a.turn_id = ? AND b.state = ? ORDER BY b.id`, turnID, state)
}

// AllBlocksForTurn retrieves all blocks for a turn, regardless of state.
func (s *Store) AllBlocksForTurn(turnID string) ([]BlockState, error) {
	var blocks []BlockState
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.AllBlocksForTurn(turnID)
		return txErr
	})
	return blocks, err
}

// AllBlocksForTurn retrieves all blocks for a turn, regardless of state.
func (tx *Tx) AllBlocksForTurn(turnID string) ([]BlockState, error) {
	return tx.queryBlockStates("all blocks", `SELECT `+blockStateColumns+turnBlocksFrom+`
		WHERE a.turn_id = ? ORDER BY b.id`, turnID)
}

// WithdrawnBlocksAgainstHash retrieves all blocks withdrawn against a specific hash.
func (s *Store) WithdrawnBlocksAgainstHash(turnID, hash string) ([]BlockState, error) {
	var blocks []BlockState
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.WithdrawnBlocksAgainstHash(turnID, hash)
		return txErr
	})
	return blocks, err
}

// WithdrawnBlocksAgainstHash retrieves all blocks withdrawn against a specific hash.
func (tx *Tx) WithdrawnBlocksAgainstHash(turnID, hash string) ([]BlockState, error) {
	return tx.queryBlockStates("withdrawn blocks", `SELECT `+blockStateColumns+turnBlocksFrom+`
		WHERE a.turn_id = ? AND b.state = 'withdrawn' AND b.withdrawn_against_hash = ? ORDER BY b.id`, turnID, hash)
}

// BlocksForCandidate retrieves all open blocks for a specific candidate.
func (s *Store) BlocksForCandidate(turnID, candidateHash string) ([]BlockState, error) {
	var blocks []BlockState
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.BlocksForCandidate(turnID, candidateHash)
		return txErr
	})
	return blocks, err
}

// BlocksForCandidate retrieves all open blocks for a specific candidate.
func (tx *Tx) BlocksForCandidate(turnID, candidateHash string) ([]BlockState, error) {
	return tx.queryBlockStates("blocks for candidate", `SELECT `+blockStateColumns+turnBlocksFrom+`
		WHERE a.turn_id = ? AND b.candidate_hash = ? AND b.state = 'open' ORDER BY b.id`, turnID, candidateHash)
}
