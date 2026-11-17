---
key: A-05
title: "A-05: fake adapter with scripted failure modes"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-04b]
---

## Goal

An in-process `transport.Adapter` that can be scripted to produce every status and failure the contract names, so the engine (milestone B) and the acceptance tests can run without any network or model.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 (contract), section 4 item 9 (the fake adapter drives every negative case) and section 6 (verdict envelope text, for the fixtures). `docs/design/v2-candidate.md` section 10 (deterministic failure tests).

## Deliverables

Package `internal/transport/fake`. Standard library only. Do not change `internal/transport` or `internal/transport/contracttest`; if either is insufficient, stop and report.

1. A script: an ordered list of steps. Each step says what the next `Complete` call returns. Step kinds:
   - reply: text, status, native finish reason, transport status, identity, reported model, optional delay;
   - hang: block until the request deadline passes or `Cancel` is called for that attempt, then return `interrupted` with transport status `timeout` or `cancelled` respectively;
   - echo: reply whose text contains a given string, to exercise the scrub.
   A `Complete` call with the script exhausted returns status `error` and a transport status saying so; it never panics and never repeats a step.
2. A constructor taking the script and options: the identity and reported model used when a step does not set them; the `CapabilityRecord` that `Probe` returns; the secret values the adapter pretends it resolved; whether `Cancel` reports server confirmation as `yes` (default) or `unknown`; whether `Usage` is available.
3. `Complete` consumes one step per call, sets `RawOutput` and `Text` to the step's text after `transport.Scrub` with the configured secrets, copies the request envelope into the result, and records the request. A `Calls()` accessor returns every request received, in order, including the full message array, so tests can assert what the engine sent. Safe for concurrent use.
4. `Cancel`, `Probe` and `Usage` per the options above.
5. A `contracttest.Backend` implementation in this package's test file, arranging each case with a one-step script.
6. Verdict fixture helpers, each returning reply text. The envelope format is fixed by delta section 6; copy it exactly.
   - a well-formed approve, given candidate hash and attempt id;
   - a well-formed block, given candidate hash, attempt id, block id, severity, evidence, remedy and reason;
   - prose containing the word "approve" and no envelope;
   - a well-formed approve echoing a different candidate hash than the one given;
   - an otherwise well-formed envelope whose verdict is `approve-with-changes`;
   - output containing two envelopes;
   - a block whose reason contains a line that is exactly `END VERDICT`, followed by more text and a second `END VERDICT`.
   These make milestone B's negative tests one-liners.

## Acceptance

- `contracttest.Run` passes against the fake backend for every case.
- A test drives a script containing every step kind and asserts the sequence of statuses, then asserts the exhausted-script behaviour.
- Hang plus `Cancel` yields `interrupted` with transport status `cancelled`, promptly. Hang with no cancel yields `interrupted` with `timeout` at the deadline.
- An echo step is scrubbed with `ScrubCount` 1 when the echoed string is a configured secret, and returned unchanged with count 0 when it is not (the fake must not scrub what it did not "resolve", mirroring the real contract).
- `Calls()` returns the exact message array passed to `Complete`.
- Fixture smoke test: for the two well-formed helpers, the text between the first line that is exactly `VERDICT v1` and the first later line that is exactly `END VERDICT` contains each required field name from section 6 once. The real parser is milestone B; this only checks the fixtures are well-formed.
- `go test -race` passes for this package.

## Out of scope

Any HTTP. Any verdict parsing beyond the smoke check. Deliberate-mode fixtures.
