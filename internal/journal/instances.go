package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Instance and provenance records (v9 delta sections 6 and 8). Every method
// runs inside the caller's transaction and emits its journal event in that
// same transaction. None belongs to a turn, so the events carry no turn.

// Instance kinds.
const (
	MCPInstanceTurn    = "turn"
	MCPInstanceApply   = "apply"
	MCPInstanceListing = "listing"
	MCPInstanceRun     = "run"
)

// ErrMCPInstanceEnded is returned for a second end of one instance.
var ErrMCPInstanceEnded = errors.New("mcp instance already ended")

// ErrMCPClearNotStarted is returned for a clear's second record when its
// first is not in the journal.
var ErrMCPClearNotStarted = errors.New("provenance clear was not started")

// MCPInstanceInput is what is known of an instance before its process
// starts: the server, the kind, the owner (empty for the run instance) and
// the directories it uses; a directory it has none of is empty.
type MCPInstanceInput struct {
	Server       string
	Kind         string
	Owner        string
	StateDir     string
	TempDir      string
	WorkspaceDir string
}

// MCPInstance is one instance, as returned for an unresolved one.
// ContainmentFailures counts the failures journaled for it.
type MCPInstance struct {
	ID                  string
	Server              string
	Kind                string
	Owner               string
	StateDir            string
	TempDir             string
	WorkspaceDir        string
	RecordedAt          time.Time
	ContainmentFailures int
}

// ContainmentUnverified reports whether a containment failure was journaled
// for the instance, which is unresolved, so it has not ended since.
func (i MCPInstance) ContainmentUnverified() bool { return i.ContainmentFailures > 0 }

// RecordMCPInstanceStarted journals an instance before its process starts. It
// emits mcp_instance_started.
func (tx *Tx) RecordMCPInstanceStarted(in MCPInstanceInput) (MCPInstance, error) {
	switch in.Kind {
	case MCPInstanceTurn, MCPInstanceApply, MCPInstanceListing, MCPInstanceRun:
	default:
		return MCPInstance{}, fmt.Errorf("unknown mcp instance kind %q", in.Kind)
	}
	if in.Server == "" {
		return MCPInstance{}, fmt.Errorf("mcp instance needs a server")
	}
	if (in.Kind == MCPInstanceRun) != (in.Owner == "") {
		return MCPInstance{}, fmt.Errorf("only the run instance has no owner")
	}
	id, err := generateID()
	if err != nil {
		return MCPInstance{}, err
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO mcp_instances (id, server, kind, owner, state_dir, temp_dir, workspace_dir, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, in.Server, in.Kind, in.Owner, in.StateDir, in.TempDir, in.WorkspaceDir, at); err != nil {
		return MCPInstance{}, fmt.Errorf("insert mcp instance: %w", err)
	}
	if err := tx.emitTools("", "mcp_instance_started", map[string]any{
		"instance_id": id, "server": in.Server, "kind": in.Kind, "owner": in.Owner,
		"state_dir": in.StateDir, "temp_dir": in.TempDir, "workspace_dir": in.WorkspaceDir,
	}); err != nil {
		return MCPInstance{}, err
	}
	out := MCPInstance{ID: id, Server: in.Server, Kind: in.Kind, Owner: in.Owner,
		StateDir: in.StateDir, TempDir: in.TempDir, WorkspaceDir: in.WorkspaceDir}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// RecordMCPInstanceEnded journals that the backend has confirmed nothing of
// the instance remains. A second end is refused with ErrMCPInstanceEnded. It
// emits mcp_instance_ended.
func (tx *Tx) RecordMCPInstanceEnded(instanceID string) error {
	server, err := tx.mcpInstanceServer(instanceID)
	if err != nil {
		return err
	}
	var ended bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM mcp_instance_ends WHERE instance_id = ?)`, instanceID).Scan(&ended); err != nil {
		return fmt.Errorf("query mcp instance end: %w", err)
	}
	if ended {
		return ErrMCPInstanceEnded
	}
	if _, err := tx.tx.Exec(`INSERT INTO mcp_instance_ends (instance_id, recorded_at) VALUES (?, ?)`, instanceID, nowStamp()); err != nil {
		return fmt.Errorf("insert mcp instance end: %w", err)
	}
	return tx.emitTools("", "mcp_instance_ended", map[string]any{"instance_id": instanceID, "server": server})
}

// RecordMCPContainmentFailure journals that the instance and everything it
// started were not confirmed gone. The instance stays unresolved; a failure for
// an instance that has already ended is refused with ErrMCPInstanceEnded. It emits
// mcp_containment_unverified.
func (tx *Tx) RecordMCPContainmentFailure(instanceID, detail string) error {
	server, err := tx.mcpInstanceServer(instanceID)
	if err != nil {
		return err
	}
	var ended bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM mcp_instance_ends WHERE instance_id = ?)`, instanceID).Scan(&ended); err != nil {
		return fmt.Errorf("query mcp instance end: %w", err)
	}
	if ended {
		return ErrMCPInstanceEnded
	}
	id, err := generateID()
	if err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`INSERT INTO mcp_containment_failures (id, instance_id, detail, recorded_at) VALUES (?, ?, ?, ?)`,
		id, instanceID, detail, nowStamp()); err != nil {
		return fmt.Errorf("insert mcp containment failure: %w", err)
	}
	return tx.emitTools("", "mcp_containment_unverified", map[string]any{
		"instance_id": instanceID, "server": server, "detail": detail,
	})
}

