package journal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrNoSuchContent = errors.New("no such content")

type VerdictRow struct {
	ID               string
	AttemptID        string
	CandidateHash    string
	Parsed           string // "", "approve" or "block"
	Reason           string
	ValidationStatus string
}

type CandidateRow struct {
	ID          string
	TurnID      string
	ContentHash string
	ByteLen     int64
}

type TranscriptItem struct {
	Kind    string // "message" or "attempt"
	Ref     string // "msg:<id>" or "attempt:<id>"
	At      time.Time
	Phase   string
	Sender  string // message sender, or the attempt's participant
	SHA256  string
	ByteLen int64
	Attempt *AttemptRow // set for Kind "attempt", with RawOutput cleared
}

func (s *Store) ListAttempts(turnID string) ([]AttemptRow, error) {
	var attempts []AttemptRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		attempts, txErr = tx.ListAttempts(turnID)
		return txErr
	})
	return attempts, err
}

func (tx *Tx) ListAttempts(turnID string) ([]AttemptRow, error) {
	rows, err := tx.tx.Query(`
		SELECT id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status,
		       cancellation_state, quarantined, raw_output, scrub_count, observed_identity,
		       native_finish, transport_status, repair_of, gate_error, commentary, delimiter,
		       max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion, retry_count
		FROM attempts
		WHERE turn_id = ?
		ORDER BY dispatched_at ASC, id ASC
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []AttemptRow
	for rows.Next() {
		var row AttemptRow
		var dispatchedAtStr string
		var deadlineStr sql.NullString
		var repairOfStr sql.NullString

		if err := rows.Scan(
			&row.ID, &row.TurnID, &row.Participant, &row.Phase, &row.Epoch,
			&dispatchedAtStr, &deadlineStr, &row.State, &row.Status,
			&row.CancellationState, &row.Quarantined, &row.RawOutput, &row.ScrubCount,
			&row.ObservedIdentity, &row.NativeFinish, &row.TransportStatus,
			&repairOfStr, &row.GateError, &row.Commentary, &row.Delimiter,
			&row.MaxOutputTokens, &row.EstimatedPromptTokens, &row.UsagePrompt,
			&row.UsageCompletion, &row.RetryCount,
		); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}

		dispatchedAt, err := parseTime(dispatchedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse dispatched_at: %w", err)
		}
		row.DispatchedAt = dispatchedAt

		if deadlineStr.Valid {
			dt, err := parseTime(deadlineStr.String)
			if err != nil {
				return nil, fmt.Errorf("parse deadline: %w", err)
			}
			row.Deadline = &dt
		}

		if repairOfStr.Valid {
			row.RepairOf = &repairOfStr.String
		}

		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}

	return result, nil
}

func (s *Store) ListVerdicts(turnID string) ([]VerdictRow, error) {
	var verdicts []VerdictRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		verdicts, txErr = tx.ListVerdicts(turnID)
		return txErr
	})
	return verdicts, err
}

func (tx *Tx) ListVerdicts(turnID string) ([]VerdictRow, error) {
	rows, err := tx.tx.Query(`
		SELECT v.id, v.attempt_id, v.candidate_hash, v.parsed_verdict, v.reason, v.validation_status
		FROM verdicts v
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ?
		ORDER BY a.dispatched_at ASC, a.id ASC, v.id ASC
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query verdicts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []VerdictRow
	for rows.Next() {
		var row VerdictRow
		var parsed sql.NullString

		if err := rows.Scan(&row.ID, &row.AttemptID, &row.CandidateHash, &parsed, &row.Reason, &row.ValidationStatus); err != nil {
			return nil, fmt.Errorf("scan verdict: %w", err)
		}

		if parsed.Valid {
			row.Parsed = parsed.String
		}

		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verdicts: %w", err)
	}

	return result, nil
}

func (s *Store) ListBlocks(turnID string) ([]Block, error) {
	var blocks []Block
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.ListBlocks(turnID)
		return txErr
	})
	return blocks, err
}

