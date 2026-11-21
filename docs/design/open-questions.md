# Open questions

Questions the design documents do not settle, found during implementation. The CLI Fable or a task worker adds an entry here and stops; Brian takes it to the peer discussion. Entries are removed when a design revision resolves them, with the revision noted.

OQ-3 (racing reservations in Budgets). Section 8, "Budgets", asks that "two reviewers in different pools whose combined reservations exceed `turn_max_estimated_tokens` by one token yield exactly one dispatched and one `budget:turn`, never two dispatched, under a test that makes both `BeginAttempt` transactions race". The engine serialises these transactions in process: it holds its mutex from the scheduler's release through the `BeginAttempt` transaction, and the hub holds its own lock around the store transaction, so two cannot overlap. The interim reading is that "race" means the two reservations are attempted by separate goroutines released together, with the order decided by the engine's own locking and not by the test; no production hook is added to force overlapping transactions, and `TestBudgets_RacingReservations` states the limit of what it shows and logs the dispatch split. A design revision should settle whether the clause should require a journal-level guarantee that holds without the engine lock.

OQ-4 (preset author as reviewer). Section 8, "N and roles", states: "a preset naming the author as reviewer is refused at submit". The config validator (`internal/config/validator.go`) rejects a preset when the roster is loaded (the check that reports `author %q is also in required reviewers`), so the engine refuses to start on such a roster and no submit can ever name the preset. The interim reading is that a preset naming its author as a required reviewer is a configuration error, refused when the roster is loaded, and the engine keeps that behaviour. A design revision should settle whether the clause should read "refused at roster load" instead of "at submit", and whether one invalid preset should stop the engine starting entirely or only make that preset unusable.

OQ-5 (the history `change` field is ambiguous). `v5-delta-conversations-revision-tools.md` section 3, "Context identity", gives each changed path one `change` field "holding `<path> applied|not applied|conflict`". Section 6, "Snapshot", allows a path to contain any byte. A path `a not` with outcome `applied` and a path `a` with outcome `not applied` then have the same payload, `a not applied`, the same canonical bytes and the same `context_hash`, so the history document is not lossless and an approval could bind to either reading. The interim reading is that the payload is written outcome first: one of `applied`, `not applied` or `conflict`, then one space, then the path's bytes unchanged to the end of the field. No outcome followed by a space is a prefix of another, so every payload has exactly one reading and no path is excluded. The outcomes and their meaning are unchanged. A design revision should settle whether to adopt this order or to keep the path first and restrict which paths are allowed.

Resolved: OQ-1 (which Chat Completions response field is a model identity) and OQ-2 (no endpoint profile field enables optional request parameters) were closed by `v4-delta-engine-tui.md` revision 7, section 1, which adopts the conservative readings as design.

## Known but deferred (not open)

These are explicitly deferred by the design, not undecided:

- Deliberate mode and its acceptance items (delta §4, out of scope).
- Streaming (delta §4, out of scope; v3 §3 calls it an additional tested capability).
- CLI route adapters and their startup-isolation probes (delta §3).
- Anthropic native Messages API dialect (delta §2, out of scope for alpha).
- Herdr/cmux outward status reporting (v3 §6; after alpha).
- Odonian handoff of coding turns from inside syndic (v2 §8; milestone D).
