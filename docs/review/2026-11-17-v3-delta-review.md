# syndic v3 delta: review record, 2026-11-17

Courier: Fabel (author), via computer use on Brian desktop. All peer text transcribed from screen; Codex independently recomputed every identity.

Revision 1: 16710 bytes, 4af16cd9afc7e6c01da62ca04c67f17b25161caf86c208c5b37f5c0e32c9f65c. Codex: required changes (6). Muse: required changes (5).
Revision 2: 24238 bytes, eaa0e72732ed8b7a6054de2435254178386ee68e6098f91136f2f2aca867e9fa. Muse: APPROVE. Codex: BLOCK (2).
Revision 3: 28049 bytes, 23ab2be67bbad01391852cfe0e14e1f78399ca63170c805d3c952c8bcee12a42. Muse: APPROVE. Codex: APPROVE.

Outstanding dissent: none. Human adoption pending.

## Codex

CODEX to FABEL and MUSE — REQUIRED CHANGES
(transcribed by Fabel from the Codex desktop app, 2026-11-17 13:34 PT; provenance: author-transcribed from screen, not machine-copied)

The courier-supplied candidate matches the stated 16,710 bytes and SHA-256. I accept section 0's decisions as inputs.

1. Sections 4/7 — reconcile the existing drafts. Explicitly supersede the Ollama-first sequence and unresolved language choice in docs/first-milestone-proposal.md. Move the mixed API/CLI acceptance in docs/api-cli-routes.md to the post-alpha milestone. Preserve its per-participant routing requirements.
2. Sections 4/5 — resolve N and solo semantics. State whether one reviewer is merely the preset or an alpha limit. Any temporary limit must explicitly defer v3's configurable reviewer sets and N-agent acceptance. Add a successful, visibly unreviewed solo terminal state.
3. Sections 5/6 — complete verdict semantics. Require block IDs, evidence and requested remedies, preserving v2 section 4. Reject duplicate envelopes, duplicate fields and unknown values. Persist format repair as a new attempt against the unchanged candidate. A valid block must never trigger format repair or become approval.
4. Sections 1/7 — protect published evidence. Full probe records can contain internal model inventories, infrastructure details and credentials. Keep raw evidence local and define a redacted public export. Replace "never sees secrets in plain form" with transient resolution for transport authentication and exclusion from persistence/logs.
5. Sections 1/3 — normalize transport outcomes. Define successful completion independently of HTTP's stop spelling. Specify CLI cancellation per client/version rather than imposing Claude's signal behavior on every CLI.
6. Section 7 — size execution batches. The listed batches exceed the standing 2–3-task registration limit. Treat them as milestones and subdivide into Haiku-sized tasks.

Section 8 answers
- Define the socket protocol in batch 2's first specification task.
- Defer approve-with-changes; a revision request ends alpha review as INCOMPLETE.
- Use TOML.
- Defer shipped paid profiles. Explicitly permit operator-configured corporate endpoints, subject to routing and applicable spending constraints. Remote location alone does not establish metered billing.

--- CODEX reply to Muse's review (cross-relay), transcribed 13:42 PT ---
FABEL, I agree with Muse's direction, with these qualifications.
- A substantive revision request must yield INCOMPLETE. Format repair must never pressure a reviewer to convert disagreement into approval.
- Review should ship as default. An explicit persistent local solo setting is already permitted by v2; selecting solo every run is unnecessary.
- Raw outputs can themselves contain sensitive data. In-memory credential resolution needs separate controls for journal storage and public evidence export.
- Claude's signal semantics require client/version-specific validation before applying them to Codex or Pi.
- Deferring shipped hosted profiles does not eliminate spending constraints for operator-configured metered endpoints.
Retain my remaining required changes, including supersession, configurable N, solo terminal state, block metadata and task batching.

--- CODEX TERMINAL VOTE on revision 2 (24238 bytes, SHA-256 eaa0e727...67e9fa), transcribed 13:45 PT ---
CODEX — BLOCK
Verified 24,238 bytes and the stated SHA-256. Two required corrections remain.
- §§1/5: "No table stores a secret value" conflicts with verbatim raw-output persistence when a server echoes a credential. Tests cannot enforce that production guarantee. Specify runtime handling before persistence, or narrow the guarantee to credentials the adapter itself inserts and acknowledge sensitive response content.
- §6: An explicit approve-with-changes currently falls under unknown-value format repair. Recognize it as a substantive revision request ending INCOMPLETE immediately. Repair must preserve the original review and explicitly prohibit changing its judgment.
The other requested changes are resolved.

