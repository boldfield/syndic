-- Tool calls and results (section 6): Each call and result is journaled and
-- stored by content hash. The attempt identifies whose call it was.
CREATE TABLE tool_calls (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	name TEXT NOT NULL,
	args_sha256 TEXT NOT NULL,
	class TEXT,
	status TEXT NOT NULL CHECK (status IN ('accepted', 'executed', 'error', 'abandoned')),
	result_sha256 TEXT,
	result_byte_len INTEGER,
	observed_identity TEXT,
	error_message TEXT
);

CREATE INDEX tool_calls_attempt_idx ON tool_calls(attempt_id);

-- Immutability: tool_calls are journaled at dispatch and never change.
CREATE TRIGGER tool_calls_update_prevented BEFORE UPDATE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

CREATE TRIGGER tool_calls_delete_prevented BEFORE DELETE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

-- Tool results: Append-only records of tool call results and status transitions.
-- Each result record captures the final status and outcome of a tool call after execution.
-- One tool call has at most one terminal result record.
CREATE TABLE tool_results (
	id TEXT PRIMARY KEY,
	tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id),
	status TEXT NOT NULL CHECK (status IN ('executed', 'error', 'abandoned')),
	result_sha256 TEXT,
	result_byte_len INTEGER,
	error_message TEXT,
	recorded_at TEXT NOT NULL
);

CREATE INDEX tool_results_tool_call_idx ON tool_results(tool_call_id);

-- Immutability: tool results are journaled and never change.
CREATE TRIGGER tool_results_update_prevented BEFORE UPDATE ON tool_results
BEGIN
	SELECT RAISE(ABORT, 'tool_results table is immutable');
END;

CREATE TRIGGER tool_results_delete_prevented BEFORE DELETE ON tool_results
BEGIN
	SELECT RAISE(ABORT, 'tool_results table is immutable');
END;

-- Observations: file observations acquired at different times during a turn.
-- An observation is one of: a file's content (kind='f'), a path's absence (kind='a'),
-- or a directory's entry list (kind='d'). The record is versioned: every observation
-- appends to it and bumps snapshot_version, journaled with the observation and the attempt.
CREATE TABLE observations (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	snapshot_version INTEGER NOT NULL,
	path TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('f', 'a', 'd')),
	base_sha256 TEXT,
	entries_sha256 TEXT,
	observed_at TEXT NOT NULL,
	UNIQUE (turn_id, path, snapshot_version)
);

CREATE INDEX observations_turn_idx ON observations(turn_id);
CREATE INDEX observations_attempt_idx ON observations(attempt_id);

-- Immutability: observations are journaled and never change.
CREATE TRIGGER observations_update_prevented BEFORE UPDATE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

CREATE TRIGGER observations_delete_prevented BEFORE DELETE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

-- Change sets: At candidate freeze, the change set is the list of changes.
-- A change set is in one of: appliable, applied, partial, conflict, discarded.
CREATE TABLE change_sets (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL UNIQUE REFERENCES turns(id),
	state TEXT NOT NULL DEFAULT 'appliable' CHECK (state IN ('appliable', 'applied', 'partial', 'conflict', 'discarded')),
	snapshot_hash TEXT NOT NULL,
	changes_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX change_sets_turn_idx ON change_sets(turn_id);

-- Per-path records in a change set.
-- Outcomes are derived from apply_ops: paths with apply_file records are applied,
-- others remain pending or become conflict based on apply state.
CREATE TABLE change_set_paths (
	id TEXT PRIMARY KEY,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	path TEXT NOT NULL,
	op TEXT NOT NULL CHECK (op IN ('create', 'modify')),
	base_sha256 TEXT,
	new_sha256 TEXT NOT NULL,
	new_len INTEGER NOT NULL,
	UNIQUE (change_set_id, path)
);

CREATE INDEX change_set_paths_change_set_idx ON change_set_paths(change_set_id);

-- Immutability: change set paths are journaled and never change.
CREATE TRIGGER change_set_paths_update_prevented BEFORE UPDATE ON change_set_paths
BEGIN
	SELECT RAISE(ABORT, 'change_set_paths table is immutable');
END;

-- Apply operations: Track apply state and per-file results.
-- apply_started marks the beginning; apply_file records each applied path;
-- apply_finished marks the end with the final state.
CREATE TABLE apply_ops (
	id TEXT PRIMARY KEY,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	op_type TEXT NOT NULL CHECK (op_type IN ('started', 'file', 'finished')),
	path TEXT,
	new_sha256 TEXT,
	applied_at TEXT NOT NULL
);

CREATE INDEX apply_ops_change_set_idx ON apply_ops(change_set_id);

-- Immutability: apply operations are journaled and never change.
CREATE TRIGGER apply_ops_update_prevented BEFORE UPDATE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;

CREATE TRIGGER apply_ops_delete_prevented BEFORE DELETE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;

-- Verdict evidence: A verdict's record carries the ids of the tool results
-- that the reviewer received before giving it.
CREATE TABLE verdict_tool_results (
	id TEXT PRIMARY KEY,
	verdict_id TEXT NOT NULL REFERENCES verdicts(id),
	tool_call_id TEXT NOT NULL REFERENCES tool_calls(id),
	UNIQUE (verdict_id, tool_call_id)
);

CREATE INDEX verdict_tool_results_verdict_idx ON verdict_tool_results(verdict_id);
CREATE INDEX verdict_tool_results_tool_call_idx ON verdict_tool_results(tool_call_id);

-- Immutability: verdict evidence is journaled and never changes.
CREATE TRIGGER verdict_tool_results_update_prevented BEFORE UPDATE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

CREATE TRIGGER verdict_tool_results_delete_prevented BEFORE DELETE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

-- Staging: The engine keeps a staging map for the turn, path to new content hash.
-- A read in the same turn sees staged content over the snapshot.
CREATE TABLE staging (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path TEXT NOT NULL,
	new_content_hash TEXT NOT NULL,
	staged_at TEXT NOT NULL,
	UNIQUE (turn_id, path)
);

CREATE INDEX staging_turn_idx ON staging(turn_id);
