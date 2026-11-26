-- Command execution records (v7 delta sections 2, 3, 4, 6 and 8). A bash call
-- is a tool_calls row of class exec, so the class CHECK is widened by
-- rebuilding the table as migration 016 did; rows, ids, the index and the
-- append-only triggers are kept. The tables after it hold what the call
-- cites: the command and its referee decision before it starts, its result or
-- the outcome that delivers none, each referee read, and each proxy
-- connection. All are append-only; a connection's close is a second row.

CREATE TABLE tool_calls_new (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	turn_id TEXT NOT NULL REFERENCES turns(id),
	participant TEXT NOT NULL,
	call_index INTEGER NOT NULL CHECK (call_index >= 0),
	provider_call_id TEXT NOT NULL,
	name TEXT NOT NULL,
	class TEXT NOT NULL CHECK (class IN ('read', 'web', 'write', 'none', 'exec')),
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	recorded_at TEXT NOT NULL,
	UNIQUE (attempt_id, call_index),
	UNIQUE (attempt_id, provider_call_id)
);

INSERT INTO tool_calls_new (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
	SELECT id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at FROM tool_calls;

DROP TABLE tool_calls;
ALTER TABLE tool_calls_new RENAME TO tool_calls;

CREATE INDEX tool_calls_turn_idx ON tool_calls(turn_id, participant);

CREATE TRIGGER tool_calls_update_prevented BEFORE UPDATE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

CREATE TRIGGER tool_calls_delete_prevented BEFORE DELETE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

-- One accepted bash call, written before the command starts. The command's
-- bytes are in tool_content. view is the workspace state it ran against.
CREATE TABLE referee_decisions (
	id TEXT PRIMARY KEY,
	tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id),
	decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny')),
	reason TEXT NOT NULL,
	cause TEXT CHECK (cause IS NULL OR cause IN ('answer', 'timeout', 'adapter_error', 'refusal', 'malformed', 'cut_off', 'calls_wrong_status', 'calls_without_tools')),
	recorded_at TEXT NOT NULL,
	CHECK ((decision = 'deny') = (cause IS NOT NULL))
);

CREATE TABLE exec_calls (
	tool_call_id TEXT PRIMARY KEY REFERENCES tool_calls(id),
	command_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	timeout_s_effective INTEGER NOT NULL CHECK (timeout_s_effective > 0),
	shell TEXT NOT NULL,
	snapshot_version INTEGER NOT NULL CHECK (snapshot_version >= 0),
	overlay_sha256 TEXT NOT NULL,
	modes_sha256 TEXT NOT NULL,
	referee_decision_id TEXT NOT NULL UNIQUE REFERENCES referee_decisions(id),
	sandbox_backend TEXT NOT NULL,
	policy_hash TEXT NOT NULL,
	recorded_at TEXT NOT NULL
);

-- The result of a finished command. At most one per call, and none beside an
-- abandoned outcome (the triggers below keep the two exclusive).
CREATE TABLE exec_results (
	tool_call_id TEXT PRIMARY KEY REFERENCES exec_calls(tool_call_id),
	exit_code INTEGER NOT NULL,
	signal INTEGER,
	timed_out BOOLEAN NOT NULL CHECK (timed_out IN (0, 1)),
	truncated BOOLEAN NOT NULL CHECK (truncated IN (0, 1)),
	capture_complete BOOLEAN NOT NULL CHECK (capture_complete IN (0, 1)),
	output_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	output_bytes_kept INTEGER NOT NULL CHECK (output_bytes_kept >= 0),
	output_bytes_total INTEGER NOT NULL CHECK (output_bytes_total >= output_bytes_kept),
	duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
	recorded_at TEXT NOT NULL
);

-- A call that delivers no result. A call has at most one abandoned outcome;
-- containment_unverified may stand beside it.
CREATE TABLE exec_outcomes (
	id TEXT PRIMARY KEY,
	tool_call_id TEXT NOT NULL REFERENCES exec_calls(tool_call_id),
	kind TEXT NOT NULL CHECK (kind IN ('abandoned', 'containment_unverified')),
	cause TEXT NOT NULL CHECK (cause IN ('cancelled', 'turn_deadline', 'terminal_transition', 'engine_stop', 'recovery', 'cleanup_deadline')),
	recorded_at TEXT NOT NULL,
	UNIQUE (tool_call_id, kind)
);

CREATE TRIGGER exec_results_no_abandoned BEFORE INSERT ON exec_results
WHEN EXISTS (SELECT 1 FROM exec_outcomes WHERE tool_call_id = NEW.tool_call_id AND kind = 'abandoned')
BEGIN
	SELECT RAISE(ABORT, 'exec call is abandoned and has no result');
END;

