package journal

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Tool records (v5 delta section 6). Every record method runs inside the
// caller's transaction and emits its journal event in that same transaction,
// so a rollback drops the record and its event together. Every byte string a
// record cites (call arguments, results, observed file contents and directory
// listings, staged content and bases) is stored in tool_content under its
// SHA-256, and Content or ContentRange("content:<sha256>") rebuilds it.

// Errors returned by the tool record methods, wrapped with detail.
var (
	// ErrNotObserved reports a path the turn's snapshot record does not hold.
	ErrNotObserved = errors.New("path not observed in this turn")
	// ErrAlreadyObserved is returned by RecordObservation for a path the turn
	// has already observed.
	ErrAlreadyObserved = errors.New("path already observed in this turn")
	// ErrNotStaged reports an unstage of a path that is not staged.
	ErrNotStaged = errors.New("path not staged")
	// ErrNoChangeSet reports a turn whose current candidate has no change set.
	ErrNoChangeSet = errors.New("no change set")
	// ErrChangeSetState reports an apply operation the change set's state forbids.
	ErrChangeSetState = errors.New("change set state forbids operation")
	// ErrEvidenceScope reports verdict evidence from another turn, another
	// participant, from an attempt dispatched after the verdict's, or
	// received after the verdict was recorded.
	ErrEvidenceScope = errors.New("evidence out of the verdict's scope")
	// ErrNotToolStep is returned by RecordToolStep for an attempt that is not
	// an accepted tool step: still pending, quarantined as late, failed by
	// the gate, or completed under any status but tool_calls. Nothing of such
	// a step is journaled as intent and nothing of it is executed.
	ErrNotToolStep = errors.New("attempt is not an accepted tool step")
	// ErrToolStepRecorded is returned by RecordToolStep for an attempt whose
	// calls are already journaled: a step's intent is recorded, and the step
	// executed, once.
	ErrToolStepRecorded = errors.New("tool step already recorded")
)

// ToolClassNone is the class of a call that can never be executed: it names
// a tool in no group, names no tool, or is not a function call. It is still
// journaled as accepted intent so that the tool error answering it is too.
const ToolClassNone = "none"

// ToolClassExec is the class of a bash call (v7 delta section 2).
const ToolClassExec = "exec"

// Reasons ToolStepLive gives for a step that may have no further effect.
const (
	StepEndedTurn  = "turn_ended" // the turn has a terminal state
	StepEndedEpoch = "epoch"      // the turn's epoch has moved past the attempt's
)

// Tool call statuses. Accepted is implied by a call with no result.
const (
	ToolCallAccepted  = "accepted"
	ToolCallExecuted  = "executed"
	ToolCallError     = "error"
	ToolCallAbandoned = "abandoned"
)

// Observation kinds, as in the snapshot record's canonical form.
const (
	ObservationFile    = "f"
	ObservationAbsent  = "a"
	ObservationListing = "d"
)

// Change-set states and per-path outcomes.
const (
	ChangeSetAppliable = "appliable"
	ChangeSetApplied   = "applied"
	ChangeSetPartial   = "partial"
	ChangeSetConflict  = "conflict"
	ChangeSetDiscarded = "discarded"

	PathPending  = "pending"
	PathApplied  = "applied"
	PathConflict = "conflict"
)

// ToolCall is one accepted call of a tool step with its outcome, if any.
type ToolCall struct {
	ID             string
	AttemptID      string
	TurnID         string
	Participant    string
	CallIndex      int
	ProviderCallID string
	Name           string
	Class          string
	ArgsSHA256     string
	ArgsByteLen    int64
	RecordedAt     time.Time
	// Status is accepted until a result is recorded, then the result's status.
	Status string
	Result *ToolResult
}

// ToolResult is the one terminal outcome of a call. For executed and error it
// carries what the model received, by hash and length; abandoned carries none.
// JournalSeq is the journal-wide position at which the result was received;
// it orders the result against the verdicts of the same journal.
type ToolResult struct {
	ID            string
	ToolCallID    string
	Status        string
	ResultSHA256  string
	ResultByteLen int64
	ErrorCode     string
	JournalSeq    int64
	RecordedAt    time.Time
}

// Observation is one record of the turn's snapshot. ContentSHA256 names the
// file's base content for kind f, the canonical entry list for kind d, and is
// empty for kind a. JournalSeq is the journal-wide position at which it was
// recorded, which orders it against verdicts.
type Observation struct {
	ID              string
	TurnID          string
	AttemptID       string
	SnapshotVersion int
	Path            string
	Kind            string
	ContentSHA256   string
	JournalSeq      int64
	RecordedAt      time.Time
}

// StagingEntry is one path of the turn's staging map. An empty BaseSHA256 is
// a base of absent.
type StagingEntry struct {
	TurnID     string
	Path       string
	BaseSHA256 string
	NewSHA256  string
	StagedAt   time.Time
	UpdatedAt  time.Time
}

// ChangeSetPath is one path of a frozen change set with its derived outcome.
type ChangeSetPath struct {
	Path       string
	Op         string
	BaseSHA256 string
	NewSHA256  string
	NewLen     int64
	Outcome    string
}

// ChangeSet is a candidate's frozen change set with its derived state.
type ChangeSet struct {
	ID              string
	TurnID          string
	CandidateID     string
	SnapshotVersion int
	SnapshotHash    string
	ChangesHash     string
	FrozenAt        time.Time
	State           string
	Paths           []ChangeSetPath
}

// ApplyOp is one entry of a change set's append-only apply log.
type ApplyOp struct {
	Seq         int64
	ChangeSetID string
	OpType      string
	Path        string
	Outcome     string
	State       string
	Override    bool
	RecordedAt  time.Time
}

// VerdictEvidence is what a verdict rested on.
type VerdictEvidence struct {
	VerdictID       string
	SnapshotVersion int
	ResultIDs       []string
	ObservationIDs  []string
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// pathJSON renders a path for an event payload: as a string when it is valid
// UTF-8, else base64 under path_b64. The record keeps the exact bytes.
func pathJSON(payload map[string]any, path string) {
	if utf8.ValidString(path) {
		payload["path"] = path
	} else {
		payload["path_b64"] = base64.StdEncoding.EncodeToString([]byte(path))
	}
}

func (tx *Tx) emitTools(turnID, kind string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", kind, err)
	}
	if _, err := tx.Emit(turnID, kind, data); err != nil {
		return err
	}
	return nil
}

