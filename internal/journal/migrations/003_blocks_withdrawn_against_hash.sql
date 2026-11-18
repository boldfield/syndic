-- Add withdrawn_against_hash column to blocks table

ALTER TABLE blocks ADD COLUMN withdrawn_against_hash TEXT;

-- Create trigger to enforce the constraint: withdrawn_against_hash must be non-null
-- exactly when state is 'withdrawn'
CREATE TRIGGER blocks_withdrawn_hash_constraint BEFORE INSERT ON blocks
WHEN NEW.state = 'withdrawn' AND NEW.withdrawn_against_hash IS NULL
BEGIN
	SELECT RAISE(ABORT, 'withdrawn_against_hash must be non-null when state is withdrawn');
END;

CREATE TRIGGER blocks_withdrawn_hash_constraint_update BEFORE UPDATE ON blocks
WHEN NEW.state = 'withdrawn' AND NEW.withdrawn_against_hash IS NULL
BEGIN
	SELECT RAISE(ABORT, 'withdrawn_against_hash must be non-null when state is withdrawn');
END;

-- Create trigger to prevent non-null withdrawn_against_hash when state is 'open' or 'overridden'
CREATE TRIGGER blocks_withdrawn_hash_non_null_constraint BEFORE INSERT ON blocks
WHEN (NEW.state IN ('open', 'overridden')) AND NEW.withdrawn_against_hash IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'withdrawn_against_hash must be null when state is open or overridden');
END;

CREATE TRIGGER blocks_withdrawn_hash_non_null_constraint_update BEFORE UPDATE ON blocks
WHEN (NEW.state IN ('open', 'overridden')) AND NEW.withdrawn_against_hash IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'withdrawn_against_hash must be null when state is open or overridden');
END;
