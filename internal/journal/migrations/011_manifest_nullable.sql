-- Make manifest and manifest_hash nullable to support probing run lifecycle.
-- A run allocated for probing has NULL manifest and manifest_hash until finalization.

-- SQLite table rebuild for nullable manifest and manifest_hash.
-- Note: Rebuilding the table with foreign key constraints requires the connection to have
-- foreign_keys PRAGMA disabled. The Store code should handle this if needed.

-- Create new runs table with nullable manifest and manifest_hash columns.
-- Preserve the DEFAULT 'probe' for kind column established in migration 004.
-- Preserve the CHECK constraint order to match the original migration 004.
CREATE TABLE runs_new (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT 'probe' CHECK (kind IN ('probe', 'engine')),
    state TEXT NOT NULL DEFAULT 'started' CHECK (state IN ('probing', 'started')),
    manifest TEXT,
    manifest_hash TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Copy data from old table, preserving values
INSERT INTO runs_new (id, kind, state, manifest, manifest_hash, created_at)
SELECT id, kind, state, manifest, manifest_hash, created_at
FROM runs;

-- Drop old table
DROP TABLE runs;

-- Rename new table to runs
ALTER TABLE runs_new RENAME TO runs;

-- Recreate the manifest immutability trigger with proper NULL checking.
-- Only prevent updates when the manifest is already set (not NULL).
CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
WHEN OLD.manifest IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'runs manifest is immutable');
END;
