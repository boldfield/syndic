---
key: A-05
title: "A-05: fake adapter with scripted failure modes"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-04]
---

## Goal

An in-process `transport.Adapter` that can be scripted to produce every status and failure the contract names, so the engine (milestone B) and the acceptance tests can run without any network or model.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 (contract) and section 4 item 9 (fake adapter drives every negative case). `docs/design/v2-candidate.md` section 10 (deterministic failure tests).

## Deliverables

Package `internal/transport/fake`.

1. `Script`: an ordered list of `Step`s. Each `Step` says what the next `Complete` call returns: `Reply{Text, Status, NativeFinish, Identity, Delay}`, `Hang` (block until context cancelled, then `interrupted`), `Echo{Secret}` (returns body containing the given string, to exercise the scrub), `Error{TransportStatus}`, `WrongIdentity{Model}`.
2. `New(script Script, opts ...Option) *Adapter` implementing `transport.Adapter`. Options: `WithIdentity(ModelIdentity)`, `WithCapabilities(CapabilityRecord)`, `WithSecrets([]string)` (the values the adapter pretends it resolved, so `Echo` steps get scrubbed exactly as a real adapter would).
3. `Complete` consumes one step per call, applies `transport.Scrub` with the configured secrets, echoes the request envelope, and records the call (`Calls() []CompleteRequest`) so tests can assert what the engine sent, including the full message array.
4. `Cancel` marks the current `Hang` step as cancelled; `ServerConfirmed` is `yes` for the fake, with an option to make it `unknown`.
5. `Probe` returns the configured `CapabilityRecord`; `Usage` returns a counter or `ErrUsageUnavailable` per option.
6. A `contracttest.Scenario` implementation so the A-04 harness runs against this adapter.
7. Helper constructors for common scripts: `Approves(candidateHash, attemptID)` returning a well-formed VERDICT v1 approve block (format from delta section 6), `Blocks(...)`, `ProseApprove()` (the word "approve" with no envelope), `StaleHash()`, `ApproveWithChanges()`, `DoubleEnvelope()`, `EndVerdictInReason()`. These make milestone B's negative tests one-liners. The verdict text format is fixed by section 6; copy it exactly.

## Acceptance

- `contracttest.Run` passes against the fake adapter for every case.
- A test drives a script of all step kinds and asserts the sequence of statuses.
- A test asserts `Hang` plus `Cancel` yields `interrupted` and that the adapter returns promptly.
- A test asserts `Echo{Secret}` output is scrubbed and `ScrubCount == 1` when the secret is configured, and unscrubbed with count 0 when it is not (the fake must not scrub what it did not "resolve", mirroring the real contract).
- Helper constructors produce text that round-trips through a minimal parser test here (find `VERDICT v1` … `END VERDICT`, check fields present). The real parser is milestone B; this is a smoke check that the fixtures are well-formed.

## Out of scope

Any HTTP. Any parsing beyond the smoke check. Deliberate-mode fixtures.
