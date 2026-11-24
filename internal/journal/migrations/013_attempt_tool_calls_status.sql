-- A tool step (finish status tool_calls) is an accepted result, so
-- attempts.status must admit it. A CHECK cannot be altered in place, so
-- attempts is rebuilt as in 010, with the same indexes and trigger.

DROP TRIGGER attempts_raw_output_immutable;
DROP INDEX attempts_journal_seq_unique;

CREATE TABLE attempts_new (
	id TEXT PRIMARY KEY,
	turn_id TEXT,
	participant TEXT NOT NULL,
	phase TEXT NOT NULL,
	epoch INTEGER NOT NULL,
	dispatched_at TEXT NOT NULL,
	deadline TEXT,
	state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'completed', 'unknown')),
	status TEXT CHECK (status IS NULL OR status IN ('complete', 'truncated', 'refused', 'interrupted', 'error', 'tool_calls')),
	cancellation_state TEXT NOT NULL DEFAULT 'none' CHECK (cancellation_state IN ('none', 'requested', 'confirmed', 'unknown')),
	quarantined BOOLEAN NOT NULL DEFAULT 0,
	raw_output TEXT,
	scrub_count INTEGER,
	observed_identity TEXT,
	native_finish TEXT,
	transport_status TEXT,
	repair_of TEXT,
	gate_error TEXT,
	commentary TEXT,
	delimiter TEXT,
	max_output_tokens INTEGER,
	estimated_prompt_tokens INTEGER,
	usage_prompt INTEGER,
	usage_completion INTEGER,
	retry_count INTEGER NOT NULL DEFAULT 0,
	journal_seq INTEGER,
	text TEXT,
	probe_run_id TEXT REFERENCES runs(id),
	probe_endpoint TEXT,
	probe_model TEXT,
	probe_pool TEXT,
	probe_group TEXT,
	probe_step TEXT,
	FOREIGN KEY (turn_id) REFERENCES turns(id),
	FOREIGN KEY (repair_of) REFERENCES attempts(id),
	CHECK ((turn_id IS NOT NULL) <> (probe_run_id IS NOT NULL))
);

INSERT INTO attempts_new (
	id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status,
	cancellation_state, quarantined, raw_output, scrub_count, observed_identity,
	native_finish, transport_status, repair_of, gate_error, commentary, delimiter,
	max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion,
	retry_count, journal_seq, text
)
SELECT
	id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, status,
	cancellation_state, quarantined, raw_output, scrub_count, observed_identity,
	native_finish, transport_status, repair_of, gate_error, commentary, delimiter,
	max_output_tokens, estimated_prompt_tokens, usage_prompt, usage_completion,
	retry_count, journal_seq, text
FROM attempts;

DROP TABLE attempts;
ALTER TABLE attempts_new RENAME TO attempts;

CREATE UNIQUE INDEX attempts_journal_seq_unique ON attempts(journal_seq) WHERE journal_seq IS NOT NULL;

CREATE TRIGGER attempts_raw_output_immutable BEFORE UPDATE OF raw_output ON attempts
WHEN OLD.raw_output IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'attempts.raw_output cannot be updated once set');
END;
