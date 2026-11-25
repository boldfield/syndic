package journal

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// ToolCall represents a tool call and its result stored in the journal.
type ToolCall struct {
	ID               string
	AttemptID        string
	Name             string
	ArgsSHA256       string
	Class            string
	Status           string
	ResultSHA256     *string
	ResultByteLen    *int64
	ObservedIdentity *string
	ErrorMessage     *string
}

// ToolCallStatus values as constrained by the schema.
const (
	ToolCallStatusAccepted  = "accepted"
	ToolCallStatusExecuted  = "executed"
	ToolCallStatusError     = "error"
	ToolCallStatusAbandoned = "abandoned"
)

// ToolResult represents the result of a tool call recorded after execution.
type ToolResult struct {
	ID            string
	ToolCallID    string
	Status        string
	ResultSHA256  *string
	ResultByteLen *int64
	ErrorMessage  *string
	RecordedAt    time.Time
}

// Observation represents a file observation (content, absence, or directory listing).
type Observation struct {
	ID              string
	TurnID          string
	AttemptID       string
	SnapshotVersion int
	Path            string
	Kind            string
	BaseSHA256      *string
	EntriesSHA256   *string
	ObservedAt      time.Time
}

// ObservationKind values for different observation types.
const (
	ObservationKindFile      = "f"
	ObservationKindAbsent    = "a"
	ObservationKindDirectory = "d"
)

// ChangeSetState values as constrained by the schema.
const (
	ChangeSetStateAppliable = "appliable"
	ChangeSetStateApplied   = "applied"
	ChangeSetStatePartial   = "partial"
	ChangeSetStateConflict  = "conflict"
	ChangeSetStateDiscarded = "discarded"
)

// ChangeSetPathOutcome values as constrained by the schema.
const (
	ChangeSetPathOutcomePending  = "pending"
	ChangeSetPathOutcomeApplied  = "applied"
	ChangeSetPathOutcomeConflict = "conflict"
)

// ChangeSet represents a change set at candidate freeze.
type ChangeSet struct {
	ID           string
	TurnID       string
	State        string
	SnapshotHash string
	ChangesHash  string
	CreatedAt    time.Time
}

// ChangeSetPath represents a single path in a change set.
type ChangeSetPath struct {
	ID          string
	ChangeSetID string
	Path        string
	Op          string
	BaseSHA256  *string
	NewSHA256   string
	NewLen      int64
	Outcome     string
}

// ChangeSetOp values: create or modify.
const (
	ChangeSetOpCreate = "create"
	ChangeSetOpModify = "modify"
)

// StoreToolCall records a tool call in the journal.
func (tx *Tx) StoreToolCall(call ToolCall) error {
	_, err := tx.tx.Exec(`
		INSERT INTO tool_calls (id, attempt_id, name, args_sha256, class, status, result_sha256, result_byte_len, observed_identity, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, call.ID, call.AttemptID, call.Name, call.ArgsSHA256, call.Class, call.Status,
		call.ResultSHA256, call.ResultByteLen, call.ObservedIdentity, call.ErrorMessage)
	if err != nil {
		return fmt.Errorf("store tool call: %w", err)
	}
	return nil
}

// GetToolCallsByAttempt retrieves all tool calls for an attempt.
func (tx *Tx) GetToolCallsByAttempt(attemptID string) ([]ToolCall, error) {
	rows, err := tx.tx.Query(`
		SELECT id, attempt_id, name, args_sha256, class, status, result_sha256, result_byte_len, observed_identity, error_message
		FROM tool_calls
		WHERE attempt_id = ?
		ORDER BY rowid
	`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var calls []ToolCall
	for rows.Next() {
		var call ToolCall
		if err := rows.Scan(&call.ID, &call.AttemptID, &call.Name, &call.ArgsSHA256, &call.Class,
			&call.Status, &call.ResultSHA256, &call.ResultByteLen, &call.ObservedIdentity, &call.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool calls: %w", err)
	}
	return calls, nil
}

// StoreToolResult records a tool call result in the journal.
func (tx *Tx) StoreToolResult(result ToolResult) error {
	_, err := tx.tx.Exec(`
		INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, error_message, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, result.ID, result.ToolCallID, result.Status, result.ResultSHA256, result.ResultByteLen, result.ErrorMessage, result.RecordedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store tool result: %w", err)
	}
	return nil
}

