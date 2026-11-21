-- Separate durable run allocation from immutable manifest finalization.
-- A run allocated for probing has NULL manifest and manifest_hash until the
-- manifest is finalized; state tracks the lifecycle. NOT NULL cannot be
-- dropped in place, so runs is rebuilt following SQLite's documented
-- procedure. The migrator disables foreign keys on its connection and runs
-- PRAGMA foreign_key_check before committing.
--
-- Objects on runs: only the runs_manifest_immutable trigger (001); migration
-- 004's kind column is carried over with its DEFAULT and CHECK.

DROP TRIGGER runs_manifest_immutable;

CREATE TABLE runs_new (
	id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	manifest TEXT,
	manifest_hash TEXT,
	kind TEXT NOT NULL DEFAULT 'probe' CHECK (kind IN ('probe', 'engine')),
	state TEXT NOT NULL DEFAULT 'started' CHECK (state IN ('probing', 'started', 'abandoned')),
	CHECK (
		(state = 'started' AND manifest IS NOT NULL AND manifest_hash IS NOT NULL) OR
		(state IN ('probing', 'abandoned') AND manifest IS NULL AND manifest_hash IS NULL)
	)
);

INSERT INTO runs_new (id, created_at, manifest, manifest_hash, kind, state)
SELECT id, created_at, manifest, manifest_hash, kind, 'started' FROM runs;

DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

-- The manifest is written once, at finalization, and never changes after.
CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
WHEN OLD.manifest IS NOT NULL OR OLD.state != 'probing'
BEGIN
	SELECT RAISE(ABORT, 'runs manifest is immutable');
END;
