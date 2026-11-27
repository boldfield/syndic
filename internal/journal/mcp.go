package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MCP records (v9 delta sections 2, 4 and 9). As for the tool records, every
// method runs inside the caller's transaction and emits its journal event in
// that same transaction. Arguments and results are stored in tool_content
// under their SHA-256.

// ErrMCPOutcomeRecorded is returned for a second outcome of one MCP call.
var ErrMCPOutcomeRecorded = errors.New("mcp call already has an outcome")

// MCP call states. A call is in MCPStateSent from the moment it is journaled
// until its outcome is recorded.
const (
	MCPStateSent      = "sent"
	MCPStateResult    = "result"
	MCPStateError     = "error"
	MCPStateAbandoned = "abandoned"
)

// MCPCallInput is what is known about an accepted read call before it is
// sent: the calling attempt, the server and tool, the canonical arguments, the
// referee's decision, the server identity, the tool's pin and the protocol
// revision negotiated.
type MCPCallInput struct {
	AttemptID      string
	Server         string
	Tool           string
	Args           []byte
	RefereeAllowed bool
	RefereeReason  string
	ServerIdentity string
	PinSHA256      string
	Revision       string
}

// MCPCall is one MCP read call. State is MCPStateSent until an outcome is
// recorded. ResultSHA256 is empty for an abandoned call or one still sent;
// Cause is set for abandoned only.
type MCPCall struct {
	ID              string
	TurnID          string
	Participant     string
	AttemptID       string
	Server          string
	Tool            string
	ArgsSHA256      string
	RefereeDecision string
	RefereeReason   string
	ServerIdentity  string
	PinSHA256       string
	Revision        string
	RecordedAt      time.Time
	State           string
	ResultSHA256    string
	ResultByteLen   int64
	Cause           string
	OutcomeAt       time.Time
}

// MCPProviderName maps the name a participant was offered to the server and
// tool it stands for. Index is k in mcp_<k>_<s>.
type MCPProviderName struct {
	TurnID       string
	Participant  string
	Index        int
	ProviderName string
	Server       string
	Tool         string
	RecordedAt   time.Time
}

