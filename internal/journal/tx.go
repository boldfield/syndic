package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Tx wraps a database transaction and provides the same operations as Store.
type Tx struct {
	tx    *sql.Tx
	store *Store
}

// Tx runs all operations in fn inside a single transaction.
// The store holds a mutex for the duration of each Tx, so two goroutines calling Tx
// at once run one after the other. fn must use the *Tx it is given; calling a Store
// method from inside fn deadlocks. If fn returns an error, the transaction is rolled back
// and the error is returned unwrapped. If fn panics, the transaction is rolled back and
// the panic is re-raised.
func (s *Store) Tx(fn func(tx *Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	wrapper := &Tx{tx: tx, store: s}
	var panicVal interface{}

	defer func() {
		if panicVal != nil {
			_ = tx.Rollback()
			panic(panicVal)
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()

	fnErr := fn(wrapper)
	if fnErr != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(fnErr, rbErr)
		}
		return fnErr
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// CreateRun creates a new run with the given manifest, returning the run ID and manifest hash.
// The manifest must be valid JSON; the hash is computed by the store.
func (tx *Tx) CreateRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	if !json.Valid(manifest) {
		return "", "", fmt.Errorf("invalid JSON manifest")
	}

	runID, err = generateID()
	if err != nil {
		return "", "", err
	}

	manifestHash = computeSHA256(manifest)

	if _, err := tx.tx.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, ?, ?)
	`, runID, string(manifest), manifestHash); err != nil {
		return "", "", fmt.Errorf("insert run: %w", err)
	}

	return runID, manifestHash, nil
}

// CreateTurn creates a new turn for the given run, returning the turn ID.
func (tx *Tx) CreateTurn(runID, mode string, deadline *time.Time) (turnID string, err error) {
	turnID, err = generateID()
	if err != nil {
		return "", err
	}

	var deadlineStr *string
	if deadline != nil {
		ds := deadline.Format(time.RFC3339)
		deadlineStr = &ds
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO turns (id, run_id, mode, whole_turn_deadline)
		VALUES (?, ?, ?, ?)
	`, turnID, runID, mode, deadlineStr); err != nil {
		return "", fmt.Errorf("insert turn: %w", err)
	}

	return turnID, nil
}

// SetTurnTerminal sets the terminal state of a turn. It rejects if the turn already has a terminal state.
func (tx *Tx) SetTurnTerminal(turnID, state string) error {
	var existingState sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT terminal_state FROM turns WHERE id = ?
	`, turnID).Scan(&existingState); err != nil {
		return fmt.Errorf("query turn state: %w", err)
	}

	if existingState.Valid {
		return fmt.Errorf("turn already has terminal state: %s", existingState.String)
	}

	if _, err := tx.tx.Exec(`
		UPDATE turns SET terminal_state = ? WHERE id = ?
	`, state, turnID); err != nil {
		return fmt.Errorf("update turn state: %w", err)
	}

	return nil
}

// AppendMessage appends a message to a turn, computing the hash and sequence number.
func (tx *Tx) AppendMessage(turnID, phase, sender string, recipients []string, content []byte, parents []string) (msgID string, sha256 string, byteLen int, err error) {
	msgID, err = generateID()
	if err != nil {
		return "", "", 0, err
	}

	sha256 = computeSHA256(content)
	byteLen = len(content)

	recipientSet, err := json.Marshal(recipients)
	if err != nil {
		return "", "", 0, fmt.Errorf("marshal recipients: %w", err)
	}

	var parentIDs *string
	if len(parents) > 0 {
		pJSON, err := json.Marshal(parents)
		if err != nil {
			return "", "", 0, fmt.Errorf("marshal parents: %w", err)
		}
		s := string(pJSON)
		parentIDs = &s
	}

	var nextSeq int
	if err := tx.tx.QueryRow(`
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM messages WHERE turn_id = ?
	`, turnID).Scan(&nextSeq); err != nil {
		return "", "", 0, fmt.Errorf("get next sequence: %w", err)
	}

	journalSeq, err := tx.nextJournalSeq()
	if err != nil {
		return "", "", 0, err
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, parent_ids, journal_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msgID, turnID, phase, sender, string(recipientSet), nextSeq, string(content), sha256, byteLen, parentIDs, journalSeq); err != nil {
		return "", "", 0, fmt.Errorf("insert message: %w", err)
	}

	return msgID, sha256, byteLen, nil
}

// BeginAttempt creates a pending attempt.
func (tx *Tx) BeginAttempt(turnID, participant, phase string, epoch int, deadline *time.Time, repairOf string) (attemptID string, err error) {
	attemptID, err = generateID()
	if err != nil {
		return "", err
	}

	var deadlineStr *string
	if deadline != nil {
		ds := deadline.Format(time.RFC3339)
		deadlineStr = &ds
	}

	var repairOfPtr *string
	if repairOf != "" {
		repairOfPtr = &repairOf
	}

	dispatchedAt := time.Now().UTC().Format(time.RFC3339)

	journalSeq, err := tx.nextJournalSeq()
	if err != nil {
		return "", err
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, repair_of, journal_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
	`, attemptID, turnID, participant, phase, epoch, dispatchedAt, deadlineStr, repairOfPtr, journalSeq); err != nil {
		return "", fmt.Errorf("insert attempt: %w", err)
	}

	return attemptID, nil
}

