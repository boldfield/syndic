-- Tool calls, results, observations, staging, change sets, apply state and
-- verdict evidence (v5 delta section 6). Every byte string any of these cites
-- is in tool_content under its SHA-256, so args, results, bases, listings and
-- staged content can be rebuilt from the journal after a restart. Records are
-- append-only; state that changes (per-path apply outcome, change-set state)
-- is derived from apply_ops rather than rewritten in place.

-- Content-addressed store for tool evidence.
CREATE TABLE tool_content (
	sha256 TEXT PRIMARY KEY CHECK (length(sha256) = 64),
	byte_len INTEGER NOT NULL CHECK (byte_len >= 0),
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

-- The accepted intent of one call in an accepted tool step. The row holds only
-- what was known before dispatch; the outcome is a separate tool_results row.
CREATE TABLE tool_calls (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	turn_id TEXT NOT NULL REFERENCES turns(id),
	participant TEXT NOT NULL,
	call_index INTEGER NOT NULL CHECK (call_index >= 0),
	provider_call_id TEXT NOT NULL,
	name TEXT NOT NULL,
	class TEXT NOT NULL CHECK (class IN ('read', 'web', 'write')),
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	recorded_at TEXT NOT NULL,
	UNIQUE (attempt_id, call_index),
	UNIQUE (attempt_id, provider_call_id)
);

CREATE INDEX tool_calls_turn_idx ON tool_calls(turn_id, participant);

CREATE TRIGGER tool_calls_update_prevented BEFORE UPDATE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

CREATE TRIGGER tool_calls_delete_prevented BEFORE DELETE ON tool_calls
BEGIN
	SELECT RAISE(ABORT, 'tool_calls table is immutable');
END;

-- The one terminal outcome of a call. A call with no row is still 'accepted'.
-- journal_seq is taken from the journal-wide counter (migration 005) when the
-- result is received, so its receipt orders against verdicts and attempts.
CREATE TABLE tool_results (
	id TEXT PRIMARY KEY,
	tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id),
	status TEXT NOT NULL CHECK (status IN ('executed', 'error', 'abandoned')),
	result_sha256 TEXT REFERENCES tool_content(sha256),
	result_byte_len INTEGER CHECK (result_byte_len IS NULL OR result_byte_len >= 0),
	error_code TEXT,
	journal_seq INTEGER NOT NULL UNIQUE,
	recorded_at TEXT NOT NULL,
	CHECK ((status = 'abandoned') = (result_sha256 IS NULL)),
	CHECK ((result_sha256 IS NULL) = (result_byte_len IS NULL)),
	CHECK ((status = 'error') = (error_code IS NOT NULL))
);

CREATE TRIGGER tool_results_update_prevented BEFORE UPDATE ON tool_results
BEGIN
	SELECT RAISE(ABORT, 'tool_results table is immutable');
END;

CREATE TRIGGER tool_results_delete_prevented BEFORE DELETE ON tool_results
BEGIN
	SELECT RAISE(ABORT, 'tool_results table is immutable');
END;

-- The turn's snapshot record: one observation per path, each at its own
-- version, so a later observation never contradicts an earlier one. kind f
-- stores the file's base content, d the canonical entry list, a nothing.
-- journal_seq orders the observation against verdicts, as for tool_results.
CREATE TABLE observations (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	snapshot_version INTEGER NOT NULL CHECK (snapshot_version >= 1),
	path BLOB NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('f', 'a', 'd')),
	content_sha256 TEXT REFERENCES tool_content(sha256),
	journal_seq INTEGER NOT NULL UNIQUE,
	recorded_at TEXT NOT NULL,
	UNIQUE (turn_id, snapshot_version),
	UNIQUE (turn_id, path),
	CHECK ((kind = 'a') = (content_sha256 IS NULL))
);

CREATE TRIGGER observations_update_prevented BEFORE UPDATE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

CREATE TRIGGER observations_delete_prevented BEFORE DELETE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

-- The turn's staging map: path to new content, with the base fixed the first
-- time the path is staged (NULL base is 'absent'). Re-staging replaces the new
-- content only; unstage deletes the row. Both are journaled as events.
CREATE TABLE staging (
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path BLOB NOT NULL,
	base_sha256 TEXT REFERENCES tool_content(sha256),
	new_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	staged_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (turn_id, path)
);

CREATE TRIGGER staging_base_immutable BEFORE UPDATE OF turn_id, path, base_sha256, staged_at ON staging
BEGIN
	SELECT RAISE(ABORT, 'staging base is fixed at first staging');
END;

-- A candidate's change set, frozen with the snapshot version and hashes it
-- rested on. State is derived from apply_ops.
CREATE TABLE change_sets (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	candidate_id TEXT NOT NULL UNIQUE REFERENCES candidates(id),
	snapshot_version INTEGER NOT NULL CHECK (snapshot_version >= 0),
	snapshot_hash TEXT NOT NULL,
	changes_hash TEXT NOT NULL,
	frozen_at TEXT NOT NULL
);

CREATE INDEX change_sets_turn_idx ON change_sets(turn_id);

CREATE TRIGGER change_sets_update_prevented BEFORE UPDATE ON change_sets
BEGIN
	SELECT RAISE(ABORT, 'change_sets table is immutable');