// StoreContent stores body under its SHA-256 and returns the hash. Storing the
// same bytes again is a no-op.
func (tx *Tx) StoreContent(body []byte) (string, error) {
	if body == nil {
		// A nil slice would bind as NULL, which OR IGNORE drops silently.
		body = []byte{}
	}
	sum := computeSHA256(body)
	if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO tool_content (sha256, byte_len, body) VALUES (?, ?, ?)`,
		sum, len(body), body); err != nil {
		return "", fmt.Errorf("store content: %w", err)
	}
	return sum, nil
}

// StoreContent stores body under its SHA-256 and returns the hash.
func (s *Store) StoreContent(body []byte) (sha256 string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		sha256, txErr = tx.StoreContent(body)
		return txErr
	})
	return sha256, err
}

// Content returns the bytes stored under sha256, or ErrNoSuchContent.
func (tx *Tx) Content(sha256 string) ([]byte, error) {
	var body []byte
	err := tx.tx.QueryRow(`SELECT body FROM tool_content WHERE sha256 = ?`, sha256).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("content %s: %w", sha256, ErrNoSuchContent)
	}
	if err != nil {
		return nil, fmt.Errorf("query content: %w", err)
	}
	if body == nil {
		body = []byte{}
	}
	return body, nil
}

// Content returns the bytes stored under sha256, or ErrNoSuchContent.
func (s *Store) Content(sha256 string) (body []byte, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		body, txErr = tx.Content(sha256)
		return txErr
	})
	return body, err
}

// turnAttempt returns the turn and participant of an attempt that belongs to
// a turn, refusing probe attempts.
func (tx *Tx) turnAttempt(attemptID string) (turnID, participant string, err error) {
	var turn sql.NullString
	err = tx.tx.QueryRow(`SELECT turn_id, participant FROM attempts WHERE id = ?`, attemptID).Scan(&turn, &participant)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("attempt %q not found", attemptID)
	}
	if err != nil {
		return "", "", fmt.Errorf("query attempt: %w", err)
	}
	if !turn.Valid {
		return "", "", fmt.Errorf("attempt %q belongs to no turn", attemptID)
	}
	return turn.String, participant, nil
}

// RecordToolCall journals the accepted intent of one call of a tool step
// before it is dispatched: the calling attempt, the call's position in the
// step, the model's own call id, the tool, its class and its arguments, which
// are stored by hash. It emits tool_call_accepted in the same transaction.
func (tx *Tx) RecordToolCall(attemptID string, callIndex int, providerCallID, name, class string, args []byte) (ToolCall, error) {
	if callIndex < 0 {
		return ToolCall{}, fmt.Errorf("call index must be >= 0, got %d", callIndex)
	}
	// Only a call of class none may name no tool: it is journaled so that
	// its tool error can be, and is never executed.
	if providerCallID == "" || (name == "" && class != ToolClassNone) {
		return ToolCall{}, fmt.Errorf("tool call needs a provider call id and a name")
	}
	switch class {
	case "read", "web", "write", ToolClassExec, ToolClassNone:
	default:
		return ToolCall{}, fmt.Errorf("unknown tool class %q", class)
	}
	turnID, participant, err := tx.turnAttempt(attemptID)
	if err != nil {
		return ToolCall{}, err
	}
	argsSHA, err := tx.StoreContent(args)
	if err != nil {
		return ToolCall{}, err
	}
	id, err := generateID()
	if err != nil {
		return ToolCall{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, attemptID, turnID, participant, callIndex, providerCallID, name, class, argsSHA, at); err != nil {
		return ToolCall{}, fmt.Errorf("insert tool call: %w", err)
	}
	if err := tx.emitTools(turnID, "tool_call_accepted", map[string]any{
		"call_id": id, "attempt_id": attemptID, "participant": participant,
		"call_index": callIndex, "provider_call_id": providerCallID,
		"name": name, "class": class, "args_sha256": argsSHA,
	}); err != nil {
		return ToolCall{}, err
	}
	recordedAt, _ := parseTime(at)
	return ToolCall{
		ID: id, AttemptID: attemptID, TurnID: turnID, Participant: participant,
		CallIndex: callIndex, ProviderCallID: providerCallID, Name: name, Class: class,
		ArgsSHA256: argsSHA, ArgsByteLen: int64(len(args)), RecordedAt: recordedAt,
		Status: ToolCallAccepted,
	}, nil
}

// StepCall is one call of a tool step as the model returned it, with the
// class of the tool it names: read, web or write for a tool in that group,
// ToolClassNone for anything that cannot be executed.
type StepCall struct {
	ProviderCallID string
	Name           string
	Class          string
	Args           []byte
}

// RecordToolStep journals the accepted intent of every call of an accepted
// tool step, in the order the model returned them, before any call is checked
// or has an effect. The attempt must be an accepted tool step: completed, not
// quarantined, with no gate error and status tool_calls. Anything else, a
// pending attempt, a late result quarantined at acceptance, a result the gate
// failed, a result under another status, is refused with ErrNotToolStep, so
// a late or quarantined step is journaled only by its acceptance and never
// has a call recorded or executed. A step whose calls are already journaled
// is refused with ErrToolStepRecorded. Each call emits tool_call_accepted in
// this transaction, so a rollback leaves no trace of the step.
func (tx *Tx) RecordToolStep(attemptID string, calls []StepCall) ([]ToolCall, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("tool step of attempt %q carries no calls", attemptID)
	}
	var state string
	var status, gateErr sql.NullString
	var quarantined bool
	err := tx.tx.QueryRow(`SELECT state, status, quarantined, gate_error FROM attempts WHERE id = ?`, attemptID).
		Scan(&state, &status, &quarantined, &gateErr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("attempt %q not found", attemptID)
	}
	if err != nil {
		return nil, fmt.Errorf("query attempt: %w", err)
	}
	if state != "completed" || quarantined || gateErr.Valid || status.String != "tool_calls" {
		return nil, fmt.Errorf("%w: attempt %q (state %s, status %q, quarantined %t, gate error %q)",
			ErrNotToolStep, attemptID, state, status.String, quarantined, gateErr.String)
	}
	var recorded bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tool_calls WHERE attempt_id = ?)`, attemptID).Scan(&recorded); err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	if recorded {
		return nil, fmt.Errorf("%w: attempt %q", ErrToolStepRecorded, attemptID)
	}
	out := make([]ToolCall, 0, len(calls))
	for i, c := range calls {
		call, err := tx.RecordToolCall(attemptID, i, c.ProviderCallID, c.Name, c.Class, c.Args)
		if err != nil {
			return nil, fmt.Errorf("call %d of attempt %q: %w", i, attemptID, err)
		}
		out = append(out, call)
	}
	return out, nil
}

// ToolStepLive reports whether a call of the attempt's tool step may still
// have an effect: the attempt's turn has no terminal state (a cancel ends the
// turn) and the turn's epoch is still the attempt's. When it may not, reason
// is StepEndedTurn or StepEndedEpoch. It is the re-check the engine makes
// immediately before each effect and again after every wait inside one.
func (tx *Tx) ToolStepLive(attemptID string) (live bool, reason string, err error) {
	var attemptEpoch, turnEpoch int
	var terminal sql.NullString
	err = tx.tx.QueryRow(`
		SELECT a.epoch, t.epoch, t.terminal_state
		FROM attempts a JOIN turns t ON t.id = a.turn_id
		WHERE a.id = ?
	`, attemptID).Scan(&attemptEpoch, &turnEpoch, &terminal)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", fmt.Errorf("attempt %q not found in any turn", attemptID)
	}
	if err != nil {
		return false, "", fmt.Errorf("query tool step: %w", err)
	}
	switch {
	case terminal.Valid:
		return false, StepEndedTurn, nil
	case turnEpoch != attemptEpoch:
		return false, StepEndedEpoch, nil
	}
	return true, "", nil
}

