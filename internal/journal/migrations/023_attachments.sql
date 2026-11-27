-- Attachment records and snapshot bases (v6 delta section 4). Each
-- attachment is recorded at submit with path, content hash, byte length, and
-- content reference. The attachments are stored in ascending byte order of path.
-- The attachments_hash is the SHA-256 of the manifest, which begins with
-- "syndic-attachments/v1" and includes one line per attachment: path length,
-- colon, path; content SHA-256; and content byte length. The hash is "none"
-- when the turn has no attachments. Each attachment is also recorded as a
-- snapshot observation with its base_sha256, with attempt_id NULL to distinguish
-- it from tool-call observations. All tables are append-only.

-- Allow attempt_id to be NULL for attachment-based observations and other
-- non-tool-call records. SQLite requires table recreation to change NOT NULL
-- constraints. Create new table with nullable attempt_id, copy data, rename.

-- Drop existing triggers before altering the table
DROP TRIGGER observations_update_prevented;
DROP TRIGGER observations_delete_prevented;

-- Create observations table with nullable attempt_id
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

-- Copy existing data
INSERT INTO observations_new SELECT * FROM observations;

-- Recreate indexes and triggers
CREATE TRIGGER observations_update_prevented BEFORE UPDATE ON observations_new
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

CREATE TRIGGER observations_delete_prevented BEFORE DELETE ON observations_new
BEGIN
	SELECT RAISE(ABORT, 'observations table is immutable');
END;

-- Drop old table and rename new one
DROP TABLE observations;
ALTER TABLE observations_new RENAME TO observations;

-- Record of an attachment: path, content hash, byte length, and content reference.
-- Multiple attachments per turn, one row per attachment. Stored in ascending
-- byte order of path.
CREATE TABLE attachments (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path TEXT NOT NULL,
	sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	byte_len INTEGER NOT NULL CHECK (byte_len >= 0),
	recorded_at TEXT NOT NULL,
	UNIQUE (turn_id, path)
);

CREATE INDEX attachments_turn_idx ON attachments(turn_id);

-- Immutable attachments_hash on turns. Set at turn creation for turns with
-- attachments; "none" for turns without. Never changed after creation.
ALTER TABLE turns ADD COLUMN attachments_hash TEXT CHECK (attachments_hash IS NULL OR length(attachments_hash) = 64 OR attachments_hash = 'none');

CREATE TRIGGER attachments_update_prevented BEFORE UPDATE ON attachments
BEGIN
	SELECT RAISE(ABORT, 'attachments table is immutable');
END;

CREATE TRIGGER attachments_delete_prevented BEFORE DELETE ON attachments
BEGIN
	SELECT RAISE(ABORT, 'attachments table is immutable');
END;

-- Note: attachments_hash immutability is enforced by the application logic
-- (it is only set once during turn creation, never updated after).
-- No trigger is needed since SQL cannot distinguish between "set" and "update"
-- for a NULL->value transition vs a value->value change.
