# Milestone A: foundations

Source: `docs/design/v3-delta-api-alpha.md` section 7, "Milestone A". This file breaks it into Haiku-sized tasks and groups them into registration batches of two or three, per Brian's standing Odonian rule.

Revised 2026-11-17 after the handoff: A-03, A-04 and A-07 were each too large for one Haiku pass and were split (suffixes `a`, `b`, `c`), and several specs were tightened. Keys that did not split keep their numbers, so `A-08` is still the probe command. `docs/HANDOFF.md` describes the original eight-task plan and is left as written.

## Exit criterion

Milestone A is done when `go test ./...` passes with: a transport contract test harness; a fake adapter and an OpenAI-compatible adapter both passing that harness; a probe that produces a raw local record and a redacted export; and a `syndic probe` command Brian can run against the Spark. No engine, no TUI, no real endpoint in CI.

## Package layout the tasks assume

```
github.com/boldfield/syndic
  cmd/syndic/          CLI entry (probe subcommand in A; attach later)
  cmd/syndicd/         engine daemon entry (stub in A)
  internal/config/     TOML roster, endpoint profiles, presets, secret refs
  internal/journal/    SQLite schema, migrations, store
  internal/transport/  Adapter interface, types, status, scrub, result check
  internal/transport/contracttest/  contract test harness
  internal/transport/fake/     fake adapter
  internal/transport/openai/   OpenAI-compatible adapter and probe
  internal/evidence/   redaction rule, export, raw record writer
  docs/                design, odonian, evidence, review
```

Tasks may propose a different layout only by stopping and asking; the layout is shared state across tasks and changing it mid-milestone breaks serialization.

## Tasks and dependencies

| Key   | Title                                             | Depends on          | Package                          |
|-------|---------------------------------------------------|---------------------|----------------------------------|
| A-01  | Repository scaffold, CI, license                  |                     | go.mod, cmd/*, .github, LICENSE  |
| A-02  | Config: TOML roster and secret references         | A-01                | internal/config                  |
| A-03a | Journal: schema and migrations                    | A-01                | internal/journal                 |
| A-03b | Journal store: runs, turns, messages, attempts    | A-03a               | internal/journal                 |
| A-03c | Journal store: candidates, verdicts, blocks       | A-03b               | internal/journal                 |
| A-04a | Transport: types, interface, scrub, result check  | A-01                | internal/transport               |
| A-04b | Transport contract test harness                   | A-04a               | internal/transport/contracttest  |
| A-05  | Fake adapter                                      | A-04b               | internal/transport/fake          |
| A-06  | OpenAI-compatible adapter: complete, cancel       | A-04b, A-02         | internal/transport/openai        |
| A-07a | Endpoint probe                                    | A-06                | internal/transport/openai        |
| A-07b | Evidence: redaction, export, raw writer           | A-04a, A-02         | internal/evidence                |
| A-08  | `syndic probe` command                            | A-07a, A-07b, A-03b | cmd/syndic                       |

Tasks sharing a package are chained by `depends_on` (the three journal tasks; A-06 then A-07a). Everything else in a batch touches disjoint directories.

`go.mod` and `go.sum` are the one shared file. Only A-02 (TOML parser) and A-03a (SQLite driver) add a module dependency, and they are in different batches so they never run concurrently. Every other task is standard library plus this repository.

## Batches

- **Batch 1:** A-01, A-02.
- **Batch 2:** A-03a, A-04a.
- **Batch 3:** A-03b, A-04b.
- **Batch 4:** A-03c, A-05, A-06.
- **Batch 5:** A-07a, A-07b.
- **Batch 6:** A-08.

`tasks/build-batches.py` writes `tasks/batch-N.json`. Each batch is posted only after Brian approves it, and after the previous batch has merged unless he says otherwise. Cross-batch `depends_on` entries must be the UUIDs Odonian returned for the earlier tasks; re-run the script with `--uuid KEY=UUID` after each post.

## Why the split is this shape

- A-04a is the keystone. Its types, status vocabulary, scrub and result check are what every later transport task is written against, so the spec fixes names and values exactly.
- The harness (A-04b) is separate from the types because it is a design problem of its own: it defines a named list of cases and a backend interface that each adapter supplies, and its assertions are tested directly.
- The scrub lives in `internal/transport`, not in the HTTP adapter, because delta section 1 makes it a contract invariant for every route.
- The journal is split by what can be reviewed in one sitting: schema with constraints and triggers; the store methods that carry the durable-intent and recovery invariants; the verdict and block store.
- The probe (A-07a) is separate from the adapter because the adapter is testable against the contract harness alone. Evidence (A-07b) is separate from the probe because it depends only on the capability record type and deserves its own review: it is the control that decides what reaches a public repository.
- A-08 is the first thing Brian can run. It exists so the Spark and inference.nvidia.com conformance runs can happen while milestone B is being specified.

## Readings the specs take where the design is silent

Recorded in `docs/design/open-questions.md` (OQ-1 identity field, OQ-2 optional parameters). One more is a spec decision rather than a design question: A-02 rejects any endpoint declared `metered`, because the design requires a spending ceiling before such an endpoint can be dispatched and the alpha does not implement one.

## What is deliberately absent

No engine, no protocol, no verdict parser, no socket, no TUI. Those are milestones B and C and their specs are written against merged A code, per the agreed workflow. No task calls a real network endpoint. No task handles a real secret; tests use fake references resolving to fake values.