// RecordToolResult journals the one terminal outcome of an accepted call.
// For executed, result is what the model received; for error it is the tool
// error text the model received and errorCode names the failure; for
// abandoned both must be empty. The hash and length are derived from result.
// It emits tool_result in the same transaction. A second result for the same
// call is refused.
func (tx *Tx) RecordToolResult(toolCallID, status string, result []byte, errorCode string) (ToolResult, error) {
	switch status {
	case ToolCallExecuted:
		if errorCode != "" {
			return ToolResult{}, fmt.Errorf("executed result carries no error code")
		}
	case ToolCallError:
		if errorCode == "" {
			return ToolResult{}, fmt.Errorf("error result needs an error code")
		}
	case ToolCallAbandoned:
		if len(result) != 0 || errorCode != "" {
			return ToolResult{}, fmt.Errorf("abandoned result carries no content or error code")
		}
	default:
		return ToolResult{}, fmt.Errorf("unknown tool result status %q", status)
	}
	var turnID string
	err := tx.tx.QueryRow(`SELECT turn_id FROM tool_calls WHERE id = ?`, toolCallID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolResult{}, fmt.Errorf("tool call %q not found", toolCallID)
	}
	if err != nil {
		return ToolResult{}, fmt.Errorf("query tool call: %w", err)
	}
	var exists bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tool_results WHERE tool_call_id = ?)`, toolCallID).Scan(&exists); err != nil {
		return ToolResult{}, fmt.Errorf("query tool result: %w", err)
	}
	if exists {
		return ToolResult{}, fmt.Errorf("tool call %q already has a result", toolCallID)
	}

	var resultSHA *string
	var resultLen *int64
	if status != ToolCallAbandoned {
		sum, err := tx.StoreContent(result)
		if err != nil {
			return ToolResult{}, err
		}
		n := int64(len(result))
		resultSHA, resultLen = &sum, &n
	}
	var code *string
	if errorCode != "" {
		code = &errorCode
	}
	id, err := generateID()
	if err != nil {
		return ToolResult{}, err
	}
	seq, err := tx.nextJournalSeq()
	if err != nil {
		return ToolResult{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, error_code, journal_seq, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, toolCallID, status, resultSHA, resultLen, code, seq, at); err != nil {
		return ToolResult{}, fmt.Errorf("insert tool result: %w", err)
	}
	payload := map[string]any{"result_id": id, "call_id": toolCallID, "status": status, "journal_seq": seq}
	if resultSHA != nil {
		payload["sha256"] = *resultSHA
		payload["byte_len"] = *resultLen
	}
	if code != nil {
		payload["error_code"] = *code
	}
	if err := tx.emitTools(turnID, "tool_result", payload); err != nil {
		return ToolResult{}, err
	}
	out := ToolResult{ID: id, ToolCallID: toolCallID, Status: status, ErrorCode: errorCode, JournalSeq: seq}
	out.RecordedAt, _ = parseTime(at)
	if resultSHA != nil {
		out.ResultSHA256, out.ResultByteLen = *resultSHA, *resultLen
	}
	return out, nil
}

const toolCallSelect = `
	SELECT c.id, c.attempt_id, c.turn_id, c.participant, c.call_index, c.provider_call_id, c.name, c.class,
	       c.args_sha256, ac.byte_len, c.recorded_at,
	       r.id, r.status, r.result_sha256, r.result_byte_len, r.error_code, r.journal_seq, r.recorded_at
	FROM tool_calls c
	JOIN tool_content ac ON ac.sha256 = c.args_sha256
	LEFT JOIN tool_results r ON r.tool_call_id = c.id
`

func scanToolCall(sc interface{ Scan(...any) error }) (ToolCall, error) {
	var c ToolCall
	var recordedAt string
	var rID, rStatus, rSHA, rCode, rAt sql.NullString
	var rLen, rSeq sql.NullInt64
	if err := sc.Scan(&c.ID, &c.AttemptID, &c.TurnID, &c.Participant, &c.CallIndex, &c.ProviderCallID, &c.Name, &c.Class,
		&c.ArgsSHA256, &c.ArgsByteLen, &recordedAt, &rID, &rStatus, &rSHA, &rLen, &rCode, &rSeq, &rAt); err != nil {
		return ToolCall{}, err
	}
	var err error
	if c.RecordedAt, err = parseTime(recordedAt); err != nil {
		return ToolCall{}, fmt.Errorf("parse recorded_at: %w", err)
	}
	c.Status = ToolCallAccepted
	if rID.Valid {
		r := &ToolResult{ID: rID.String, ToolCallID: c.ID, Status: rStatus.String,
			ResultSHA256: rSHA.String, ResultByteLen: rLen.Int64, ErrorCode: rCode.String, JournalSeq: rSeq.Int64}
		if r.RecordedAt, err = parseTime(rAt.String); err != nil {
			return ToolCall{}, fmt.Errorf("parse result recorded_at: %w", err)
		}
		c.Result = r
		c.Status = r.Status
	}
	return c, nil
}

func (tx *Tx) queryToolCalls(where string, args ...any) ([]ToolCall, error) {
	rows, err := tx.tx.Query(toolCallSelect+where+` ORDER BY c.recorded_at, c.call_index, c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var calls []ToolCall
	for rows.Next() {
		c, err := scanToolCall(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool calls: %w", err)
	}
	return calls, nil
}

// GetToolCall returns one call with its result, if any.
func (tx *Tx) GetToolCall(id string) (ToolCall, error) {
	c, err := scanToolCall(tx.tx.QueryRow(toolCallSelect+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ToolCall{}, fmt.Errorf("tool call %q not found", id)
	}
	if err != nil {
		return ToolCall{}, fmt.Errorf("query tool call: %w", err)
	}
	return c, nil
}

// ToolCallsByAttempt returns an attempt's calls in step order.
func (tx *Tx) ToolCallsByAttempt(attemptID string) ([]ToolCall, error) {
	return tx.queryToolCalls(` WHERE c.attempt_id = ?`, attemptID)
}

// ToolCallsByTurn returns a turn's calls, all participants when participant
// is empty, in the order they were recorded.
func (tx *Tx) ToolCallsByTurn(turnID, participant string) ([]ToolCall, error) {
	if participant == "" {
		return tx.queryToolCalls(` WHERE c.turn_id = ?`, turnID)
	}
	return tx.queryToolCalls(` WHERE c.turn_id = ? AND c.participant = ?`, turnID, participant)
}

// ToolCallsByTurn returns a turn's calls with their results.
func (s *Store) ToolCallsByTurn(turnID, participant string) (calls []ToolCall, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		calls, txErr = tx.ToolCallsByTurn(turnID, participant)
		return txErr
	})
	return calls, err
}

// SnapshotVersion returns the turn's current snapshot version, 0 before any
// observation.
func (tx *Tx) SnapshotVersion(turnID string) (int, error) {
	var v int
	if err := tx.tx.QueryRow(`SELECT COALESCE(MAX(snapshot_version), 0) FROM observations WHERE turn_id = ?`, turnID).Scan(&v); err != nil {
		return 0, fmt.Errorf("query snapshot version: %w", err)
	}
	return v, nil
}

