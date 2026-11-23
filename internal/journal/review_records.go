package journal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ReviewBlock represents a block within a verdict record.
type ReviewBlock struct {
	BlockID  string      `json:"block_id"`
	State    string      `json:"state"` // opened, maintained, withdrawn
	Severity *string     `json:"severity,omitempty"`
	Evidence *ContentRef `json:"evidence,omitempty"`
	Remedy   *ContentRef `json:"remedy,omitempty"`
}

// ContentRef represents a reference to stored content (commentary, evidence, remedy).
type ContentRef struct {
	Ref     string `json:"ref"`
	SHA256  string `json:"sha256"`
	ByteLen int64  `json:"byte_len"`
}

// ReviewRecord represents a structured accepted review verdict.
type ReviewRecord struct {
	ID            string
	TurnID        string
	Round         int
	CandidateHash string
	Participant   string
	AttemptID     string
	Kind          string // approve, block
	Blocks        []ReviewBlock
	Commentary    *ContentRef
	Oversized     bool
	RecordRef     *ContentRef // set when oversized
	CreatedAt     time.Time
	VerdictJSON   []byte
}

// StoreReviewRecord stores a new review record when a verdict is accepted.
// The verdict_json parameter should be the full structured record as JSON.
func (tx *Tx) StoreReviewRecord(turnID string, round int, candidateHash, participant, attemptID, kind string, verdictJSON []byte, oversized bool) (string, error) {
	recordID, err := generateID()
	if err != nil {
		return "", err
	}

	_, err = tx.tx.Exec(`
		INSERT INTO review_records (id, turn_id, round, candidate_hash, participant, attempt_id, kind, verdict_json, oversized, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, recordID, turnID, round, candidateHash, participant, attemptID, kind, verdictJSON, oversized, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("insert review record: %w", err)
	}

	// Update turn's review_count
	_, err = tx.tx.Exec(`
		UPDATE turns SET review_count = review_count + 1 WHERE id = ?
	`, turnID)
	if err != nil {
		return "", fmt.Errorf("update review count: %w", err)
	}

	return recordID, nil
}

// GetReviewRecords retrieves accepted review records for a turn, ordered by round then roster order.
// If cursor is provided, returns records after that cursor position.
// limit is capped at 50 per the wire contract.
func (tx *Tx) GetReviewRecords(turnID string, cursor *string, limit int) (records []ReviewRecord, nextCursor *string, err error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}

	query := `
		SELECT id, turn_id, round, candidate_hash, participant, attempt_id, kind, verdict_json, oversized, created_at
		FROM review_records
		WHERE turn_id = ?
	`
	var args []interface{}
	args = append(args, turnID)

	if cursor != nil {
		query += ` AND (round, created_at, id) > (SELECT round, created_at, id FROM review_records WHERE id = ?)`
		args = append(args, *cursor)
	}

	query += ` ORDER BY round ASC, created_at ASC, id ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := tx.tx.Query(query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query review records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var r ReviewRecord
		var createdAtStr string
		if err := rows.Scan(&r.ID, &r.TurnID, &r.Round, &r.CandidateHash, &r.Participant, &r.AttemptID, &r.Kind, &r.VerdictJSON, &r.Oversized, &createdAtStr); err != nil {
			return nil, nil, fmt.Errorf("scan review record: %w", err)
		}

		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, nil, fmt.Errorf("parse created_at: %w", err)
		}
		r.CreatedAt = createdAt

		// Parse the verdict_json to extract blocks and commentary references
		if err := json.Unmarshal(r.VerdictJSON, &r); err != nil {
			return nil, nil, fmt.Errorf("parse verdict json: %w", err)
		}

		records = append(records, r)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate review records: %w", err)
	}

	// Set next_cursor if there are more records
	if len(records) > limit {
		records = records[:limit]
		nextCursor = &records[len(records)-1].ID
	}

	return records, nextCursor, nil
}

// GetReviewCount returns the number of accepted review records for a turn.
func (tx *Tx) GetReviewCount(turnID string) (int, error) {
	var count int
	err := tx.tx.QueryRow(`
		SELECT COALESCE(review_count, 0) FROM turns WHERE id = ?
	`, turnID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get review count: %w", err)
	}
	return count, nil
}

