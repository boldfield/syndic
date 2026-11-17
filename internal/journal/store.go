package journal

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Store provides the durable-intent and recovery API over the journal schema.
// All methods run in a single transaction.
type Store struct {
	db *DB
}

// NewStore creates a Store from a database handle.
func NewStore(db *DB) *Store {
	return &Store{db: db}
}

func generateID() (string, error) {
	const idLen = 16
	b := make([]byte, idLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	}
	var lastErr error
	for _, format := range formats {
		t, err := time.Parse(format, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("unable to parse time %q: %w", s, lastErr)
}

// CreateRun creates a new run with the given manifest, returning the run ID and manifest hash.
// The manifest must be valid JSON; the hash is computed by the store.
func (s *Store) CreateRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	if !json.Valid(manifest) {
		return "", "", fmt.Errorf("invalid JSON manifest")
	}

	runID, err = generateID()
	if err != nil {
		return "", "", err
	}

	manifestHash = computeSHA256(manifest)

	tx, err := s.db.conn.Begin()
	if err != nil {
		return "", "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	if _, err := tx.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash)
		VALUES (?, ?, ?)
	`, runID, string(manifest), manifestHash); err != nil {
		return "", "", fmt.Errorf("insert run: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit transaction: %w", err)
	}

	return runID, manifestHash, nil
}

// CreateTurn creates a new turn for the given run, returning the turn ID.
func (s *Store) CreateTurn(runID, mode string, deadline *time.Time) (turnID string, err error) {
	turnID, err = generateID()
	if err != nil {
		return "", err
	}

	tx, err := s.db.conn.Begin()
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var deadlineStr *string
	if deadline != nil {
		ds := deadline.Format(time.RFC3339)
		deadlineStr = &ds
	}

	if _, err := tx.Exec(`
		INSERT INTO turns (id, run_id, mode, whole_turn_deadline)
		VALUES (?, ?, ?, ?)
	`, turnID, runID, mode, deadlineStr); err != nil {
		return "", fmt.Errorf("insert turn: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}

	return turnID, nil
}

// SetTurnTerminal sets the terminal state of a turn. It rejects if the turn already has a terminal state.
func (s *Store) SetTurnTerminal(turnID, state string) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var existingState sql.NullString
	if err = tx.QueryRow(`
		SELECT terminal_state FROM turns WHERE id = ?
	`, turnID).Scan(&existingState); err != nil {
		return fmt.Errorf("query turn state: %w", err)
	}

	if existingState.Valid {
		return fmt.Errorf("turn already has terminal state: %s", existingState.String)
	}

	if _, err = tx.Exec(`
		UPDATE turns SET terminal_state = ? WHERE id = ?
	`, state, turnID); err != nil {
		return fmt.Errorf("update turn state: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// AppendMessage appends a message to a turn, computing the hash and sequence number.
func (s *Store) AppendMessage(turnID, phase, sender string, recipients []string, content []byte, parents []string) (msgID string, sha256 string, byteLen int, err error) {
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

	tx, err := s.db.conn.Begin()
	if err != nil {
		return "", "", 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var nextSeq int
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM messages WHERE turn_id = ?
	`, turnID).Scan(&nextSeq); err != nil {
		return "", "", 0, fmt.Errorf("get next sequence: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length, parent_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msgID, turnID, phase, sender, string(recipientSet), nextSeq, string(content), sha256, byteLen, parentIDs); err != nil {
		return "", "", 0, fmt.Errorf("insert message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", 0, fmt.Errorf("commit transaction: %w", err)
	}

	return msgID, sha256, byteLen, nil
}

// BeginAttempt creates a pending attempt. The row is committed before the function returns.
func (s *Store) BeginAttempt(turnID, participant, phase string, epoch int, deadline *time.Time, repairOf string) (attemptID string, err error) {
	attemptID, err = generateID()
	if err != nil {
		return "", err
	}

	tx, err := s.db.conn.Begin()
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

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

	if _, err := tx.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, repair_of)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)
	`, attemptID, turnID, participant, phase, epoch, dispatchedAt, deadlineStr, repairOfPtr); err != nil {
		return "", fmt.Errorf("insert attempt: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}

	return attemptID, nil
}

// CompleteAttempt sets an attempt's output and state to completed.
// It rejects if the attempt is not in pending state.
func (s *Store) CompleteAttempt(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var currentState string
	if err = tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState != "pending" {
		return fmt.Errorf("attempt not in pending state: %s", currentState)
	}

	if _, err = tx.Exec(`
		UPDATE attempts SET state = 'completed', raw_output = ?, scrub_count = ?, observed_identity = ?, status = ?, native_finish = ?, transport_status = ?
		WHERE id = ?
	`, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// QuarantineLateOutput sets an attempt to completed with the same output as CompleteAttempt, but marks it quarantined.
// It rejects if the attempt is not in pending or unknown state.
func (s *Store) QuarantineLateOutput(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var currentState string
	if err = tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState == "completed" {
		return fmt.Errorf("attempt already completed: cannot quarantine late output")
	}

	if _, err = tx.Exec(`
		UPDATE attempts SET state = 'completed', raw_output = ?, scrub_count = ?, observed_identity = ?, status = ?, native_finish = ?, transport_status = ?, quarantined = 1
		WHERE id = ?
	`, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// SetCancellation sets the cancellation state of an attempt.
func (s *Store) SetCancellation(attemptID, cancellationState string) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	if _, err = tx.Exec(`
		UPDATE attempts SET cancellation_state = ? WHERE id = ?
	`, cancellationState, attemptID); err != nil {
		return fmt.Errorf("update cancellation state: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// MarkUnknown sets an attempt's state to unknown.
// It rejects if the attempt is not in pending state.
func (s *Store) MarkUnknown(attemptID string) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	var currentState string
	if err = tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentState); err != nil {
		return fmt.Errorf("query attempt state: %w", err)
	}

	if currentState != "pending" {
		return fmt.Errorf("attempt not in pending state: %s", currentState)
	}

	if _, err = tx.Exec(`
		UPDATE attempts SET state = 'unknown' WHERE id = ?
	`, attemptID); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// RecoverPending returns all attempts currently in pending state.
func (s *Store) RecoverPending() ([]string, error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if errClose := tx.Rollback(); errClose != nil && err == nil {
			err = fmt.Errorf("rollback: %w", errClose)
		}
	}()

	rows, err := tx.Query(`
		SELECT id FROM attempts WHERE state = 'pending'
	`)
	if err != nil {
		return nil, fmt.Errorf("query pending attempts: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil && err == nil {
			err = fmt.Errorf("close rows: %w", errClose)
		}
	}()

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

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return attemptIDs, nil
}

// AppendEvent appends an event to the journal for a turn.
// Note: The schema requires turn_id NOT NULL; spec specifies AppendEvent(kind, payload) but this cannot
// be implemented with the current schema. This is a schema incompatibility that requires resolution.
func (s *Store) AppendEvent(turnID, kind string, payload json.RawMessage) (err error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
			}
		}
	}()

	if _, err = tx.Exec(`
		INSERT INTO events (turn_id, event_type, data)
		VALUES (?, ?, ?)
	`, turnID, kind, string(payload)); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// AttemptRow represents a row from the attempts table.
type AttemptRow struct {
	ID                string
	TurnID            string
	Participant       string
	Phase             string
	Epoch             int
	DispatchedAt      time.Time
	Deadline          *time.Time
	State             string
	Status            *string
	CancellationState string
	Quarantined       bool
	RawOutput         *string
	ScrubCount        *int
	ObservedIdentity  *string
	NativeFinish      *string
	TransportStatus   *string
	RepairOf          *string
}

// GetAttempt retrieves an attempt by ID.
func (s *Store) GetAttempt(attemptID string) (*AttemptRow, error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if errClose := tx.Rollback(); errClose != nil && err == nil {
			err = fmt.Errorf("rollback: %w", errClose)
		}
	}()

	var row AttemptRow
	var dispatchedAtStr string
	var deadlineStr sql.NullString
	var status sql.NullString
	var scrubCount sql.NullInt64
	var observedIdentity sql.NullString
	var nativeFinish sql.NullString
	var transportStatus sql.NullString
	var repairOf sql.NullString

	if err := tx.QueryRow(`
		SELECT id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status, cancellation_state, quarantined, raw_output, scrub_count, observed_identity, native_finish, transport_status, repair_of
		FROM attempts WHERE id = ?
	`, attemptID).Scan(
		&row.ID, &row.TurnID, &row.Participant, &row.Phase, &row.Epoch, &dispatchedAtStr, &deadlineStr, &row.State,
		&status, &row.CancellationState, &row.Quarantined, &row.RawOutput, &scrubCount, &observedIdentity, &nativeFinish, &transportStatus, &repairOf,
	); err != nil {
		if err == sql.ErrNoRows {
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

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &row, nil
}

// RunRow represents a row from the runs table.
type RunRow struct {
	ID           string
	CreatedAt    time.Time
	Manifest     json.RawMessage
	ManifestHash string
}

// GetRun retrieves a run by ID.
func (s *Store) GetRun(runID string) (*RunRow, error) {
	tx, err := s.db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if errClose := tx.Rollback(); errClose != nil && err == nil {
			err = fmt.Errorf("rollback: %w", errClose)
		}
	}()

	var row RunRow
	var manifestStr string
	var createdAtStr string

	if err := tx.QueryRow(`
		SELECT id, created_at, manifest, manifest_hash
		FROM runs WHERE id = ?
	`, runID).Scan(&row.ID, &createdAtStr, &manifestStr, &row.ManifestHash); err != nil {
		if err == sql.ErrNoRows {
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

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &row, nil
}
