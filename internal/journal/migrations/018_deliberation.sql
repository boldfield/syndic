-- Deliberate mode records (v8 delta sections 2, 5 and 7). A deliberate turn
-- carries its mode and current phase; both are NULL for every other turn. A
-- candidate carries the hash of the deliberation it binds. The proposals,
-- critiques and dispositions are rows of deliberation_items, append-only,
-- their text in tool_content under its SHA-256.

ALTER TABLE turns ADD COLUMN deliberation_mode TEXT CHECK (deliberation_mode IS NULL OR deliberation_mode = 'deliberate');
ALTER TABLE turns ADD COLUMN deliberation_phase TEXT CHECK (deliberation_phase IS NULL OR deliberation_phase IN ('propose', 'critique', 'integrate', 'review'));
ALTER TABLE candidates ADD COLUMN deliberation_hash TEXT;

-- Candidates stay immutable except that deliberation_hash, which the engine
-- knows only after the candidate row exists, may be set once from NULL.
DROP TRIGGER candidates_update_prevented;
CREATE TRIGGER candidates_update_prevented BEFORE UPDATE ON candidates
WHEN NOT (OLD.deliberation_hash IS NULL AND NEW.deliberation_hash IS NOT NULL
	AND NEW.id IS OLD.id AND NEW.turn_id IS OLD.turn_id AND NEW.content IS OLD.content
	AND NEW.content_hash IS OLD.content_hash AND NEW.round IS OLD.round AND NEW.supersedes IS OLD.supersedes)
BEGIN
	SELECT RAISE(ABORT, 'candidates table is immutable');
END;

-- roster_position orders participants within a phase. label is the numbered
-- label peers saw and is NULL for a disposition, which instead has the round
-- and the candidate it belongs to.
CREATE TABLE deliberation_items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	kind TEXT NOT NULL CHECK (kind IN ('proposal', 'critique', 'disposition')),
	participant TEXT NOT NULL,
	roster_position INTEGER NOT NULL CHECK (roster_position >= 0),
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	label TEXT,
	content_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	content_byte_len INTEGER NOT NULL CHECK (content_byte_len >= 0),
	round INTEGER CHECK (round IS NULL OR round >= 1),
	candidate_id TEXT REFERENCES candidates(id),
	recorded_at TEXT NOT NULL,
	CHECK ((kind = 'disposition') = (label IS NULL)),
	CHECK ((kind = 'disposition') = (round IS NOT NULL)),
	CHECK ((kind = 'disposition') = (candidate_id IS NOT NULL))
);

CREATE UNIQUE INDEX deliberation_items_participant_unique
	ON deliberation_items(turn_id, participant, kind) WHERE kind IN ('proposal', 'critique');
CREATE UNIQUE INDEX deliberation_items_candidate_unique
	ON deliberation_items(candidate_id) WHERE kind = 'disposition';

CREATE TRIGGER deliberation_items_update_prevented BEFORE UPDATE ON deliberation_items
BEGIN
	SELECT RAISE(ABORT, 'deliberation_items table is immutable');
END;

CREATE TRIGGER deliberation_items_delete_prevented BEFORE DELETE ON deliberation_items
BEGIN
	SELECT RAISE(ABORT, 'deliberation_items table is immutable');
END;
