package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Command execution records (v7 delta sections 2, 3, 4, 6 and 8). As for the
// tool records, every method runs inside the caller's transaction and emits
// its journal event in that same transaction. The command, its output and the
// referee's reads are stored in tool_content under their SHA-256.

// Errors returned by the exec record methods, wrapped with detail.
var (
	// ErrExecResultRecorded is returned for a second result, or a result for
	// a call that has an outcome.
	ErrExecResultRecorded = errors.New("exec call already has a result or is abandoned")
	// ErrExecOutcomeRecorded is returned for a second outcome of one kind, or
	// an outcome for a call that has a result.
	ErrExecOutcomeRecorded = errors.New("exec call outcome already recorded")
)

// Referee decisions and the causes of a deny.
const (
	RefereeAllow = "allow"
	RefereeDeny  = "deny"

	RefereeCauseAnswer            = "answer"
	RefereeCauseTimeout           = "timeout"
	RefereeCauseAdapterError      = "adapter_error"
	RefereeCauseRefusal           = "refusal"
	RefereeCauseMalformed         = "malformed"
	RefereeCauseCutOff            = "cut_off"
	RefereeCauseCallsWrongStatus  = "calls_wrong_status"
	RefereeCauseCallsWithoutTools = "calls_without_tools"
)

// Exec outcome kinds and causes.
const (
	ExecAbandoned             = "abandoned"
	ExecContainmentUnverified = "containment_unverified"

	ExecCauseCancelled          = "cancelled"
	ExecCauseTurnDeadline       = "turn_deadline"
	ExecCauseTerminalTransition = "terminal_transition"
	ExecCauseEngineStop         = "engine_stop"
	ExecCauseRecovery           = "recovery"
	ExecCauseCleanupDeadline    = "cleanup_deadline"
)

// ExecView is the workspace state a command ran against (v7 section 2).
type ExecView struct {
	SnapshotVersion int
	OverlaySHA256   string
	ModesSHA256     string
}

// ExecCallInput is what is known about an accepted bash call before it starts.
type ExecCallInput struct {
	ToolCallID        string
	Command           []byte
	TimeoutSEffective int
	Shell             string
	View              ExecView
	RefereeDecisionID string
	SandboxBackend    string
	PolicyHash        string
}

// ExecResultInput is what a finished command produced. Signal is nil unless
// the shell ended by a signal. Output is the kept raw bytes.
type ExecResultInput struct {
	ExitCode         int
	Signal           *int
	TimedOut         bool
	Truncated        bool
	CaptureComplete  bool
	Output           []byte
	OutputBytesTotal int64
	DurationMS       int64
}

// ExecCall is one accepted bash call with its result and outcomes, if any.
type ExecCall struct {
	ToolCallID        string
	TurnID            string
	CommandSHA256     string
	TimeoutSEffective int
	Shell             string
	View              ExecView
	RefereeDecisionID string
	SandboxBackend    string
	PolicyHash        string
	RecordedAt        time.Time
	Result            *ExecResult
	Outcomes          []ExecOutcome
}

// ExecResult is the result of a finished command.
type ExecResult struct {
	ToolCallID       string
	ExitCode         int
	Signal           *int
	TimedOut         bool
	Truncated        bool
	CaptureComplete  bool
	OutputSHA256     string
	OutputBytesKept  int64
	OutputBytesTotal int64
	DurationMS       int64
	RecordedAt       time.Time
}

// ExecOutcome records a call that delivers no result.
type ExecOutcome struct {
	ID         string
	ToolCallID string
	Kind       string
	Cause      string
	RecordedAt time.Time
}

// RefereeDecision is the referee's decision on one bash call. Cause is set
// for a deny only. Reads are its reads in order.
type RefereeDecision struct {
	ID         string
	ToolCallID string
	TurnID     string
	Decision   string
	Reason     string
	Cause      string
	RecordedAt time.Time
	Reads      []RefereeRead
}

