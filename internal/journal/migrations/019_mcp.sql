-- MCP records (v9 delta sections 2, 4 and 9). The turn carries the SHA-256 of
-- the pin file it read at submit, fixed for the turn and set once from NULL.
-- A read call is journaled before it is sent; its state starts as sent and
-- its one outcome (a result, an error or abandoned) is a second row. The
-- name a participant was offered for each MCP tool is recorded per turn.
-- All three tables are append-only.

ALTER TABLE turns ADD COLUMN mcp_pin_file_sha256 TEXT;

CREATE TRIGGER turns_mcp_pin_file_set_once BEFORE UPDATE OF mcp_pin_file_sha256 ON turns
WHEN OLD.mcp_pin_file_sha256 IS NOT NULL AND NEW.mcp_pin_file_sha256 IS NOT OLD.mcp_pin_file_sha256
BEGIN
	SELECT RAISE(ABORT, 'turn pin file hash is fixed once set');
END;

CREATE TABLE mcp_calls (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	participant TEXT NOT NULL,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	server TEXT NOT NULL CHECK (server <> ''),
	tool TEXT NOT NULL CHECK (tool <> ''),
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	referee_decision TEXT NOT NULL CHECK (referee_decision IN ('allow', 'deny')),
	referee_reason TEXT NOT NULL,
	server_identity TEXT NOT NULL CHECK (server_identity <> ''),
	pin_sha256 TEXT NOT NULL CHECK (pin_sha256 <> ''),
	revision TEXT NOT NULL CHECK (revision <> ''),
	recorded_at TEXT NOT NULL
);

CREATE INDEX mcp_calls_turn_idx ON mcp_calls(turn_id, participant);

-- The outcome of a sent call, at most one. A result and an error carry their
-- text by hash; abandoned carries none and a cause.
CREATE TABLE mcp_outcomes (
	call_id TEXT PRIMARY KEY REFERENCES mcp_calls(id),
	state TEXT NOT NULL CHECK (state IN ('result', 'error', 'abandoned')),
	result_sha256 TEXT REFERENCES tool_content(sha256),
	result_byte_len INTEGER CHECK (result_byte_len IS NULL OR result_byte_len >= 0),
	cause TEXT,
	recorded_at TEXT NOT NULL,
	CHECK ((state = 'abandoned') = (result_sha256 IS NULL)),
	CHECK ((result_sha256 IS NULL) = (result_byte_len IS NULL)),
	CHECK ((state = 'abandoned') = (cause IS NOT NULL))
);

-- tool_name is the name offered to the model, mcp_<k>_<s>; k is index.
CREATE TABLE mcp_provider_names (
	turn_id TEXT NOT NULL REFERENCES turns(id),
	participant TEXT NOT NULL,
	index_k INTEGER NOT NULL CHECK (index_k >= 0),
	provider_name TEXT NOT NULL,
	server TEXT NOT NULL CHECK (server <> ''),
	tool TEXT NOT NULL CHECK (tool <> ''),
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (turn_id, participant, index_k),
	UNIQUE (turn_id, participant, provider_name),
	UNIQUE (turn_id, participant, server, tool)
);

CREATE TRIGGER mcp_calls_update_prevented BEFORE UPDATE ON mcp_calls
BEGIN
	SELECT RAISE(ABORT, 'mcp_calls table is immutable');
END;

CREATE TRIGGER mcp_calls_delete_prevented BEFORE DELETE ON mcp_calls
BEGIN
	SELECT RAISE(ABORT, 'mcp_calls table is immutable');
END;

CREATE TRIGGER mcp_outcomes_update_prevented BEFORE UPDATE ON mcp_outcomes
BEGIN
	SELECT RAISE(ABORT, 'mcp_outcomes table is immutable');
END;

CREATE TRIGGER mcp_outcomes_delete_prevented BEFORE DELETE ON mcp_outcomes
BEGIN
	SELECT RAISE(ABORT, 'mcp_outcomes table is immutable');
END;

CREATE TRIGGER mcp_provider_names_update_prevented BEFORE UPDATE ON mcp_provider_names
BEGIN
	SELECT RAISE(ABORT, 'mcp_provider_names table is immutable');
END;

CREATE TRIGGER mcp_provider_names_delete_prevented BEFORE DELETE ON mcp_provider_names
BEGIN
	SELECT RAISE(ABORT, 'mcp_provider_names table is immutable');
END;
