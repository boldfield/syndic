---
key: A-08
title: "A-08: syndic probe command"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-07, A-03]
---

## Goal

The first thing Brian runs. `syndic probe` loads the roster, probes one endpoint and model, writes the raw record to a local directory outside the repository, writes the redacted export to `docs/evidence/`, and records the probe in the journal. After this task merges, the Spark and inference.nvidia.com conformance runs can happen.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 7 ("Conformance runs" and the alpha-naming criterion) and section 1 (probe record stored in the journal). `docs/odonian/milestone-a.md` exit criterion.

## Deliverables

In `cmd/syndic`:

1. Subcommand `probe` with flags:
   - `--roster PATH` (default `./syndic.toml`, then `$XDG_CONFIG_HOME/syndic/syndic.toml`);
   - `--endpoint NAME` (required; an endpoint profile name from the roster);
   - `--model NAME` (required);
   - `--raw-dir PATH` (default `$XDG_DATA_HOME/syndic/evidence-raw`; must not be inside the repository; refuse if it resolves under the current git worktree);
   - `--export-dir PATH` (default `./docs/evidence`);
   - `--journal PATH` (default `$XDG_DATA_HOME/syndic/journal.sqlite`).
2. Behavior: load and validate the roster (A-02); construct the OpenAI-compatible adapter for the named profile with the `EnvFileResolver` (A-06); run `Probe` (A-07); write raw and export (A-07's writers); open the journal (A-03), create a run with a manifest containing the export (never the raw record), and append an event `probe_completed`; print a one-screen summary: endpoint, model, auth mode, identity, max-tokens field, usage available, system role honored, marker-test outcome, eligible as required reviewer (yes/no/unknown), latency, and the two file paths written.
3. Exit codes: 0 on a completed probe regardless of findings; 2 on roster or flag error; 3 on `unreachable` or `unauthorized` (print the operational state, not a stack trace); 4 if `--raw-dir` resolves inside the repository.
4. A `docs/evidence/README.md` explaining that files here are redacted exports produced by `syndic probe`, that raw records live outside the repository, and how to run the command for a new endpoint. One short example with a placeholder endpoint name.
5. Update the repository `README.md` Status line to say milestone A is complete and point to `docs/evidence/README.md`.

## Acceptance

- Integration test using `httptest` and a temporary roster: `syndic probe --endpoint test --model m` exits 0, writes one file to each directory, the export passes the A-07 guard test when re-read, and the journal contains one run whose manifest JSON does not contain the sentinel header value the test server sent.
- Test: `--raw-dir` pointing inside a temporary git repository exits 4 and writes nothing.
- Test: unreachable endpoint exits 3 with a message containing `unreachable`.
- `make probe ENDPOINT=test` runs the command.

## Out of scope

Any other subcommand. The engine daemon. Attaching a TUI. Interpreting probe results beyond the one eligibility rule from A-07.

## Note for the operator

After merge, Brian runs this against the Spark for each model he intends to use, and against inference.nvidia.com with `declared_location = "corporate"` in the profile. He commits only `docs/evidence/*.json`. Raw records stay under `--raw-dir`. These runs are the evidence that lets milestone A's result be called an alpha foundation; they are not a task and no agent performs them.