// RefereeRead is one read the referee made. SHA256 is empty and ByteLen zero
// when the read returned an error.
type RefereeRead struct {
	DecisionID string
	Position   int
	AttemptID  string
	Name       string
	ArgsSHA256 string
	SHA256     string
	ByteLen    int64
	RecordedAt time.Time
}

// ExecConnection is one proxy connection. RefusalStatus is 403 or 407 when it
// was not allowed. Close is nil until the connection's close is recorded.
type ExecConnection struct {
	ID            string
	ToolCallID    string
	TurnID        string
	Host          string
	Port          int
	Allowed       bool
	RefusalStatus int
	RecordedAt    time.Time
	Close         *ExecConnectionClose
}

// ExecConnectionClose is the bytes each way, recorded once at close.
type ExecConnectionClose struct {
	ConnectionID string
	BytesUp      int64
	BytesDown    int64
	RecordedAt   time.Time
}

// execCallTurn returns the turn of a tool call of class exec.
func (tx *Tx) execCallTurn(toolCallID string) (string, error) {
	var turnID, class string
	err := tx.tx.QueryRow(`SELECT turn_id, class FROM tool_calls WHERE id = ?`, toolCallID).Scan(&turnID, &class)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("tool call %q not found", toolCallID)
	}
	if err != nil {
		return "", fmt.Errorf("query tool call: %w", err)
	}
	if class != ToolClassExec {
		return "", fmt.Errorf("tool call %q is class %q, not exec", toolCallID, class)
	}
	return turnID, nil
}

