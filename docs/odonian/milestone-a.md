# Milestone A: foundations

Source: `docs/design/v3-delta-api-alpha.md` section 7, "Milestone A". This file breaks it into Haiku-sized tasks and groups them into registration batches of two or three, per Brian's standing Odonian rule.

## Exit criterion

Milestone A is done when `go test ./...` passes with: a transport contract test harness; a fake adapter and an OpenAI-compatible adapter both passing that harness; a probe that produces a raw local record and a redacted export; and a `syndic probe` command Brian can run against the Spark. No engine, no TUI, no real endpoint in CI.

## Package layout the tasks assume

```
github.com/boldfield/syndic
  cmd/syndic/          CLI entry (probe subcommand in A; attach later)
  cmd/syndicd/         engine daemon entry (stub in A)
  internal/config/     TOML roster, endpoint profiles, presets, secret refs
  internal/journal/    SQLite schema, migrations, store
  internal/transport/  Adapter interface, types, status, scrub, contract harness
  internal/transport/fake/     fake adapter
  internal/transport/openai/   OpenAI-compatible adapter and probe
  internal/evidence/   redaction rule and export
  docs/                design, odonian, evidence, review
```

Tasks may propose a different layout only by stopping and asking; the layout is shared state across tasks and changing it mid-milestone breaks serialization.

## Tasks and dependencies

| Key  | Title                                        | Depends on | Files (primary)                                  |
|------|----------------------------------------------|------------|--------------------------------------------------|
| A-01 | Repository scaffold, CI, license             |            | go.mod, cmd/*, .github/workflows, LICENSE, NOTICE |
| A-02 | Config: TOML roster and secret references    | A-01       | internal/config                                  |
| A-03 | Journal: SQLite schema and migrations        | A-01       | internal/journal                                 |
| A-04 | Transport contract, status, scrub, harness   | A-01       | internal/transport                               |
| A-05 | Fake adapter                                 | A-04       | internal/transport/fake                          |
| A-06 | OpenAI-compatible adapter: complete, cancel  | A-04, A-02 | internal/transport/openai                        |
| A-07 | Probe and redacted export                    | A-06       | internal/transport/openai, internal/evidence     |
| A-08 | `syndic probe` command                       | A-07, A-03 | cmd/syndic                                       |

A-02, A-03 and A-04 are independent of each other and touch disjoint directories; they can be claimed in parallel. A-05 and A-06 both depend on A-04 and are disjoint. A-07 extends A-06's package, so it is serialized after it. A-08 is last.

## Batches

- **Batch 1:** A-01, A-02. (`tasks/batch-1.json`)
- **Batch 2:** A-03, A-04. (`tasks/batch-2.json`)
- **Batch 3:** A-05, A-06. (`tasks/batch-3.json`)
- **Batch 4:** A-07, A-08. (`tasks/batch-4.json`)

Each batch is posted only after Brian approves it, and after the previous batch has merged unless he says otherwise. Cross-batch `depends_on` entries must be the UUIDs Odonian returned for the earlier tasks; the JSON files carry `FILL_UUID_A-NN` placeholders.

## Why the split is this shape

- A-04 is the keystone. The contract, status vocabulary and scrub are the parts of the delta that every later task depends on, and the contract test harness is what makes "passes the contract" a checkable claim. It is specified tightly so that A-05 and A-06 can be written against it without interpretation.
- The scrub lives in A-04, not in the OpenAI adapter, because delta section 1 makes it a contract invariant for every route.
- The probe is split from the adapter (A-07 after A-06) because the adapter is testable against `httptest` alone, while the probe adds the redaction rule and export format, which deserve their own review.
- A-08 is the first thing Brian can run. It exists so the Spark and inference.nvidia.com conformance runs can happen while milestone B is being specified.

## What is deliberately absent

No engine, no protocol, no verdict parser, no socket, no TUI. Those are milestones B and C and their specs are written against merged A code, per the agreed workflow. No task calls a real network endpoint. No task handles a real secret; tests use fake references resolving to fake values.
