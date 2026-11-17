# Open questions

Questions the design documents do not settle, found during implementation. The CLI Fable or a task worker adds an entry here and stops; Brian takes it to the peer discussion. Entries are removed when a design revision resolves them, with the revision noted.

Neither entry below blocks milestone A. Each task takes the conservative reading stated, which stays correct whichever way the question is answered.

### OQ-1: which Chat Completions response field, if any, is a model identity

Raised 2026-11-17 while revising the A-06 spec. Delta §1 says the observed model identity is a "server-provided digest or revision where exposed, otherwise unknown; never manufactured from the model name". For the OpenAI-compatible dialect the design does not say which response field qualifies. The candidates are `model` (a name, often an alias the server resolves) and `system_fingerprint` (a backend-configuration identifier that some servers return, some return as a constant, and some omit).

Why it matters: if `system_fingerprint` counts as a revision, a hosted gateway that serves one model from several backend configurations will produce identity mismatches, and so INCOMPLETE turns, in normal operation. If nothing counts, identity is unknown on every OpenAI-compatible endpoint and the identity-mismatch rule rests entirely on the reported model name.

Conservative reading taken by A-04a, A-06 and A-07a: identity is always `unknown` on this route. The response's `model` value is recorded verbatim as a reported model name, explicitly not an identity, and a result whose reported name differs from the one frozen in the manifest at probe time cannot count toward approval. `system_fingerprint` is preserved in raw output and not interpreted.

### OQ-2: no endpoint profile field enables optional request parameters

Raised 2026-11-17 while revising the A-02 and A-06 specs. Delta §2 says optional parameters (temperature, stop, response_format, seed) "are sent only when the profile enables them and the probe confirmed acceptance", but the endpoint profile field list in the same section has no field that enables them, and the probe paragraph does not list parameter acceptance among the things the probe records.

Conservative reading taken by A-02, A-06 and A-07a: the alpha sends no optional parameters, the profile has no such field, and the probe does not test for them.

## Known but deferred (not open)

These are explicitly deferred by the design, not undecided:

- Control-socket protocol: milestone B's first task, one-page spec, frozen before engine code (delta §4 item 3).
- Deliberate mode and its acceptance items (delta §4, out of scope).
- Streaming (delta §4, out of scope; v3 §3 calls it an additional tested capability).
- CLI route adapters and their startup-isolation probes (delta §3).
- Anthropic native Messages API dialect (delta §2, out of scope for alpha).
- Herdr/cmux outward status reporting (v3 §6; after alpha).
- Odonian handoff of coding turns from inside syndic (v2 §8; milestone D).
