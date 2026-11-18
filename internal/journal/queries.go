package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNoSuchContent is returned when a ref cannot be found or resolved to content.
var ErrNoSuchContent = errors.New("no such content")

// ErrOutOfRange is returned when the offset is outside the content range.
var ErrOutOfRange = errors.New("offset out of range")

// VerdictRow represents a verdict from an attempt.
type VerdictRow struct {
	ID               string
	AttemptID        string
	CandidateHash    string
	Parsed           string
	Reason           string
	ValidationStatus string
}

// ListVerdicts returns all verdicts for a turn's attempts.
func (s *Store) ListVerdicts(turnID string) ([]VerdictRow, error) {
	var verdicts []VerdictRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		verdicts, txErr = tx.ListVerdicts(turnID)
		return txErr
	})
	return verdicts, err
}

// ListVerdicts returns all verdicts for a turn's attempts.
func (tx *Tx) ListVerdicts(turnID string) ([]VerdictRow, error) {
	rows, err := tx.tx.Query(`
		SELECT v.id, v.attempt_id, v.candidate_hash, COALESCE(v.parsed_verdict, ''), COALESCE(v.reason, ''), v.validation_status
		FROM verdicts v
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ?
		ORDER BY v.id
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query verdicts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var verdicts []VerdictRow
	for rows.Next() {
		var v VerdictRow
		if err := rows.Scan(&v.ID, &v.AttemptID, &v.CandidateHash, &v.Parsed, &v.Reason, &v.ValidationStatus); err != nil {
			return nil, fmt.Errorf("scan verdict: %w", err)
		}
		verdicts = append(verdicts, v)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verdicts: %w", err)
	}

	return verdicts, nil
}

// ListAttempts returns the turn's attempts in dispatch order (by dispatched_at, then insertion order).
func (s *Store) ListAttempts(turnID string) ([]AttemptRow, error) {
	var attempts []AttemptRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		attempts, txErr = tx.ListAttempts(turnID)
		return txErr
	})
	return attempts, err
}

// ListAttempts returns the turn's attempts in dispatch order (by dispatched_at, then insertion order).
func (tx *Tx) ListAttempts(turnID string) ([]AttemptRow, error) {
	rows, err := tx.tx.Query(`
		SELECT id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status, cancellation_state, quarantined, raw_output, scrub_count, observed_identity, native_finish, transport_status, repair_of, gate_error, commentary, delimiter, max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion, retry_count
		FROM attempts
		WHERE turn_id = ?
		ORDER BY dispatched_at, rowid
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attempts []AttemptRow
	for rows.Next() {
		var a AttemptRow
		var dispatchedAtStr string
		var deadlineStr sql.NullString
		var status sql.NullString
		var scrubCount sql.NullInt64
		var observedIdentity sql.NullString
		var nativeFinish sql.NullString
		var transportStatus sql.NullString
		var repairOf sql.NullString
		var gateError sql.NullString
		var commentary sql.NullString
		var delimiter sql.NullString
		var maxOutputTokens sql.NullInt64
		var estimatedPromptTokens sql.NullInt64
		var usagePrompt sql.NullInt64
		var usageCompletion sql.NullInt64
		var retryCount sql.NullInt64

		if err := rows.Scan(
			&a.ID, &a.TurnID, &a.Participant, &a.Phase, &a.Epoch, &dispatchedAtStr, &deadlineStr, &a.State,
			&status, &a.CancellationState, &a.Quarantined, &a.RawOutput, &scrubCount, &observedIdentity, &nativeFinish, &transportStatus, &repairOf,
			&gateError, &commentary, &delimiter, &maxOutputTokens, &estimatedPromptTokens, &usagePrompt, &usageCompletion, &retryCount,
		); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}

		t, err := parseTime(dispatchedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse dispatched_at: %w", err)
		}
		a.DispatchedAt = t

		if deadlineStr.Valid {
			t, err := parseTime(deadlineStr.String)
			if err != nil {
				return nil, fmt.Errorf("parse deadline: %w", err)
			}
			a.Deadline = &t
		}
		if status.Valid {
			a.Status = &status.String
		}
		if scrubCount.Valid {
			sc := int(scrubCount.Int64)
			a.ScrubCount = &sc
		}
		if observedIdentity.Valid {
			a.ObservedIdentity = &observedIdentity.String
		}
		if nativeFinish.Valid {
			a.NativeFinish = &nativeFinish.String
		}
		if transportStatus.Valid {
			a.TransportStatus = &transportStatus.String
		}
		if repairOf.Valid {
			a.RepairOf = &repairOf.String
		}
		if gateError.Valid {
			a.GateError = &gateError.String
		}
		if commentary.Valid {
			a.Commentary = &commentary.String
		}
		if delimiter.Valid {
			a.Delimiter = &delimiter.String
		}
		if maxOutputTokens.Valid {
			mot := int(maxOutputTokens.Int64)
			a.MaxOutputTokens = &mot
		}
		if estimatedPromptTokens.Valid {
			ept := int(estimatedPromptTokens.Int64)
			a.EstimatedPromptTokens = &ept
		}
		if usagePrompt.Valid {
			up := int(usagePrompt.Int64)
			a.UsagePrompt = &up
		}
		if usageCompletion.Valid {
			uc := int(usageCompletion.Int64)
			a.UsageCompletion = &uc
		}
		if retryCount.Valid {
			rc := int(retryCount.Int64)
			a.RetryCount = &rc
		}

		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}

	return attempts, nil
}

