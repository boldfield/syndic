-- Tool calls and results (section 6): Each call and result is journaled and
-- stored by content hash. The attempt identifies whose call it was.
-- Content store: every byte a tool call, result, observation, change set or
-- staged write cites is stored here under its sha256, so the evidence can be
-- rebuilt from the hashes alone after a restart. Rows are append-only.
CREATE TABLE tool_content (
	sha256 TEXT PRIMARY KEY,
	body BLOB NOT NULL
);

CREATE TRIGGER tool_content_update_prevented BEFORE UPDATE ON tool_content
BEGIN
	SELECT RAISE(ABORT, 'tool_content table is immutable');
END;

CREATE TRIGGER tool_content_delete_prevented BEFORE DELETE ON tool_content
BEGIN
	SELECT RAISE(ABORT, 'tool_content table is immutable');
END;

-- A tool_calls row is the durable intent, journaled before dispatch. Its
-- status is accepted until a tool_results row exists for it.
CREATE TABLE tool_calls (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	name TEXT NOT NULL,
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	class TEXT,
	observed_identity TEXT
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
	result_sha256 TEXT REFERENCES tool_content(sha256),
	result_byte_len INTEGER,
	error_message TEXT,
	recorded_at TEXT NOT NULL,
	CHECK (status <> 'executed' OR result_sha256 IS NOT NULL),
	-- A stored result carries its byte length, and only a stored result does.
	CHECK ((result_sha256 IS NULL) = (result_byte_len IS NULL)),
	CHECK (result_byte_len IS NULL OR result_byte_len >= 0)
);

CREATE INDEX tool_results_tool_call_idx ON tool_results(tool_call_id);

-- The byte length must be that of the stored result content.
CREATE TRIGGER tool_results_byte_len_matches BEFORE INSERT ON tool_results
WHEN NEW.result_sha256 IS NOT NULL
	AND NEW.result_byte_len IS NOT (SELECT length(body) FROM tool_content WHERE sha256 = NEW.result_sha256)
BEGIN
	SELECT RAISE(ABORT, 'tool_results.result_byte_len does not match stored content');
END;

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
	base_sha256 TEXT REFERENCES tool_content(sha256),
	entries_sha256 TEXT REFERENCES tool_content(sha256),
	observed_at TEXT NOT NULL,
	UNIQUE (turn_id, path),
	UNIQUE (turn_id, snapshot_version),
	-- A file carries its content, a directory its entry list, an absence neither.
	CHECK ((kind = 'f' AND base_sha256 IS NOT NULL AND entries_sha256 IS NULL)
		OR (kind = 'a' AND base_sha256 IS NULL AND entries_sha256 IS NULL)
		OR (kind = 'd' AND base_sha256 IS NULL AND entries_sha256 IS NOT NULL))
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
-- The state is not stored here: it is derived from the append-only apply_ops.
CREATE TABLE change_sets (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL UNIQUE REFERENCES turns(id),
	snapshot_hash TEXT NOT NULL REFERENCES tool_content(sha256),
	snapshot_version INTEGER NOT NULL DEFAULT 0,
	changes_hash TEXT NOT NULL REFERENCES tool_content(sha256),
	created_at TEXT NOT NULL
);

CREATE INDEX change_sets_turn_idx ON change_sets(turn_id);

-- Per-path records in a change set.
-- Outcomes (pending, applied, conflict) are derived from apply_ops.
CREATE TABLE change_set_paths (
	id TEXT PRIMARY KEY,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	path TEXT NOT NULL,
	op TEXT NOT NULL CHECK (op IN ('create', 'modify')),
	base_sha256 TEXT REFERENCES tool_content(sha256),
	new_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	new_len INTEGER NOT NULL,
	UNIQUE (change_set_id, path)
);

CREATE INDEX change_set_paths_change_set_idx ON change_set_paths(change_set_id);

-- Immutability: change set paths are journaled and never change.
CREATE TRIGGER change_set_paths_update_prevented BEFORE UPDATE ON change_set_paths
BEGIN
	SELECT RAISE(ABORT, 'change_set_paths table is immutable');
