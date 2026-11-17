---
key: A-08
title: "A-08: syndic probe command"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-07a, A-07b, A-03b]
---

## Goal

The first thing Brian runs. `syndic probe` loads the roster, probes one endpoint and model, writes the raw record to a local directory outside the repository, writes the redacted export to `docs/evidence/`, and records the probe in the journal. After this task merges, the Spark and inference.nvidia.com conformance runs can happen.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 7 ("Conformance runs" and the alpha-naming criterion) and section 1 (probe record stored in the journal, secret references only). `docs/odonian/milestone-a.md` exit criterion.

## Deliverables

In `cmd/syndic`, wiring together existing packages. Do not change them; if one is insufficient, stop and report. Standard library `flag` only; no CLI framework.

1. Subcommand `probe` with flags:
   - `--roster PATH` (default `./syndic.toml`, then `$XDG_CONFIG_HOME/syndic/syndic.toml`);
   - `--endpoint NAME` (required; an endpoint profile name from the roster);
   - `--model NAME` (required);
   - `--raw-dir PATH` (default `$XDG_DATA_HOME/syndic/evidence-raw`);
   - `--export-dir PATH` (default `./docs/evidence`);
   - `--journal PATH` (default `$XDG_DATA_HOME/syndic/journal.sqlite`);
   - `--secrets-dir PATH` (directory for `file` secret references; default `$XDG_CONFIG_HOME/syndic/secrets`);
   - `--context-tokens N` (assumed context length for the over-length test; default is the probe's);
   - `--skip-overlength` (do not run the over-length test).
   Where an XDG variable is unset, use its standard fallback under the home directory.
2. Behaviour, in order:
   - Load and validate the roster. The named endpoint must exist.
   - Refuse to continue if `--raw-dir` resolves (after symlink evaluation of its nearest existing ancestor) inside a git worktree: walk up from it looking for a `.git` entry. This check runs before any network request and before anything is written.
   - Construct the OpenAI-compatible adapter for the profile with the `EnvFileResolver` and run `Probe`.
   - Write the raw record, then redact and write the export.
   - Open the journal, create a run whose manifest is a JSON object containing the export (never the raw record), and append a `probe_completed` event naming the run.
   - Print a one-screen summary: endpoint, model, auth mode, identity, reported model, max-tokens field, usage available, system role honored, over-length outcome, eligible as required reviewer (yes, no or unknown), latency, and the two file paths written.
3. Exit codes: 0 on a completed probe regardless of findings; 2 on a roster or flag error; 3 when the probe returns unreachable, unauthorized or model-unavailable (print that operational state in one line, no stack trace; write no export); 4 when `--raw-dir` is inside a git worktree; 1 for anything else.
4. Replace `docs/evidence/README.md`: files here are redacted exports produced by `syndic probe` under the rule in `internal/evidence/RULE.md`; raw records live outside the repository; how to run the command for a new endpoint, with one short example using a placeholder endpoint name.
5. Update the repository `README.md` Status line to say milestone A is complete and point to `docs/evidence/README.md`.

## Acceptance

Tests run the command's entry function in-process against `httptest` with a temporary roster and temporary directories.

- A successful probe exits 0 and writes exactly one file to each directory. The export file parses, has `redacted` true, and does not contain a sentinel header value the test server sent. The journal contains one run whose manifest does not contain that sentinel, and one `probe_completed` event.
- `--raw-dir` inside a temporary directory containing a `.git` entry exits 4, writes nothing, and the test server received zero requests.
- A closed port exits 3 with output containing `unreachable`; a 401 exits 3 with output containing `unauthorized`. No export is written in either case.
- A roster that fails validation exits 2 and prints every violation.
- A missing `--endpoint` or `--model` exits 2.
- `make probe` invokes the command.

## Out of scope

Any other subcommand. The engine daemon. Attaching a TUI. Interpreting probe results beyond what the record already states. Any file under `docs/design/`.

## Note for the operator

After merge, Brian runs this against the Spark for each model he intends to use, and against inference.nvidia.com with `declared_location = "corporate"` in the profile. He commits only `docs/evidence/*.json`. Raw records stay under `--raw-dir`. These runs are not a task and no agent performs them.
