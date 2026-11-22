-- Frozen history bytes: add history_bytes column to turns to store the canonical
-- syndic-history/v1 document bytes, allowing the exact history to be reproduced later.

ALTER TABLE turns ADD COLUMN history_bytes BLOB;

-- Create index for efficient queries by conversation
CREATE INDEX turns_conversation_id_terminal_idx ON turns(conversation_id, terminal_state);
