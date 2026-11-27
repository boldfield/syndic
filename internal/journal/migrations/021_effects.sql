-- Effect sets (v9 delta section 5). An effect set is staged for a turn with
-- its canary set, which is stored with it and never removed. Each effect row
-- carries its own state and acknowledgement; the set's state is not stored,
-- it is derived from the rows and the discard record. done, failed and
-- uncertain are never rewritten: the triggers below allow only the
-- transitions pending|skipped -> dispatching -> done|failed|uncertain, a
-- recheck of a skipped effect, and the acknowledgement set once from NULL.

CREATE TABLE effect_sets (
	turn_id TEXT PRIMARY KEY REFERENCES turns(id),
	canary_set TEXT NOT NULL,
	recorded_at TEXT NOT NULL
);

CREATE TABLE effects (
	id TEXT PRIMARY KEY,
	turn_id TEXT NOT NULL REFERENCES effect_sets(turn_id),
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
	server TEXT NOT NULL CHECK (server <> ''),
	server_identity TEXT NOT NULL CHECK (server_identity <> ''),
	tool TEXT NOT NULL CHECK (tool <> ''),
	pin_sha256 TEXT NOT NULL CHECK (pin_sha256 <> ''),
	args_sha256 TEXT NOT NULL REFERENCES tool_content(sha256),
	state TEXT NOT NULL CHECK (state IN ('pending', 'skipped', 'dispatching', 'done', 'failed', 'uncertain')),
	skipped_rule TEXT,
	reply_sha256 TEXT REFERENCES tool_content(sha256),
	reply_byte_len INTEGER CHECK (reply_byte_len IS NULL OR reply_byte_len >= 0),
	ack_state TEXT CHECK (ack_state IN ('skipped', 'failed', 'uncertain')),
	acked_at TEXT,
	recorded_at TEXT NOT NULL,
	UNIQUE (turn_id, ordinal),
	CHECK ((state = 'skipped') = (skipped_rule IS NOT NULL AND skipped_rule <> '')),
	CHECK ((reply_sha256 IS NULL) = (reply_byte_len IS NULL)),
	CHECK (state IN ('done', 'failed', 'uncertain') OR reply_sha256 IS NULL),
	CHECK (state NOT IN ('done', 'failed') OR reply_sha256 IS NOT NULL),
	CHECK ((ack_state IS NULL) = (acked_at IS NULL)),
	CHECK (ack_state IS NULL OR ack_state = state)
);

CREATE TABLE effect_set_discards (
	turn_id TEXT PRIMARY KEY REFERENCES effect_sets(turn_id),
	discarded_at TEXT NOT NULL
);

CREATE TRIGGER effects_state_transition BEFORE UPDATE ON effects
WHEN NEW.id IS NOT OLD.id OR NEW.turn_id IS NOT OLD.turn_id OR NEW.ordinal IS NOT OLD.ordinal
	OR NEW.server IS NOT OLD.server OR NEW.server_identity IS NOT OLD.server_identity
	OR NEW.tool IS NOT OLD.tool OR NEW.pin_sha256 IS NOT OLD.pin_sha256
	OR NEW.args_sha256 IS NOT OLD.args_sha256 OR NEW.recorded_at IS NOT OLD.recorded_at
BEGIN
	SELECT RAISE(ABORT, 'effect identity columns are immutable');
END;

CREATE TRIGGER effects_final_not_rewritten BEFORE UPDATE ON effects
WHEN OLD.state IN ('done', 'failed', 'uncertain')
	AND (NEW.state IS NOT OLD.state OR NEW.skipped_rule IS NOT OLD.skipped_rule
		OR NEW.reply_sha256 IS NOT OLD.reply_sha256 OR NEW.reply_byte_len IS NOT OLD.reply_byte_len)
BEGIN
	SELECT RAISE(ABORT, 'a done, failed or uncertain effect is never rewritten');
END;

CREATE TRIGGER effects_ack_set_once BEFORE UPDATE ON effects
WHEN OLD.ack_state IS NOT NULL
	AND (NEW.ack_state IS NOT OLD.ack_state OR NEW.acked_at IS NOT OLD.acked_at)
BEGIN
	SELECT RAISE(ABORT, 'an effect acknowledgement is never rewritten');
END;

CREATE TRIGGER effects_state_path BEFORE UPDATE OF state ON effects
WHEN NEW.state IS NOT OLD.state
	AND NOT ((OLD.state IN ('pending', 'skipped') AND NEW.state IN ('skipped', 'dispatching') AND OLD.ack_state IS NULL)
		OR (OLD.state = 'dispatching' AND NEW.state IN ('done', 'failed', 'uncertain')))
BEGIN
	SELECT RAISE(ABORT, 'invalid effect state transition');
END;

CREATE TRIGGER effects_delete_prevented BEFORE DELETE ON effects
BEGIN
	SELECT RAISE(ABORT, 'effects table is append-only');
END;

CREATE TRIGGER effect_sets_update_prevented BEFORE UPDATE ON effect_sets
BEGIN
	SELECT RAISE(ABORT, 'effect_sets table is immutable');
END;

CREATE TRIGGER effect_sets_delete_prevented BEFORE DELETE ON effect_sets
BEGIN
	SELECT RAISE(ABORT, 'effect_sets table is immutable');
END;

CREATE TRIGGER effect_set_discards_update_prevented BEFORE UPDATE ON effect_set_discards
BEGIN
	SELECT RAISE(ABORT, 'effect_set_discards table is immutable');
END;

CREATE TRIGGER effect_set_discards_delete_prevented BEFORE DELETE ON effect_set_discards
BEGIN
	SELECT RAISE(ABORT, 'effect_set_discards table is immutable');
END;
