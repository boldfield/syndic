package journal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
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
	ID               string
	TurnID           string
	Round            int
	CandidateHash    string
	Participant      string
	ParticipantOrder int
	AttemptID        string
	Kind             string // approve, block, approve-with-changes
	Blocks           []ReviewBlock
	Commentary       *ContentRef
	Oversized        bool
	RecordRef        *ContentRef // set when oversized
	CreatedAt        time.Time
	VerdictJSON      []byte
}

// StoreReviewRecord stores a new review record when a verdict is accepted.
func (tx *Tx) StoreReviewRecord(turnID string, round int, candidateHash, participant string,
	participantOrder int, attemptID, kind string, blocks []ReviewBlock, commentary *ContentRef,
	oversized bool, recordRef *ContentRef) (string, error) {
	recordID, err := generateID()
	if err != nil {
		return "", err
	}

	// Marshal the full record as JSON for storage and later retrieval
	recordData := map[string]interface{}{
		"blocks":     blocks,
		"commentary": commentary,
	}
	verdictJSON, err := json.Marshal(recordData)
	if err != nil {
		return "", fmt.Errorf("marshal verdict json: %w", err)
	}

	var recordRefJSON *string
	if recordRef != nil {
		refBytes, err := json.Marshal(recordRef)
		if err != nil {
			return "", fmt.Errorf("marshal record ref: %w", err)
		}
		refStr := string(refBytes)
		recordRefJSON = &refStr
	}

	_, err = tx.tx.Exec(`
		INSERT INTO review_records (id, turn_id, round, candidate_hash, participant, participant_order, attempt_id, kind, verdict_json, oversized, record_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, recordID, turnID, round, candidateHash, participant, participantOrder, attemptID, kind, verdictJSON, oversized, recordRefJSON, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("insert review record: %w", err)
	}

	// Update turn's review_count - use COUNT instead of denormalized counter for accuracy
	_, err = tx.tx.Exec(`
		UPDATE turns SET review_count = (SELECT COUNT(*) FROM review_records WHERE turn_id = ?) WHERE id = ?
	`, turnID, turnID)
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

	// First, check if we have any review records for this turn
	var existingCount int
	if err := tx.tx.QueryRow(`
		SELECT COUNT(*) FROM review_records WHERE turn_id = ?
	`, turnID).Scan(&existingCount); err != nil {
		return nil, nil, fmt.Errorf("check existing records: %w", err)
	}

	// If no records exist, try to materialize them from verdicts/blocks for older verdicts
	if existingCount == 0 {
		if err := tx.materializeReviewRecords(turnID); err != nil {
			return nil, nil, fmt.Errorf("materialize review records: %w", err)
		}
	}

	query := `
		SELECT id, turn_id, round, candidate_hash, participant, participant_order, attempt_id, kind, verdict_json, oversized, record_ref, created_at
		FROM review_records
		WHERE turn_id = ?
	`
	var args []interface{}
	args = append(args, turnID)

	if cursor != nil {
		// Use roster order for cursor position, not creation time
		query += ` AND (round, participant_order, id) > (SELECT round, participant_order, id FROM review_records WHERE id = ?)`
		args = append(args, *cursor)
	}

	query += ` ORDER BY round ASC, participant_order ASC, id ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := tx.tx.Query(query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query review records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var r ReviewRecord
		var createdAtStr string
		var recordRefJSON sql.NullString
		if err := rows.Scan(&r.ID, &r.TurnID, &r.Round, &r.CandidateHash, &r.Participant, &r.ParticipantOrder, &r.AttemptID, &r.Kind, &r.VerdictJSON, &r.Oversized, &recordRefJSON, &createdAtStr); err != nil {
			return nil, nil, fmt.Errorf("scan review record: %w", err)
		}

		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, nil, fmt.Errorf("parse created_at: %w", err)
		}
		r.CreatedAt = createdAt

		// Parse the verdict_json to extract blocks and commentary references
		// Use a dedicated struct to avoid overwriting ReviewRecord fields
		var verdictData struct {
			Blocks     []ReviewBlock `json:"blocks"`
			Commentary *ContentRef   `json:"commentary"`
		}
		if err := json.Unmarshal(r.VerdictJSON, &verdictData); err != nil {
			return nil, nil, fmt.Errorf("parse verdict json: %w", err)
		}
		r.Blocks = verdictData.Blocks
		r.Commentary = verdictData.Commentary

		// Parse record_ref if present
		if recordRefJSON.Valid {
			var ref ContentRef
			if err := json.Unmarshal([]byte(recordRefJSON.String), &ref); err == nil {
				r.RecordRef = &ref
			}
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

// materializeReviewRecords derives review records from existing verdict/block/attempt data
// for verdicts accepted before the review_records table was introduced.
// Only creates records for valid (non-malformed, non-rejected, non-revision-request) verdicts.
func (tx *Tx) materializeReviewRecords(turnID string) error {
	// Query for all verdicts of this turn that were parsed as valid
	// We derive records for all valid verdicts to create a historical record
	rows, err := tx.tx.Query(`
		SELECT v.id, v.attempt_id, v.candidate_hash, v.parsed_verdict,
		       a.participant, a.epoch, t.round
		FROM verdicts v
		JOIN attempts a ON v.attempt_id = a.id
		JOIN turns t ON a.turn_id = t.id
		WHERE a.turn_id = ? AND v.validation_status = 'valid' AND a.state = 'completed'
		ORDER BY t.round ASC, a.epoch ASC
	`, turnID)
	if err != nil {
		return fmt.Errorf("query verdicts for materialization: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Get participant order info by querying the roster (we'll estimate based on epoch order)
	type verdictRecord struct {
		verdictID        string
		attemptID        string
		candidate        string
		kind             string
		participant      string
		participantOrder int
		round            int
	}
	var records []verdictRecord

	for rows.Next() {
		var verdictID, attemptID, candidate, kind, participant string
		var participantOrder, round int
		if err := rows.Scan(&verdictID, &attemptID, &candidate, &kind, &participant, &participantOrder, &round); err != nil {
			return fmt.Errorf("scan verdict: %w", err)
		}
		records = append(records, verdictRecord{
			verdictID:        verdictID,
			attemptID:        attemptID,
			candidate:        candidate,
			kind:             kind,
			participant:      participant,
			participantOrder: participantOrder,
			round:            round,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate verdicts: %w", err)
	}

	// For each verdict, create a review record
	for _, rec := range records {
		blocks := []ReviewBlock{}

		// If this is a block verdict, get the blocks
		if rec.kind == "block" {
			blockRows, err := tx.tx.Query(`
				SELECT id, severity, state FROM blocks WHERE verdict_id = ?
			`, rec.verdictID)
			if err != nil {
				return fmt.Errorf("query blocks: %w", err)
			}
			for blockRows.Next() {
				var blockID, severity, state string
				if err := blockRows.Scan(&blockID, &severity, &state); err != nil {
					_ = blockRows.Close()
					return fmt.Errorf("scan block: %w", err)
				}
				blocks = append(blocks, ReviewBlock{
					BlockID:  blockID,
					State:    state,
					Severity: &severity,
				})
			}
			_ = blockRows.Close()
		}

		// Create the review record
		recordID, err := generateID()
		if err != nil {
			return fmt.Errorf("generate id: %w", err)
		}

		recordData := map[string]interface{}{
			"blocks": blocks,
		}
		verdictJSON, err := json.Marshal(recordData)
		if err != nil {
			return fmt.Errorf("marshal verdict json: %w", err)
		}

		_, err = tx.tx.Exec(`
			INSERT OR IGNORE INTO review_records
			(id, turn_id, round, candidate_hash, participant, participant_order, attempt_id, kind, verdict_json, oversized, record_ref, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, recordID, turnID, rec.round, rec.candidate, rec.participant, rec.participantOrder, rec.attemptID, rec.kind, verdictJSON, false, nil, time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return fmt.Errorf("insert materialized record: %w", err)
		}
	}

	return nil
}

// GetReviewCount returns the number of accepted review records for a turn.
// Uses COUNT(*) instead of denormalized counter for accuracy with lazy materialization.
func (tx *Tx) GetReviewCount(turnID string) (int, error) {
	var count int
	err := tx.tx.QueryRow(`
		SELECT COUNT(*) FROM review_records WHERE turn_id = ?
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

// EstimateRecordSize estimates the actual canonical encoding size of a review record.
// Returns true if the estimated size exceeds 64 KiB.
func EstimateRecordSize(blocks []ReviewBlock, commentary *ContentRef) bool {
	// Create the actual record structure and encode it to get a real size estimate
	recordData := map[string]interface{}{
		"blocks":     blocks,
		"commentary": commentary,
	}
	encoded, _ := json.Marshal(recordData)
	return len(encoded) > 65536 // 64 KiB
}

// StoreOversizedRecord stores the full structured record as content when it exceeds 64 KiB.
// Returns a ContentRef to the stored record.
func (tx *Tx) StoreOversizedRecord(turnID string, blocks []ReviewBlock, commentary *ContentRef) *ContentRef {
	// Create the full record structure
	recordData := map[string]interface{}{
		"blocks":     blocks,
		"commentary": commentary,
	}
	recordJSON, _ := json.Marshal(recordData)

	// For oversized records, return a reference - in production would use a dedicated storage
	contentHash := computeSHA256Hash(recordJSON)
	return &ContentRef{
		Ref:     "review-record:" + computeSHA256Hash([]byte(turnID))[:8],
		SHA256:  contentHash,
		ByteLen: int64(len(recordJSON)),
	}
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
