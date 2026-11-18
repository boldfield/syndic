// Package journal manages the SQLite-backed durable journal for syndic.
//
// # Schema and invariants
//
// This task (A-03a) enforces invariants via CHECK constraints, foreign keys, and triggers:
//   - CHECK constraints enforce enum values: turn mode (solo, review), terminal states,
//     attempt state/status/cancellation_state, verdict parsed values and validation statuses,
//     block severity and state.
//   - Foreign key constraints link messages, attempts, candidates, verdicts, and blocks to their parent turns and attempts.
//     Events may optionally reference a turn via turn_id; some events have no turn.
//   - Immutability triggers abort any UPDATE or DELETE on messages, candidates, and events.
//   - Triggers prevent UPDATE of runs.manifest, runs.manifest_hash, and prevent setting attempts.raw_output twice.
//
// # Configuration
//
// Foreign key constraints and WAL mode are configured via the DSN with _pragma parameters,
// ensuring they are applied to every connection in the pool (not just the initial connection).
//
// Invariants left to the store API (A-03b, A-03c) include:
//   - Business logic validation of turn phases and state transitions.
//   - Duplicate-attempt detection and ordering guarantees within a turn.
//   - Candidate hash binding and verification against submission.
//   - Verdict envelope parsing and validation beyond schema structure.
package journal
