package journal

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ToolCall represents a tool call and, once recorded, its result. The row is
// the durable intent journaled before dispatch; Status is derived: accepted
// until a ToolResult exists, then the result's status.
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

// ChangeSet represents a change set at candidate freeze.
type ChangeSet struct {
	ID           string
	TurnID       string
	State        string
	SnapshotHash string
	// SnapshotVersion is the snapshot version the candidate rested on, frozen at candidate time.
	SnapshotVersion int
	ChangesHash     string
	CreatedAt       time.Time
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
}

// ChangeSetOp values: create or modify.
const (
	ChangeSetOpCreate = "create"
	ChangeSetOpModify = "modify"
)

// Event kinds appended by the tool record methods, in the same transaction as
// the record they describe.
const (
	EventToolCallAccepted      = "tool_call_accepted"
	EventToolResultRecorded    = "tool_result_recorded"
	EventObservationStored     = "observation_stored"
	EventChangeSetFrozen       = "change_set_frozen"
	EventApplyOpRecorded       = "apply_op_recorded"
	EventChangeSetPathRecorded = "change_set_path_recorded"
	EventVerdictSnapshot       = "verdict_snapshot_recorded"
	EventVerdictObservation    = "verdict_observation_linked"
	EventVerdictToolResult     = "verdict_tool_result_linked"
	EventPathStaged            = "path_staged"
	EventPathUnstaged          = "path_unstaged"
)

// emitToolEvent appends a journal event in the caller's transaction, so the
// record and its event commit or roll back together.
func (tx *Tx) emitToolEvent(kind string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", kind, err)
	}
	return tx.AppendEvent(kind, data)
}

