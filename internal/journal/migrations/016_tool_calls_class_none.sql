-- A call in an accepted tool step may name a tool that is in no group: the
-- model invented it. It is still answered with a tool error and journaled
-- (v5 delta section 6), so tool_calls.class must admit 'none' for it. The
-- CHECK of migration 014 is widened by rebuilding the table; rows, ids, the
-- index and the append-only triggers are kept.

CREATE TABLE tool_calls_new (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	turn_id TEXT NOT NULL REFERENCES turns(id),
	participant TEXT NOT NULL,
	call_index INTEGER NOT NULL CHECK (call_index >= 0),
	provider_call_id TEXT NOT NULL,
	name TEXT NOT NULL,
	class TEXT NOT NULL CHECK (class IN ('read', 'web', 'write', 'none')),
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