// RecordObservation appends one observation to the turn's snapshot record and
// bumps its version. kind f stores content as the file's base, kind d stores
// it as the canonical entry list, kind a takes no content. The attempt must
// belong to the turn, and a path observed once is refused thereafter: later
// observations are served from the record. It emits observation in the same
// transaction.
func (tx *Tx) RecordObservation(turnID, attemptID, path, kind string, content []byte) (Observation, error) {
	if path == "" {
		return Observation{}, fmt.Errorf("observation needs a path")
	}
	switch kind {
	case ObservationFile, ObservationListing:
	case ObservationAbsent:
		if content != nil {
			return Observation{}, fmt.Errorf("absence observation carries no content")
		}
	default:
		return Observation{}, fmt.Errorf("unknown observation kind %q", kind)
	}
	attemptTurn, participant, err := tx.turnAttempt(attemptID)
	if err != nil {
		return Observation{}, err
	}
	if attemptTurn != turnID {
		return Observation{}, fmt.Errorf("attempt %q belongs to turn %q, not %q", attemptID, attemptTurn, turnID)
	}
	var exists bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM observations WHERE turn_id = ? AND path = ?)`,
		turnID, []byte(path)).Scan(&exists); err != nil {
		return Observation{}, fmt.Errorf("query observation: %w", err)
	}
	if exists {
		return Observation{}, fmt.Errorf("path %q in turn %q: %w", path, turnID, ErrAlreadyObserved)
	}
	var contentSHA *string
	if kind != ObservationAbsent {
		sum, err := tx.StoreContent(content)
		if err != nil {
			return Observation{}, err
		}
		contentSHA = &sum
	}
	version, err := tx.SnapshotVersion(turnID)
	if err != nil {
		return Observation{}, err
	}
	version++
	id, err := generateID()
	if err != nil {
		return Observation{}, err
	}
	seq, err := tx.nextJournalSeq()
	if err != nil {
		return Observation{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, turnID, attemptID, version, []byte(path), kind, contentSHA, seq, at); err != nil {
		return Observation{}, fmt.Errorf("insert observation: %w", err)
	}
	payload := map[string]any{"observation_id": id, "attempt_id": attemptID, "participant": participant,
		"snapshot_version": version, "kind": kind, "journal_seq": seq}
	pathJSON(payload, path)
	if contentSHA != nil {
		payload["sha256"] = *contentSHA
	}
	if err := tx.emitTools(turnID, "observation", payload); err != nil {
		return Observation{}, err
	}
	o := Observation{ID: id, TurnID: turnID, AttemptID: attemptID, SnapshotVersion: version, Path: path, Kind: kind, JournalSeq: seq}
	if contentSHA != nil {
		o.ContentSHA256 = *contentSHA
	}
	o.RecordedAt, _ = parseTime(at)
	return o, nil
}

const observationSelect = `SELECT id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at FROM observations`

func scanObservation(sc interface{ Scan(...any) error }) (Observation, error) {
	var o Observation
	var path []byte
	var contentSHA sql.NullString
	var at string
	if err := sc.Scan(&o.ID, &o.TurnID, &o.AttemptID, &o.SnapshotVersion, &path, &o.Kind, &contentSHA, &o.JournalSeq, &at); err != nil {
		return Observation{}, err
	}
	o.Path = string(path)
	o.ContentSHA256 = contentSHA.String
	var err error
	if o.RecordedAt, err = parseTime(at); err != nil {
		return Observation{}, fmt.Errorf("parse recorded_at: %w", err)
	}
	return o, nil
}

// ObservationAt returns the turn's observation of path, or ErrNotObserved.
func (tx *Tx) ObservationAt(turnID, path string) (Observation, error) {
	o, err := scanObservation(tx.tx.QueryRow(observationSelect+` WHERE turn_id = ? AND path = ?`, turnID, []byte(path)))
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{}, fmt.Errorf("path %q in turn %q: %w", path, turnID, ErrNotObserved)
	}
	if err != nil {
		return Observation{}, fmt.Errorf("query observation: %w", err)
	}
	return o, nil
}

// Observations returns the turn's snapshot record sorted bytewise by path,
// the order its canonical form and snapshot_hash use.
func (tx *Tx) Observations(turnID string) ([]Observation, error) {
	rows, err := tx.tx.Query(observationSelect+` WHERE turn_id = ? ORDER BY path`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan observation: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate observations: %w", err)
	}
	return out, nil
}

// Observations returns the turn's snapshot record sorted bytewise by path.
func (s *Store) Observations(turnID string) (out []Observation, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		out, txErr = tx.Observations(turnID)
		return txErr
	})
	return out, err
}

// StageWrite records path's new content in the turn's staging map. The path
// must already be observed in the turn: the first time it is staged its base
// is the observed file content, or absent for an observed absence. Staging it
// again replaces the new content and keeps the base. It emits staged in the
// same transaction.
func (tx *Tx) StageWrite(turnID, path string, content []byte) (StagingEntry, error) {
	obs, err := tx.ObservationAt(turnID, path)
	if err != nil {
		return StagingEntry{}, err
	}
	if obs.Kind == ObservationListing {
		return StagingEntry{}, fmt.Errorf("path %q is a directory in turn %q", path, turnID)
	}
	newSHA, err := tx.StoreContent(content)
	if err != nil {
		return StagingEntry{}, err
	}
	at := nowStamp()
	var current StagingEntry
	var base sql.NullString
	var stagedAt string
	err = tx.tx.QueryRow(`SELECT base_sha256, staged_at FROM staging WHERE turn_id = ? AND path = ?`, turnID, []byte(path)).
		Scan(&base, &stagedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var basePtr *string
		if obs.Kind == ObservationFile {
			b := obs.ContentSHA256
			basePtr = &b
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO staging (turn_id, path, base_sha256, new_sha256, staged_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, turnID, []byte(path), basePtr, newSHA, at, at); err != nil {
			return StagingEntry{}, fmt.Errorf("insert staging: %w", err)
		}
		if basePtr != nil {
			current.BaseSHA256 = *basePtr
		}
		stagedAt = at
	case err != nil:
		return StagingEntry{}, fmt.Errorf("query staging: %w", err)
	default:
		if _, err := tx.tx.Exec(`UPDATE staging SET new_sha256 = ?, updated_at = ? WHERE turn_id = ? AND path = ?`,
			newSHA, at, turnID, []byte(path)); err != nil {
			return StagingEntry{}, fmt.Errorf("update staging: %w", err)
		}
		current.BaseSHA256 = base.String
	}
	payload := map[string]any{"new_sha256": newSHA, "byte_len": len(content)}
	pathJSON(payload, path)
	if current.BaseSHA256 != "" {
		payload["base_sha256"] = current.BaseSHA256
	} else {
		payload["base"] = "absent"
	}
	if err := tx.emitTools(turnID, "staged", payload); err != nil {
		return StagingEntry{}, err
	}
	current.TurnID, current.Path, current.NewSHA256 = turnID, path, newSHA
	current.StagedAt, _ = parseTime(stagedAt)
	current.UpdatedAt, _ = parseTime(at)
	return current, nil
}

// Unstage removes path from the turn's staging map, or returns ErrNotStaged.
// It emits unstaged in the same transaction.
func (tx *Tx) Unstage(turnID, path string) error {
	res, err := tx.tx.Exec(`DELETE FROM staging WHERE turn_id = ? AND path = ?`, turnID, []byte(path))
	if err != nil {
		return fmt.Errorf("delete staging: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete staging: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("path %q in turn %q: %w", path, turnID, ErrNotStaged)
	}
	payload := map[string]any{}
	pathJSON(payload, path)
	return tx.emitTools(turnID, "unstaged", payload)
}

// Staging returns the turn's staging map sorted bytewise by path.
func (tx *Tx) Staging(turnID string) ([]StagingEntry, error) {
	rows, err := tx.tx.Query(`SELECT path, base_sha256, new_sha256, staged_at, updated_at FROM staging WHERE turn_id = ? ORDER BY path`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query staging: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StagingEntry
	for rows.Next() {
		var e StagingEntry
		var path []byte
		var base sql.NullString
		var stagedAt, updatedAt string
		if err := rows.Scan(&path, &base, &e.NewSHA256, &stagedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan staging: %w", err)
		}
		e.TurnID, e.Path, e.BaseSHA256 = turnID, string(path), base.String
		if e.StagedAt, err = parseTime(stagedAt); err != nil {
			return nil, fmt.Errorf("parse staged_at: %w", err)
		}
		if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staging: %w", err)
	}
	return out, nil
}

// Staging returns the turn's staging map sorted bytewise by path.
func (s *Store) Staging(turnID string) (out []StagingEntry, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		out, txErr = tx.Staging(turnID)
		return txErr
	})
	return out, err
}

// FreezeChangeSet records the candidate's change set from the turn's staging
// map as it stands, with the snapshot version the candidate rested on and the
// snapshot and changes hashes the caller computed over the canonical forms.
// The candidate must belong to the turn, and an empty staging map yields no
// change set. It emits change_set_frozen in the same transaction.
func (tx *Tx) FreezeChangeSet(turnID, candidateID, snapshotHash, changesHash string) (ChangeSet, error) {
	if snapshotHash == "" || changesHash == "" {
		return ChangeSet{}, fmt.Errorf("change set needs snapshot and changes hashes")
	}
	var candidateTurn string
	err := tx.tx.QueryRow(`SELECT turn_id FROM candidates WHERE id = ?`, candidateID).Scan(&candidateTurn)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeSet{}, fmt.Errorf("candidate %q not found", candidateID)
	}
	if err != nil {
		return ChangeSet{}, fmt.Errorf("query candidate: %w", err)
	}
	if candidateTurn != turnID {
		return ChangeSet{}, fmt.Errorf("candidate %q belongs to turn %q, not %q", candidateID, candidateTurn, turnID)
	}
	staged, err := tx.Staging(turnID)
	if err != nil {
		return ChangeSet{}, err
	}
	if len(staged) == 0 {
		return ChangeSet{}, fmt.Errorf("turn %q has nothing staged: %w", turnID, ErrNoChangeSet)
	}
	version, err := tx.SnapshotVersion(turnID)
	if err != nil {
		return ChangeSet{}, err
	}
	id, err := generateID()
	if err != nil {
		return ChangeSet{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO change_sets (id, turn_id, candidate_id, snapshot_version, snapshot_hash, changes_hash, frozen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, turnID, candidateID, version, snapshotHash, changesHash, at); err != nil {
		return ChangeSet{}, fmt.Errorf("insert change set: %w", err)
	}
	cs := ChangeSet{ID: id, TurnID: turnID, CandidateID: candidateID, SnapshotVersion: version,
		SnapshotHash: snapshotHash, ChangesHash: changesHash, State: ChangeSetAppliable}
	cs.FrozenAt, _ = parseTime(at)
	paths := make([]map[string]any, 0, len(staged))
	for _, e := range staged {
		var newLen int64
		if err := tx.tx.QueryRow(`SELECT byte_len FROM tool_content WHERE sha256 = ?`, e.NewSHA256).Scan(&newLen); err != nil {
			return ChangeSet{}, fmt.Errorf("query staged content: %w", err)
		}
		p := ChangeSetPath{Path: e.Path, Op: "modify", BaseSHA256: e.BaseSHA256, NewSHA256: e.NewSHA256, NewLen: newLen, Outcome: PathPending}
		var basePtr *string
		if e.BaseSHA256 == "" {
			p.Op = "create"
		} else {
			b := e.BaseSHA256
			basePtr = &b
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO change_set_paths (change_set_id, path, op, base_sha256, new_sha256, new_len)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, []byte(e.Path), p.Op, basePtr, e.NewSHA256, newLen); err != nil {
			return ChangeSet{}, fmt.Errorf("insert change set path: %w", err)
		}
		cs.Paths = append(cs.Paths, p)
		pj := map[string]any{"op": p.Op, "new_sha256": p.NewSHA256, "new_len": newLen}
		pathJSON(pj, e.Path)
		if basePtr != nil {
			pj["base_sha256"] = *basePtr
		}
		paths = append(paths, pj)
	}
	if err := tx.emitTools(turnID, "change_set_frozen", map[string]any{
		"change_set_id": id, "candidate_id": candidateID, "snapshot_version": version,
		"snapshot_hash": snapshotHash, "changes_hash": changesHash, "paths": paths,
	}); err != nil {
		return ChangeSet{}, err
	}
	return cs, nil
}

// applyLog reads a change set's apply ops in order.
func (tx *Tx) applyLog(changeSetID string) ([]ApplyOp, error) {
	rows, err := tx.tx.Query(`SELECT id, op_type, path, outcome, state, override, recorded_at FROM apply_ops WHERE change_set_id = ? ORDER BY id`, changeSetID)
	if err != nil {
		return nil, fmt.Errorf("query apply ops: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ops []ApplyOp
	for rows.Next() {
		op := ApplyOp{ChangeSetID: changeSetID}
		var path []byte
		var outcome, state sql.NullString
		var at string
		if err := rows.Scan(&op.Seq, &op.OpType, &path, &outcome, &state, &op.Override, &at); err != nil {
			return nil, fmt.Errorf("scan apply op: %w", err)
		}
		op.Path, op.Outcome, op.State = string(path), outcome.String, state.String
		if op.RecordedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate apply ops: %w", err)
	}
	return ops, nil
}

// ApplyOps returns a change set's apply log in order.
func (tx *Tx) ApplyOps(changeSetID string) ([]ApplyOp, error) {
	if _, err := tx.changeSetTurn(changeSetID); err != nil {
		return nil, err
	}
	return tx.applyLog(changeSetID)
}

// ApplyOps returns a change set's apply log in order.
func (s *Store) ApplyOps(changeSetID string) (ops []ApplyOp, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		ops, txErr = tx.ApplyOps(changeSetID)
		return txErr
	})
	return ops, err
}

// deriveApplyState computes the change set's state and each path's outcome
// from the apply log: the latest control op fixes the state, the latest file
// op for a path fixes its outcome.
func deriveApplyState(ops []ApplyOp, paths []ChangeSetPath) (string, map[string]string) {
	state := ChangeSetAppliable
	outcomes := make(map[string]string, len(paths))
	for _, p := range paths {
		outcomes[p.Path] = PathPending
	}
	for _, op := range ops {
		switch op.OpType {
		case "started", "recovered":
			state = ChangeSetPartial
		case "finished", "discarded":
			state = op.State
		case "file":
			outcomes[op.Path] = op.Outcome
		}
	}
	return state, outcomes
}

func (tx *Tx) changeSetTurn(changeSetID string) (string, error) {
	var turnID string
	err := tx.tx.QueryRow(`SELECT turn_id FROM change_sets WHERE id = ?`, changeSetID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("change set %q: %w", changeSetID, ErrNoChangeSet)
	}
	if err != nil {
		return "", fmt.Errorf("query change set: %w", err)
	}
	return turnID, nil
}

func (tx *Tx) loadChangeSet(where string, arg string) (ChangeSet, error) {
	var cs ChangeSet
	var at string
	err := tx.tx.QueryRow(`SELECT id, turn_id, candidate_id, snapshot_version, snapshot_hash, changes_hash, frozen_at FROM change_sets `+where, arg).
		Scan(&cs.ID, &cs.TurnID, &cs.CandidateID, &cs.SnapshotVersion, &cs.SnapshotHash, &cs.ChangesHash, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeSet{}, fmt.Errorf("change set %s: %w", arg, ErrNoChangeSet)
	}
	if err != nil {
		return ChangeSet{}, fmt.Errorf("query change set: %w", err)
	}
	if cs.FrozenAt, err = parseTime(at); err != nil {
		return ChangeSet{}, fmt.Errorf("parse frozen_at: %w", err)
	}
	rows, err := tx.tx.Query(`SELECT path, op, base_sha256, new_sha256, new_len FROM change_set_paths WHERE change_set_id = ? ORDER BY path`, cs.ID)
	if err != nil {
		return ChangeSet{}, fmt.Errorf("query change set paths: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p ChangeSetPath
		var path []byte
		var base sql.NullString
		if err := rows.Scan(&path, &p.Op, &base, &p.NewSHA256, &p.NewLen); err != nil {
			return ChangeSet{}, fmt.Errorf("scan change set path: %w", err)
		}
		p.Path, p.BaseSHA256 = string(path), base.String
		cs.Paths = append(cs.Paths, p)
	}
	if err := rows.Err(); err != nil {
		return ChangeSet{}, fmt.Errorf("iterate change set paths: %w", err)
	}
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return ChangeSet{}, err
	}
	state, outcomes := deriveApplyState(ops, cs.Paths)
	cs.State = state
	for i := range cs.Paths {
		cs.Paths[i].Outcome = outcomes[cs.Paths[i].Path]
	}
	return cs, nil
}

// GetChangeSet returns a change set with its derived state and outcomes.
func (tx *Tx) GetChangeSet(changeSetID string) (ChangeSet, error) {
	return tx.loadChangeSet(`WHERE id = ?`, changeSetID)
}

// GetChangeSet returns a change set with its derived state and outcomes.
func (s *Store) GetChangeSet(changeSetID string) (cs ChangeSet, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		cs, txErr = tx.GetChangeSet(changeSetID)
		return txErr
	})
	return cs, err
}

// GetCandidateChangeSetID returns the change set ID for a candidate, or
// ErrNoChangeSet when the candidate has no change set.
func (s *Store) GetCandidateChangeSetID(candidateID string) (string, error) {
	var changeSetID string
	err := s.Tx(func(tx *Tx) error {
		return tx.tx.QueryRow(`SELECT id FROM change_sets WHERE candidate_id = ?`, candidateID).Scan(&changeSetID)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("candidate %q: %w", candidateID, ErrNoChangeSet)
	}
	if err != nil {
		return "", fmt.Errorf("query change set: %w", err)
	}
	return changeSetID, nil
}

// ChangeSetForTurn returns the change set of the turn's current candidate, or
// ErrNoChangeSet when the turn has no candidate or its candidate none.
func (tx *Tx) ChangeSetForTurn(turnID string) (ChangeSet, error) {
	var candidateID sql.NullString
	err := tx.tx.QueryRow(`SELECT candidate_id FROM turns WHERE id = ?`, turnID).Scan(&candidateID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeSet{}, fmt.Errorf("turn %q not found", turnID)
	}
	if err != nil {
		return ChangeSet{}, fmt.Errorf("query turn: %w", err)
	}
	if !candidateID.Valid {
		return ChangeSet{}, fmt.Errorf("turn %q has no candidate: %w", turnID, ErrNoChangeSet)
	}
	return tx.loadChangeSet(`WHERE candidate_id = ?`, candidateID.String)
}

// ChangeSetForTurn returns the change set of the turn's current candidate.
func (s *Store) ChangeSetForTurn(turnID string) (cs ChangeSet, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		cs, txErr = tx.ChangeSetForTurn(turnID)
		return txErr
	})
	return cs, err
}

func (tx *Tx) appendApplyOp(cs ChangeSet, opType, path, outcome, state string, override bool) error {
	var pathBytes []byte
	if path != "" {
		pathBytes = []byte(path)
	}
	var outcomePtr, statePtr *string
	if outcome != "" {
		outcomePtr = &outcome
	}
	if state != "" {
		statePtr = &state
	}
	if _, err := tx.tx.Exec(`
		INSERT INTO apply_ops (change_set_id, op_type, path, outcome, state, override, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, cs.ID, opType, pathBytes, outcomePtr, statePtr, override, nowStamp()); err != nil {
		return fmt.Errorf("insert apply op: %w", err)
	}
	return nil
}