// ListBlocks returns all blocks for a turn, in every state (open, withdrawn, overridden).
func (s *Store) ListBlocks(turnID string) ([]Block, error) {
	var blocks []Block
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.ListBlocks(turnID)
		return txErr
	})
	return blocks, err
}

// ListBlocks returns all blocks for a turn, in every state (open, withdrawn, overridden).
func (tx *Tx) ListBlocks(turnID string) ([]Block, error) {
	rows, err := tx.tx.Query(`
		SELECT b.id, b.verdict_id, b.candidate_hash, b.severity, b.evidence, b.remedy, b.state, b.reviewer_block_id
		FROM blocks b
		JOIN verdicts v ON b.verdict_id = v.id
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ?
		ORDER BY b.id
	`, turnID)
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
		return nil, fmt.Errorf("iterate blocks: %w", err)
	}

	return blocks, nil
}

// CandidateRow represents a row from the candidates table.
type CandidateRow struct {
	ID          string
	TurnID      string
	ContentHash string
	ByteLen     int64
}

// GetCandidate returns a candidate by ID.
func (s *Store) GetCandidate(candidateID string) (*CandidateRow, error) {
	var row *CandidateRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetCandidate(candidateID)
		return txErr
	})
	return row, err
}

// GetCandidate returns a candidate by ID.
func (tx *Tx) GetCandidate(candidateID string) (*CandidateRow, error) {
	var row CandidateRow
	if err := tx.tx.QueryRow(`
		SELECT id, turn_id, content_hash, LENGTH(CAST(content AS BLOB))
		FROM candidates WHERE id = ?
	`, candidateID).Scan(&row.ID, &row.TurnID, &row.ContentHash, &row.ByteLen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("candidate not found: %w", ErrNoSuchContent)
		}
		return nil, fmt.Errorf("query candidate: %w", err)
	}
	return &row, nil
}

// TranscriptItem represents a message or attempt in the transcript.
type TranscriptItem struct {
	Kind    string
	Ref     string
	At      time.Time
	Phase   string
	Sender  string
	SHA256  string
	ByteLen int64
	Attempt *AttemptRow
}

// TranscriptPage returns a page of transcript items for a turn in journal order.
// limit is clamped to 1-200. cursor is opaque; empty for the first page.
func (s *Store) TranscriptPage(turnID, cursor string, limit int) (items []TranscriptItem, nextCursor string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		items, nextCursor, txErr = tx.TranscriptPage(turnID, cursor, limit)
		return txErr
	})
	return items, nextCursor, err
}

