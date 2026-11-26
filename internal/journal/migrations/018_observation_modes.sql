-- The mode record kept beside a turn's snapshot (v7 delta section 5, "Modes"):
-- for each observed regular file, whether it was executable for its owner when
-- its content was first observed. It is written in the transaction that
-- records the observation and never changes. A path with no row here was
-- observed before this table existed and reads as not executable.

CREATE TABLE observation_modes (
	turn_id TEXT NOT NULL REFERENCES turns(id),
	path BLOB NOT NULL,
	owner_exec BOOLEAN NOT NULL CHECK (owner_exec IN (0, 1)),
	PRIMARY KEY (turn_id, path)
);

CREATE TRIGGER observation_modes_update_prevented BEFORE UPDATE ON observation_modes
BEGIN
	SELECT RAISE(ABORT, 'observation_modes table is immutable');
END;

CREATE TRIGGER observation_modes_delete_prevented BEFORE DELETE ON observation_modes
BEGIN
	SELECT RAISE(ABORT, 'observation_modes table is immutable');
END;