// RecordMCPCall journals a call before it is sent, with its referee decision,
// server identity, pin and revision. Its state is sent. It emits mcp_call.
func (tx *Tx) RecordMCPCall(in MCPCallInput) (MCPCall, error) {
	if in.Server == "" || in.Tool == "" {
		return MCPCall{}, fmt.Errorf("mcp call needs a server and a tool")
	}
	if in.ServerIdentity == "" || in.PinSHA256 == "" || in.Revision == "" {
		return MCPCall{}, fmt.Errorf("mcp call needs a server identity, a pin and a revision")
	}
	decision := RefereeDeny
	if in.RefereeAllowed {
		decision = RefereeAllow
	}
	turnID, participant, err := tx.turnAttempt(in.AttemptID)
	if err != nil {
		return MCPCall{}, err
	}
	argsSHA, err := tx.StoreContent(in.Args)
	if err != nil {
		return MCPCall{}, err
	}
	id, err := generateID()
	if err != nil {
		return MCPCall{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO mcp_calls (id, turn_id, participant, attempt_id, server, tool, args_sha256,
			referee_decision, referee_reason, server_identity, pin_sha256, revision, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, turnID, participant, in.AttemptID, in.Server, in.Tool, argsSHA,
		decision, in.RefereeReason, in.ServerIdentity, in.PinSHA256, in.Revision, at); err != nil {
		return MCPCall{}, fmt.Errorf("insert mcp call: %w", err)
	}
	if err := tx.emitTools(turnID, "mcp_call", map[string]any{
		"call_id": id, "attempt_id": in.AttemptID, "participant": participant,
		"server": in.Server, "tool": in.Tool, "args_sha256": argsSHA,
		"referee_decision": decision, "server_identity": in.ServerIdentity,
		"pin_sha256": in.PinSHA256, "revision": in.Revision,
	}); err != nil {
		return MCPCall{}, err
	}
	out := MCPCall{
		ID: id, TurnID: turnID, Participant: participant, AttemptID: in.AttemptID,
		Server: in.Server, Tool: in.Tool, ArgsSHA256: argsSHA,
		RefereeDecision: decision, RefereeReason: in.RefereeReason,
		ServerIdentity: in.ServerIdentity, PinSHA256: in.PinSHA256, Revision: in.Revision,
		State: MCPStateSent,
	}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordMCPOutcome journals how a call ended: MCPStateResult with the
// rendered result, MCPStateError with the error text, or MCPStateAbandoned
// with a cause and no body. The body is stored by hash. A second outcome is
// refused with ErrMCPOutcomeRecorded. It emits mcp_result, mcp_error or
// mcp_abandoned.
func (tx *Tx) RecordMCPOutcome(callID, state string, body []byte, cause string) (MCPCall, error) {
	var event string
	switch state {
	case MCPStateResult:
		event = "mcp_result"
	case MCPStateError:
		event = "mcp_error"
	case MCPStateAbandoned:
		event = "mcp_abandoned"
	default:
		return MCPCall{}, fmt.Errorf("unknown mcp outcome state %q", state)
	}
	if (state == MCPStateAbandoned) != (cause != "") {
		return MCPCall{}, fmt.Errorf("an abandoned mcp call needs a cause and no other outcome has one")
	}
	if state == MCPStateAbandoned && body != nil {
		return MCPCall{}, fmt.Errorf("an abandoned mcp call delivers no result")
	}
	call, err := tx.getMCPCall(callID)
	if err != nil {
		return MCPCall{}, err
	}
	if call.State != MCPStateSent {
		return MCPCall{}, fmt.Errorf("call %q: %w", callID, ErrMCPOutcomeRecorded)
	}
	var shaArg *string
	var lenArg *int64
	payload := map[string]any{"call_id": callID}
	if state == MCPStateAbandoned {
		payload["cause"] = cause
	} else {
		sum, err := tx.StoreContent(body)
		if err != nil {
			return MCPCall{}, err
		}
		n := int64(len(body))
		shaArg, lenArg = &sum, &n
		payload["sha256"], payload["byte_len"] = sum, n
		call.ResultSHA256, call.ResultByteLen = sum, n
	}
	var causeArg *string
	if cause != "" {
		causeArg = &cause
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO mcp_outcomes (call_id, state, result_sha256, result_byte_len, cause, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, callID, state, shaArg, lenArg, causeArg, at); err != nil {
		return MCPCall{}, fmt.Errorf("insert mcp outcome: %w", err)
	}
	if err := tx.emitTools(call.TurnID, event, payload); err != nil {
		return MCPCall{}, err
	}
	call.State, call.Cause = state, cause
	call.OutcomeAt, _ = parseTime(at)
	return call, nil
}

// SetTurnMCPPinFile records the SHA-256 of the pin file read at submit. It is
// fixed for the turn: setting the same hash again is a no-op and a different
// one is an error. It emits mcp_pin_file.
func (tx *Tx) SetTurnMCPPinFile(turnID, sha256 string) error {
	if sha256 == "" {
		return fmt.Errorf("pin file hash must not be empty")
	}
	var cur sql.NullString
	err := tx.tx.QueryRow(`SELECT mcp_pin_file_sha256 FROM turns WHERE id = ?`, turnID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("turn %q not found", turnID)
	}
	if err != nil {
		return fmt.Errorf("query turn pin file hash: %w", err)
	}
	if cur.Valid {
		if cur.String == sha256 {
			return nil
		}
		return fmt.Errorf("turn %q already has pin file hash %s", turnID, cur.String)
	}
	if _, err := tx.tx.Exec(`UPDATE turns SET mcp_pin_file_sha256 = ? WHERE id = ?`, sha256, turnID); err != nil {
		return fmt.Errorf("update turn pin file hash: %w", err)
	}
	return tx.emitTools(turnID, "mcp_pin_file", map[string]any{"sha256": sha256})
}

// TurnMCPPinFile returns the pin file hash recorded on a turn, empty if none.
func (tx *Tx) TurnMCPPinFile(turnID string) (string, error) {
	var cur sql.NullString
	err := tx.tx.QueryRow(`SELECT mcp_pin_file_sha256 FROM turns WHERE id = ?`, turnID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("turn %q not found", turnID)
	}
	if err != nil {
		return "", fmt.Errorf("query turn pin file hash: %w", err)
	}
	return cur.String, nil
}

// RecordMCPProviderNames records the names one participant was offered for
// the turn, in index order: names[i] is the provider name for index i. The
// mapping is written once per participant; a second write is an error. It
// emits mcp_provider_names.
func (tx *Tx) RecordMCPProviderNames(turnID, participant string, names []MCPProviderName) error {
	if participant == "" {
		return fmt.Errorf("provider names need a participant")
	}
	var n int
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM turns WHERE id = ?`, turnID).Scan(&n); err != nil {
		return fmt.Errorf("query turn: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("turn %q not found", turnID)
	}
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM mcp_provider_names WHERE turn_id = ? AND participant = ?`,
		turnID, participant).Scan(&n); err != nil {
		return fmt.Errorf("query provider names: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("provider names for %q on turn %q already recorded", participant, turnID)
	}
	at := nowStamp()
	entries := make([]map[string]any, 0, len(names))
	for i, m := range names {
		if m.ProviderName == "" || m.Server == "" || m.Tool == "" {
			return fmt.Errorf("provider name %d needs a name, a server and a tool", i)
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO mcp_provider_names (turn_id, participant, index_k, provider_name, server, tool, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, turnID, participant, i, m.ProviderName, m.Server, m.Tool, at); err != nil {
			return fmt.Errorf("insert provider name: %w", err)
		}
		entries = append(entries, map[string]any{"index": i, "name": m.ProviderName, "server": m.Server, "tool": m.Tool})
	}
	return tx.emitTools(turnID, "mcp_provider_names", map[string]any{"participant": participant, "names": entries})
}

// MCPProviderNames returns the names a participant was offered for a turn, in
// index order.
func (tx *Tx) MCPProviderNames(turnID, participant string) ([]MCPProviderName, error) {
	rows, err := tx.tx.Query(`
		SELECT index_k, provider_name, server, tool, recorded_at
		FROM mcp_provider_names WHERE turn_id = ? AND participant = ? ORDER BY index_k
	`, turnID, participant)
	if err != nil {
		return nil, fmt.Errorf("query provider names: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPProviderName
	for rows.Next() {
		m := MCPProviderName{TurnID: turnID, Participant: participant}
		var at string
		if err := rows.Scan(&m.Index, &m.ProviderName, &m.Server, &m.Tool, &at); err != nil {
			return nil, fmt.Errorf("scan provider name: %w", err)
		}
		m.RecordedAt, _ = parseTime(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

const mcpCallSelect = `
	SELECT c.id, c.turn_id, c.participant, c.attempt_id, c.server, c.tool, c.args_sha256,
		c.referee_decision, c.referee_reason, c.server_identity, c.pin_sha256, c.revision, c.recorded_at,
		o.state, o.result_sha256, o.result_byte_len, o.cause, o.recorded_at
	FROM mcp_calls c LEFT JOIN mcp_outcomes o ON o.call_id = c.id`

func scanMCPCall(sc interface{ Scan(...any) error }) (MCPCall, error) {
	var c MCPCall
	var at string
	var state, sha, cause, outAt sql.NullString
	var n sql.NullInt64
	if err := sc.Scan(&c.ID, &c.TurnID, &c.Participant, &c.AttemptID, &c.Server, &c.Tool, &c.ArgsSHA256,
		&c.RefereeDecision, &c.RefereeReason, &c.ServerIdentity, &c.PinSHA256, &c.Revision, &at,
		&state, &sha, &n, &cause, &outAt); err != nil {
		return MCPCall{}, err
	}
	c.RecordedAt, _ = parseTime(at)
	c.State = MCPStateSent
	if state.Valid {
		c.State = state.String
		c.ResultSHA256, c.ResultByteLen, c.Cause = sha.String, n.Int64, cause.String
		c.OutcomeAt, _ = parseTime(outAt.String)
	}
	return c, nil
}

func (tx *Tx) getMCPCall(callID string) (MCPCall, error) {
	c, err := scanMCPCall(tx.tx.QueryRow(mcpCallSelect+` WHERE c.id = ?`, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPCall{}, fmt.Errorf("mcp call %q not found", callID)
	}
	if err != nil {
		return MCPCall{}, fmt.Errorf("query mcp call: %w", err)
	}
	return c, nil
}

// GetMCPCall returns one MCP call with its outcome, if any.
func (tx *Tx) GetMCPCall(callID string) (MCPCall, error) { return tx.getMCPCall(callID) }

// MCPCallsByTurn returns a turn's MCP calls in the order they were journaled.
func (tx *Tx) MCPCallsByTurn(turnID string) ([]MCPCall, error) {
	rows, err := tx.tx.Query(mcpCallSelect+` WHERE c.turn_id = ? ORDER BY c.recorded_at, c.rowid`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query mcp calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPCall
	for rows.Next() {
		c, err := scanMCPCall(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp call: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
