-- Attachment records and admitted snapshot bases (v6 delta section 4, order
-- step 7 and "Snapshot"). The turn, its attachment records, the attachments
-- as bases in the turn's snapshot record, file_tainted and attachments_hash
-- are committed by one transaction (CreateEngineTurn).
--
-- A base is an ordinary observation of its path, so v5's overlay, versioning
-- and unstage need no second source. It is not made by any attempt, so
-- observations.attempt_id becomes nullable: the table is rebuilt (a NOT NULL
-- cannot be dropped in place) keeping rows, ids, the unique constraints and
-- the append-only triggers.

CREATE TABLE observations_new (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	attempt_id TEXT REFERENCES attempts(id),
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

INSERT INTO observations_new (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at)
	SELECT id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at FROM observations;

DROP TABLE observations;
ALTER TABLE observations_new RENAME TO observations;

CREATE TRIGGER observations_update_prevented BEFORE UPDATE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

CREATE TRIGGER observations_delete_prevented BEFORE DELETE ON observations
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

-- The manifest identity of the turn's attachments: the hash, or 'none'. NULL
-- only on turns created before this migration or by CreateTurn. It is set in
-- the turn's INSERT and never rewritten.
ALTER TABLE turns ADD COLUMN attachments_hash TEXT;

CREATE TRIGGER turns_attachments_hash_immutable BEFORE UPDATE OF attachments_hash ON turns
WHEN OLD.attachments_hash IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'turns.attachments_hash is fixed at admission');
END;

-- One row per attachment: the path, the content's hash and length, and the
-- base observation that records it. The bytes are tool_content's.
CREATE TABLE turn_attachments (
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path BLOB NOT NULL,
	sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	byte_len INTEGER NOT NULL CHECK (byte_len >= 0),
	observation_id TEXT NOT NULL UNIQUE REFERENCES observations(id),
	PRIMARY KEY (turn_id, path)
);

-- The length must be the stored content's, the base must be the observation
-- of this path in this turn at this content, and the turn must have been
-- admitted with a manifest (attachments_hash set and not 'none').
CREATE TRIGGER turn_attachments_consistent BEFORE INSERT ON turn_attachments
WHEN NEW.byte_len IS NOT (SELECT byte_len FROM tool_content WHERE sha256 = NEW.sha256)
	OR NOT EXISTS (SELECT 1 FROM observations o WHERE o.id = NEW.observation_id AND o.turn_id = NEW.turn_id
		AND o.path = NEW.path AND o.kind = 'f' AND o.content_sha256 = NEW.sha256 AND o.attempt_id IS NULL)
	OR COALESCE((SELECT attachments_hash FROM turns WHERE id = NEW.turn_id), 'none') = 'none'
BEGIN
	SELECT RAISE(ABORT, 'attachment record disagrees with its content, base or turn');
END;

CREATE TRIGGER turn_attachments_update_prevented BEFORE UPDATE ON turn_attachments
BEGIN
	SELECT RAISE(ABORT, 'turn_attachments table is immutable');
END;

CREATE TRIGGER turn_attachments_delete_prevented BEFORE DELETE ON turn_attachments
BEGIN
	SELECT RAISE(ABORT, 'turn_attachments table is immutable');
END;
