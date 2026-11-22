package journal

import "fmt"

// ReviewerBlock is an open block as the reviewer that opened it sees it: its
// own id for the block beside the journal's.
type ReviewerBlock struct {
	ID              string
	ReviewerBlockID string
	Severity        string
	Evidence        string
	Remedy          string
}

// ReviewerOpenBlocks returns the open blocks the participant opened in the
// turn, across every candidate, in the order they were opened.
func (tx *Tx) ReviewerOpenBlocks(turnID, participant string) ([]ReviewerBlock, error) {
	rows, err := tx.tx.Query(`
		SELECT b.id, COALESCE(b.reviewer_block_id, ''), b.severity, b.evidence, b.remedy
		FROM blocks b
		JOIN verdicts v ON b.verdict_id = v.id
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ? AND a.participant = ? AND b.state = 'open'
		ORDER BY b.rowid
	`, turnID, participant)
	if err != nil {
		return nil, fmt.Errorf("query reviewer blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var blocks []ReviewerBlock
	for rows.Next() {
		var b ReviewerBlock
		if err := rows.Scan(&b.ID, &b.ReviewerBlockID, &b.Severity, &b.Evidence, &b.Remedy); err != nil {
			return nil, fmt.Errorf("scan reviewer block: %w", err)
		}
		blocks = append(blocks, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reviewer blocks: %w", err)
	}
	return blocks, nil
}
