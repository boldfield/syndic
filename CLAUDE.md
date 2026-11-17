# syndic: instructions for Claude Code in this repository

Read `docs/HANDOFF.md` before doing anything else. It explains the project, the decisions already made, and your role.

## Hard rules

- Design documents under `docs/design/` are not edited here. They are hash-bound peer-approved texts. If something cannot be built as written, add an entry to `docs/design/open-questions.md` and stop.
- Implementation goes through Odonian: Haiku-sized tasks, `escalate: true`, independent Opus review, human merge. Do not implement milestone tasks directly in this checkout unless Brian explicitly asks for a spike, and label any spike as such.
- Task batches are registered two or three at a time, each batch after Brian approves it. `docs/odonian/` has the payloads and `docs/odonian/tasks/build-batches.py` regenerates them with real UUIDs.
- Never commit a secret value, a raw probe record, or anything under a `--raw-dir`. `docs/evidence/` holds redacted exports only.
- No task or spike calls a real model endpoint. Tests use the fake adapter and `httptest`.
- Do not push, open PRs, or register Odonian tasks without Brian's say-so for that action.

## Verify the design before relying on it

```
shasum -a 256 docs/design/v2-candidate.md docs/design/v3-addendum.md docs/design/v3-delta-api-alpha.md
```

Expected: `4eeb7055…513321`, `c5cac3df…584114a`, `23ab2be6…12a42` (full values in `README.md`). A mismatch means the design on disk is not the approved design; stop and tell Brian.

## Where things are

- `docs/HANDOFF.md`: start here.
- `docs/design/v3-delta-api-alpha.md`: the governing document for the alpha.
- `docs/odonian/milestone-a.md`: task breakdown, batches, package layout.
- `docs/odonian/tasks/`: one spec per task; the `spec` body is what Odonian receives.
- `docs/review/`: why the delta says what it says.
