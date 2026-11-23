-- Accepted review records: one row per accepted verdict, holding the full
-- reviews-drawer item as canonical JSON, written by the engine when it accepts
-- the verdict or on first request for one accepted earlier.

CREATE TABLE review_records (
	verdict_id TEXT PRIMARY KEY REFERENCES verdicts(id),
	turn_id TEXT NOT NULL REFERENCES turns(id),
	round INTEGER NOT NULL,
	roster_index INTEGER NOT NULL,
	participant TEXT NOT NULL,
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	candidate_hash TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('approve', 'block', 'approve-with-changes')),
	record BLOB NOT NULL,
	record_sha256 TEXT NOT NULL,
	record_len INTEGER NOT NULL
);

CREATE INDEX review_records_turn_idx ON review_records(turn_id, round, roster_index);

-- A record is immutable once written.
CREATE TRIGGER review_records_update_prevented BEFORE UPDATE ON review_records
BEGIN
	SELECT RAISE(ABORT, 'review_records table is immutable');
END;

CREATE TRIGGER review_records_delete_prevented BEFORE DELETE ON review_records
BEGIN
	SELECT RAISE(ABORT, 'review_records table is immutable');
END;
