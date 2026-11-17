-- Create runs table
CREATE TABLE runs (
	id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	manifest TEXT NOT NULL,
	manifest_hash TEXT NOT NULL
);

-- Create turns table
CREATE TABLE turns (
	id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	mode TEXT NOT NULL CHECK (mode IN ('solo', 'review')),
	whole_turn_deadline TEXT,
	terminal_state TEXT CHECK (
		terminal_state IS NULL OR
		terminal_state IN ('approved', 'solo_complete', 'incomplete', 'overridden', 'cancelled')
	),
	FOREIGN KEY (run_id) REFERENCES runs(id)
);

-- Create messages table (immutable)
CREATE TABLE messages (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL,
	phase TEXT NOT NULL,
	sender TEXT NOT NULL,
	recipient_set TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	content TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	byte_length INTEGER NOT NULL,
	parent_ids TEXT,
	FOREIGN KEY (turn_id) REFERENCES turns(id)
);

-- Create attempts table
CREATE TABLE attempts (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL,
	participant TEXT NOT NULL,
	phase TEXT NOT NULL,
	epoch INTEGER NOT NULL,
	dispatched_at TEXT NOT NULL,
	deadline TEXT,
	state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'completed', 'unknown')),
	status TEXT CHECK (status IS NULL OR status IN ('complete', 'truncated', 'refused', 'interrupted', 'error')),
	cancellation_state TEXT NOT NULL DEFAULT 'none' CHECK (cancellation_state IN ('none', 'requested', 'confirmed', 'unknown')),
	quarantined BOOLEAN NOT NULL DEFAULT 0,
	raw_output TEXT,
	scrub_count INTEGER,
	observed_identity TEXT,
	native_finish TEXT,
	transport_status TEXT,
	repair_of TEXT,
	FOREIGN KEY (turn_id) REFERENCES turns(id),
	FOREIGN KEY (repair_of) REFERENCES attempts(id)
);

-- Create candidates table (immutable)
CREATE TABLE candidates (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL,
	content TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	FOREIGN KEY (turn_id) REFERENCES turns(id)
);

-- Create verdicts table
CREATE TABLE verdicts (
	id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL,
	candidate_hash TEXT NOT NULL,
	parsed_verdict TEXT CHECK (parsed_verdict IS NULL OR parsed_verdict IN ('approve', 'block')),
	reason TEXT,
	block_id TEXT,
	severity TEXT CHECK (severity IS NULL OR severity IN ('low', 'medium', 'high')),
	evidence TEXT,
	remedy TEXT,
	validation_status TEXT NOT NULL CHECK (validation_status IN ('valid', 'rejected', 'malformed', 'revision_request')),
	FOREIGN KEY (attempt_id) REFERENCES attempts(id)
);

-- Create blocks table
CREATE TABLE blocks (
	id TEXT PRIMARY KEY,
	verdict_id TEXT NOT NULL,
	candidate_hash TEXT NOT NULL,
	severity TEXT NOT NULL CHECK (severity IN ('low', 'medium', 'high')),
	evidence TEXT NOT NULL,
	remedy TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('open', 'withdrawn', 'overridden')),
	FOREIGN KEY (verdict_id) REFERENCES verdicts(id)
);

-- Create events table (append-only)
CREATE TABLE events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	turn_id TEXT NOT NULL,
	event_type TEXT NOT NULL,
	timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	data TEXT,
	FOREIGN KEY (turn_id) REFERENCES turns(id)
);

-- Create immutability triggers
-- Prevent updates and deletes on messages
CREATE TRIGGER messages_update_prevented BEFORE UPDATE ON messages
BEGIN
	SELECT RAISE(ABORT, 'messages table is immutable');
END;

CREATE TRIGGER messages_delete_prevented BEFORE DELETE ON messages
BEGIN
	SELECT RAISE(ABORT, 'messages table is immutable');
END;

-- Prevent updates and deletes on candidates
CREATE TRIGGER candidates_update_prevented BEFORE UPDATE ON candidates
BEGIN
	SELECT RAISE(ABORT, 'candidates table is immutable');
END;

CREATE TRIGGER candidates_delete_prevented BEFORE DELETE ON candidates
BEGIN
	SELECT RAISE(ABORT, 'candidates table is immutable');
END;

-- Prevent updates and deletes on events
CREATE TRIGGER events_update_prevented BEFORE UPDATE ON events
BEGIN
	SELECT RAISE(ABORT, 'events table is immutable');
END;

CREATE TRIGGER events_delete_prevented BEFORE DELETE ON events
BEGIN
	SELECT RAISE(ABORT, 'events table is immutable');
END;

-- Prevent updates to runs.manifest and runs.manifest_hash
CREATE TRIGGER runs_manifest_immutable BEFORE UPDATE OF manifest, manifest_hash ON runs
BEGIN
	SELECT RAISE(ABORT, 'runs manifest is immutable');
END;

-- Prevent setting attempts.raw_output twice
CREATE TRIGGER attempts_raw_output_immutable BEFORE UPDATE OF raw_output ON attempts
WHEN OLD.raw_output IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'attempts.raw_output cannot be updated once set');
END;