// PutToolContent stores body in the content store under its sha256 and returns
// the hash. Storing the same bytes again is a no-op. Every hash a tool record
// cites must be stored first (the schema enforces it), so the evidence can be
// rebuilt from the hashes after a restart.
func (tx *Tx) PutToolContent(body []byte) (string, error) {
	sum := computeSHA256(body)
	if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO tool_content (sha256, body) VALUES (?, ?)`, sum, body); err != nil {
		return "", fmt.Errorf("store tool content: %w", err)
	}
	return sum, nil
}

// GetToolContent returns the bytes stored under sha256, or ErrNoSuchContent.
func (tx *Tx) GetToolContent(sha256 string) ([]byte, error) {
	var body []byte
	err := tx.tx.QueryRow(`SELECT body FROM tool_content WHERE sha256 = ?`, sha256).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("tool content %q: %w", sha256, ErrNoSuchContent)
	}
	if err != nil {
		return nil, fmt.Errorf("query tool content: %w", err)
	}
	return body, nil
}

// StoreToolCall records the durable intent of a tool call, before dispatch.
// The call's Status, result and error fields are ignored: a result is recorded
// separately with StoreToolResult. ArgsSHA256 must already be in the content store.
func (tx *Tx) StoreToolCall(call ToolCall) error {
	_, err := tx.tx.Exec(`
		INSERT INTO tool_calls (id, attempt_id, name, args_sha256, class, observed_identity)
		VALUES (?, ?, ?, ?, ?, ?)
	`, call.ID, call.AttemptID, call.Name, call.ArgsSHA256, call.Class, call.ObservedIdentity)
	if err != nil {
		return fmt.Errorf("store tool call: %w", err)
	}
	return tx.emitToolEvent(EventToolCallAccepted, map[string]any{
		"tool_call_id": call.ID, "attempt_id": call.AttemptID, "name": call.Name, "args_sha256": call.ArgsSHA256,
	})
}

// GetToolCallsByAttempt retrieves all tool calls for an attempt.
func (tx *Tx) GetToolCallsByAttempt(attemptID string) ([]ToolCall, error) {
	rows, err := tx.tx.Query(`
		SELECT c.id, c.attempt_id, c.name, c.args_sha256, c.class, COALESCE(r.status, 'accepted'),
			r.result_sha256, r.result_byte_len, c.observed_identity, r.error_message
		FROM tool_calls c
		LEFT JOIN tool_results r ON r.tool_call_id = c.id
		WHERE c.attempt_id = ?
		ORDER BY c.rowid
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
	return tx.emitToolEvent(EventToolResultRecorded, map[string]any{
		"tool_call_id": result.ToolCallID, "status": result.Status, "result_sha256": result.ResultSHA256,
	})
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
	return tx.emitToolEvent(EventObservationStored, map[string]any{
		"observation_id": obs.ID, "turn_id": obs.TurnID, "attempt_id": obs.AttemptID,
		"snapshot_version": obs.SnapshotVersion, "path": obs.Path, "kind": obs.Kind,
	})
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

// StoreChangeSet records a change set at candidate freeze. It starts appliable;
// cs.State is ignored because the state is derived from apply_ops.
func (tx *Tx) StoreChangeSet(cs ChangeSet) error {
	_, err := tx.tx.Exec(`
		INSERT INTO change_sets (id, turn_id, snapshot_hash, snapshot_version, changes_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, cs.ID, cs.TurnID, cs.SnapshotHash, cs.SnapshotVersion, cs.ChangesHash, cs.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store change set: %w", err)
	}
	return tx.emitToolEvent(EventChangeSetFrozen, map[string]any{
		"change_set_id": cs.ID, "turn_id": cs.TurnID, "changes_hash": cs.ChangesHash,
	})
}

// GetChangeSetByTurn retrieves the change set for a turn, or nil if not found.
// State is derived from the change set's apply operations.
func (tx *Tx) GetChangeSetByTurn(turnID string) (*ChangeSet, error) {
	var cs ChangeSet
	var createdAtStr string
	err := tx.tx.QueryRow(`
		SELECT id, turn_id, snapshot_hash, snapshot_version, changes_hash, created_at
		FROM change_sets
		WHERE turn_id = ?
	`, turnID).Scan(&cs.ID, &cs.TurnID, &cs.SnapshotHash, &cs.SnapshotVersion, &cs.ChangesHash, &createdAtStr)
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
	state, _, err := tx.ComputeApplyState(cs.ID)
	if err != nil {
		return nil, err
	}
	cs.State = state
	return &cs, nil
}

// StoreChangeSetPath records a single path in a change set.
func (tx *Tx) StoreChangeSetPath(csp ChangeSetPath) error {
	_, err := tx.tx.Exec(`
		INSERT INTO change_set_paths (id, change_set_id, path, op, base_sha256, new_sha256, new_len)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, csp.ID, csp.ChangeSetID, csp.Path, csp.Op, csp.BaseSHA256, csp.NewSHA256, csp.NewLen)
	if err != nil {
		return fmt.Errorf("store change set path: %w", err)
	}
	return tx.emitToolEvent(EventChangeSetPathRecorded, map[string]any{
		"change_set_id": csp.ChangeSetID, "path": csp.Path, "op": csp.Op, "new_sha256": csp.NewSHA256,
	})
}

// GetChangeSetPathsByChangeSet retrieves all paths in a change set.
func (tx *Tx) GetChangeSetPathsByChangeSet(changeSetID string) ([]ChangeSetPath, error) {
	rows, err := tx.tx.Query(`
		SELECT id, change_set_id, path, op, base_sha256, new_sha256, new_len
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
			&csp.NewSHA256, &csp.NewLen); err != nil {
			return nil, fmt.Errorf("scan change set path: %w", err)
		}
		paths = append(paths, csp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate change set paths: %w", err)
	}
	return paths, nil
}

// StoreVerdictToolResult links a received tool result to a verdict, by the id
// of the call's tool_results row. A call with no result yet is not evidence and
// is rejected. It returns the linked tool result id. Uses a randomly generated
// row ID to avoid ambiguity.
func (tx *Tx) StoreVerdictToolResult(verdictID, toolCallID string) (string, error) {
	var resultID string
	err := tx.tx.QueryRow(`SELECT id FROM tool_results WHERE tool_call_id = ?`, toolCallID).Scan(&resultID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("tool call %q has no recorded result to cite as verdict evidence", toolCallID)
	}
	if err != nil {
		return "", fmt.Errorf("query tool result: %w", err)
	}
	id, err := generateRandomID()
	if err != nil {
		return "", fmt.Errorf("generate verdict tool result id: %w", err)
	}
	if _, err = tx.tx.Exec(`
		INSERT INTO verdict_tool_results (id, verdict_id, tool_result_id)
		VALUES (?, ?, ?)
	`, id, verdictID, resultID); err != nil {
		return "", fmt.Errorf("store verdict tool result: %w", err)
	}
	return resultID, tx.emitToolEvent(EventVerdictToolResult, map[string]any{"verdict_id": verdictID, "tool_result_id": resultID})
}

// StoreVerdictSnapshot records the snapshot version a verdict saw.
func (tx *Tx) StoreVerdictSnapshot(verdictID string, version int) error {
	if _, err := tx.tx.Exec(`INSERT INTO verdict_snapshots (verdict_id, snapshot_version) VALUES (?, ?)`, verdictID, version); err != nil {
		return fmt.Errorf("store verdict snapshot: %w", err)
	}
	return tx.emitToolEvent(EventVerdictSnapshot, map[string]any{"verdict_id": verdictID, "snapshot_version": version})
}

// GetVerdictSnapshot returns the snapshot version a verdict saw; ok is false when none was recorded.
func (tx *Tx) GetVerdictSnapshot(verdictID string) (version int, ok bool, err error) {
	err = tx.tx.QueryRow(`SELECT snapshot_version FROM verdict_snapshots WHERE verdict_id = ?`, verdictID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query verdict snapshot: %w", err)
	}
	return version, true, nil
}

// StoreVerdictObservation records an observation the verdict's reviewer added.
func (tx *Tx) StoreVerdictObservation(verdictID, observationID string) error {
	if _, err := tx.tx.Exec(`INSERT INTO verdict_observations (verdict_id, observation_id) VALUES (?, ?)`, verdictID, observationID); err != nil {
		return fmt.Errorf("store verdict observation: %w", err)
	}
	return tx.emitToolEvent(EventVerdictObservation, map[string]any{"verdict_id": verdictID, "observation_id": observationID})
}

// GetVerdictObservations returns the ids of the observations a verdict added.
func (tx *Tx) GetVerdictObservations(verdictID string) ([]string, error) {
	rows, err := tx.tx.Query(`SELECT observation_id FROM verdict_observations WHERE verdict_id = ? ORDER BY rowid`, verdictID)
	if err != nil {
		return nil, fmt.Errorf("query verdict observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan verdict observation: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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

// GetVerdictToolResults returns the tool result IDs for a verdict.
func (tx *Tx) GetVerdictToolResults(verdictID string) ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT tool_result_id FROM verdict_tool_results
		WHERE verdict_id = ?
		ORDER BY rowid
	`, verdictID)
	if err != nil {
		return nil, fmt.Errorf("query verdict tool results: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var toolResultIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tool result id: %w", err)
		}
		toolResultIDs = append(toolResultIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verdict tool results: %w", err)
	}
	return toolResultIDs, nil
}

// StoreStaging records a staged write in the turn's staging map.
// The base is taken the first time a path is staged (a content hash, or
// StagingBaseAbsent) and kept on restage; restaging updates the content hash
// and staged_at timestamp.
// Uses a generated ID to avoid ambiguity when paths contain ':'.
func (tx *Tx) StoreStaging(turnID, path, contentHash, baseSHA256 string) error {
	if baseSHA256 == "" {
		baseSHA256 = StagingBaseAbsent
	}
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
		return tx.emitToolEvent(EventPathStaged, map[string]any{"turn_id": turnID, "path": path, "content_hash": contentHash})
	case errors.Is(err, sql.ErrNoRows):
		// Path not staged yet, insert it with new ID
		id, genErr := generateRandomID()
		if genErr != nil {
			return fmt.Errorf("generate staging id: %w", genErr)
		}
		_, insertErr := tx.tx.Exec(`
			INSERT INTO staging (id, turn_id, path, new_content_hash, base_sha256, staged_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, turnID, path, contentHash, baseSHA256, now)
		if insertErr != nil {
			return fmt.Errorf("insert staging: %w", insertErr)
		}
		return tx.emitToolEvent(EventPathStaged, map[string]any{"turn_id": turnID, "path": path, "content_hash": contentHash})
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
	return tx.emitToolEvent(EventPathUnstaged, map[string]any{"turn_id": turnID, "path": path})
}

// StagingBaseAbsent is the base of a staged path that did not exist.
const StagingBaseAbsent = "absent"

// ApplyOp op types.
const (
	ApplyOpStarted   = "started"
	ApplyOpFile      = "file"
	ApplyOpFinished  = "finished"
	ApplyOpDiscarded = "discarded"
)

// ApplyOp represents an apply operation in the journal.
// File ops carry Path and Outcome (applied or conflict); NewSHA256 is the
// content written for an applied path. Finished ops carry FinalState.
type ApplyOp struct {
	ID          string
	ChangeSetID string
	OpType      string
	Path        *string
	NewSHA256   *string
	Outcome     *string
	FinalState  *string
	AppliedAt   time.Time
}

// StoreApplyOp records an apply operation. Apply operations are append-only;
// per-path outcomes and the change set state are derived from them.
func (tx *Tx) StoreApplyOp(op ApplyOp) error {
	_, err := tx.tx.Exec(`
		INSERT INTO apply_ops (id, change_set_id, op_type, path, new_sha256, outcome, final_state, applied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, op.ID, op.ChangeSetID, op.OpType, op.Path, op.NewSHA256, op.Outcome, op.FinalState, op.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store apply op: %w", err)
	}
	return tx.emitToolEvent(EventApplyOpRecorded, map[string]any{
		"change_set_id": op.ChangeSetID, "op_type": op.OpType, "path": op.Path, "outcome": op.Outcome, "final_state": op.FinalState,
	})
}

// GetApplyOps retrieves all apply operations for a change set, in order.
func (tx *Tx) GetApplyOps(changeSetID string) ([]ApplyOp, error) {
	rows, err := tx.tx.Query(`
		SELECT id, change_set_id, op_type, path, new_sha256, outcome, final_state, applied_at
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
		if err := rows.Scan(&op.ID, &op.ChangeSetID, &op.OpType, &op.Path, &op.NewSHA256, &op.Outcome, &op.FinalState, &appliedAtStr); err != nil {
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

// ChangeSetPathOutcome values derived from apply_ops.
const (
	ChangeSetPathOutcomePending  = "pending"
	ChangeSetPathOutcomeApplied  = "applied"
	ChangeSetPathOutcomeConflict = "conflict"
)

// ComputeApplyState derives the change set state and per-path outcomes from
// the change set's paths and its append-only apply_ops. Every path starts
// pending; a file op moves it to applied or conflict. A discarded op makes the
// state discarded. Otherwise the state comes from the latest started/finished
// pair: a started with no finished after it is partial (a crash mid-apply,
// even before the first file), a later finished gives its final state, and a
// change set never started is appliable.
func (tx *Tx) ComputeApplyState(changeSetID string) (string, map[string]string, error) {
	ops, err := tx.GetApplyOps(changeSetID)
	if err != nil {
		return "", nil, fmt.Errorf("get apply ops: %w", err)
	}
	paths, err := tx.GetChangeSetPathsByChangeSet(changeSetID)
	if err != nil {
		return "", nil, fmt.Errorf("get change set paths: %w", err)
	}

	outcomes := make(map[string]string, len(paths))
	for _, p := range paths {
		outcomes[p.Path] = ChangeSetPathOutcomePending
	}
	discarded := false
	state := ChangeSetStateAppliable
	for _, op := range ops {
		switch op.OpType {
		case ApplyOpStarted:
			state = ChangeSetStatePartial
		case ApplyOpFile:
			if op.Path == nil || op.Outcome == nil {
				continue
			}
			if _, ok := outcomes[*op.Path]; ok {
				outcomes[*op.Path] = *op.Outcome
			}
		case ApplyOpFinished:
			if op.FinalState != nil {
				state = *op.FinalState
			}
		case ApplyOpDiscarded:
			discarded = true
		}
	}
	if discarded {
		state = ChangeSetStateDiscarded
	}
	return state, outcomes, nil
}