// CompleteAttempt sets an attempt's output and state to completed.
// It rejects if the attempt is not in pending state.
func (tx *Tx) CompleteAttempt(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) error {
	var currentState string
	if err := tx.tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState != "pending" {
		return fmt.Errorf("attempt not in pending state: %s", currentState)
	}

	if _, err := tx.tx.Exec(`
		UPDATE attempts SET state = 'completed', raw_output = ?, scrub_count = ?, observed_identity = ?, status = ?, native_finish = ?, transport_status = ?
		WHERE id = ?
	`, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	return nil
}

// QuarantineLateOutput sets an attempt to completed with the same output as CompleteAttempt, but marks it quarantined.
// It accepts pending or unknown state and rejects other states.
func (tx *Tx) QuarantineLateOutput(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) error {
	var currentState string
	if err := tx.tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState == "completed" {
		return fmt.Errorf("attempt already completed: cannot quarantine late output")
	}

	if _, err := tx.tx.Exec(`
		UPDATE attempts SET state = 'completed', raw_output = ?, scrub_count = ?, observed_identity = ?, status = ?, native_finish = ?, transport_status = ?, quarantined = 1
		WHERE id = ?
	`, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	return nil
}

// SetCancellation sets the cancellation state of an attempt.
func (tx *Tx) SetCancellation(attemptID, cancellationState string) error {
	if _, err := tx.tx.Exec(`
		UPDATE attempts SET cancellation_state = ? WHERE id = ?
	`, cancellationState, attemptID); err != nil {
		return fmt.Errorf("update cancellation state: %w", err)
	}

	return nil
}

// MarkUnknown sets an attempt's state to unknown.
// It rejects if the attempt is not in pending state.
func (tx *Tx) MarkUnknown(attemptID string) error {
	var currentState string
	if err := tx.tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState != "pending" {
		return fmt.Errorf("attempt not in pending state: %s", currentState)
	}

	if _, err := tx.tx.Exec(`
		UPDATE attempts SET state = 'unknown' WHERE id = ?
	`, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	return nil
}

// RecoverPending returns all attempts currently in pending state.
func (tx *Tx) RecoverPending() ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT id FROM attempts WHERE state = 'pending'
	`)
	if err != nil {
		return nil, fmt.Errorf("query pending attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attemptIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan attempt id: %w", err)
		}
		attemptIDs = append(attemptIDs, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return attemptIDs, nil
}

// AppendEvent appends an event not tied to any turn (events.turn_id is NULL).
func (tx *Tx) AppendEvent(kind string, payload json.RawMessage) error {
	// Allocate next sequence number from the store's counter, same as Emit.
	tx.store.nextSeq++
	seq := tx.store.nextSeq

	if _, err := tx.tx.Exec(`
		INSERT INTO events (id, event_type, data)
		VALUES (?, ?, ?)
	`, seq, kind, string(payload)); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	return nil
}

// GetAttempt retrieves an attempt by ID.
func (tx *Tx) GetAttempt(attemptID string) (*AttemptRow, error) {
	var row AttemptRow
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

	if err := tx.tx.QueryRow(`
		SELECT id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status, cancellation_state, quarantined, raw_output, scrub_count, observed_identity, native_finish, transport_status, repair_of, gate_error, commentary, delimiter, max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion, retry_count
		FROM attempts WHERE id = ?
	`, attemptID).Scan(
		&row.ID, &row.TurnID, &row.Participant, &row.Phase, &row.Epoch, &dispatchedAtStr, &deadlineStr, &row.State,
		&status, &row.CancellationState, &row.Quarantined, &row.RawOutput, &scrubCount, &observedIdentity, &nativeFinish, &transportStatus, &repairOf,
		&gateError, &commentary, &delimiter, &maxOutputTokens, &estimatedPromptTokens, &usagePrompt, &usageCompletion, &retryCount,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("attempt not found")
		}
		return nil, fmt.Errorf("query attempt: %w", err)
	}

	t, err := parseTime(dispatchedAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse dispatched_at: %w", err)
	}
	row.DispatchedAt = t

	if deadlineStr.Valid {
		t, err := parseTime(deadlineStr.String)
		if err != nil {
			return nil, fmt.Errorf("parse deadline: %w", err)
		}
		row.Deadline = &t
	}

	if status.Valid {
		row.Status = &status.String
	}
	if scrubCount.Valid {
		sc := int(scrubCount.Int64)
		row.ScrubCount = &sc
	}
	if observedIdentity.Valid {
		row.ObservedIdentity = &observedIdentity.String
	}
	if nativeFinish.Valid {
		row.NativeFinish = &nativeFinish.String
	}
	if transportStatus.Valid {
		row.TransportStatus = &transportStatus.String
	}
	if repairOf.Valid {
		row.RepairOf = &repairOf.String
	}
	if gateError.Valid {
		row.GateError = &gateError.String
	}
	if commentary.Valid {
		row.Commentary = &commentary.String
	}
	if delimiter.Valid {
		row.Delimiter = &delimiter.String
	}
	if maxOutputTokens.Valid {
		mot := int(maxOutputTokens.Int64)
		row.MaxOutputTokens = &mot
	}
	if estimatedPromptTokens.Valid {
		ept := int(estimatedPromptTokens.Int64)
		row.EstimatedPromptTokens = &ept
	}
	if usagePrompt.Valid {
		up := int(usagePrompt.Int64)
		row.UsagePrompt = &up
	}
	if usageCompletion.Valid {
		uc := int(usageCompletion.Int64)
		row.UsageCompletion = &uc
	}
	if retryCount.Valid {
		rc := int(retryCount.Int64)
		row.RetryCount = &rc
	}

	return &row, nil
}

// GetRun retrieves a run by ID.
func (tx *Tx) GetRun(runID string) (*RunRow, error) {
	var row RunRow
	var manifestStr string
	var createdAtStr string

	if err := tx.tx.QueryRow(`
		SELECT id, created_at, manifest, manifest_hash
		FROM runs WHERE id = ?
	`, runID).Scan(&row.ID, &createdAtStr, &manifestStr, &row.ManifestHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("run not found")
		}
		return nil, fmt.Errorf("query run: %w", err)
	}

	row.Manifest = json.RawMessage(manifestStr)

	t, err := parseTime(createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	row.CreatedAt = t

	return &row, nil
}

// CreateCandidate stores a new candidate with a caller-provided hash.
func (tx *Tx) CreateCandidate(turnID, content, hash string) (candidateID string, err error) {
	candidateID, err = generateID()
	if err != nil {
		return "", err
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO candidates (id, turn_id, content, content_hash)
		VALUES (?, ?, ?, ?)
	`, candidateID, turnID, content, hash); err != nil {
		return "", fmt.Errorf("insert candidate: %w", err)
	}

	return candidateID, nil
}

// RecordVerdict stores a verdict from an attempt.
func (tx *Tx) RecordVerdict(attemptID, echoedCandidateHash, parsed, reason, validationStatus string) (verdictID string, err error) {
	verdictID, err = generateID()
	if err != nil {
		return "", err
	}

	var parsedVerdict *string
	if parsed != "" {
		parsedVerdict = &parsed
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, reason, validation_status)
		VALUES (?, ?, ?, ?, ?, ?)
	`, verdictID, attemptID, echoedCandidateHash, parsedVerdict, reason, validationStatus); err != nil {
		return "", fmt.Errorf("insert verdict: %w", err)
	}

	return verdictID, nil
}

// OpenBlock opens a block from a verdict. Rejects if the verdict's parsed value is not block.
func (tx *Tx) OpenBlock(verdictID, blockID, candidateHash, severity, evidence, remedy string) error {
	var parsedVerdict sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT parsed_verdict FROM verdicts WHERE id = ?
	`, verdictID).Scan(&parsedVerdict); err != nil {
		return fmt.Errorf("query verdict: %w", err)
	}

	if !parsedVerdict.Valid || parsedVerdict.String != "block" {
		return fmt.Errorf("verdict is not a block")
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state)
		VALUES (?, ?, ?, ?, ?, ?, 'open')
	`, blockID, verdictID, candidateHash, severity, evidence, remedy); err != nil {
		return fmt.Errorf("insert block: %w", err)
	}

	return nil
}

// WithdrawBlock withdraws a block against a new candidate hash.
func (tx *Tx) WithdrawBlock(blockID, againstCandidateHash string) error {
	if againstCandidateHash == "" {
		return fmt.Errorf("againstCandidateHash cannot be empty")
	}

	var state string
	var originalHash string
	if err := tx.tx.QueryRow(`
		SELECT state, candidate_hash FROM blocks WHERE id = ?
	`, blockID).Scan(&state, &originalHash); err != nil {
		return fmt.Errorf("query block: %w", err)
	}

	if state != "open" {
		return fmt.Errorf("block is not in open state: %s", state)
	}

	if againstCandidateHash == originalHash {
		return fmt.Errorf("cannot withdraw block against the same candidate hash")
	}

	if _, err := tx.tx.Exec(`
		UPDATE blocks SET state = 'withdrawn', withdrawn_against_hash = ? WHERE id = ?
	`, againstCandidateHash, blockID); err != nil {
		return fmt.Errorf("update block: %w", err)
	}

	return nil
}

// OverrideBlock overrides a block. Rejects unless the block is open.
func (tx *Tx) OverrideBlock(blockID string) error {
	var state string
	if err := tx.tx.QueryRow(`
		SELECT state FROM blocks WHERE id = ?
	`, blockID).Scan(&state); err != nil {
		return fmt.Errorf("query block: %w", err)
	}

	if state != "open" {
		return fmt.Errorf("block is not in open state: %s", state)
	}

	if _, err := tx.tx.Exec(`
		UPDATE blocks SET state = 'overridden' WHERE id = ?
	`, blockID); err != nil {
		return fmt.Errorf("update block: %w", err)
	}

	return nil
}

// OpenBlocks returns blocks in open state for a turn, across all its candidates.
func (tx *Tx) OpenBlocks(turnID string) ([]Block, error) {
	rows, err := tx.tx.Query(`
		SELECT b.id, b.verdict_id, b.candidate_hash, b.severity, b.evidence, b.remedy, b.state, b.reviewer_block_id
		FROM blocks b
		JOIN verdicts v ON b.verdict_id = v.id
		JOIN attempts a ON v.attempt_id = a.id
		WHERE a.turn_id = ? AND b.state = 'open'
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
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return blocks, nil
}

