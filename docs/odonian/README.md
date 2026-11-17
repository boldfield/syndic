# Odonian registration for syndic

This directory holds everything needed to register syndic in Odonian: the Project, the design Document, and the milestone A tasks. Payloads follow `docs/api.md` in the Odonian repository as read on November 17, 2026 (`POST /projects`, `POST /projects/{id}/documents`, `POST /projects/{id}/tasks` with a JSON array). Verify field names against the running Odonian's `docs/api.md` before posting; the API may have moved.

## Order of operations

1. **Repository first.** The Document `ref` is a repository-relative path and should carry a commit pin, so the repository must exist with `docs/design/v3-delta-api-alpha.md` committed before the Document is registered. Task A-01 is the scaffold, but the design docs can be committed before any code: an initial commit containing only `docs/` and a stub `README.md` is fine and gives the Document something to pin to.
2. **Project.** `project.json`.
3. **Document.** `document.json`, with `commit` set to the hash of the commit that added the design docs. Capture the returned document UUID; every task needs it.
4. **Tasks, batch by batch.** `milestone-a.md` lists the batches. Each batch is one `POST` with a JSON array. Within a batch, `key` values let later tasks depend on earlier ones. Across batches, `depends_on` must use the UUIDs returned by earlier posts, so record them.
5. **Approval gate.** Brian approves each batch before it is posted. The standing rule is two or three tasks per batch. Do not post batch N+1 until batch N's tasks are merged or Brian says otherwise.

## Files

- `project.json`: the Project body.
- `document.json`: the design Document body. Fill in `commit`.
- `milestone-a.md`: the batch plan, dependency graph and the reasoning behind the split.
- `tasks/A-NN-*.md`: one file per task. The `spec` field for the API is the body of the file below the front matter, verbatim. Front matter holds the API fields.
- `tasks/batch-1.json` and following: ready-to-post arrays assembled from the task files. `document_id` and cross-batch `depends_on` are placeholders to fill from earlier responses.

## Model and review defaults

Every task: `model: "haiku"`, `review_models: ["opus"]`, `escalate: true`, `agent_merge: false`, `track: "build"`. These are Brian's standing settings; they are in the task files for completeness, not because they vary.
