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
ALTER TABLE candidates ADD COLUMN round INTEGER;
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
