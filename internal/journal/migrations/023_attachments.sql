-- Attachment records and snapshot bases (v6 delta section 4). Each
-- attachment is recorded at submit with path, content hash, byte length, and
-- content reference. The attachments are stored in ascending byte order of path.
-- The attachments_hash is the SHA-256 of the manifest, which begins with
-- "syndic-attachments/v1" and includes one line per attachment: path length,
-- colon, path; content SHA-256; and content byte length. The hash is "none"
-- when the turn has no attachments. Each attachment is also recorded as a
-- snapshot observation with its base_sha256. All tables are append-only.

ALTER TABLE turns ADD COLUMN attachments_hash TEXT CHECK (attachments_hash IS NULL OR length(attachments_hash) = 64 OR attachments_hash = 'none');

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

-- Snapshot base observation for an attached path: the content hash as it was
-- at submit time, before any staged edits. Each attachment has one base.
CREATE TABLE attachment_bases (
	attachment_id TEXT PRIMARY KEY REFERENCES attachments(id),
	base_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	recorded_at TEXT NOT NULL
);

CREATE TRIGGER attachments_update_prevented BEFORE UPDATE ON attachments
BEGIN
	SELECT RAISE(ABORT, 'attachments table is immutable');
END;

CREATE TRIGGER attachments_delete_prevented BEFORE DELETE ON attachments
BEGIN
	SELECT RAISE(ABORT, 'attachments table is immutable');
END;

CREATE TRIGGER attachment_bases_update_prevented BEFORE UPDATE ON attachment_bases
BEGIN
	SELECT RAISE(ABORT, 'attachment_bases table is immutable');
END;

CREATE TRIGGER attachment_bases_delete_prevented BEFORE DELETE ON attachment_bases
BEGIN
	SELECT RAISE(ABORT, 'attachment_bases table is immutable');
END;
