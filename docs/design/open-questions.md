# Open questions

Questions the design documents do not settle, found during implementation. The CLI Fable or a task worker adds an entry here and stops; Brian takes it to the peer discussion. Entries are removed when a design revision resolves them, with the revision noted.

OQ-3 (racing reservations in Budgets). Section 8, "Budgets", asks that "two reviewers in different pools whose combined reservations exceed `turn_max_estimated_tokens` by one token yield exactly one dispatched and one `budget:turn`, never two dispatched, under a test that makes both `BeginAttempt` transactions race". The engine serialises these transactions in process: it holds its mutex from the scheduler's release through the `BeginAttempt` transaction, and the hub holds its own lock around the store transaction, so two cannot overlap. The interim reading is that "race" means the two reservations are attempted by separate goroutines released together, with the order decided by the engine's own locking and not by the test; no production hook is added to force overlapping transactions, and `TestBudgets_RacingReservations` states the limit of what it shows and logs the dispatch split. A design revision should settle whether the clause should require a journal-level guarantee that holds without the engine lock.

Resolved: OQ-1 (which Chat Completions response field is a model identity) and OQ-2 (no endpoint profile field enables optional request parameters) were closed by `v4-delta-engine-tui.md` revision 7, section 1, which adopts the conservative readings as design.

## Known but deferred (not open)

These are explicitly deferred by the design, not undecided:

- Deliberate mode and its acceptance items (delta §4, out of scope).
- Streaming (delta §4, out of scope; v3 §3 calls it an additional tested capability).
- CLI route adapters and their startup-isolation probes (delta §3).
- Anthropic native Messages API dialect (delta §2, out of scope for alpha).
- Herdr/cmux outward status reporting (v3 §6; after alpha).
- Odonian handoff of coding turns from inside syndic (v2 §8; milestone D).
