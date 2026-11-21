-- Durable, immutable turn submission time (UTC, RFC 3339) for conversation history.
-- Nullable: turns written before this migration are backfilled from their run's creation time.
ALTER TABLE turns ADD COLUMN submitted_at TEXT;

UPDATE turns
SET submitted_at = (SELECT created_at FROM runs WHERE runs.id = turns.run_id)
WHERE submitted_at IS NULL;