func (tx *Tx) ListBlocks(turnID string) ([]Block, error) {
	rows, err := tx.tx.Query(`
		SELECT b.id, b.verdict_id, b.candidate_hash, b.severity, b.evidence, b.remedy, b.state, b.reviewer_block_id
		FROM blocks b
		JOIN verdicts v ON b.verdict_id = v.id
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ?
		ORDER BY b.id ASC
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []Block
	for rows.Next() {
		var row Block
		var reviewerBlockID sql.NullString

		if err := rows.Scan(&row.ID, &row.VerdictID, &row.CandidateHash, &row.Severity, &row.Evidence, &row.Remedy, &row.State, &reviewerBlockID); err != nil {
			return nil, fmt.Errorf("scan block: %w", err)
		}

		if reviewerBlockID.Valid {
			row.ReviewerBlockID = reviewerBlockID.String
		}

		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocks: %w", err)
	}

	return result, nil
}

func (s *Store) GetCandidate(candidateID string) (*CandidateRow, error) {
	var row *CandidateRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetCandidate(candidateID)
		return txErr
	})
	return row, err
}

func (tx *Tx) GetCandidate(candidateID string) (*CandidateRow, error) {
	var row CandidateRow
	err := tx.tx.QueryRow(`
		SELECT id, turn_id, content_hash, LENGTH(content)
		FROM candidates
		WHERE id = ?
	`, candidateID).Scan(&row.ID, &row.TurnID, &row.ContentHash, &row.ByteLen)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query candidate: %w", err)
	}

	return &row, nil
}

type transcriptRow struct {
	kind        string
	ref         string
	at          time.Time
	phase       string
	sender      string
	sha256      string
	byteLen     int64
	attemptID   *string
	attemptData *AttemptRow
}

func (s *Store) TranscriptPage(turnID, cursor string, limit int) (items []TranscriptItem, nextCursor string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		items, nextCursor, txErr = tx.TranscriptPage(turnID, cursor, limit)
		return txErr
	})
	return items, nextCursor, err
}

func (tx *Tx) TranscriptPage(turnID, cursor string, limit int) ([]TranscriptItem, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	var cursorOffset int
	if cursor != "" {
		// Parse cursor to get offset
		if _, err := fmt.Sscanf(cursor, "%d", &cursorOffset); err != nil {
			return nil, "", fmt.Errorf("parse cursor: %w", err)
		}
	}

	var allRows []transcriptRow

	msgRows, err := tx.tx.Query(`
		SELECT id, phase, sender, content_hash, byte_length, sequence
		FROM messages
		WHERE turn_id = ?
		ORDER BY sequence ASC, id ASC
	`, turnID)
	if err != nil {
		return nil, "", fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = msgRows.Close() }()

	for msgRows.Next() {
		var row transcriptRow
		var msgID string
		var sequence int

		if err := msgRows.Scan(&msgID, &row.phase, &row.sender, &row.sha256, &row.byteLen, &sequence); err != nil {
			return nil, "", fmt.Errorf("scan message: %w", err)
		}

		row.kind = "message"
		row.ref = "msg:" + msgID
		// Use sequence as a proxy for the message's logical time (assigns earlier times to lower sequences)
		row.at = time.Unix(int64(sequence), 0)

		allRows = append(allRows, row)
	}
	if err := msgRows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate messages: %w", err)
	}

	attemptRows, err := tx.tx.Query(`
		SELECT id, dispatched_at, phase, participant
		FROM attempts
		WHERE turn_id = ?
		ORDER BY dispatched_at ASC, id ASC
	`, turnID)
	if err != nil {
		return nil, "", fmt.Errorf("query attempts: %w", err)
	}
	defer func() { _ = attemptRows.Close() }()

	for attemptRows.Next() {
		var row transcriptRow
		var attemptID string
		var dispatchedAtStr string

		if err := attemptRows.Scan(&attemptID, &dispatchedAtStr, &row.phase, &row.sender); err != nil {
			return nil, "", fmt.Errorf("scan attempt: %w", err)
		}

		dispatchedAt, err := parseTime(dispatchedAtStr)
		if err != nil {
			return nil, "", fmt.Errorf("parse dispatched_at: %w", err)
		}

		row.kind = "attempt"
		row.ref = "attempt:" + attemptID
		row.at = dispatchedAt
		row.attemptID = &attemptID

		allRows = append(allRows, row)
	}
	if err := attemptRows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate attempts: %w", err)
	}

	// Merge by time, keeping insertion order for ties
	for i := len(allRows) - 1; i > 0; i-- {
		for j := 0; j < i; j++ {
			if allRows[j].at.After(allRows[j+1].at) {
				allRows[j], allRows[j+1] = allRows[j+1], allRows[j]
			}
		}
	}

	var result []TranscriptItem
	var newCursor string

	for i := cursorOffset; i < len(allRows) && len(result) < limit; i++ {
		row := allRows[i]
		item := TranscriptItem{
			Kind:    row.kind,
			Ref:     row.ref,
			At:      row.at,
			Phase:   row.phase,
			Sender:  row.sender,
			SHA256:  row.sha256,
			ByteLen: row.byteLen,
		}

		if row.attemptID != nil {
			attemptRow, err := tx.GetAttempt(*row.attemptID)
			if err != nil {
				return nil, "", fmt.Errorf("get attempt: %w", err)
			}
			if attemptRow != nil {
				// Set SHA256 and ByteLen for the attempt's output
				if attemptRow.RawOutput != nil && *attemptRow.RawOutput != "" {
					h := sha256.Sum256([]byte(*attemptRow.RawOutput))
					item.SHA256 = hex.EncodeToString(h[:])
					item.ByteLen = int64(len(*attemptRow.RawOutput))
				} else {
					item.SHA256 = ""
					item.ByteLen = 0
				}

				cleanedAttempt := *attemptRow
				cleanedAttempt.RawOutput = nil
				item.Attempt = &cleanedAttempt
			}
		}

		result = append(result, item)

		if len(result) == limit && i+1 < len(allRows) {
			newCursor = fmt.Sprintf("%d", i+1)
		}
	}

	return result, newCursor, nil
}

func (s *Store) ContentRange(ref string, offset, limit int64) (data []byte, total int64, sha256 string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		data, total, sha256, txErr = tx.ContentRange(ref, offset, limit)
		return txErr
	})
	return data, total, sha256, err
}

func (tx *Tx) ContentRange(ref string, offset, limit int64) ([]byte, int64, string, error) {
	if limit < 1 {
		return nil, 0, "", fmt.Errorf("limit must be at least 1")
	}
	if limit > 262144 {
		limit = 262144
	}

	var contentType string
	var contentID string

	if _, err := fmt.Sscanf(ref, "%[^:]:%s", &contentType, &contentID); err != nil {
		return nil, 0, "", fmt.Errorf("parse ref: %w", err)
	}

	var fullContent string
	var fullSHA256 string

	switch contentType {
	case "msg":
		err := tx.tx.QueryRow(`
			SELECT content, content_hash FROM messages WHERE id = ?
		`, contentID).Scan(&fullContent, &fullSHA256)

		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", ErrNoSuchContent
		}
		if err != nil {
			return nil, 0, "", fmt.Errorf("query message: %w", err)
		}

	case "attempt":
		err := tx.tx.QueryRow(`
			SELECT COALESCE(raw_output, ''),
			       CASE WHEN raw_output IS NOT NULL THEN lower(hex(sha256(raw_output))) ELSE '' END
			FROM attempts WHERE id = ?
		`, contentID).Scan(&fullContent, &fullSHA256)

		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", ErrNoSuchContent
		}
		if err != nil {
			return nil, 0, "", fmt.Errorf("query attempt: %w", err)
		}

	case "candidate":
		err := tx.tx.QueryRow(`
			SELECT content, content_hash FROM candidates WHERE id = ?
		`, contentID).Scan(&fullContent, &fullSHA256)

		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", ErrNoSuchContent
		}
		if err != nil {
			return nil, 0, "", fmt.Errorf("query candidate: %w", err)
		}

	default:
		return nil, 0, "", ErrNoSuchContent
	}

	fullBytes := []byte(fullContent)
	total := int64(len(fullBytes))

	if offset > total {
		return nil, 0, "", fmt.Errorf("offset beyond total")
	}

	end := offset + limit
	if end > total {
		end = total
	}

	data := fullBytes[offset:end]

	if fullSHA256 == "" {
		h := sha256.Sum256(fullBytes)
		fullSHA256 = hex.EncodeToString(h[:])
	}

	return data, total, fullSHA256, nil
}