// TranscriptPage returns a page of transcript items for a turn in journal order.
func (tx *Tx) TranscriptPage(turnID, cursor string, limit int) ([]TranscriptItem, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	type rawRow struct {
		kind            string
		id              string
		rowid           int64
		phase           string
		sender          string
		sha256          string
		byteLen         int64
		journalSeq      sql.NullInt64
		sequence        sql.NullInt64
		dispatchedAtStr sql.NullString
		rawOutput       sql.NullString
		isNullRow       bool
	}

	var allRows []rawRow

	rows, err := tx.tx.Query(`
		WITH combined AS (
			SELECT 'message' as kind, m.id, m.rowid, m.phase, m.sender, m.content_hash as sha256, m.byte_length, m.journal_seq, m.sequence, NULL as dispatched_at, NULL as raw_output, 1 as is_null_row
			FROM messages m
			WHERE m.turn_id = ? AND m.journal_seq IS NULL
			UNION ALL
			SELECT 'attempt' as kind, a.id, a.rowid, a.phase, a.participant, '', LENGTH(CAST(COALESCE(a.raw_output, '') AS BLOB)), a.journal_seq, NULL, a.dispatched_at, a.raw_output, 1 as is_null_row
			FROM attempts a
			WHERE a.turn_id = ? AND a.journal_seq IS NULL
			UNION ALL
			SELECT 'message' as kind, m.id, m.rowid, m.phase, m.sender, m.content_hash as sha256, m.byte_length, m.journal_seq, m.sequence, NULL as dispatched_at, NULL as raw_output, 0 as is_null_row
			FROM messages m
			WHERE m.turn_id = ? AND m.journal_seq IS NOT NULL
			UNION ALL
			SELECT 'attempt' as kind, a.id, a.rowid, a.phase, a.participant, '', LENGTH(CAST(COALESCE(a.raw_output, '') AS BLOB)), a.journal_seq, NULL, a.dispatched_at, a.raw_output, 0 as is_null_row
			FROM attempts a
			WHERE a.turn_id = ? AND a.journal_seq IS NOT NULL
		)
		SELECT kind, id, rowid, phase, sender, sha256, byte_length, journal_seq, sequence, dispatched_at, raw_output, is_null_row
		FROM combined
		ORDER BY
			is_null_row DESC,
			CASE WHEN is_null_row = 1 AND kind = 'message' THEN 0 WHEN is_null_row = 1 AND kind = 'attempt' THEN 1 ELSE 2 END,
			CASE WHEN is_null_row = 1 AND kind = 'message' THEN COALESCE(sequence, 0) END,
			CASE WHEN is_null_row = 1 AND kind = 'attempt' THEN dispatched_at END,
			CASE WHEN is_null_row = 1 THEN rowid END,
			journal_seq
	`, turnID, turnID, turnID, turnID)

	if err != nil {
		return nil, "", fmt.Errorf("query transcript: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var kind, id, phase, sender, sha256 string
		var rowid int64
		var byteLen int64
		var journalSeq sql.NullInt64
		var sequence sql.NullInt64
		var dispatchedAtStr sql.NullString
		var rawOutput sql.NullString
		var isNullRow int

		if err := rows.Scan(&kind, &id, &rowid, &phase, &sender, &sha256, &byteLen, &journalSeq, &sequence, &dispatchedAtStr, &rawOutput, &isNullRow); err != nil {
			return nil, "", fmt.Errorf("scan transcript row: %w", err)
		}

		// Compute SHA256 for attempts based on raw_output
		if kind == "attempt" && rawOutput.Valid {
			sha256 = computeSHA256([]byte(rawOutput.String))
		}

		allRows = append(allRows, rawRow{
			kind:            kind,
			id:              id,
			rowid:           rowid,
			phase:           phase,
			sender:          sender,
			sha256:          sha256,
			byteLen:         byteLen,
			journalSeq:      journalSeq,
			sequence:        sequence,
			dispatchedAtStr: dispatchedAtStr,
			rawOutput:       rawOutput,
			isNullRow:       isNullRow == 1,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate transcript: %w", err)
	}

	var startIdx int
	if cursor != "" {
		found := false
		for i, row := range allRows {
			var sortKey string
			if row.isNullRow {
				if row.kind == "message" && row.sequence.Valid {
					sortKey = fmt.Sprintf("null:m:%d:%d", row.sequence.Int64, row.rowid)
				} else if row.kind == "attempt" && row.dispatchedAtStr.Valid {
					sortKey = fmt.Sprintf("null:a:%s:%d", row.dispatchedAtStr.String, row.rowid)
				}
			} else if row.journalSeq.Valid {
				sortKey = fmt.Sprintf("seq:%d", row.journalSeq.Int64)
			}
			if sortKey == cursor {
				startIdx = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("unrecognized cursor: %w", fmt.Errorf("cursor %q not found", cursor))
		}
	}

	var items []TranscriptItem
	var nextCursor string

	for i := startIdx; i < len(allRows) && len(items) < limit; i++ {
		row := allRows[i]

		item := TranscriptItem{
			Kind:    row.kind,
			Ref:     row.kind + ":" + row.id,
			Phase:   row.phase,
			Sender:  row.sender,
			SHA256:  row.sha256,
			ByteLen: row.byteLen,
		}

		if row.kind == "attempt" {
			attempt, err := tx.GetAttempt(row.id)
			if err != nil {
				return nil, "", fmt.Errorf("get attempt for transcript: %w", err)
			}
			attempt.RawOutput = nil
			item.Attempt = attempt
			if row.dispatchedAtStr.Valid {
				t, err := parseTime(row.dispatchedAtStr.String)
				if err == nil {
					item.At = t
				}
			}
		}

		items = append(items, item)

		if len(items) == limit && i+1 < len(allRows) {
			var sortKey string
			if row.isNullRow {
				if row.kind == "message" && row.sequence.Valid {
					sortKey = fmt.Sprintf("null:m:%d:%d", row.sequence.Int64, row.rowid)
				} else if row.kind == "attempt" && row.dispatchedAtStr.Valid {
					sortKey = fmt.Sprintf("null:a:%s:%d", row.dispatchedAtStr.String, row.rowid)
				}
			} else if row.journalSeq.Valid {
				sortKey = fmt.Sprintf("seq:%d", row.journalSeq.Int64)
			}
			nextCursor = sortKey
		}
	}

	return items, nextCursor, nil
}

// ContentRange returns a byte range of content from a message, attempt output, or candidate.
// ref is "msg:<id>", "attempt:<id>", or "candidate:<id>".
// offset and limit are in raw bytes. limit is clamped to 262144.
func (s *Store) ContentRange(ref string, offset, limit int64) (data []byte, total int64, sha256 string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		data, total, sha256, txErr = tx.ContentRange(ref, offset, limit)
		return txErr
	})
	return data, total, sha256, err
}

// ContentRange returns a byte range of content.
func (tx *Tx) ContentRange(ref string, offset, limit int64) ([]byte, int64, string, error) {
	if limit < 1 {
		return nil, 0, "", fmt.Errorf("limit must be >= 1")
	}
	if limit > 262144 {
		limit = 262144
	}

	if offset < 0 {
		return nil, 0, "", fmt.Errorf("offset must be >= 0: %w", ErrOutOfRange)
	}

	var content string
	var sha256 string

	if len(ref) >= 4 && ref[:4] == "msg:" {
		msgID := ref[4:]
		if err := tx.tx.QueryRow(`
			SELECT content, content_hash FROM messages WHERE id = ?
		`, msgID).Scan(&content, &sha256); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, 0, "", ErrNoSuchContent
			}
			return nil, 0, "", fmt.Errorf("query message: %w", err)
		}
	} else if len(ref) >= 8 && ref[:8] == "attempt:" {
		attemptID := ref[8:]
		var rawOutput sql.NullString
		if err := tx.tx.QueryRow(`
			SELECT COALESCE(raw_output, '')
			FROM attempts WHERE id = ?
		`, attemptID).Scan(&rawOutput); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, 0, "", ErrNoSuchContent
			}
			return nil, 0, "", fmt.Errorf("query attempt: %w", err)
		}

		if rawOutput.Valid {
			content = rawOutput.String
			sha256 = computeSHA256([]byte(content))
		}
	} else if len(ref) >= 10 && ref[:10] == "candidate:" {
		candidateID := ref[10:]
		if err := tx.tx.QueryRow(`
			SELECT content, content_hash FROM candidates WHERE id = ?
		`, candidateID).Scan(&content, &sha256); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, 0, "", ErrNoSuchContent
			}
			return nil, 0, "", fmt.Errorf("query candidate: %w", err)
		}
	} else {
		return nil, 0, "", ErrNoSuchContent
	}

	contentBytes := []byte(content)
	total := int64(len(contentBytes))

	if offset > total {
		return nil, 0, "", fmt.Errorf("offset beyond content: %w", ErrOutOfRange)
	}

	if offset == total {
		return []byte{}, total, sha256, nil
	}

	end := offset + limit
	if end > total {
		end = total
	}

	data := contentBytes[offset:end]
	return data, total, sha256, nil
}