// GetToolResultsByToolCall retrieves all results for a tool call.
func (tx *Tx) GetToolResultsByToolCall(toolCallID string) ([]ToolResult, error) {
	rows, err := tx.tx.Query(`
		SELECT id, tool_call_id, status, result_sha256, result_byte_len, error_message, recorded_at
		FROM tool_results
		WHERE tool_call_id = ?
		ORDER BY rowid
	`, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("query tool results: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []ToolResult
	for rows.Next() {
		var result ToolResult
		var recordedAtStr string
		if err := rows.Scan(&result.ID, &result.ToolCallID, &result.Status, &result.ResultSHA256, &result.ResultByteLen, &result.ErrorMessage, &recordedAtStr); err != nil {
			return nil, fmt.Errorf("scan tool result: %w", err)
		}
		recordedAt, err := time.Parse(time.RFC3339Nano, recordedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		result.RecordedAt = recordedAt
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool results: %w", err)
	}
	return results, nil
}

// StoreObservation records a file observation in the journal.
func (tx *Tx) StoreObservation(obs Observation) error {
	_, err := tx.tx.Exec(`
		INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, base_sha256, entries_sha256, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, obs.ID, obs.TurnID, obs.AttemptID, obs.SnapshotVersion, obs.Path, obs.Kind,
		obs.BaseSHA256, obs.EntriesSHA256, obs.ObservedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store observation: %w", err)
	}
	return nil
}

// GetObservationsByTurn retrieves all observations for a turn, ordered by snapshot version.
func (tx *Tx) GetObservationsByTurn(turnID string) ([]Observation, error) {
	rows, err := tx.tx.Query(`
		SELECT id, turn_id, attempt_id, snapshot_version, path, kind, base_sha256, entries_sha256, observed_at
		FROM observations
		WHERE turn_id = ?
		ORDER BY snapshot_version, path
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var obss []Observation
	for rows.Next() {
		var obs Observation
		var observedAtStr string
		if err := rows.Scan(&obs.ID, &obs.TurnID, &obs.AttemptID, &obs.SnapshotVersion, &obs.Path,
			&obs.Kind, &obs.BaseSHA256, &obs.EntriesSHA256, &observedAtStr); err != nil {
			return nil, fmt.Errorf("scan observation: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, observedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse observation time: %w", err)
		}
		obs.ObservedAt = t
		obss = append(obss, obs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate observations: %w", err)
	}
	return obss, nil
}

// GetLatestObservationVersion returns the highest snapshot version for a turn.
func (tx *Tx) GetLatestObservationVersion(turnID string) (int, error) {
	var version int
	err := tx.tx.QueryRow(`
		SELECT COALESCE(MAX(snapshot_version), 0)
		FROM observations
		WHERE turn_id = ?
	`, turnID).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("get latest observation version: %w", err)
	}
	return version, nil
}

// StoreChangeSet records a change set at candidate freeze.
func (tx *Tx) StoreChangeSet(cs ChangeSet) error {
	_, err := tx.tx.Exec(`
		INSERT INTO change_sets (id, turn_id, state, snapshot_hash, changes_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, cs.ID, cs.TurnID, cs.State, cs.SnapshotHash, cs.ChangesHash, cs.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store change set: %w", err)
	}
	return nil
}

// GetChangeSetByTurn retrieves the change set for a turn, or nil if not found.
func (tx *Tx) GetChangeSetByTurn(turnID string) (*ChangeSet, error) {
	var cs ChangeSet
	var createdAtStr string
	err := tx.tx.QueryRow(`
		SELECT id, turn_id, state, snapshot_hash, changes_hash, created_at
		FROM change_sets
		WHERE turn_id = ?
	`, turnID).Scan(&cs.ID, &cs.TurnID, &cs.State, &cs.SnapshotHash, &cs.ChangesHash, &createdAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query change set: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse change set time: %w", err)
	}
	cs.CreatedAt = t
	return &cs, nil
}

// StoreChangeSetPath records a single path in a change set.
func (tx *Tx) StoreChangeSetPath(csp ChangeSetPath) error {
	_, err := tx.tx.Exec(`
		INSERT INTO change_set_paths (id, change_set_id, path, op, base_sha256, new_sha256, new_len, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, csp.ID, csp.ChangeSetID, csp.Path, csp.Op, csp.BaseSHA256, csp.NewSHA256, csp.NewLen, csp.Outcome)
	if err != nil {
		return fmt.Errorf("store change set path: %w", err)
	}
	return nil
}

// GetChangeSetPathsByChangeSet retrieves all paths in a change set.
func (tx *Tx) GetChangeSetPathsByChangeSet(changeSetID string) ([]ChangeSetPath, error) {
	rows, err := tx.tx.Query(`
		SELECT id, change_set_id, path, op, base_sha256, new_sha256, new_len, outcome
		FROM change_set_paths
		WHERE change_set_id = ?
		ORDER BY path
	`, changeSetID)
	if err != nil {
		return nil, fmt.Errorf("query change set paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []ChangeSetPath
	for rows.Next() {
		var csp ChangeSetPath
		if err := rows.Scan(&csp.ID, &csp.ChangeSetID, &csp.Path, &csp.Op, &csp.BaseSHA256,
			&csp.NewSHA256, &csp.NewLen, &csp.Outcome); err != nil {
			return nil, fmt.Errorf("scan change set path: %w", err)
		}
		paths = append(paths, csp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate change set paths: %w", err)
	}
	return paths, nil
}

// StoreVerdictToolResult links a tool call result to a verdict.
// Uses a randomly generated ID to avoid ambiguity (paths can contain ':').
func (tx *Tx) StoreVerdictToolResult(verdictID, toolCallID string) error {
	id, err := generateRandomID()
	if err != nil {
		return fmt.Errorf("generate verdict tool result id: %w", err)
	}
	_, err = tx.tx.Exec(`
		INSERT INTO verdict_tool_results (id, verdict_id, tool_call_id)
		VALUES (?, ?, ?)
	`, id, verdictID, toolCallID)
	if err != nil {
		return fmt.Errorf("store verdict tool result: %w", err)
	}
	return nil
}

// generateRandomID generates a unique random ID.
func generateRandomID() (string, error) {
	const idLen = 16
	b := make([]byte, idLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GetVerdictToolResults retrieves all tool result IDs for a verdict.
func (tx *Tx) GetVerdictToolResults(verdictID string) ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT tool_call_id FROM verdict_tool_results
		WHERE verdict_id = ?
		ORDER BY rowid
	`, verdictID)
	if err != nil {
		return nil, fmt.Errorf("query verdict tool results: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var toolCallIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tool call id: %w", err)
		}
		toolCallIDs = append(toolCallIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verdict tool results: %w", err)
	}
	return toolCallIDs, nil
}

// StoreStaging records a staged write in the turn's staging map.
// If the path is already staged, updates both the content hash and staged_at timestamp.
// Uses a generated ID to avoid ambiguity when paths contain ':'.
func (tx *Tx) StoreStaging(turnID, path, contentHash string) error {
	now := time.Now().Format(time.RFC3339Nano)

	// Check if this path is already staged
	var existingID string
	err := tx.tx.QueryRow(`SELECT id FROM staging WHERE turn_id = ? AND path = ?`, turnID, path).Scan(&existingID)

	switch {
	case err == nil:
		// Path already staged, update it
		_, updateErr := tx.tx.Exec(`
			UPDATE staging SET new_content_hash = ?, staged_at = ?
			WHERE turn_id = ? AND path = ?
		`, contentHash, now, turnID, path)
		if updateErr != nil {
			return fmt.Errorf("update staging: %w", updateErr)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		// Path not staged yet, insert it with new ID
		id, genErr := generateRandomID()
		if genErr != nil {
			return fmt.Errorf("generate staging id: %w", genErr)
		}
		_, insertErr := tx.tx.Exec(`
			INSERT INTO staging (id, turn_id, path, new_content_hash, staged_at)
			VALUES (?, ?, ?, ?, ?)
		`, id, turnID, path, contentHash, now)
		if insertErr != nil {
			return fmt.Errorf("insert staging: %w", insertErr)
		}
		return nil
	default:
		return fmt.Errorf("query staging: %w", err)
	}
}

// GetStagingByTurn retrieves all staged paths for a turn.
func (tx *Tx) GetStagingByTurn(turnID string) (map[string]string, error) {
	rows, err := tx.tx.Query(`
		SELECT path, new_content_hash FROM staging
		WHERE turn_id = ?
		ORDER BY path
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query staging: %w", err)
	}
	defer func() { _ = rows.Close() }()

	staging := make(map[string]string)
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			return nil, fmt.Errorf("scan staging: %w", err)
		}
		staging[path] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staging: %w", err)
	}
	return staging, nil
}

// UnstageFile removes a path from a turn's staging map.
func (tx *Tx) UnstageFile(turnID, path string) error {
	_, err := tx.tx.Exec(`DELETE FROM staging WHERE turn_id = ? AND path = ?`, turnID, path)
	if err != nil {
		return fmt.Errorf("unstage file: %w", err)
	}
	return nil
}

// ApplyOp represents an apply operation in the journal.
type ApplyOp struct {
	ID          string
	ChangeSetID string
	OpType      string // "started", "file", or "finished"
	Path        *string
	NewSHA256   *string
	AppliedAt   time.Time
}

// StoreApplyOp records an apply operation (started, file, or finished).
// This is an append-only record of apply state transitions.
func (tx *Tx) StoreApplyOp(op ApplyOp) error {
	_, err := tx.tx.Exec(`
		INSERT INTO apply_ops (id, change_set_id, op_type, path, new_sha256, applied_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, op.ID, op.ChangeSetID, op.OpType, op.Path, op.NewSHA256, op.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store apply op: %w", err)
	}
	return nil
}

// GetApplyOps retrieves all apply operations for a change set, in order.
func (tx *Tx) GetApplyOps(changeSetID string) ([]ApplyOp, error) {
	rows, err := tx.tx.Query(`
		SELECT id, change_set_id, op_type, path, new_sha256, applied_at
		FROM apply_ops
		WHERE change_set_id = ?
		ORDER BY rowid
	`, changeSetID)
	if err != nil {
		return nil, fmt.Errorf("query apply ops: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ops []ApplyOp
	for rows.Next() {
		var op ApplyOp
		var appliedAtStr string
		if err := rows.Scan(&op.ID, &op.ChangeSetID, &op.OpType, &op.Path, &op.NewSHA256, &appliedAtStr); err != nil {
			return nil, fmt.Errorf("scan apply op: %w", err)
		}
		t, err := time.Parse(time.RFC3339Nano, appliedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse apply op time: %w", err)
		}
		op.AppliedAt = t
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate apply ops: %w", err)
	}
	return ops, nil
}