// nextJournalSeq takes the next value of the journal-wide ordering counter and
// advances it. It runs inside the caller's transaction, so a rollback leaves
// the counter unchanged.
func (tx *Tx) nextJournalSeq() (int64, error) {
	var seq int64
	if err := tx.tx.QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("get journal sequence: %w", err)
	}
	if _, err := tx.tx.Exec(`UPDATE journal_counter SET next = next + 1 WHERE id = 1`); err != nil {
		return 0, fmt.Errorf("increment journal counter: %w", err)
	}
	return seq, nil
}

// PendingTurnsWithoutTerminal returns all turn IDs that have no terminal state.
func (tx *Tx) PendingTurnsWithoutTerminal() ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT id FROM turns WHERE terminal_state IS NULL ORDER BY rowid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var turnIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan turn id: %w", err)
		}
		turnIDs = append(turnIDs, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return turnIDs, nil
}

// GetAttemptTurnAndRun returns the turn_id and run_id for an attempt.
func (tx *Tx) GetAttemptTurnAndRun(attemptID string) (turnID, runID string, err error) {
	if err := tx.tx.QueryRow(`
		SELECT a.turn_id, t.run_id FROM attempts a
		JOIN turns t ON a.turn_id = t.id
		WHERE a.id = ?
	`, attemptID).Scan(&turnID, &runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("attempt not found")
		}
		return "", "", fmt.Errorf("query attempt: %w", err)
	}
	return turnID, runID, nil
}
