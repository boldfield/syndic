-- Journal sequence counter for ordering messages and attempts across a turn.

CREATE TABLE journal_counter (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	next INTEGER NOT NULL
);

INSERT INTO journal_counter (id, next) VALUES (1, 1);

ALTER TABLE messages ADD COLUMN journal_seq INTEGER;
CREATE UNIQUE INDEX messages_journal_seq_unique ON messages(journal_seq) WHERE journal_seq IS NOT NULL;

ALTER TABLE attempts ADD COLUMN journal_seq INTEGER;
CREATE UNIQUE INDEX attempts_journal_seq_unique ON attempts(journal_seq) WHERE journal_seq IS NOT NULL;