CREATE TRIGGER exec_outcomes_no_result BEFORE INSERT ON exec_outcomes
WHEN NEW.kind = 'abandoned' AND EXISTS (SELECT 1 FROM exec_results WHERE tool_call_id = NEW.tool_call_id)
BEGIN
	SELECT RAISE(ABORT, 'exec call has a result and is not abandoned');
END;

-- Each read the referee made in one decision, in order. sha256 and byte_len
-- are NULL when the read returned an error.
CREATE TABLE referee_reads (
	decision_id TEXT NOT NULL REFERENCES referee_decisions(id),
	position INTEGER NOT NULL CHECK (position >= 0),
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	name TEXT NOT NULL,
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	sha256 TEXT REFERENCES tool_content(sha256),
	byte_len INTEGER CHECK (byte_len IS NULL OR byte_len >= 0),
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (decision_id, position),
	CHECK ((sha256 IS NULL) = (byte_len IS NULL))
);

-- A proxy connection, journaled before it is opened. A refused one carries
-- the status it was refused with. Its close is a separate row.
CREATE TABLE exec_connections (
	id TEXT PRIMARY KEY,
	tool_call_id TEXT NOT NULL REFERENCES tool_calls(id),
	host TEXT NOT NULL,
	port INTEGER NOT NULL CHECK (port BETWEEN 0 AND 65535),
	allowed BOOLEAN NOT NULL CHECK (allowed IN (0, 1)),
	refusal_status INTEGER CHECK (refusal_status IS NULL OR refusal_status IN (403, 407)),
	recorded_at TEXT NOT NULL,
	CHECK (allowed = (refusal_status IS NULL))
);

CREATE INDEX exec_connections_call_idx ON exec_connections(tool_call_id, recorded_at);

CREATE TABLE exec_connection_closes (
	connection_id TEXT PRIMARY KEY REFERENCES exec_connections(id),
	bytes_up INTEGER NOT NULL CHECK (bytes_up >= 0),
	bytes_down INTEGER NOT NULL CHECK (bytes_down >= 0),
	recorded_at TEXT NOT NULL
);

CREATE TRIGGER referee_decisions_update_prevented BEFORE UPDATE ON referee_decisions
BEGIN
	SELECT RAISE(ABORT, 'referee_decisions table is immutable');
END;

CREATE TRIGGER referee_decisions_delete_prevented BEFORE DELETE ON referee_decisions
BEGIN
	SELECT RAISE(ABORT, 'referee_decisions table is immutable');
END;

CREATE TRIGGER exec_calls_update_prevented BEFORE UPDATE ON exec_calls
BEGIN
	SELECT RAISE(ABORT, 'exec_calls table is immutable');
END;

CREATE TRIGGER exec_calls_delete_prevented BEFORE DELETE ON exec_calls
BEGIN
	SELECT RAISE(ABORT, 'exec_calls table is immutable');
END;

CREATE TRIGGER exec_results_update_prevented BEFORE UPDATE ON exec_results
BEGIN
	SELECT RAISE(ABORT, 'exec_results table is immutable');
END;

CREATE TRIGGER exec_results_delete_prevented BEFORE DELETE ON exec_results
BEGIN
	SELECT RAISE(ABORT, 'exec_results table is immutable');
END;

CREATE TRIGGER exec_outcomes_update_prevented BEFORE UPDATE ON exec_outcomes
BEGIN
	SELECT RAISE(ABORT, 'exec_outcomes table is immutable');
END;

CREATE TRIGGER exec_outcomes_delete_prevented BEFORE DELETE ON exec_outcomes
BEGIN
	SELECT RAISE(ABORT, 'exec_outcomes table is immutable');
END;

CREATE TRIGGER referee_reads_update_prevented BEFORE UPDATE ON referee_reads
BEGIN
	SELECT RAISE(ABORT, 'referee_reads table is immutable');
END;

CREATE TRIGGER referee_reads_delete_prevented BEFORE DELETE ON referee_reads
BEGIN
	SELECT RAISE(ABORT, 'referee_reads table is immutable');
END;

CREATE TRIGGER exec_connections_update_prevented BEFORE UPDATE ON exec_connections
BEGIN
	SELECT RAISE(ABORT, 'exec_connections table is immutable');
END;

CREATE TRIGGER exec_connections_delete_prevented BEFORE DELETE ON exec_connections
BEGIN
	SELECT RAISE(ABORT, 'exec_connections table is immutable');
END;

CREATE TRIGGER exec_connection_closes_update_prevented BEFORE UPDATE ON exec_connection_closes
BEGIN
	SELECT RAISE(ABORT, 'exec_connection_closes table is immutable');
END;

CREATE TRIGGER exec_connection_closes_delete_prevented BEFORE DELETE ON exec_connection_closes
BEGIN
	SELECT RAISE(ABORT, 'exec_connection_closes table is immutable');
END;
