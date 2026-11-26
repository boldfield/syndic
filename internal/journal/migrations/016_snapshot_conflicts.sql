-- Snapshot conflicts (v5 delta section 6, E-08). When a later observation
-- contradicts an earlier one, the conflict is journaled here and no observation
-- is recorded. The entry records the turn, path, the attempt that detected the
-- conflict, and the conflict type (unlisted, deleted, type_changed, or
-- directory_conflict).

CREATE TABLE snapshot_conflicts (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES turns(id),
	attempt_id TEXT NOT NULL REFERENCES attempts(id),
	path BLOB NOT NULL,
	conflict_type TEXT NOT NULL CHECK (conflict_type IN ('deleted', 'type_changed', 'directory_conflict')),
	journal_seq INTEGER NOT NULL UNIQUE,
	recorded_at TEXT NOT NULL
);

CREATE INDEX snapshot_conflicts_turn_idx ON snapshot_conflicts(turn_id);

CREATE TRIGGER snapshot_conflicts_update_prevented BEFORE UPDATE ON snapshot_conflicts
BEGIN
	SELECT RAISE(ABORT, 'snapshot_conflicts table is immutable');
END;

CREATE TRIGGER snapshot_conflicts_delete_prevented BEFORE DELETE ON snapshot_conflicts
BEGIN
	SELECT RAISE(ABORT, 'snapshot_conflicts table is immutable');
END;
