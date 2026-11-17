---
key: A-03a
title: "A-03a: journal: SQLite schema and migrations"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-01]
---

## Goal

The schema of the durable journal the engine will own, and the code that opens a database and migrates it. No store API beyond `Open`; that is A-03b and A-03c.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 5 (tables, terminal states, validation statuses) and section 1 (scrub count on attempts). `docs/design/v2-candidate.md` sections 3 and 7 (immutable messages, epochs, append-only events).

## Deliverables

Package `internal/journal`. This is the only milestone A task besides A-02 that adds a module dependency.

1. Driver: `modernc.org/sqlite` (pure Go), pinned.
2. `Open(path string) (*DB, error)`: opens the file, sets WAL mode and foreign keys on, applies pending migrations, returns a handle wrapping `*sql.DB`. `(*DB) Close()`.
3. Embedded migrations (`embed.FS`, numbered SQL files) and a `schema_version` table. Applying migrations to an already-migrated database is a no-op.
4. Tables, with the columns delta section 5 lists for each: `runs`, `turns`, `messages`, `attempts`, `candidates`, `verdicts`, `blocks`, `events`. Foreign keys follow the ids section 5 names (turn to run, message/attempt/candidate to turn, verdict to attempt, block to verdict).
5. The `attempts` table keeps lifecycle and outcome in separate columns, because the design's normalized completion status has exactly five values:
   - `state`: `pending`, `completed`, `unknown`. Not null. `pending` is the durable-intent state; `unknown` is what crash recovery sets.
   - `status`: null until completed, then one of `complete`, `truncated`, `refused`, `interrupted`, `error`.
   - `cancellation_state`: `none`, `requested`, `confirmed`, `unknown`. Default `none`.
   - `quarantined`: boolean, default false.
   - `raw_output`, `scrub_count`, `observed_identity`, `native_finish`, `transport_status`: null until completed.
   - `repair_of`: nullable reference to another attempt.
6. `CHECK` constraints for every enum: turn mode (`solo`, `review`); turn terminal state (null, or `approved`, `solo_complete`, `incomplete`, `overridden`, `cancelled`); the three attempt enums above; verdict parsed value (null, or `approve`, `block`); verdict validation status (`valid`, `rejected`, `malformed`, `revision_request`); block severity (`low`, `medium`, `high`); block state (`open`, `withdrawn`, `overridden`).
7. Immutability triggers that abort the statement:
   - any `UPDATE` or `DELETE` on `messages`, `candidates`, `events`;
   - any `UPDATE` of `runs.manifest` or `runs.manifest_hash`;
   - any `UPDATE` of `attempts.raw_output` when the existing value is not null.
8. Package comment listing which invariants are enforced by triggers and constraints (this task) and which are left to the store API (A-03b, A-03c).

## Acceptance

All tests use a database in `t.TempDir()` and raw SQL.

- `Open` on an empty path creates every table; `Open` again on the same file succeeds and changes nothing (schema version unchanged).
- Inserting an unknown value into each enum column fails its `CHECK` (table-driven, one row per enum).
- `UPDATE` and `DELETE` on a `messages` row, a `candidates` row and an `events` row each fail.
- Setting `attempts.raw_output` once succeeds; setting it again fails and the first value is unchanged.
- Inserting an attempt for a turn that does not exist fails the foreign key.
- `go vet` and lint clean.

## Out of scope

Any store method other than `Open` and `Close`. Hash computation. Any engine logic or network.
