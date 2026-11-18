-- Make events.turn_id nullable

-- Rename the old events table
ALTER TABLE events RENAME TO events_old;

-- Create the new events table with nullable turn_id
CREATE TABLE events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	turn_id TEXT,
	event_type TEXT NOT NULL,
	timestamp TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	data TEXT,
	FOREIGN KEY (turn_id) REFERENCES turns(id)
);

-- Copy all data from the old table
INSERT INTO events (id, turn_id, event_type, timestamp, data)
SELECT id, turn_id, event_type, timestamp, data FROM events_old;

-- Drop the old table
DROP TABLE events_old;

-- Recreate immutability triggers on the new events table
CREATE TRIGGER events_update_prevented BEFORE UPDATE ON events
BEGIN
	SELECT RAISE(ABORT, 'events table is immutable');
END;

CREATE TRIGGER events_delete_prevented BEFORE DELETE ON events
BEGIN
	SELECT RAISE(ABORT, 'events table is immutable');
END;
