-- Make manifest and manifest_hash nullable to support probing run lifecycle.
-- A run allocated for probing has NULL manifest and manifest_hash until finalization.

-- SQLite doesn't support direct ALTER COLUMN, so we rebuild the table.
-- Create new runs table with nullable manifest and manifest_hash columns
CREATE TABLE runs_new (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('engine', 'probe')),
    state TEXT NOT NULL DEFAULT 'started' CHECK (state IN ('probing', 'started')),
    manifest TEXT,
    manifest_hash TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Copy data from old table, preserving values
INSERT INTO runs_new (id, kind, state, manifest, manifest_hash, created_at)
SELECT id, kind, state,
       CASE WHEN manifest = '' THEN NULL ELSE manifest END,
       CASE WHEN manifest_hash = '' THEN NULL ELSE manifest_hash END,
       created_at
FROM runs;

-- Drop old table
DROP TABLE runs;

-- Rename new table
ALTER TABLE runs_new RENAME TO runs;

-- Recreate the manifest immutability trigger with proper NULL checking
DROP TRIGGER IF EXISTS runs_manifest_immutable;

CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
WHEN OLD.manifest IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'runs manifest is immutable');
END;