// computeSHA256Hash computes the SHA-256 hash of data and returns the hex string.
func computeSHA256Hash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// StoreReviewRecordContent stores the content referenced by a review record
// (commentary, evidence, remedy) and returns a ContentRef.
// Note: This function requires a turn context from the transaction; callers should
// pass a valid turn_id when calling this from a turn's context.
func (tx *Tx) StoreReviewRecordContent(turnID string, content []byte) (*ContentRef, error) {
	contentHash := computeSHA256Hash(content)
	byteLen := int64(len(content))

	// Content is stored via AppendMessage in the messages table with a special sender.
	// Use a synthetic message ID for the content reference.
	msgID, _, _, err := tx.AppendMessage(turnID, "review", "system", nil, content, nil)
	if err != nil {
		return nil, fmt.Errorf("store review content: %w", err)
	}

	return &ContentRef{
		Ref:     msgID,
		SHA256:  contentHash,
		ByteLen: byteLen,
	}, nil
}

// SortReviewRecords sorts review records by round and then by participant roster order.
// participantOrder is a map of participant ID to roster order (0-based index).
func SortReviewRecords(records []ReviewRecord, participantOrder map[string]int) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Round != records[j].Round {
			return records[i].Round < records[j].Round
		}
		// If rounds are equal, sort by participant roster order
		orderI := participantOrder[records[i].Participant]
		orderJ := participantOrder[records[j].Participant]
		return orderI < orderJ
	})
}

// MarshalReviewRecordJSON creates the JSON representation of a review record
// for storage in verdict_json.
func MarshalReviewRecordJSON(blocks []ReviewBlock, commentary *ContentRef) ([]byte, error) {
	record := map[string]interface{}{
		"blocks":     blocks,
		"commentary": commentary,
	}
	return json.Marshal(record)
}

// UnmarshalReviewRecordJSON extracts blocks and commentary from the verdict_json.
func UnmarshalReviewRecordJSON(verdictJSON []byte) (blocks []ReviewBlock, commentary *ContentRef, err error) {
	var record map[string]interface{}
	if err := json.Unmarshal(verdictJSON, &record); err != nil {
		return nil, nil, fmt.Errorf("unmarshal verdict json: %w", err)
	}

	// Extract blocks
	if blocksData, ok := record["blocks"]; ok {
		if blocksJSON, err := json.Marshal(blocksData); err == nil {
			_ = json.Unmarshal(blocksJSON, &blocks)
		}
	}

	// Extract commentary
	if commentaryData, ok := record["commentary"]; ok {
		if commentaryJSON, err := json.Marshal(commentaryData); err == nil {
			_ = json.Unmarshal(commentaryJSON, &commentary)
		}
	}

	return blocks, commentary, nil
}

// EstimateRecordSize estimates the encoded size of a review record item.
// Returns true if the estimated size exceeds 64 KiB.
func EstimateRecordSize(blocks []ReviewBlock, commentary *ContentRef) bool {
	// Rough estimate: each block is ~200 bytes, commentary is ~100 bytes, overhead ~500 bytes
	estimatedSize := 500 + len(blocks)*200
	if commentary != nil {
		estimatedSize += 100
	}
	return estimatedSize > 65536 // 64 KiB
}

// StoreOversizedRecord stores the full structured record as content when it exceeds 64 KiB.
// Returns a ContentRef to the stored record.
func (tx *Tx) StoreOversizedRecord(turnID string, recordJSON []byte) (*ContentRef, error) {
	return tx.StoreReviewRecordContent(turnID, recordJSON)
}

// GetReviewRecordsForStore is used by the Store wrapper to call GetReviewRecords within a transaction.
func (s *Store) GetReviewRecords(turnID string, cursor *string, limit int) (records []ReviewRecord, nextCursor *string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		records, nextCursor, txErr = tx.GetReviewRecords(turnID, cursor, limit)
		return txErr
	})
	return records, nextCursor, err
}

// GetReviewCount is used by the Store wrapper to call GetReviewCount within a transaction.
func (s *Store) GetReviewCount(turnID string) (int, error) {
	var count int
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		count, txErr = tx.GetReviewCount(turnID)
		return txErr
	})
	return count, err
}
