---
key: A-03
title: "A-03: journal: SQLite schema, migrations and store"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-01]
---

## Goal

The durable journal that the engine will own: schema, migrations, and a small store API that enforces the invariants the design states. No engine logic.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 5 (tables, terminal states, validation statuses, intent-before-dispatch) and section 1 (scrub count on attempts). `docs/design/v2-candidate.md` sections 3 and 7 (immutable messages, engine-computed hashes, epochs, append-only events).

## Deliverables

Package `internal/journal`.

1. Driver: `modernc.org/sqlite` (pure Go). Open with WAL mode and foreign keys on.
2. Embedded migrations (`embed.FS`, numbered SQL files) creating these tables exactly as section 5 lists their columns: `runs`, `turns`, `messages`, `attempts`, `candidates`, `verdicts`, `blocks`, `events`. Add `schema_version`. Use `CHECK` constraints for every enum: turn terminal state (`approved`, `solo_complete`, `incomplete`, `overridden`, `cancelled`), attempt normalized status (`complete`, `truncated`, `refused`, `interrupted`, `error`, plus `pending` and `unknown` for the lifecycle), verdict parsed value (`approve`, `block`), verdict validation status (`valid`, `rejected`, `malformed`, `revision_request`), block state (`open`, `withdrawn`, `overridden`).
3. `Store` with methods, each in a transaction:
   - `CreateRun(manifest json.RawMessage) (runID, manifestHash)`; the hash is SHA-256 of the canonical manifest bytes, computed here, never supplied by the caller.
   - `AppendMessage(turnID, phase, sender, recipients, content, parents) (msgID, sha256, byteLen)`; sequence is assigned by the store; hash and length are computed by the store.
   - `BeginAttempt(turnID, participant, phase, epoch, deadline) attemptID`; row is committed with status `pending` before the function returns. This is the durable-intent record.
   - `CompleteAttempt(attemptID, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus)`; rejects if the attempt is not `pending`.
   - `QuarantineLateOutput(attemptID, ...)` for an attempt whose epoch is no longer current: stores output, marks quarantined, never changes turn state.
   - `MarkUnknown(attemptID)` for crash recovery.
   - `RecordVerdict`, `OpenBlock`, `WithdrawBlock(blockID, againstCandidateHash)`, `OverrideBlock`.
   - `AppendEvent(kind, payload)`; events table has no update or delete path in the API.
   - `RecoverPending() []attemptID`: returns attempts left `pending`; the caller marks them unknown. The store never re-dispatches.
4. Immutability: no `Update` on `messages` or `candidates`; attempt `raw_output` is write-once (second `CompleteAttempt` on the same row errors). Enforce with triggers where SQLite allows, and with the store API otherwise. Document which is which in the package comment.

## Acceptance

- Migrations apply on an empty database and are idempotent on a migrated one.
- Test: `BeginAttempt` row is visible from a second connection before `CompleteAttempt` is called (intent precedes dispatch).
- Test: `CompleteAttempt` twice on one attempt errors; the first output is unchanged.
- Test: inserting an unknown enum value through raw SQL fails the `CHECK`.
- Test: `AppendMessage` hash equals an independently computed SHA-256 of the content and byte length equals `len(content)`.
- Test: `RecoverPending` after a simulated crash (begin, close DB, reopen) returns the attempt; `MarkUnknown` sets it; nothing is re-dispatched (there is no API that could).
- Test: `events` has no exported mutation other than append.

## Out of scope

Candidate hashing rules beyond storing a caller-provided hash (the engine computes candidate identity in milestone B). Any scheduling, deadline enforcement or protocol logic. Any network.