END;

CREATE TRIGGER change_set_paths_delete_prevented BEFORE DELETE ON change_set_paths
BEGIN
	SELECT RAISE(ABORT, 'change_set_paths table is immutable');
END;

CREATE TRIGGER change_sets_delete_prevented BEFORE DELETE ON change_sets
BEGIN
	SELECT RAISE(ABORT, 'change_sets table is immutable');
END;

CREATE TRIGGER change_sets_update_prevented BEFORE UPDATE ON change_sets
BEGIN
	SELECT RAISE(ABORT, 'change_sets table is immutable');
END;

-- Apply operations: Track apply state and per-file results.
-- 'started' marks the beginning; 'file' records each path's outcome (applied or
-- conflict); 'finished' marks the end with the final state (applied, partial or
-- conflict); 'discarded' marks the change set discarded.
CREATE TABLE apply_ops (
	id TEXT PRIMARY KEY,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	op_type TEXT NOT NULL CHECK (op_type IN ('started', 'file', 'finished', 'discarded')),
	path TEXT,
	new_sha256 TEXT REFERENCES tool_content(sha256),
	outcome TEXT CHECK (outcome IN ('applied', 'conflict')),
	final_state TEXT CHECK (final_state IN ('applied', 'partial', 'conflict')),
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
-- that the reviewer received before giving it, the snapshot version it saw
-- and the observations it added.
CREATE TABLE verdict_tool_results (
	id TEXT PRIMARY KEY,
	verdict_id TEXT NOT NULL REFERENCES verdicts(id),
	tool_result_id TEXT NOT NULL REFERENCES tool_results(id),
	UNIQUE (verdict_id, tool_result_id)
);

CREATE INDEX verdict_tool_results_verdict_idx ON verdict_tool_results(verdict_id);
CREATE INDEX verdict_tool_results_tool_result_idx ON verdict_tool_results(tool_result_id);

CREATE TABLE verdict_snapshots (
	verdict_id TEXT PRIMARY KEY REFERENCES verdicts(id),
	snapshot_version INTEGER NOT NULL
);

CREATE TABLE verdict_observations (
	verdict_id TEXT NOT NULL REFERENCES verdicts(id),
	observation_id TEXT NOT NULL REFERENCES observations(id),
	PRIMARY KEY (verdict_id, observation_id)
);

CREATE TRIGGER verdict_tool_results_update_prevented BEFORE UPDATE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

CREATE TRIGGER verdict_tool_results_delete_prevented BEFORE DELETE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

CREATE TRIGGER verdict_snapshots_update_prevented BEFORE UPDATE ON verdict_snapshots
BEGIN
	SELECT RAISE(ABORT, 'verdict_snapshots table is immutable');
END;

CREATE TRIGGER verdict_snapshots_delete_prevented BEFORE DELETE ON verdict_snapshots
BEGIN
	SELECT RAISE(ABORT, 'verdict_snapshots table is immutable');
END;

CREATE TRIGGER verdict_observations_update_prevented BEFORE UPDATE ON verdict_observations
BEGIN
	SELECT RAISE(ABORT, 'verdict_observations table is immutable');
END;

CREATE TRIGGER verdict_observations_delete_prevented BEFORE DELETE ON verdict_observations
BEGIN
	SELECT RAISE(ABORT, 'verdict_observations table is immutable');
END;

-- Staging: The engine keeps a staging map for the turn, path to new content hash.
-- A read in the same turn sees staged content over the snapshot.
CREATE TABLE staging (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path TEXT NOT NULL,
	new_content_hash TEXT NOT NULL REFERENCES tool_content(sha256),
	-- base_sha256 is taken the first time the path is staged: the hash of the
	-- stored base content, or 'absent'.
	base_sha256 TEXT NOT NULL DEFAULT 'absent',
	staged_at TEXT NOT NULL,
	UNIQUE (turn_id, path)
);

CREATE INDEX staging_turn_idx ON staging(turn_id);
