---
key: A-03c
title: "A-03c: journal store: candidates, verdicts, blocks"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-03b]
---

## Goal

The remaining store methods: candidates, verdicts and blocks. Storage and state rules only; deciding whether a verdict is valid is the engine's job in milestone B.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 5 (`candidates`, `verdicts`, `blocks`). `docs/design/v2-candidate.md` section 4 (blocks survive revisions until withdrawn against a new candidate or overridden by the human).

## Deliverables

In package `internal/journal`, added to the `Store` from A-03b. Every method runs in one transaction. Do not change the migrations or the A-03b methods; if something cannot be done without that, stop and report.

- `CreateCandidate(turnID, content, hash) (candidateID, error)`: stores a caller-provided hash. The engine computes candidate identity in milestone B; this method does not hash.
- `RecordVerdict(attemptID, echoedCandidateHash, parsed, reason, validationStatus) (verdictID, error)`: `parsed` is empty, `approve` or `block`. Stores what it is given; it does not judge validity.
- `OpenBlock(verdictID, blockID, candidateHash, severity, evidence, remedy) error`: state `open`. Rejects if the verdict's parsed value is not `block`.
- `WithdrawBlock(blockID, againstCandidateHash) error`: state `withdrawn`, recording the new candidate hash. Rejects unless the block is `open`, and rejects when `againstCandidateHash` is empty or equals the hash the block was opened against (a block is withdrawn against a named new candidate).
- `OverrideBlock(blockID) error`: state `overridden`. Rejects unless the block is `open`.
- `OpenBlocks(turnID) ([]Block, error)`: blocks in state `open` for the turn, across all its candidates.

## Acceptance

- A block opened against candidate hash H1 is still returned by `OpenBlocks` after a second candidate with hash H2 is created for the same turn.
- `WithdrawBlock` with H2 removes it from `OpenBlocks`; with H1 or an empty hash it errors and the block stays open.
- `OverrideBlock` and `WithdrawBlock` each error on a block that is not `open`.
- `OpenBlock` on a verdict whose parsed value is `approve` errors.
- `RecordVerdict` stores each of the four validation statuses and rejects an unknown one.
- `go vet` and lint clean.

## Out of scope

Verdict parsing or validation. Candidate hashing. Turn state changes as a consequence of a verdict. Any network.