// BeginApply journals apply_started for an appliable, partial or conflict
// change set, with its paths and new hashes; override records an apply under
// human override. An applied or discarded change set is refused with
// ErrChangeSetState.
func (tx *Tx) BeginApply(changeSetID string, override bool) error {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return err
	}
	switch cs.State {
	case ChangeSetAppliable, ChangeSetPartial, ChangeSetConflict:
	default:
		return fmt.Errorf("apply of %s change set %q: %w", cs.State, cs.ID, ErrChangeSetState)
	}
	if err := tx.appendApplyOp(cs, "started", "", "", "", override); err != nil {
		return err
	}
	paths := make([]map[string]any, 0, len(cs.Paths))
	for _, p := range cs.Paths {
		pj := map[string]any{"new_sha256": p.NewSHA256, "outcome": p.Outcome}
		pathJSON(pj, p.Path)
		paths = append(paths, pj)
	}
	return tx.emitTools(cs.TurnID, "apply_started", map[string]any{
		"change_set_id": cs.ID, "override": override, "paths": paths,
	})
}

// inProgress reports whether the change set's latest control op is started,
// that is an apply has begun and neither finished nor been recovered.
func inProgress(ops []ApplyOp) bool {
	last := ""
	for _, op := range ops {
		if op.OpType != "file" {
			last = op.OpType
		}
	}
	return last == "started"
}

