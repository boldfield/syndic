-- v6 delta: Store structured accepted review records for drawer display.
-- Wire contract: reviews{turn_id, cursor?, limit?} → {items: [{round, candidate_hash, participant, attempt_id, kind, blocks, commentary}], next_cursor?}
-- Records are written from acceptance data at acceptance time for verdicts accepted under this delta.
-- Older verdicts are materialized from acceptance records on first request (lazy materialization).

CREATE TABLE review_records (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL,
	round INTEGER NOT NULL,
	candidate_hash TEXT NOT NULL,
	participant TEXT NOT NULL,
	attempt_id TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('approve', 'block')),
	verdict_json BLOB NOT NULL,
	oversized BOOLEAN NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (turn_id) REFERENCES turns(id),
	FOREIGN KEY (attempt_id) REFERENCES attempts(id),
	UNIQUE (turn_id, round, participant, attempt_id)
);

CREATE INDEX review_records_turn_round ON review_records(turn_id, round);
CREATE INDEX review_records_participant ON review_records(participant);

ALTER TABLE turns ADD COLUMN review_count INTEGER DEFAULT 0;
