package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	Round       int
	Supersedes  *string
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
	var supersedes sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT id, turn_id, content_hash, LENGTH(CAST(content AS BLOB)), round, supersedes
		FROM candidates WHERE id = ?
	`, candidateID).Scan(&row.ID, &row.TurnID, &row.ContentHash, &row.ByteLen, &row.Round, &supersedes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("candidate not found: %w", ErrNoSuchContent)
		}
		return nil, fmt.Errorf("query candidate: %w", err)
	}
	if supersedes.Valid {
		row.Supersedes = &supersedes.String
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

const (
	transcriptMaxLimit = 200
	contentMaxLimit    = 262144
)

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

type transcriptRef struct {
	kind  string
	id    string
	after string // cursor that resumes just after this row
}

// parseTranscriptCursor returns the section ("legacy" or "seq") and position
// encoded in cursor. "legacy:n" means n pre-journal_seq rows were consumed;
// "seq:n" means rows up to journal_seq n were consumed.
func parseTranscriptCursor(cursor string) (section string, pos int64, err error) {
	if cursor == "" {
		return "legacy", 0, nil
	}
	sec, num, ok := strings.Cut(cursor, ":")
	if ok && (sec == "legacy" || sec == "seq") {
		n, perr := strconv.ParseInt(num, 10, 64)
		if perr == nil && n >= 0 {
			return sec, n, nil
		}
	}
	return "", 0, fmt.Errorf("invalid transcript cursor %q", cursor)
}

// TranscriptPage returns a page of transcript items for a turn in journal order.
// Rows written before migration 005 (NULL journal_seq) come first: messages by
// sequence, then attempts by dispatched_at and rowid. All other rows follow in
// ascending journal_seq.
func (tx *Tx) TranscriptPage(turnID, cursor string, limit int) ([]TranscriptItem, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > transcriptMaxLimit {
		limit = transcriptMaxLimit
	}
	section, pos, err := parseTranscriptCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	want := limit + 1 // one lookahead row tells us whether another page exists
	var refs []transcriptRef

	if section == "legacy" {
		rows, err := tx.tx.Query(`
			SELECT kind, id FROM (
				SELECT 0 AS grp, 'message' AS kind, id, sequence AS k1, '' AS k2, rowid AS k3
				FROM messages WHERE turn_id = ? AND journal_seq IS NULL
				UNION ALL
				SELECT 1, 'attempt', id, 0, dispatched_at, rowid
				FROM attempts WHERE turn_id = ? AND journal_seq IS NULL
			)
			ORDER BY grp, k1, k2, k3
			LIMIT ? OFFSET ?
		`, turnID, turnID, want, pos)
		if err != nil {
			return nil, "", fmt.Errorf("query legacy transcript: %w", err)
		}
		offset := pos
		for rows.Next() {
			var r transcriptRef
			if err := rows.Scan(&r.kind, &r.id); err != nil {
				_ = rows.Close()
				return nil, "", fmt.Errorf("scan transcript row: %w", err)
			}
			offset++
			r.after = fmt.Sprintf("legacy:%d", offset)
			refs = append(refs, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, "", fmt.Errorf("iterate transcript: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, "", fmt.Errorf("close transcript rows: %w", err)
		}
		pos = 0 // continue into the journal_seq section from the start
	}

	if len(refs) < want {
		rows, err := tx.tx.Query(`
			SELECT kind, id, journal_seq FROM (
				SELECT 'message' AS kind, id, journal_seq FROM messages WHERE turn_id = ? AND journal_seq > ?
				UNION ALL
				SELECT 'attempt', id, journal_seq FROM attempts WHERE turn_id = ? AND journal_seq > ?
			)
			ORDER BY journal_seq
			LIMIT ?
		`, turnID, pos, turnID, pos, want-len(refs))
		if err != nil {
			return nil, "", fmt.Errorf("query transcript: %w", err)
		}
		for rows.Next() {
			var r transcriptRef
			var seq int64
			if err := rows.Scan(&r.kind, &r.id, &seq); err != nil {
				_ = rows.Close()
				return nil, "", fmt.Errorf("scan transcript row: %w", err)
			}
			r.after = fmt.Sprintf("seq:%d", seq)
			refs = append(refs, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, "", fmt.Errorf("iterate transcript: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, "", fmt.Errorf("close transcript rows: %w", err)
		}
	}

	nextCursor := ""
	if len(refs) > limit {
		refs = refs[:limit]
		nextCursor = refs[limit-1].after
	}

	items := make([]TranscriptItem, 0, len(refs))
	for _, r := range refs {
		item, err := tx.transcriptItem(r)
		if err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	return items, nextCursor, nil
}

func (tx *Tx) transcriptItem(r transcriptRef) (TranscriptItem, error) {
	if r.kind == "message" {
		item := TranscriptItem{Kind: "message", Ref: "msg:" + r.id}
		if err := tx.tx.QueryRow(`
			SELECT phase, sender, content_hash, LENGTH(CAST(content AS BLOB)) FROM messages WHERE id = ?
		`, r.id).Scan(&item.Phase, &item.Sender, &item.SHA256, &item.ByteLen); err != nil {
			return TranscriptItem{}, fmt.Errorf("query message for transcript: %w", err)
		}
		return item, nil
	}

	attempt, err := tx.GetAttempt(r.id)
	if err != nil {
		return TranscriptItem{}, fmt.Errorf("get attempt for transcript: %w", err)
	}
	item := TranscriptItem{
		Kind:   "attempt",
		Ref:    "attempt:" + r.id,
		At:     attempt.DispatchedAt,
		Phase:  attempt.Phase,
		Sender: attempt.Participant,
	}
	if attempt.RawOutput != nil {
		body := []byte(*attempt.RawOutput)
		item.SHA256 = computeSHA256(body)
		item.ByteLen = int64(len(body))
	}
	attempt.RawOutput = nil
	item.Attempt = attempt
	return item, nil
}

// ContentRange returns a byte range of content from a message, attempt output, or candidate.
// ref is "msg:<id>", "attempt:<id>", "candidate:<id>", or a review content ref:
// "commentary:<attempt id>", "evidence:<block id>", "remedy:<block id>", "record:<verdict id>",
// or "tool:<sha256>" for tool call, result, observation and staged content.
// offset and limit are in raw bytes. limit is clamped to 262144.
// total and sha256 describe the whole body, not the returned range.
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
	if limit > contentMaxLimit {
		limit = contentMaxLimit
	}
	if offset < 0 {
		return nil, 0, "", fmt.Errorf("offset must be >= 0: %w", ErrOutOfRange)
	}

	kind, id, _ := strings.Cut(ref, ":")
	var query string
	switch kind {
	case "msg":
		query = `SELECT content FROM messages WHERE id = ?`
	case "attempt":
		query = `SELECT COALESCE(raw_output, '') FROM attempts WHERE id = ?`
	case "candidate":
		query = `SELECT content FROM candidates WHERE id = ?`
	case "commentary":
		query = `SELECT COALESCE(commentary, '') FROM attempts WHERE id = ?`
	case "evidence":
		query = `SELECT evidence FROM blocks WHERE id = ?`
	case "remedy":
		query = `SELECT remedy FROM blocks WHERE id = ?`
	case "record":
		query = `SELECT record FROM review_records WHERE verdict_id = ?`
	case "tool":
		query = `SELECT body FROM tool_content WHERE sha256 = ?`
	default:
		return nil, 0, "", fmt.Errorf("unknown content ref %q: %w", ref, ErrNoSuchContent)
	}

	var content string
	err := sql.ErrNoRows
	switch kind {
	case "commentary", "evidence", "remedy":
		// A ref a review record cites is served from the bytes it froze.
		var frozen []byte
		if err = tx.tx.QueryRow(`SELECT body FROM review_content WHERE ref = ?`, ref).Scan(&frozen); err == nil {
			content = string(frozen)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.tx.QueryRow(query, id).Scan(&content)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", fmt.Errorf("content %q: %w", ref, ErrNoSuchContent)
		}
		return nil, 0, "", fmt.Errorf("query content: %w", err)
	}

	body := []byte(content)
	total := int64(len(body))
	sum := computeSHA256(body)

	if offset > total {
		return nil, 0, "", fmt.Errorf("offset %d beyond content length %d: %w", offset, total, ErrOutOfRange)
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return append([]byte{}, body[offset:end]...), total, sum, nil
}