// RecordApplyFile journals one path's outcome, applied, conflict or (after a
// fresh classification) pending, during an
// apply that has begun and not finished. The path must be in the change set.
func (tx *Tx) RecordApplyFile(changeSetID, path, outcome string) error {
	if outcome != PathApplied && outcome != PathConflict && outcome != PathPending {
		return fmt.Errorf("unknown path outcome %q", outcome)
	}
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return err
	}
	found := false
	for _, p := range cs.Paths {
		if p.Path == path {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("path %q is not in change set %q", path, cs.ID)
	}
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return err
	}
	if !inProgress(ops) {
		return fmt.Errorf("change set %q has no apply in progress: %w", cs.ID, ErrChangeSetState)
	}
	if err := tx.appendApplyOp(cs, "file", path, outcome, "", false); err != nil {
		return err
	}
	payload := map[string]any{"change_set_id": cs.ID, "outcome": outcome}
	pathJSON(payload, path)
	return tx.emitTools(cs.TurnID, "apply_file", payload)
}

// FinishApply journals apply_finished for an apply in progress and returns
// the state it recorded: applied when every path is applied, else conflict.
func (tx *Tx) FinishApply(changeSetID string) (string, error) {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return "", err
	}
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return "", err
	}
	if !inProgress(ops) {
		return "", fmt.Errorf("change set %q has no apply in progress: %w", cs.ID, ErrChangeSetState)
	}
	state := ChangeSetApplied
	for _, p := range cs.Paths {
		if p.Outcome != PathApplied {
			state = ChangeSetConflict
			break
		}
	}
	if err := tx.appendApplyOp(cs, "finished", "", "", state, false); err != nil {
		return "", err
	}
	if err := tx.emitTools(cs.TurnID, "apply_finished", map[string]any{"change_set_id": cs.ID, "state": state}); err != nil {
		return "", err
	}
	return state, nil
}

