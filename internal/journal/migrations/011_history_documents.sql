-- Frozen conversation history: the exact syndic-history/v1 document a turn
-- was admitted against, stored once by its SHA-256 (the turn's context_hash).
CREATE TABLE history_documents (
	hash TEXT PRIMARY KEY,
	document BLOB NOT NULL
);