// RecordRefereeDecision journals the referee's decision on a bash call. A deny
// needs a cause and an allow carries none. It emits referee_decision.
func (tx *Tx) RecordRefereeDecision(toolCallID, decision, reason, cause string) (RefereeDecision, error) {
	switch decision {
	case RefereeAllow:
		if cause != "" {
			return RefereeDecision{}, fmt.Errorf("allow carries no cause")
		}
	case RefereeDeny:
		if cause == "" {
			return RefereeDecision{}, fmt.Errorf("deny needs a cause")
		}
	default:
		return RefereeDecision{}, fmt.Errorf("unknown referee decision %q", decision)
	}
	turnID, err := tx.execCallTurn(toolCallID)
	if err != nil {
		return RefereeDecision{}, err
	}
	id, err := generateID()
	if err != nil {
		return RefereeDecision{}, err
	}
	var causeArg *string
	if cause != "" {
		causeArg = &cause
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO referee_decisions (id, tool_call_id, decision, reason, cause, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, toolCallID, decision, reason, causeArg, at); err != nil {
		return RefereeDecision{}, fmt.Errorf("insert referee decision: %w", err)
	}
	payload := map[string]any{"decision_id": id, "call_id": toolCallID, "decision": decision, "reason": reason}
	if cause != "" {
		payload["cause"] = cause
	}
	if err := tx.emitTools(turnID, "referee_decision", payload); err != nil {
		return RefereeDecision{}, err
	}
	out := RefereeDecision{ID: id, ToolCallID: toolCallID, TurnID: turnID, Decision: decision, Reason: reason, Cause: cause}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordRefereeRead journals one read of a decision, after the reads already
// recorded for it. args is the read call's arguments; result is what the read
// returned, or nil with failed set when it returned an error. Both are stored
// by hash. It emits referee_read.
func (tx *Tx) RecordRefereeRead(decisionID, attemptID, name string, args, result []byte, failed bool) (RefereeRead, error) {
	if name == "" {
		return RefereeRead{}, fmt.Errorf("referee read needs a tool name")
	}
	var turnID string
	err := tx.tx.QueryRow(`
		SELECT c.turn_id FROM referee_decisions d JOIN tool_calls c ON c.id = d.tool_call_id WHERE d.id = ?
	`, decisionID).Scan(&turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return RefereeRead{}, fmt.Errorf("referee decision %q not found", decisionID)
	}
	if err != nil {
		return RefereeRead{}, fmt.Errorf("query referee decision: %w", err)
	}
	argsSHA, err := tx.StoreContent(args)
	if err != nil {
		return RefereeRead{}, err
	}
	var resSHA *string
	var resLen *int64
	if !failed {
		sum, err := tx.StoreContent(result)
		if err != nil {
			return RefereeRead{}, err
		}
		n := int64(len(result))
		resSHA, resLen = &sum, &n
	}
	var pos int
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM referee_reads WHERE decision_id = ?`, decisionID).Scan(&pos); err != nil {
		return RefereeRead{}, fmt.Errorf("count referee reads: %w", err)
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO referee_reads (decision_id, position, attempt_id, name, args_sha256, sha256, byte_len, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, decisionID, pos, attemptID, name, argsSHA, resSHA, resLen, at); err != nil {
		return RefereeRead{}, fmt.Errorf("insert referee read: %w", err)
	}
	payload := map[string]any{
		"decision_id": decisionID, "attempt_id": attemptID, "name": name,
		"args_sha256": argsSHA, "sha256": nil, "byte_len": nil,
	}
	out := RefereeRead{DecisionID: decisionID, Position: pos, AttemptID: attemptID, Name: name, ArgsSHA256: argsSHA}
	if resSHA != nil {
		payload["sha256"], payload["byte_len"] = *resSHA, *resLen
		out.SHA256, out.ByteLen = *resSHA, *resLen
	}
	if err := tx.emitTools(turnID, "referee_read", payload); err != nil {
		return RefereeRead{}, err
	}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordExecCall journals an accepted bash call before its command starts,
// with the referee decision that allowed it. The command is stored by hash. It
// emits exec_call.
func (tx *Tx) RecordExecCall(in ExecCallInput) (ExecCall, error) {
	if in.TimeoutSEffective <= 0 {
		return ExecCall{}, fmt.Errorf("effective timeout must be > 0, got %d", in.TimeoutSEffective)
	}
	if in.Shell == "" || in.SandboxBackend == "" || in.PolicyHash == "" {
		return ExecCall{}, fmt.Errorf("exec call needs a shell, a sandbox backend and a policy hash")
	}
	turnID, err := tx.execCallTurn(in.ToolCallID)
	if err != nil {
		return ExecCall{}, err
	}
	var decisionCall, decision string
	err = tx.tx.QueryRow(`SELECT tool_call_id, decision FROM referee_decisions WHERE id = ?`, in.RefereeDecisionID).Scan(&decisionCall, &decision)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecCall{}, fmt.Errorf("referee decision %q not found", in.RefereeDecisionID)
	}
	if err != nil {
		return ExecCall{}, fmt.Errorf("query referee decision: %w", err)
	}
	if decisionCall != in.ToolCallID || decision != RefereeAllow {
		return ExecCall{}, fmt.Errorf("referee decision %q is not an allow for call %q", in.RefereeDecisionID, in.ToolCallID)
	}
	cmdSHA, err := tx.StoreContent(in.Command)
	if err != nil {
		return ExecCall{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO exec_calls (tool_call_id, command_sha256, timeout_s_effective, shell, snapshot_version,
			overlay_sha256, modes_sha256, referee_decision_id, sandbox_backend, policy_hash, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.ToolCallID, cmdSHA, in.TimeoutSEffective, in.Shell, in.View.SnapshotVersion,
		in.View.OverlaySHA256, in.View.ModesSHA256, in.RefereeDecisionID, in.SandboxBackend, in.PolicyHash, at); err != nil {
		return ExecCall{}, fmt.Errorf("insert exec call: %w", err)
	}
	if err := tx.emitTools(turnID, "exec_call", map[string]any{
		"call_id": in.ToolCallID, "command_sha256": cmdSHA, "timeout_s_effective": in.TimeoutSEffective,
		"shell": in.Shell, "snapshot_version": in.View.SnapshotVersion,
		"overlay_sha256": in.View.OverlaySHA256, "modes_sha256": in.View.ModesSHA256,
		"decision_id": in.RefereeDecisionID, "sandbox_backend": in.SandboxBackend, "policy_hash": in.PolicyHash,
	}); err != nil {
		return ExecCall{}, err
	}
	out := ExecCall{
		ToolCallID: in.ToolCallID, TurnID: turnID, CommandSHA256: cmdSHA, TimeoutSEffective: in.TimeoutSEffective,
		Shell: in.Shell, View: in.View, RefereeDecisionID: in.RefereeDecisionID,
		SandboxBackend: in.SandboxBackend, PolicyHash: in.PolicyHash,
	}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordExecResult journals the result of a finished command. The kept output
// is stored by hash. A second result, or a result for a call with an outcome, is
// refused with ErrExecResultRecorded. It emits exec_result.
func (tx *Tx) RecordExecResult(toolCallID string, in ExecResultInput) (ExecResult, error) {
	if in.DurationMS < 0 || in.OutputBytesTotal < int64(len(in.Output)) {
		return ExecResult{}, fmt.Errorf("exec result has inconsistent byte counts or duration")
	}
	if in.Truncated != (in.OutputBytesTotal != int64(len(in.Output))) {
		return ExecResult{}, fmt.Errorf("truncated must be true exactly when kept and total bytes differ")
	}
	turnID, err := tx.execCallTurn(toolCallID)
	if err != nil {
		return ExecResult{}, err
	}
	var taken bool
	if err := tx.tx.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM exec_results WHERE tool_call_id = ?)
		    OR EXISTS(SELECT 1 FROM exec_outcomes WHERE tool_call_id = ?)
	`, toolCallID, toolCallID).Scan(&taken); err != nil {
		return ExecResult{}, fmt.Errorf("query exec result: %w", err)
	}
	if taken {
		return ExecResult{}, fmt.Errorf("call %q: %w", toolCallID, ErrExecResultRecorded)
	}
	sum, err := tx.StoreContent(in.Output)
	if err != nil {
		return ExecResult{}, err
	}
	kept := int64(len(in.Output))
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO exec_results (tool_call_id, exit_code, signal, timed_out, truncated, capture_complete,
			output_sha256, output_bytes_kept, output_bytes_total, duration_ms, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, toolCallID, in.ExitCode, in.Signal, in.TimedOut, in.Truncated, in.CaptureComplete,
		sum, kept, in.OutputBytesTotal, in.DurationMS, at); err != nil {
		return ExecResult{}, fmt.Errorf("insert exec result: %w", err)
	}
	payload := map[string]any{
		"call_id": toolCallID, "exit_code": in.ExitCode, "timed_out": in.TimedOut,
		"truncated": in.Truncated, "capture_complete": in.CaptureComplete, "output_sha256": sum,
		"output_bytes_kept": kept, "output_bytes_total": in.OutputBytesTotal, "duration_ms": in.DurationMS,
	}
	if in.Signal != nil {
		payload["signal"] = *in.Signal
	}
	if err := tx.emitTools(turnID, "exec_result", payload); err != nil {
		return ExecResult{}, err
	}
	out := ExecResult{
		ToolCallID: toolCallID, ExitCode: in.ExitCode, Signal: in.Signal, TimedOut: in.TimedOut,
		Truncated: in.Truncated, CaptureComplete: in.CaptureComplete, OutputSHA256: sum,
		OutputBytesKept: kept, OutputBytesTotal: in.OutputBytesTotal, DurationMS: in.DurationMS,
	}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordExecOutcome journals that a call delivers no result: kind abandoned or
// containment_unverified, with its cause. A call has at most one abandoned
// outcome and none beside a result; containment_unverified may stand beside
// abandoned. Either is refused with ErrExecOutcomeRecorded when it conflicts.
// It emits exec_abandoned, or exec_containment_unverified.
func (tx *Tx) RecordExecOutcome(toolCallID, kind, cause string) (ExecOutcome, error) {
	var event string
	switch kind {
	case ExecAbandoned:
		event = "exec_abandoned"
	case ExecContainmentUnverified:
		event = "exec_containment_unverified"
	default:
		return ExecOutcome{}, fmt.Errorf("unknown exec outcome kind %q", kind)
	}
	switch cause {
	case ExecCauseCancelled, ExecCauseTurnDeadline, ExecCauseTerminalTransition,
		ExecCauseEngineStop, ExecCauseRecovery, ExecCauseCleanupDeadline:
	default:
		return ExecOutcome{}, fmt.Errorf("unknown exec outcome cause %q", cause)
	}
	turnID, err := tx.execCallTurn(toolCallID)
	if err != nil {
		return ExecOutcome{}, err
	}
	var taken bool
	if err := tx.tx.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM exec_outcomes WHERE tool_call_id = ? AND kind = ?)
		    OR EXISTS(SELECT 1 FROM exec_results WHERE tool_call_id = ?)
	`, toolCallID, kind, toolCallID).Scan(&taken); err != nil {
		return ExecOutcome{}, fmt.Errorf("query exec outcome: %w", err)
	}
	if taken {
		return ExecOutcome{}, fmt.Errorf("call %q, %s: %w", toolCallID, kind, ErrExecOutcomeRecorded)
	}
	id, err := generateID()
	if err != nil {
		return ExecOutcome{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO exec_outcomes (id, tool_call_id, kind, cause, recorded_at) VALUES (?, ?, ?, ?, ?)
	`, id, toolCallID, kind, cause, at); err != nil {
		return ExecOutcome{}, fmt.Errorf("insert exec outcome: %w", err)
	}
	if err := tx.emitTools(turnID, event, map[string]any{"outcome_id": id, "call_id": toolCallID, "cause": cause}); err != nil {
		return ExecOutcome{}, err
	}
	out := ExecOutcome{ID: id, ToolCallID: toolCallID, Kind: kind, Cause: cause}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordExecConnection journals a proxy connection before it is opened. A
// connection that is not allowed carries the status it is refused with, 403
// or 407; an allowed one carries none (pass 0). It emits exec_connection.
func (tx *Tx) RecordExecConnection(toolCallID, host string, port int, allowed bool, refusalStatus int) (ExecConnection, error) {
	if host == "" || port < 0 || port > 65535 {
		return ExecConnection{}, fmt.Errorf("connection needs a host and a port in 0..65535")
	}
	if allowed != (refusalStatus == 0) || (!allowed && refusalStatus != 403 && refusalStatus != 407) {
		return ExecConnection{}, fmt.Errorf("a refused connection carries status 403 or 407, an allowed one none")
	}
	turnID, err := tx.execCallTurn(toolCallID)
	if err != nil {
		return ExecConnection{}, err
	}
	id, err := generateID()
	if err != nil {
		return ExecConnection{}, err
	}
	var status *int
	if !allowed {
		status = &refusalStatus
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO exec_connections (id, tool_call_id, host, port, allowed, refusal_status, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, toolCallID, host, port, allowed, status, at); err != nil {
		return ExecConnection{}, fmt.Errorf("insert exec connection: %w", err)
	}
	payload := map[string]any{"connection_id": id, "call_id": toolCallID, "host": host, "port": port, "allowed": allowed}
	if status != nil {
		payload["refusal_status"] = *status
	}
	if err := tx.emitTools(turnID, "exec_connection", payload); err != nil {
		return ExecConnection{}, err
	}
	out := ExecConnection{ID: id, ToolCallID: toolCallID, TurnID: turnID, Host: host, Port: port, Allowed: allowed, RefusalStatus: refusalStatus}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// CloseExecConnection records, once, the bytes each way of an allowed
// connection at its close, as a second row keyed to the first. It emits
// exec_connection_closed.
func (tx *Tx) CloseExecConnection(connectionID string, bytesUp, bytesDown int64) (ExecConnectionClose, error) {
	if bytesUp < 0 || bytesDown < 0 {
		return ExecConnectionClose{}, fmt.Errorf("byte counts must be >= 0")
	}
	var turnID string
	var allowed, closed bool
	err := tx.tx.QueryRow(`
		SELECT c.turn_id, x.allowed, EXISTS(SELECT 1 FROM exec_connection_closes WHERE connection_id = x.id)
		FROM exec_connections x JOIN tool_calls c ON c.id = x.tool_call_id WHERE x.id = ?
	`, connectionID).Scan(&turnID, &allowed, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecConnectionClose{}, fmt.Errorf("connection %q not found", connectionID)
	}
	if err != nil {
		return ExecConnectionClose{}, fmt.Errorf("query connection: %w", err)
	}
	if !allowed {
		return ExecConnectionClose{}, fmt.Errorf("connection %q was refused and carried no bytes", connectionID)
	}
	if closed {
		return ExecConnectionClose{}, fmt.Errorf("connection %q is already closed", connectionID)
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO exec_connection_closes (connection_id, bytes_up, bytes_down, recorded_at) VALUES (?, ?, ?, ?)
	`, connectionID, bytesUp, bytesDown, at); err != nil {
		return ExecConnectionClose{}, fmt.Errorf("insert connection close: %w", err)
	}
	if err := tx.emitTools(turnID, "exec_connection_closed", map[string]any{
		"connection_id": connectionID, "bytes_up": bytesUp, "bytes_down": bytesDown,
	}); err != nil {
		return ExecConnectionClose{}, err
	}
	out := ExecConnectionClose{ConnectionID: connectionID, BytesUp: bytesUp, BytesDown: bytesDown}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

const execCallSelect = `
	SELECT e.tool_call_id, c.turn_id, e.command_sha256, e.timeout_s_effective, e.shell, e.snapshot_version,
	       e.overlay_sha256, e.modes_sha256, e.referee_decision_id, e.sandbox_backend, e.policy_hash, e.recorded_at,
	       r.exit_code, r.signal, r.timed_out, r.truncated, r.capture_complete, r.output_sha256,
	       r.output_bytes_kept, r.output_bytes_total, r.duration_ms, r.recorded_at
	FROM exec_calls e
	JOIN tool_calls c ON c.id = e.tool_call_id
	LEFT JOIN exec_results r ON r.tool_call_id = e.tool_call_id
`

func (tx *Tx) queryExecCalls(where string, args ...any) ([]ExecCall, error) {
	rows, err := tx.tx.Query(execCallSelect+where+` ORDER BY e.recorded_at, e.tool_call_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query exec calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var calls []ExecCall
	for rows.Next() {
		var c ExecCall
		var recordedAt string
		var exit, sig, kept, total, dur sql.NullInt64
		var timedOut, truncated, complete sql.NullBool
		var outSHA, rAt sql.NullString
		if err := rows.Scan(&c.ToolCallID, &c.TurnID, &c.CommandSHA256, &c.TimeoutSEffective, &c.Shell, &c.View.SnapshotVersion,
			&c.View.OverlaySHA256, &c.View.ModesSHA256, &c.RefereeDecisionID, &c.SandboxBackend, &c.PolicyHash, &recordedAt,
			&exit, &sig, &timedOut, &truncated, &complete, &outSHA, &kept, &total, &dur, &rAt); err != nil {
			return nil, fmt.Errorf("scan exec call: %w", err)
		}
		if c.RecordedAt, err = parseTime(recordedAt); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		if outSHA.Valid {
			r := &ExecResult{
				ToolCallID: c.ToolCallID, ExitCode: int(exit.Int64), TimedOut: timedOut.Bool, Truncated: truncated.Bool,
				CaptureComplete: complete.Bool, OutputSHA256: outSHA.String, OutputBytesKept: kept.Int64,
				OutputBytesTotal: total.Int64, DurationMS: dur.Int64,
			}
			if sig.Valid {
				s := int(sig.Int64)
				r.Signal = &s
			}
			if r.RecordedAt, err = parseTime(rAt.String); err != nil {
				return nil, fmt.Errorf("parse result recorded_at: %w", err)
			}
			c.Result = r
		}
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exec calls: %w", err)
	}
	for i := range calls {
		if calls[i].Outcomes, err = tx.ExecOutcomes(calls[i].ToolCallID); err != nil {
			return nil, err
		}
	}
	return calls, nil
}

// GetExecCall returns one bash call with its result and outcomes, if any.
func (tx *Tx) GetExecCall(toolCallID string) (ExecCall, error) {
	calls, err := tx.queryExecCalls(` WHERE e.tool_call_id = ?`, toolCallID)
	if err != nil {
		return ExecCall{}, err
	}
	if len(calls) == 0 {
		return ExecCall{}, fmt.Errorf("exec call %q not found", toolCallID)
	}
	return calls[0], nil
}

// ExecCallsByTurn returns a turn's bash calls in the order they were recorded.
func (tx *Tx) ExecCallsByTurn(turnID string) ([]ExecCall, error) {
	return tx.queryExecCalls(` WHERE c.turn_id = ?`, turnID)
}

// ExecCallsByTurn returns a turn's bash calls with their results and outcomes.
func (s *Store) ExecCallsByTurn(turnID string) (calls []ExecCall, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		calls, txErr = tx.ExecCallsByTurn(turnID)
		return txErr
	})
	return calls, err
}

// ExecOutcomes returns a call's outcomes in the order they were recorded.
func (tx *Tx) ExecOutcomes(toolCallID string) ([]ExecOutcome, error) {
	rows, err := tx.tx.Query(`
		SELECT id, tool_call_id, kind, cause, recorded_at FROM exec_outcomes
		WHERE tool_call_id = ? ORDER BY recorded_at, rowid
	`, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("query exec outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ExecOutcome
	for rows.Next() {
		var o ExecOutcome
		var at string
		if err := rows.Scan(&o.ID, &o.ToolCallID, &o.Kind, &o.Cause, &at); err != nil {
			return nil, fmt.Errorf("scan exec outcome: %w", err)
		}
		if o.RecordedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exec outcomes: %w", err)
	}
	return out, nil
}

const refereeDecisionSelect = `
	SELECT d.id, d.tool_call_id, c.turn_id, d.decision, d.reason, d.cause, d.recorded_at
	FROM referee_decisions d JOIN tool_calls c ON c.id = d.tool_call_id
`

func (tx *Tx) queryRefereeDecisions(where string, args ...any) ([]RefereeDecision, error) {
	rows, err := tx.tx.Query(refereeDecisionSelect+where+` ORDER BY d.recorded_at, d.rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("query referee decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RefereeDecision
	for rows.Next() {
		var d RefereeDecision
		var cause sql.NullString
		var at string
		if err := rows.Scan(&d.ID, &d.ToolCallID, &d.TurnID, &d.Decision, &d.Reason, &cause, &at); err != nil {
			return nil, fmt.Errorf("scan referee decision: %w", err)
		}
		d.Cause = cause.String
		if d.RecordedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate referee decisions: %w", err)
	}
	for i := range out {
		if out[i].Reads, err = tx.RefereeReads(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetRefereeDecision returns one decision with its reads in order.
func (tx *Tx) GetRefereeDecision(id string) (RefereeDecision, error) {
	ds, err := tx.queryRefereeDecisions(` WHERE d.id = ?`, id)
	if err != nil {
		return RefereeDecision{}, err
	}
	if len(ds) == 0 {
		return RefereeDecision{}, fmt.Errorf("referee decision %q not found", id)
	}
	return ds[0], nil
}

// RefereeDecisionForCall returns the decision on a bash call, with its reads.
func (tx *Tx) RefereeDecisionForCall(toolCallID string) (RefereeDecision, error) {
	ds, err := tx.queryRefereeDecisions(` WHERE d.tool_call_id = ?`, toolCallID)
	if err != nil {
		return RefereeDecision{}, err
	}
	if len(ds) == 0 {
		return RefereeDecision{}, fmt.Errorf("no referee decision for call %q", toolCallID)
	}
	return ds[0], nil
}

// RefereeDecisionsByTurn returns a turn's decisions, with their reads, in the
// order they were recorded.
func (tx *Tx) RefereeDecisionsByTurn(turnID string) ([]RefereeDecision, error) {
	return tx.queryRefereeDecisions(` WHERE c.turn_id = ?`, turnID)
}

// RefereeDecisionsByTurn returns a turn's decisions with their reads.
func (s *Store) RefereeDecisionsByTurn(turnID string) (out []RefereeDecision, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		out, txErr = tx.RefereeDecisionsByTurn(turnID)
		return txErr
	})
	return out, err
}

// RefereeReads returns a decision's reads in order.
func (tx *Tx) RefereeReads(decisionID string) ([]RefereeRead, error) {
	rows, err := tx.tx.Query(`
		SELECT decision_id, position, attempt_id, name, args_sha256, sha256, byte_len, recorded_at
		FROM referee_reads WHERE decision_id = ? ORDER BY position
	`, decisionID)
	if err != nil {
		return nil, fmt.Errorf("query referee reads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RefereeRead
	for rows.Next() {
		var r RefereeRead
		var sha sql.NullString
		var n sql.NullInt64
		var at string
		if err := rows.Scan(&r.DecisionID, &r.Position, &r.AttemptID, &r.Name, &r.ArgsSHA256, &sha, &n, &at); err != nil {
			return nil, fmt.Errorf("scan referee read: %w", err)
		}
		r.SHA256, r.ByteLen = sha.String, n.Int64
		if r.RecordedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate referee reads: %w", err)
	}
	return out, nil
}

const execConnectionSelect = `
	SELECT x.id, x.tool_call_id, c.turn_id, x.host, x.port, x.allowed, x.refusal_status, x.recorded_at,
	       k.bytes_up, k.bytes_down, k.recorded_at
	FROM exec_connections x
	JOIN tool_calls c ON c.id = x.tool_call_id
	LEFT JOIN exec_connection_closes k ON k.connection_id = x.id
`

func (tx *Tx) queryExecConnections(where string, args ...any) ([]ExecConnection, error) {
	rows, err := tx.tx.Query(execConnectionSelect+where+` ORDER BY x.recorded_at, x.rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("query exec connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ExecConnection
	for rows.Next() {
		var x ExecConnection
		var status, up, down sql.NullInt64
		var at string
		var closeAt sql.NullString
		if err := rows.Scan(&x.ID, &x.ToolCallID, &x.TurnID, &x.Host, &x.Port, &x.Allowed, &status, &at,
			&up, &down, &closeAt); err != nil {
			return nil, fmt.Errorf("scan exec connection: %w", err)
		}
		x.RefusalStatus = int(status.Int64)
		if x.RecordedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		if closeAt.Valid {
			k := &ExecConnectionClose{ConnectionID: x.ID, BytesUp: up.Int64, BytesDown: down.Int64}
			if k.RecordedAt, err = parseTime(closeAt.String); err != nil {
				return nil, fmt.Errorf("parse close recorded_at: %w", err)
			}
			x.Close = k
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exec connections: %w", err)
	}
	return out, nil
}

// ExecConnections returns a call's proxy connections, each with its close if
// one is recorded, in the order they were journaled.
func (tx *Tx) ExecConnections(toolCallID string) ([]ExecConnection, error) {
	return tx.queryExecConnections(` WHERE x.tool_call_id = ?`, toolCallID)
}

// ExecConnectionsByTurn returns a turn's proxy connections.
func (tx *Tx) ExecConnectionsByTurn(turnID string) ([]ExecConnection, error) {
	return tx.queryExecConnections(` WHERE c.turn_id = ?`, turnID)
}