// RecordApplyRecovered journals that recovery found an apply begun and not
// finished, and classified its paths without writing: the state stays partial
// and is recorded as such.
func (tx *Tx) RecordApplyRecovered(changeSetID string) error {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return err
	}
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return err
	}
	if !inProgress(ops) {
		return fmt.Errorf("change set %q has no unfinished apply: %w", cs.ID, ErrChangeSetState)
	}
	if err := tx.appendApplyOp(cs, "recovered", "", "", ChangeSetPartial, false); err != nil {
		return err
	}
	paths := make([]map[string]any, 0, len(cs.Paths))
	for _, p := range cs.Paths {
		pj := map[string]any{"outcome": p.Outcome}
		pathJSON(pj, p.Path)
		paths = append(paths, pj)
	}
	return tx.emitTools(cs.TurnID, "apply_recovered", map[string]any{
		"change_set_id": cs.ID, "state": ChangeSetPartial, "paths": paths,
	})
}

// DiscardChangeSet moves an appliable, partial or conflict change set to
// discarded. Per-path outcomes are untouched and nothing is deleted.
func (tx *Tx) DiscardChangeSet(changeSetID string) error {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return err
	}
	switch cs.State {
	case ChangeSetAppliable, ChangeSetPartial, ChangeSetConflict:
	default:
		return fmt.Errorf("discard of %s change set %q: %w", cs.State, cs.ID, ErrChangeSetState)
	}
	if err := tx.appendApplyOp(cs, "discarded", "", "", ChangeSetDiscarded, false); err != nil {
		return err
	}
	return tx.emitTools(cs.TurnID, "change_set_discarded", map[string]any{"change_set_id": cs.ID, "from": cs.State})
}