func (tx *Tx) mcpInstanceServer(instanceID string) (string, error) {
	var server string
	err := tx.tx.QueryRow(`SELECT server FROM mcp_instances WHERE id = ?`, instanceID).Scan(&server)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("mcp instance %q not found", instanceID)
	}
	if err != nil {
		return "", fmt.Errorf("query mcp instance: %w", err)
	}
	return server, nil
}

// UnresolvedMCPInstances returns the instances with a start and no end, oldest
// first. A non-empty server limits them to that server's.
func (tx *Tx) UnresolvedMCPInstances(server string) ([]MCPInstance, error) {
	rows, err := tx.tx.Query(`
		SELECT i.id, i.server, i.kind, i.owner, i.state_dir, i.temp_dir, i.workspace_dir, i.recorded_at,
			(SELECT COUNT(*) FROM mcp_containment_failures f WHERE f.instance_id = i.id)
		FROM mcp_instances i
		WHERE (? = '' OR i.server = ?)
			AND NOT EXISTS (SELECT 1 FROM mcp_instance_ends e WHERE e.instance_id = i.id)
		ORDER BY i.recorded_at, i.rowid
	`, server, server)
	if err != nil {
		return nil, fmt.Errorf("query unresolved mcp instances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPInstance
	for rows.Next() {
		var i MCPInstance
		var at string
		if err := rows.Scan(&i.ID, &i.Server, &i.Kind, &i.Owner, &i.StateDir, &i.TempDir, &i.WorkspaceDir, &at, &i.ContainmentFailures); err != nil {
			return nil, fmt.Errorf("scan mcp instance: %w", err)
		}
		i.RecordedAt, _ = parseTime(at)
		out = append(out, i)
	}
	return out, rows.Err()
}

// MCPDirectoryMark is a marked persistent state directory, by its key and
// the server it belongs to.
type MCPDirectoryMark struct {
	Server    string
	Directory string
}

// MCPMarks is the marks in force.
type MCPMarks struct {
	Destinations []string
	Directories  []MCPDirectoryMark
}

// RecordMCPFileHolding journals, before the exposure it records, that the
// destination (a remote server's URL, or a local one's command and
// arguments) may hold file content. Marking again is allowed. It emits
// mcp_file_holding.
func (tx *Tx) RecordMCPFileHolding(destination string) error {
	if destination == "" {
		return fmt.Errorf("file-holding mark needs a destination")
	}
	if err := tx.insertProvenance("file_holding", nil, &destination, nil); err != nil {
		return err
	}
	return tx.emitTools("", "mcp_file_holding", map[string]any{"destination": destination})
}

// RecordMCPStateFileHolding journals, before the exposure it records, that
// the persistent state directory with this key, of this server, may hold
// file content. It emits mcp_state_file_holding.
func (tx *Tx) RecordMCPStateFileHolding(server, directory string) error {
	if server == "" || directory == "" {
		return fmt.Errorf("state directory mark needs a server and a directory")
	}
	if err := tx.insertProvenance("state_file_holding", &server, nil, &directory); err != nil {
		return err
	}
	return tx.emitTools("", "mcp_state_file_holding", map[string]any{"server": server, "directory": directory})
}

// RecordMCPProvenanceClearStarted journals the first record of a clear of the
// server and its destination. No mark is cleared by it. Starting again after
// an interrupted clear is allowed. It emits mcp_provenance_clear_started.
func (tx *Tx) RecordMCPProvenanceClearStarted(server, destination string) error {
	if server == "" || destination == "" {
		return fmt.Errorf("provenance clear needs a server and a destination")
	}
	if err := tx.insertProvenance("clear_started", &server, &destination, nil); err != nil {
		return err
	}
	return tx.emitTools("", "mcp_provenance_clear_started", map[string]any{"server": server, "destination": destination})
}

// RecordMCPProvenanceCleared journals the second record of a clear, once
// every marked directory is deleted. The destination's mark and every
// directory mark of the server made before it stop being in force. It is
// refused with ErrMCPClearNotStarted unless the clear was started and not
// since completed. It emits mcp_provenance_cleared.
func (tx *Tx) RecordMCPProvenanceCleared(server, destination string) error {
	if server == "" || destination == "" {
		return fmt.Errorf("provenance clear needs a server and a destination")
	}
	var open bool
	if err := tx.tx.QueryRow(`
		SELECT COALESCE((SELECT MAX(seq) FROM mcp_provenance WHERE kind = 'clear_started' AND server = ? AND destination = ?), 0)
			> COALESCE((SELECT MAX(seq) FROM mcp_provenance WHERE kind = 'cleared' AND server = ? AND destination = ?), 0)
	`, server, destination, server, destination).Scan(&open); err != nil {
		return fmt.Errorf("query provenance clear: %w", err)
	}
	if !open {
		return ErrMCPClearNotStarted
	}
	if err := tx.insertProvenance("cleared", &server, &destination, nil); err != nil {
		return err
	}
	return tx.emitTools("", "mcp_provenance_cleared", map[string]any{"server": server, "destination": destination})
}

func (tx *Tx) insertProvenance(kind string, server, destination, directory *string) error {
	if _, err := tx.tx.Exec(`INSERT INTO mcp_provenance (kind, server, destination, directory, recorded_at) VALUES (?, ?, ?, ?, ?)`,
		kind, server, destination, directory, nowStamp()); err != nil {
		return fmt.Errorf("insert mcp provenance %s: %w", kind, err)
	}
	return nil
}

// MCPMarksInForce returns the destinations and directories marked and not
// since cleared. A clear takes effect only at its second record: a mark is
// out of force when a cleared record for its destination (or, for a
// directory, its server) follows it.
func (tx *Tx) MCPMarksInForce() (MCPMarks, error) {
	var marks MCPMarks
	rows, err := tx.tx.Query(`
		SELECT DISTINCT m.destination FROM mcp_provenance m
		WHERE m.kind = 'file_holding'
			AND NOT EXISTS (SELECT 1 FROM mcp_provenance c WHERE c.kind = 'cleared' AND c.destination = m.destination AND c.seq > m.seq)
		ORDER BY m.destination
	`)
	if err != nil {
		return MCPMarks{}, fmt.Errorf("query file-holding marks: %w", err)
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			_ = rows.Close()
			return MCPMarks{}, fmt.Errorf("scan file-holding mark: %w", err)
		}
		marks.Destinations = append(marks.Destinations, d)
	}
	if err := rows.Close(); err != nil {
		return MCPMarks{}, err
	}
	if err := rows.Err(); err != nil {
		return MCPMarks{}, err
	}
	rows, err = tx.tx.Query(`
		SELECT DISTINCT m.server, m.directory FROM mcp_provenance m
		WHERE m.kind = 'state_file_holding'
			AND NOT EXISTS (SELECT 1 FROM mcp_provenance c WHERE c.kind = 'cleared' AND c.server = m.server AND c.seq > m.seq)
		ORDER BY m.server, m.directory
	`)
	if err != nil {
		return MCPMarks{}, fmt.Errorf("query state directory marks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d MCPDirectoryMark
		if err := rows.Scan(&d.Server, &d.Directory); err != nil {
			return MCPMarks{}, fmt.Errorf("scan state directory mark: %w", err)
		}
		marks.Directories = append(marks.Directories, d)
	}
	return marks, rows.Err()
}

// MCPConnection is one proxy connection of an instance. CallID is empty when
// no call was in flight. RefusalStatus is 403 or 407 when it was not allowed.
// Close is nil until the connection's close is recorded.
type MCPConnection struct {
	ID            string
	InstanceID    string
	CallID        string
	Host          string
	Port          int
	Allowed       bool
	RefusalStatus int
	RecordedAt    time.Time
	Close         *ExecConnectionClose
}

// RecordMCPConnection journals an instance's proxy connection before it is
// opened, with the call in flight if there is one (callID empty if none). A
// connection that is not allowed carries 403 or 407; an allowed one passes 0.
// It emits mcp_connection.
func (tx *Tx) RecordMCPConnection(instanceID, callID, host string, port int, allowed bool, refusalStatus int) (MCPConnection, error) {
	if host == "" || port < 0 || port > 65535 {
		return MCPConnection{}, fmt.Errorf("connection needs a host and a port in 0..65535")
	}
	if allowed != (refusalStatus == 0) || (!allowed && refusalStatus != 403 && refusalStatus != 407) {
		return MCPConnection{}, fmt.Errorf("a refused connection carries status 403 or 407, an allowed one none")
	}
	server, err := tx.mcpInstanceServer(instanceID)
	if err != nil {
		return MCPConnection{}, err
	}
	id, err := generateID()
	if err != nil {
		return MCPConnection{}, err
	}
	var call, status any
	if callID != "" {
		call = callID
	}
	if !allowed {
		status = refusalStatus
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`
		INSERT INTO mcp_connections (id, instance_id, call_id, host, port, allowed, refusal_status, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, instanceID, call, host, port, allowed, status, at); err != nil {
		return MCPConnection{}, fmt.Errorf("insert mcp connection: %w", err)
	}
	payload := map[string]any{"connection_id": id, "instance_id": instanceID, "server": server, "host": host, "port": port, "allowed": allowed}
	if callID != "" {
		payload["call_id"] = callID
	}
	if !allowed {
		payload["refusal_status"] = refusalStatus
	}
	if err := tx.emitTools("", "mcp_connection", payload); err != nil {
		return MCPConnection{}, err
	}
	out := MCPConnection{ID: id, InstanceID: instanceID, CallID: callID, Host: host, Port: port, Allowed: allowed, RefusalStatus: refusalStatus}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// CloseMCPConnection records, once, the bytes each way of an allowed
// connection at its close. It emits mcp_connection_closed.
func (tx *Tx) CloseMCPConnection(connectionID string, bytesUp, bytesDown int64) (ExecConnectionClose, error) {
	if bytesUp < 0 || bytesDown < 0 {
		return ExecConnectionClose{}, fmt.Errorf("byte counts must be >= 0")
	}
	var allowed, closed bool
	err := tx.tx.QueryRow(`
		SELECT allowed, EXISTS(SELECT 1 FROM mcp_connection_closes WHERE connection_id = mcp_connections.id)
		FROM mcp_connections WHERE id = ?
	`, connectionID).Scan(&allowed, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecConnectionClose{}, fmt.Errorf("connection %q not found", connectionID)
	}
	if err != nil {
		return ExecConnectionClose{}, fmt.Errorf("query connection: %w", err)
	}
	if !allowed {
		return ExecConnectionClose{}, fmt.Errorf("a refused connection has no close")
	}
	if closed {
		return ExecConnectionClose{}, fmt.Errorf("connection %q already closed", connectionID)
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`INSERT INTO mcp_connection_closes (connection_id, bytes_up, bytes_down, recorded_at) VALUES (?, ?, ?, ?)`,
		connectionID, bytesUp, bytesDown, at); err != nil {
		return ExecConnectionClose{}, fmt.Errorf("insert connection close: %w", err)
	}
	if err := tx.emitTools("", "mcp_connection_closed", map[string]any{
		"connection_id": connectionID, "bytes_up": bytesUp, "bytes_down": bytesDown,
	}); err != nil {
		return ExecConnectionClose{}, err
	}
	out := ExecConnectionClose{ConnectionID: connectionID, BytesUp: bytesUp, BytesDown: bytesDown}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// MCPConnections returns an instance's proxy connections, oldest first, each
// with its close if recorded.
func (tx *Tx) MCPConnections(instanceID string) ([]MCPConnection, error) {
	rows, err := tx.tx.Query(`
		SELECT c.id, c.instance_id, COALESCE(c.call_id, ''), c.host, c.port, c.allowed, COALESCE(c.refusal_status, 0), c.recorded_at,
			x.bytes_up, x.bytes_down, x.recorded_at
		FROM mcp_connections c LEFT JOIN mcp_connection_closes x ON x.connection_id = c.id
		WHERE c.instance_id = ? ORDER BY c.recorded_at, c.rowid
	`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("query mcp connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPConnection
	for rows.Next() {
		var c MCPConnection
		var at string
		var up, down sql.NullInt64
		var closedAt sql.NullString
		if err := rows.Scan(&c.ID, &c.InstanceID, &c.CallID, &c.Host, &c.Port, &c.Allowed, &c.RefusalStatus, &at, &up, &down, &closedAt); err != nil {
			return nil, fmt.Errorf("scan mcp connection: %w", err)
		}
		c.RecordedAt, _ = parseTime(at)
		if up.Valid {
			cl := &ExecConnectionClose{ConnectionID: c.ID, BytesUp: up.Int64, BytesDown: down.Int64}
			cl.RecordedAt, _ = parseTime(closedAt.String)
			c.Close = cl
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
