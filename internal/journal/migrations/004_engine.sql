-- Add kind column to runs table with CHECK constraint
-- SQLite does not support ALTER TABLE ADD COLUMN with CHECK,
-- so we recreate the table. Foreign key constraints are temporarily
-- disabled by journal.go during this migration.

CREATE TABLE runs_new (
	id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	manifest TEXT NOT NULL,
	manifest_hash TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'probe' CHECK (kind IN ('probe', 'engine'))
);

INSERT INTO runs_new SELECT id, created_at, manifest, manifest_hash, 'probe' FROM runs;

DROP TABLE runs;

ALTER TABLE runs_new RENAME TO runs;

-- Restore the manifest immutability trigger
CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
BEGIN
	SELECT RAISE(ABORT, 'runs manifest is immutable');
END;

-- Add columns to turns table
ALTER TABLE turns ADD COLUMN submission_id TEXT;
ALTER TABLE turns ADD COLUMN submission_hash TEXT;

-- Create UNIQUE index for submission_id (must be non-null to be unique)
CREATE UNIQUE INDEX turns_submission_id_unique ON turns(submission_id) WHERE submission_id IS NOT NULL;
ALTER TABLE turns ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN terminal_reason TEXT;
ALTER TABLE turns ADD COLUMN phase_at_cancel TEXT;
ALTER TABLE turns ADD COLUMN attempt_deadline_ms INTEGER;
ALTER TABLE turns ADD COLUMN turn_deadline_ms INTEGER;
ALTER TABLE turns ADD COLUMN preset TEXT;
ALTER TABLE turns ADD COLUMN quick_profile TEXT;
ALTER TABLE turns ADD COLUMN candidate_id TEXT;
ALTER TABLE turns ADD COLUMN budget_json TEXT;

-- Add columns to attempts table
ALTER TABLE attempts ADD COLUMN gate_error TEXT;
ALTER TABLE attempts ADD COLUMN commentary TEXT;
ALTER TABLE attempts ADD COLUMN delimiter TEXT;
ALTER TABLE attempts ADD COLUMN max_output_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN estimated_prompt_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN usage_prompt INTEGER;
ALTER TABLE attempts ADD COLUMN usage_completion INTEGER;
ALTER TABLE attempts ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;

-- Create attempt_dispatches table
CREATE TABLE attempt_dispatches (
	attempt_id TEXT NOT NULL,
	dispatch_index INTEGER NOT NULL,
	dispatched_at TEXT NOT NULL,
	PRIMARY KEY (attempt_id, dispatch_index),
	FOREIGN KEY (attempt_id) REFERENCES attempts(id)
);

-- Add reviewer_block_id column to blocks table
ALTER TABLE blocks ADD COLUMN reviewer_block_id TEXT;

-- Create capacity_holds table
CREATE TABLE capacity_holds (
	attempt_id TEXT PRIMARY KEY,
	pool TEXT NOT NULL,
	endpoint_name TEXT NOT NULL,
	model TEXT NOT NULL,
	group_json TEXT NOT NULL,
	held_since TEXT NOT NULL,
	released_at TEXT,
	released_how TEXT,
	released_reason TEXT,
	FOREIGN KEY (attempt_id) REFERENCES attempts(id),
	CHECK ((released_at IS NULL AND released_how IS NULL) OR (released_at IS NOT NULL AND released_how IS NOT NULL)),
	CHECK (released_how IS NULL OR released_how IN ('server_confirmed', 'late_result', 'operator', 'operator_timeout'))
);
