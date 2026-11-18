-- Review engine schema: run kind, turn/attempt bookkeeping, dispatches, capacity holds.

ALTER TABLE runs ADD COLUMN kind TEXT NOT NULL DEFAULT 'probe' CHECK (kind IN ('probe', 'engine'));

ALTER TABLE turns ADD COLUMN submission_id TEXT;
ALTER TABLE turns ADD COLUMN submission_hash TEXT;
ALTER TABLE turns ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN terminal_reason TEXT;
ALTER TABLE turns ADD COLUMN phase_at_cancel TEXT;
ALTER TABLE turns ADD COLUMN attempt_deadline_ms INTEGER;
ALTER TABLE turns ADD COLUMN turn_deadline_ms INTEGER;
ALTER TABLE turns ADD COLUMN preset TEXT;
ALTER TABLE turns ADD COLUMN quick_profile TEXT;
ALTER TABLE turns ADD COLUMN candidate_id TEXT;
ALTER TABLE turns ADD COLUMN budget_json TEXT;

CREATE UNIQUE INDEX turns_submission_id_unique ON turns(submission_id) WHERE submission_id IS NOT NULL;

ALTER TABLE attempts ADD COLUMN gate_error TEXT;
ALTER TABLE attempts ADD COLUMN commentary TEXT;
ALTER TABLE attempts ADD COLUMN delimiter TEXT;
ALTER TABLE attempts ADD COLUMN max_output_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN estimated_prompt_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN usage_prompt INTEGER;
ALTER TABLE attempts ADD COLUMN usage_completion INTEGER;
ALTER TABLE attempts ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE attempt_dispatches (
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	dispatch_index INTEGER NOT NULL,
	dispatched_at TEXT NOT NULL,
	PRIMARY KEY (attempt_id, dispatch_index)
);

ALTER TABLE blocks ADD COLUMN reviewer_block_id TEXT;

CREATE TABLE capacity_holds (
	attempt_id TEXT PRIMARY KEY REFERENCES attempts(id),
	pool TEXT NOT NULL,
	endpoint_name TEXT NOT NULL,
	model TEXT NOT NULL,
	group_json TEXT NOT NULL,
	held_since TEXT NOT NULL,
	released_at TEXT,
	released_how TEXT CHECK (released_how IS NULL OR released_how IN ('server_confirmed', 'late_result', 'operator', 'operator_timeout')),
	released_reason TEXT,
	CHECK ((released_at IS NULL) = (released_how IS NULL))
);
