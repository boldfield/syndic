-- Conversations: add conversations table and conversation-related columns to turns and candidates.

-- Create conversations table
CREATE TABLE conversations (
	id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	workspace TEXT
);

-- Add columns to turns (can be NULL initially, will be populated for existing turns)
ALTER TABLE turns ADD COLUMN conversation_id TEXT;
ALTER TABLE turns ADD COLUMN context_hash TEXT;
ALTER TABLE turns ADD COLUMN file_tainted BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN max_rounds_in_effect INTEGER;
ALTER TABLE turns ADD COLUMN max_rounds_ceiling INTEGER;

-- Add columns to candidates
ALTER TABLE candidates ADD COLUMN round INTEGER NOT NULL DEFAULT 0;
ALTER TABLE candidates ADD COLUMN supersedes TEXT;

-- Add CHECK constraint for round (0-5)
CREATE TRIGGER candidates_round_check BEFORE INSERT ON candidates
WHEN NEW.round < 0 OR NEW.round > 5
BEGIN
	SELECT RAISE(ABORT, 'round must be between 0 and 5');
END;

CREATE TRIGGER candidates_round_check_update BEFORE UPDATE OF round ON candidates
WHEN NEW.round < 0 OR NEW.round > 5
BEGIN
	SELECT RAISE(ABORT, 'round must be between 0 and 5');
END;

-- Add foreign key constraint for candidates.supersedes -> candidates(id)
CREATE TRIGGER candidates_supersedes_fk_insert BEFORE INSERT ON candidates
WHEN NEW.supersedes IS NOT NULL AND NOT EXISTS(SELECT 1 FROM candidates WHERE id = NEW.supersedes)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: candidates.supersedes');
END;

CREATE TRIGGER candidates_supersedes_fk_update BEFORE UPDATE OF supersedes ON candidates
WHEN NEW.supersedes IS NOT NULL AND NOT EXISTS(SELECT 1 FROM candidates WHERE id = NEW.supersedes)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: candidates.supersedes');
END;

-- Add foreign key constraint for turns.conversation_id
CREATE TRIGGER turns_conversation_id_fk_insert BEFORE INSERT ON turns
WHEN NEW.conversation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM conversations WHERE id = NEW.conversation_id)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: turns.conversation_id');
END;

CREATE TRIGGER turns_conversation_id_fk_update BEFORE UPDATE OF conversation_id ON turns
WHEN NEW.conversation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM conversations WHERE id = NEW.conversation_id)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: turns.conversation_id');
END;

-- Create a conversation for each existing turn with null workspace
-- Using 'conv-' prefix to avoid collisions with future conversation ids
INSERT INTO conversations (id, workspace)
SELECT 'conv-' || id, NULL
FROM turns;

-- Set conversation_id for existing turns
UPDATE turns
SET conversation_id = 'conv-' || id
WHERE conversation_id IS NULL;

-- Create the index for conversation_id
CREATE INDEX turns_conversation_id_idx ON turns(conversation_id);