END;

CREATE TRIGGER change_sets_delete_prevented BEFORE DELETE ON change_sets
BEGIN
	SELECT RAISE(ABORT, 'change_sets table is immutable');
END;

CREATE TABLE change_set_paths (
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	path BLOB NOT NULL,
	op TEXT NOT NULL CHECK (op IN ('create', 'modify')),
	base_sha256 TEXT REFERENCES tool_content(sha256),
	new_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	new_len INTEGER NOT NULL CHECK (new_len >= 0),
	PRIMARY KEY (change_set_id, path),
	CHECK ((op = 'create') = (base_sha256 IS NULL))
);

CREATE TRIGGER change_set_paths_update_prevented BEFORE UPDATE ON change_set_paths
BEGIN
	SELECT RAISE(ABORT, 'change_set_paths table is immutable');
END;

CREATE TRIGGER change_set_paths_delete_prevented BEFORE DELETE ON change_set_paths
BEGIN
	SELECT RAISE(ABORT, 'change_set_paths table is immutable');
END;

-- Append-only apply log. The change set's state is the latest control op:
-- none is appliable, started or recovered is partial, finished carries
-- applied or conflict, discarded is discarded. A path's outcome is its latest
-- file op, or pending when it has none.
CREATE TABLE apply_ops (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	op_type TEXT NOT NULL CHECK (op_type IN ('started', 'file', 'finished', 'recovered', 'discarded')),
	path BLOB,
	outcome TEXT CHECK (outcome IS NULL OR outcome IN ('applied', 'conflict')),
	state TEXT CHECK (state IS NULL OR state IN ('applied', 'partial', 'conflict', 'discarded')),
	override BOOLEAN NOT NULL DEFAULT 0,
	recorded_at TEXT NOT NULL,
	CHECK ((op_type = 'file') = (path IS NOT NULL)),
	CHECK ((op_type = 'file') = (outcome IS NOT NULL)),
	CHECK ((op_type IN ('finished', 'recovered', 'discarded')) = (state IS NOT NULL)),
	CHECK (op_type <> 'finished' OR state IN ('applied', 'conflict')),
	CHECK (op_type <> 'recovered' OR state = 'partial'),
	CHECK (op_type <> 'discarded' OR state = 'discarded'),
	CHECK (op_type = 'started' OR override = 0)
);

CREATE INDEX apply_ops_change_set_idx ON apply_ops(change_set_id, id);

CREATE TRIGGER apply_ops_update_prevented BEFORE UPDATE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;

CREATE TRIGGER apply_ops_delete_prevented BEFORE DELETE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;

-- A verdict takes a journal_seq when it is recorded, so "received before
-- giving it" is a comparison of counters: a result or observation is evidence
-- only when its journal_seq precedes the verdict's. Verdicts recorded before
-- this migration have none and take no evidence.
ALTER TABLE verdicts ADD COLUMN journal_seq INTEGER;
CREATE UNIQUE INDEX verdicts_journal_seq_unique ON verdicts(journal_seq) WHERE journal_seq IS NOT NULL;

-- What a verdict rested on: the snapshot version it saw, the tool results its
-- reviewer received before giving it, and the observations it added.
CREATE TABLE verdict_evidence (
	verdict_id TEXT PRIMARY KEY REFERENCES verdicts(id),
	snapshot_version INTEGER NOT NULL CHECK (snapshot_version >= 0),
	recorded_at TEXT NOT NULL
);

CREATE TABLE verdict_tool_results (
	verdict_id TEXT NOT NULL REFERENCES verdict_evidence(verdict_id),
	result_id TEXT NOT NULL REFERENCES tool_results(id),
	position INTEGER NOT NULL CHECK (position >= 0),
	PRIMARY KEY (verdict_id, result_id),
	UNIQUE (verdict_id, position)
);

CREATE TABLE verdict_observations (
	verdict_id TEXT NOT NULL REFERENCES verdict_evidence(verdict_id),
	observation_id TEXT NOT NULL UNIQUE REFERENCES observations(id),
	PRIMARY KEY (verdict_id, observation_id)
);

CREATE TRIGGER verdict_evidence_update_prevented BEFORE UPDATE ON verdict_evidence
BEGIN
	SELECT RAISE(ABORT, 'verdict_evidence table is immutable');
END;

CREATE TRIGGER verdict_evidence_delete_prevented BEFORE DELETE ON verdict_evidence
BEGIN
	SELECT RAISE(ABORT, 'verdict_evidence table is immutable');
END;

CREATE TRIGGER verdict_tool_results_update_prevented BEFORE UPDATE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

CREATE TRIGGER verdict_tool_results_delete_prevented BEFORE DELETE ON verdict_tool_results
BEGIN
	SELECT RAISE(ABORT, 'verdict_tool_results table is immutable');
END;

CREATE TRIGGER verdict_observations_update_prevented BEFORE UPDATE ON verdict_observations
BEGIN
	SELECT RAISE(ABORT, 'verdict_observations table is immutable');
END;

CREATE TRIGGER verdict_observations_delete_prevented BEFORE DELETE ON verdict_observations
BEGIN
	SELECT RAISE(ABORT, 'verdict_observations table is immutable');
END;