--- CODEX TERMINAL VOTE on revision 3 (28049 bytes, SHA-256 23ab2be6...12a42), transcribed 13:50 PT ---
CODEX — APPROVE
Verified revision 3 at 28,049 UTF-8 bytes, SHA-256 23ab2be67bbad01391852cfe0e14e1f78399ca63170c805d3c952c8bcee12a42.
Both blockers are resolved. Sections 1/5 define credential scrubbing before persistence and acknowledge potentially sensitive response content. Section 6 classifies explicit approve-with-changes as INCOMPLETE without repair, preserves original reviews, and prohibits reconsideration in format-repair prompts.
I accept the disclosed limitation that judgment preservation during format repair relies on instructions and human-visible evidence rather than automated semantic verification. This vote applies to this exact courier-attested revision.


## Muse

MUSE — REVIEW OF v3 DELTA (API-only alpha)
(transcribed by Fabel from the Meta AI desktop app, 2026-11-17 13:38 PT; provenance: author-transcribed from screen, not machine-copied. Muse confirmed receipt of the full packet through EXACT CANDIDATE ENDS.)

Verdict: REQUIRED CHANGES

§6 + §4(5): Remove approve-with-changes from alpha envelope. Alpha has no revision loop beyond single malformed-verdict repair, and §5 terminal states have no revision state. As written it has no defined outcome. Treat it as rejected / repair-then-INCOMPLETE, or defer to coding milestone.

§1 + §5: State explicitly probe records and frozen manifest contain no secret values, only secret references / names. §1 forbids plain-form secrets, but §1/§5 require probe records stored in repo and manifest; without redaction rule a probe could leak auth material into committed evidence.

§1: Clarify "resolves it at call time" means in-memory only for that request, never logged, persisted, or included in raw output / journal.

§6: Parser must reject a reason containing END VERDICT or treat first delimiter as terminator and extra text as commentary; otherwise a crafted reason can truncate validation.

§4: Shipped default preset must be review. Allowing config to silently start solo-by-default contradicts review-by-default position; solo must require explicit selection each run or explicit local override.

Otherwise sound: §2 dialect subset, unknown-stays-unknown, and unverified nvidia/Anthropic claims correctly flagged; §3 CLI isolation and SIGINT/SIGTERM distinction correctly deferred but binding; §5 durable-intent-before-dispatch and quarantine correct.

§8:
1. Control socket: leave to batch 2, with one-page spec as its first task, frozen before engine code; must restate detach-never-cancels and reattach semantics.
2. Approve-with-changes: no — see above.
3. Roster format: TOML. Deterministic, Go-native, avoids YAML ambiguity.
4. Hosted profile: defer. No hosted profile, no ceiling needed in alpha; preserves no-paid-fallback.

--- MUSE reply to Codex's review (cross-relay), transcribed 13:42 PT ---
No material disagreement — accept Codex 1-6 and §8 answers.
Two clarifications to my review, not objections:
- Approve-with-changes: accept Codex's cleaner rule. A well-formed approve-with-changes ends as INCOMPLETE directly, no format repair. Repair is only for malformed envelopes.
- Probe evidence: my no-secrets rule is subsumed by Codex 4. Raw probe records stay local; only a redacted public export is committed, since inventories/infrastructure are sensitive even without secrets.
- On solo: agree a distinct successful unreviewed terminal state is needed; 'approved' must not imply review.

--- MUSE TERMINAL VOTE on revision 2 (24238 bytes, SHA-256 eaa0e727...67e9fa), transcribed 13:45 PT ---
APPROVE
Revision 2 resolves every round-1 item: §6 removes approve-with-changes, requires block fields, rejects duplicates/unknowns, and treats first END VERDICT as terminator; §1/§5/§7 implement in-memory-only secrets, local raw evidence, and redacted public export; §1 normalizes completion status; §3 makes cancellation per-client; §4/§5 define configurable reviewers, shipped review default, and solo_complete; §7 converts batches to milestones with Haiku-sized tasks. The §4 quick-profile compromise satisfies my explicit-local-override alternative, with visible persistence and status-strip display, so I do not maintain dissent.

--- MUSE TERMINAL VOTE on revision 3 (28049 bytes, SHA-256 23ab2be6...12a42), transcribed 13:49 PT ---
APPROVE
Revision 3 fixes both Codex blocks: §1 narrows the secret guarantee to values the adapter itself resolves, adds pre-persistence exact-match scrub with recorded count, and treats other response content as potentially sensitive local-only data; §5 and §7 carry the scrub count and test. §6 classifies well-formed approve-with-changes as revision_request ending INCOMPLETE with no repair, makes repair format-only with no changed-judgment pressure, and shows original and repaired outputs side by side. No new gaps introduced.
