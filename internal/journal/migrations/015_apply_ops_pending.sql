-- Recovery classifies every path of an unfinished apply afresh, so a file op
-- may record a path as pending again, for instance after a recorded conflict
-- was resolved by restoring the file to its base. The outcome CHECK of
-- apply_ops (migration 014) is widened by rebuilding the table; rows, ids and
-- the append-only triggers are kept.

CREATE TABLE apply_ops_new (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	change_set_id TEXT NOT NULL REFERENCES change_sets(id),
	op_type TEXT NOT NULL CHECK (op_type IN ('started', 'file', 'finished', 'recovered', 'discarded')),
	path BLOB,
	outcome TEXT CHECK (outcome IS NULL OR outcome IN ('pending', 'applied', 'conflict')),
	state TEXT CHECK (state IS NULL OR state IN ('applied', 'partial', 'conflict', 'discarded')),
	override BOOLEAN NOT NULL DEFAULT 0,
	recorded_at TEXT NOT NULL,
	CHECK ((op_type = 'file') = (path IS NOT NULL)),
	CHECK ((op_type = 'file') = (outcome IS NOT NULL)),
	CHECK ((op_type IN ('finished', 'recovered', 'discarded')) = (state IS NOT NULL)),
	CHECK (op_type <> 'finished' OR state IN ('applied', 'conflict')),
	CHECK (op_type <> 'recovered' OR state = 'partial'),
	CHECK (op_type <> 'discarded' OR state = 'discarded'),
	CHECK (op_type = 'started' OR override = 0)
);

INSERT INTO apply_ops_new (id, change_set_id, op_type, path, outcome, state, override, recorded_at)
	SELECT id, change_set_id, op_type, path, outcome, state, override, recorded_at FROM apply_ops;

DROP TABLE apply_ops;
ALTER TABLE apply_ops_new RENAME TO apply_ops;

CREATE INDEX apply_ops_change_set_idx ON apply_ops(change_set_id, id);

CREATE TRIGGER apply_ops_update_prevented BEFORE UPDATE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;

CREATE TRIGGER apply_ops_delete_prevented BEFORE DELETE ON apply_ops
BEGIN
	SELECT RAISE(ABORT, 'apply_ops table is immutable');
END;
