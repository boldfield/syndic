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

-- Foreign key constraint for turns.conversation_id (insert/update)
-- Only enforces when a conversation_id is provided; legacy rows with NULL are allowed
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

-- Foreign key constraint for conversations (delete protection)
CREATE TRIGGER conversations_fk_delete BEFORE DELETE ON conversations
WHEN EXISTS(SELECT 1 FROM turns WHERE conversation_id = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: turns references this conversation');
END;

-- Foreign key constraint for candidates.supersedes (insert/update)
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

-- Foreign key constraint for candidates (delete protection)
CREATE TRIGGER candidates_fk_delete BEFORE DELETE ON candidates
WHEN EXISTS(SELECT 1 FROM candidates WHERE supersedes = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'FOREIGN KEY constraint failed: candidates references this candidate');
END;
