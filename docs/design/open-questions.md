# Open questions

Questions the design documents do not settle, found during implementation. The CLI Fable or a task worker adds an entry here and stops; Brian takes it to the peer discussion. Entries are removed when a design revision resolves them, with the revision noted.

No questions are open.

Resolved: OQ-1 (which Chat Completions response field is a model identity) and OQ-2 (no endpoint profile field enables optional request parameters) were closed by `v4-delta-engine-tui.md` revision 7, section 1, which adopts the conservative readings as design.

## Known but deferred (not open)

These are explicitly deferred by the design, not undecided:

- Deliberate mode and its acceptance items (delta §4, out of scope).
- Streaming (delta §4, out of scope; v3 §3 calls it an additional tested capability).
- CLI route adapters and their startup-isolation probes (delta §3).
- Anthropic native Messages API dialect (delta §2, out of scope for alpha).
- Herdr/cmux outward status reporting (v3 §6; after alpha).
- Odonian handoff of coding turns from inside syndic (v2 §8; milestone D).
