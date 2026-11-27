-- Local server instances, the provenance marks and proxy connections (v9
-- delta sections 6 and 8). An instance is journaled before its process
-- starts; its end is a second row, written only once nothing of it remains.
-- An instance with a start and no end is unresolved. A containment failure
-- is its own row and does not end the instance. The provenance table is one
-- ordered log of the four mark records, so that a clear, which takes effect
-- at its second record, is ordered against the marks it follows. All tables
-- are append-only.

CREATE TABLE mcp_instances (
	id TEXT PRIMARY KEY,
	server TEXT NOT NULL CHECK (server <> ''),
	kind TEXT NOT NULL CHECK (kind IN ('turn', 'apply', 'listing', 'run')),
	owner TEXT NOT NULL,
	state_dir TEXT NOT NULL,
	temp_dir TEXT NOT NULL,
	workspace_dir TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	CHECK (kind = 'run' OR owner <> '')
);

CREATE INDEX mcp_instances_server_idx ON mcp_instances(server);

CREATE TABLE mcp_instance_ends (
	instance_id TEXT PRIMARY KEY REFERENCES mcp_instances(id),
	recorded_at TEXT NOT NULL
);

CREATE TABLE mcp_containment_failures (
	id TEXT PRIMARY KEY,
	instance_id TEXT NOT NULL REFERENCES mcp_instances(id),
	detail TEXT NOT NULL,
	recorded_at TEXT NOT NULL
);

CREATE INDEX mcp_containment_failures_idx ON mcp_containment_failures(instance_id);

CREATE TABLE mcp_provenance (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL CHECK (kind IN ('file_holding', 'state_file_holding', 'clear_started', 'cleared')),
	server TEXT,
	destination TEXT,
	directory TEXT,
	recorded_at TEXT NOT NULL,
	CHECK (kind <> 'file_holding' OR (destination <> '' AND server IS NULL AND directory IS NULL)),
	CHECK (kind <> 'state_file_holding' OR (directory <> '' AND server <> '' AND destination IS NULL)),
	CHECK (kind NOT IN ('clear_started', 'cleared') OR (destination <> '' AND server <> '' AND directory IS NULL))
);

-- A proxy connection of an instance, journaled before it is opened, with the
-- call in flight when there is one. A refused one carries its status. Its
-- close is a separate row.
CREATE TABLE mcp_connections (
	id TEXT PRIMARY KEY,
	instance_id TEXT NOT NULL REFERENCES mcp_instances(id),
	call_id TEXT REFERENCES mcp_calls(id),
	host TEXT NOT NULL,
	port INTEGER NOT NULL CHECK (port BETWEEN 0 AND 65535),
	allowed BOOLEAN NOT NULL CHECK (allowed IN (0, 1)),
	refusal_status INTEGER CHECK (refusal_status IS NULL OR refusal_status IN (403, 407)),
	recorded_at TEXT NOT NULL,
	CHECK (allowed = (refusal_status IS NULL))
);

CREATE INDEX mcp_connections_instance_idx ON mcp_connections(instance_id, recorded_at);

CREATE TABLE mcp_connection_closes (
	connection_id TEXT PRIMARY KEY REFERENCES mcp_connections(id),
	bytes_up INTEGER NOT NULL CHECK (bytes_up >= 0),
	bytes_down INTEGER NOT NULL CHECK (bytes_down >= 0),
	recorded_at TEXT NOT NULL
);

CREATE TRIGGER mcp_instances_update_prevented BEFORE UPDATE ON mcp_instances
BEGIN
	SELECT RAISE(ABORT, 'mcp_instances table is immutable');
END;

CREATE TRIGGER mcp_instances_delete_prevented BEFORE DELETE ON mcp_instances
BEGIN
	SELECT RAISE(ABORT, 'mcp_instances table is immutable');
END;

CREATE TRIGGER mcp_instance_ends_update_prevented BEFORE UPDATE ON mcp_instance_ends
BEGIN
	SELECT RAISE(ABORT, 'mcp_instance_ends table is immutable');
END;

CREATE TRIGGER mcp_instance_ends_delete_prevented BEFORE DELETE ON mcp_instance_ends
BEGIN
	SELECT RAISE(ABORT, 'mcp_instance_ends table is immutable');
END;

CREATE TRIGGER mcp_containment_failures_update_prevented BEFORE UPDATE ON mcp_containment_failures
BEGIN
	SELECT RAISE(ABORT, 'mcp_containment_failures table is immutable');
END;

CREATE TRIGGER mcp_containment_failures_delete_prevented BEFORE DELETE ON mcp_containment_failures
BEGIN
	SELECT RAISE(ABORT, 'mcp_containment_failures table is immutable');
END;

CREATE TRIGGER mcp_provenance_update_prevented BEFORE UPDATE ON mcp_provenance
BEGIN
	SELECT RAISE(ABORT, 'mcp_provenance table is immutable');
END;

CREATE TRIGGER mcp_provenance_delete_prevented BEFORE DELETE ON mcp_provenance
BEGIN
	SELECT RAISE(ABORT, 'mcp_provenance table is immutable');
END;

CREATE TRIGGER mcp_connections_update_prevented BEFORE UPDATE ON mcp_connections
BEGIN
	SELECT RAISE(ABORT, 'mcp_connections table is immutable');
END;

CREATE TRIGGER mcp_connections_delete_prevented BEFORE DELETE ON mcp_connections
BEGIN
	SELECT RAISE(ABORT, 'mcp_connections table is immutable');
END;

CREATE TRIGGER mcp_connection_closes_update_prevented BEFORE UPDATE ON mcp_connection_closes
BEGIN
	SELECT RAISE(ABORT, 'mcp_connection_closes table is immutable');
END;

CREATE TRIGGER mcp_connection_closes_delete_prevented BEFORE DELETE ON mcp_connection_closes
BEGIN
	SELECT RAISE(ABORT, 'mcp_connection_closes table is immutable');
END;