// RecordVerdictEvidence journals what a verdict rested on: the snapshot
// version its reviewer saw, the ids of the tool results that reviewer
// received before giving it, in order, and the observations it added. Every
// item must come from the verdict's turn and participant, from an attempt
// dispatched no later than the verdict's, and must have been recorded before
// the verdict itself (its journal_seq precedes the verdict's); an observation
// must be at or below the version seen and is bound to one verdict only. A
// verdict recorded before verdicts took a journal_seq takes no evidence. It
// emits verdict_evidence in the same transaction.
func (tx *Tx) RecordVerdictEvidence(verdictID string, snapshotVersion int, resultIDs, observationIDs []string) error {
	if snapshotVersion < 0 {
		return fmt.Errorf("snapshot version must be >= 0, got %d", snapshotVersion)
	}
	var attemptID string
	var verdictSeq sql.NullInt64
	err := tx.tx.QueryRow(`SELECT attempt_id, journal_seq FROM verdicts WHERE id = ?`, verdictID).Scan(&attemptID, &verdictSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("verdict %q not found", verdictID)
	}
	if err != nil {
		return fmt.Errorf("query verdict: %w", err)
	}
	if !verdictSeq.Valid {
		return fmt.Errorf("verdict %q has no journal sequence: %w", verdictID, ErrEvidenceScope)
	}
	var turnID, participant string
	var attemptSeq sql.NullInt64
	if err := tx.tx.QueryRow(`SELECT turn_id, participant, journal_seq FROM attempts WHERE id = ?`, attemptID).
		Scan(&turnID, &participant, &attemptSeq); err != nil {
		return fmt.Errorf("query verdict attempt: %w", err)
	}
	if !attemptSeq.Valid {
		return fmt.Errorf("verdict attempt %q has no journal sequence: %w", attemptID, ErrEvidenceScope)
	}
	current, err := tx.SnapshotVersion(turnID)
	if err != nil {
		return err
	}
	if snapshotVersion > current {
		return fmt.Errorf("snapshot version %d exceeds the turn's %d", snapshotVersion, current)
	}
	var exists bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM verdict_evidence WHERE verdict_id = ?)`, verdictID).Scan(&exists); err != nil {
		return fmt.Errorf("query verdict evidence: %w", err)
	}
	if exists {
		return fmt.Errorf("verdict %q already has evidence recorded", verdictID)
	}
	if _, err := tx.tx.Exec(`INSERT INTO verdict_evidence (verdict_id, snapshot_version, recorded_at) VALUES (?, ?, ?)`,
		verdictID, snapshotVersion, nowStamp()); err != nil {
		return fmt.Errorf("insert verdict evidence: %w", err)
	}
	for i, rid := range resultIDs {
		var rTurn, rParticipant string
		var rSeq int64
		var rAttemptSeq sql.NullInt64
		err := tx.tx.QueryRow(`
			SELECT c.turn_id, c.participant, r.journal_seq, a.journal_seq
			FROM tool_results r JOIN tool_calls c ON c.id = r.tool_call_id JOIN attempts a ON a.id = c.attempt_id
			WHERE r.id = ?`, rid).Scan(&rTurn, &rParticipant, &rSeq, &rAttemptSeq)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tool result %q not found", rid)
		}
		if err != nil {
			return fmt.Errorf("query tool result: %w", err)
		}
		if rTurn != turnID || rParticipant != participant || !rAttemptSeq.Valid || rAttemptSeq.Int64 > attemptSeq.Int64 {
			return fmt.Errorf("tool result %q for verdict %q: %w", rid, verdictID, ErrEvidenceScope)
		}
		if rSeq > verdictSeq.Int64 {
			return fmt.Errorf("tool result %q was received after verdict %q: %w", rid, verdictID, ErrEvidenceScope)
		}
		if _, err := tx.tx.Exec(`INSERT INTO verdict_tool_results (verdict_id, result_id, position) VALUES (?, ?, ?)`,
			verdictID, rid, i); err != nil {
			return fmt.Errorf("insert verdict tool result: %w", err)
		}
	}
	for _, oid := range observationIDs {
		var oTurn, oParticipant string
		var oVersion int
		var oSeq int64
		var oAttemptSeq sql.NullInt64
		err := tx.tx.QueryRow(`
			SELECT o.turn_id, a.participant, o.snapshot_version, o.journal_seq, a.journal_seq
			FROM observations o JOIN attempts a ON a.id = o.attempt_id WHERE o.id = ?`, oid).
			Scan(&oTurn, &oParticipant, &oVersion, &oSeq, &oAttemptSeq)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("observation %q not found", oid)
		}
		if err != nil {
			return fmt.Errorf("query observation: %w", err)
		}
		if oTurn != turnID || oParticipant != participant || !oAttemptSeq.Valid || oAttemptSeq.Int64 > attemptSeq.Int64 {
			return fmt.Errorf("observation %q for verdict %q: %w", oid, verdictID, ErrEvidenceScope)
		}
		if oSeq > verdictSeq.Int64 {
			return fmt.Errorf("observation %q was recorded after verdict %q: %w", oid, verdictID, ErrEvidenceScope)
		}
		if oVersion > snapshotVersion {
			return fmt.Errorf("observation %q at version %d is beyond the version %d verdict %q saw", oid, oVersion, snapshotVersion, verdictID)
		}
		if _, err := tx.tx.Exec(`INSERT INTO verdict_observations (verdict_id, observation_id) VALUES (?, ?)`, verdictID, oid); err != nil {
			return fmt.Errorf("insert verdict observation: %w", err)
		}
	}
	return tx.emitTools(turnID, "verdict_evidence", map[string]any{
		"verdict_id": verdictID, "attempt_id": attemptID, "participant": participant,
		"verdict_journal_seq": verdictSeq.Int64, "snapshot_version": snapshotVersion,
		"result_ids":      append([]string{}, resultIDs...),
		"observation_ids": append([]string{}, observationIDs...),
	})
}

// UnboundObservationIDs returns, in snapshot-version order, the ids of the
// observations the participant's attempts recorded on the turn that no
// verdict's evidence has taken yet.
func (tx *Tx) UnboundObservationIDs(turnID, participant string) ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT o.id FROM observations o JOIN attempts a ON a.id = o.attempt_id
		WHERE o.turn_id = ? AND a.participant = ?
			AND o.id NOT IN (SELECT observation_id FROM verdict_observations)
		ORDER BY o.snapshot_version, o.journal_seq`, turnID, participant)
	if err != nil {
		return nil, fmt.Errorf("query unbound observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan unbound observation: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetVerdictEvidence returns what a verdict rested on; a verdict with no
// evidence recorded yields ErrNoSuchContent.
func (tx *Tx) GetVerdictEvidence(verdictID string) (VerdictEvidence, error) {
	ev := VerdictEvidence{VerdictID: verdictID, ResultIDs: []string{}, ObservationIDs: []string{}}
	err := tx.tx.QueryRow(`SELECT snapshot_version FROM verdict_evidence WHERE verdict_id = ?`, verdictID).Scan(&ev.SnapshotVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return VerdictEvidence{}, fmt.Errorf("verdict evidence %q: %w", verdictID, ErrNoSuchContent)
	}
	if err != nil {
		return VerdictEvidence{}, fmt.Errorf("query verdict evidence: %w", err)
	}
	collect := func(query string, dst *[]string) error {
		rows, err := tx.tx.Query(query, verdictID)
		if err != nil {
			return fmt.Errorf("query verdict evidence: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan verdict evidence: %w", err)
			}
			*dst = append(*dst, id)
		}
		return rows.Err()
	}
	if err := collect(`SELECT result_id FROM verdict_tool_results WHERE verdict_id = ? ORDER BY position`, &ev.ResultIDs); err != nil {
		return VerdictEvidence{}, err
	}
	if err := collect(`SELECT vo.observation_id FROM verdict_observations vo JOIN observations o ON o.id = vo.observation_id WHERE vo.verdict_id = ? ORDER BY o.snapshot_version`, &ev.ObservationIDs); err != nil {
		return VerdictEvidence{}, err
	}
	return ev, nil
}

// GetVerdictEvidence returns what a verdict rested on.
func (s *Store) GetVerdictEvidence(verdictID string) (ev VerdictEvidence, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		ev, txErr = tx.GetVerdictEvidence(verdictID)
		return txErr
	})
	return ev, err
}

// EmitAttemptEvent appends an event of the given type to the turn's journal
// after checking in the same transaction that attemptID exists and belongs to
// turnID, so an event bound to an attempt never names a foreign or missing one.
func (tx *Tx) EmitAttemptEvent(turnID, attemptID, eventType string, data []byte) error {
	attemptTurn, _, err := tx.turnAttempt(attemptID)
	if err != nil {
		return err
	}
	if attemptTurn != turnID {
		return fmt.Errorf("attempt %q belongs to turn %q, not %q", attemptID, attemptTurn, turnID)
	}
	_, err = tx.Emit(turnID, eventType, data)
	return err
}

// RecordObservationMode records, beside the turn's snapshot, whether the file
// at path was executable for its owner when its content was observed. Call it
// in the transaction that recorded the observation, which must be a file. The
// bit is written once.
func (tx *Tx) RecordObservationMode(turnID, path string, ownerExec bool) error {
	var kind string
	err := tx.tx.QueryRow(`SELECT kind FROM observations WHERE turn_id = ? AND path = ?`, turnID, []byte(path)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("path %q in turn %q: %w", path, turnID, ErrNotObserved)
	}
	if err != nil {
		return fmt.Errorf("query observation: %w", err)
	}
	if kind != ObservationFile {
		return fmt.Errorf("path %q in turn %q is not an observed file", path, turnID)
	}
	if _, err := tx.tx.Exec(`INSERT INTO observation_modes (turn_id, path, owner_exec) VALUES (?, ?, ?)`,
		turnID, []byte(path), ownerExec); err != nil {
		return fmt.Errorf("insert observation mode: %w", err)
	}
	return nil
}

// ObservationOwnerExec returns the recorded owner-execute bit for path. A path
// with no mode record, including one observed before modes were recorded,
// reads as not executable.
func (tx *Tx) ObservationOwnerExec(turnID, path string) (bool, error) {
	var exec bool
	err := tx.tx.QueryRow(`SELECT owner_exec FROM observation_modes WHERE turn_id = ? AND path = ?`, turnID, []byte(path)).Scan(&exec)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query observation mode: %w", err)
	}
	return exec, nil
}
