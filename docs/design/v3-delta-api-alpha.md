# v3 delta: participant transport contract and API-only alpha

Revision 3, dated November 17, 2026. Author: Fabel. This delta extends candidate v2 (SHA-256 `4eeb705560325a1b7a63f47cfbe2b9afc5127a33ef5a30f0e8043e7b9a513321`) and the revised v3 addendum (SHA-256 `c5cac3dfff38bc5953ef17264f6be9977d536ecfc1b96bb2042317f6e584114a`). It governs only the changes below; every other v2 and v3 requirement stands. Revision 1 (SHA-256 `4af16cd9…9f65c`) received required changes from Codex and Muse on November 17; revision 2 (SHA-256 `eaa0e727…67e9fa`) was approved by Muse and blocked by Codex on two points; section 9 lists how each was resolved. Peer review of this delta does not by itself authorize implementation. Brian has stated that, once this delta is adopted, implementation proceeds through Odonian under the standing workflow: Document first, Haiku-sized tasks with escalation, independent review, human merge.

## 0. Decisions already taken by the user

These are inputs to this delta, not proposals for review.

- The API-driven interface is built first. The alpha supports API routes only.
- CLI routes (Codex, Claude Code, Pi) remain a requirement and arrive after the alpha, behind the same abstraction, so that either route can serve any model.
- The API route must support OpenAI-compatible endpoints. Two intended endpoints are a local inference host (the Spark, serving several models) and inference.nvidia.com, an internal NVIDIA gateway that Brian believes exposes its models through the standard OpenAI-compatible interface. That belief is unverified until the conformance probe runs against it.
- Implementation language and TUI framework: Go with Bubble Tea.
- The project is named syndic. The code lives in a public GitHub repository under Brian's account, `github.com/boldfield/syndic`, licensed Apache-2.0. The first scaffold task includes LICENSE, NOTICE and a README stating the v2/v3 design lineage.

## 1. Participant transport contract

A participant is `{id, model, route, endpoint or binary, eligible roles, pool, routing policy}`. The route is `api` or `cli`. The engine never branches on the route after dispatch. Everything above the contract, including the verdict envelope, candidate hash binding, INCOMPLETE semantics, deadlines, epochs and the journal, is identical for both routes. Claude via an API key and Claude via `claude -p` are two routes for one model, and a roster may mix them.

Every route implements four operations.

**probe(endpoint, model) -> capability record.** Returns: auth and billing mode as observed (api-key, subscription, none, unknown); model identity (server-provided digest or revision where exposed, otherwise unknown; never manufactured from the model name); context and output bounds as reported or unknown; streaming support; cancellation observability; tool restrictions in effect; usage reporting availability; the exact server and adapter versions; and the timestamp. Unknowns stay unknown. A probe record is evidence scoped to that endpoint, model and version. It is stored in the journal and is part of the frozen run manifest. Probe records and manifests contain secret references by name only, never secret values. Raw probe evidence, which may include model inventories, infrastructure details and response headers, stays local; what is committed to the repository is a redacted public export produced by a documented redaction rule, and the export states that it is redacted.

**complete(attempt) -> result.** Input: an engine-owned message array (system, user and assistant roles only in the alpha), the attempt envelope (participant, turn, phase, attempt epoch, candidate hash where a candidate is under review), output bounds, and the per-attempt deadline. Output: raw text exactly as returned, the observed model identity for this attempt where the server exposes it, a normalized completion status, the transport's native finish reason preserved as data, and usage where available. The call is stateless from the engine's point of view. Any provider-side session or conversation ID is the adapter's cache and never the source of truth.

Normalized completion status is one of `complete`, `truncated` (output bound reached), `refused` (content or policy refusal signaled by the transport), `interrupted` (connection lost or cancelled before the end), or `error`. The adapter maps each transport's native vocabulary to these; `stop` is one dialect's spelling of `complete`, not the definition. Only `complete` can carry a verdict that counts. A result with a missing or mismatched attempt envelope, an observed identity different from the frozen manifest, or any status other than `complete` cannot become an approval.

**cancel(attempt) -> status.** Records cancellation requested. Server-side termination is confirmed only when the transport supplies evidence; otherwise the attempt is marked unknown and its pool capacity is held as uncertain per v3 section 5. Late output is quarantined by epoch.

**usage() -> record or unavailable.** Optional. Reported honestly as unavailable when the endpoint does not expose it.

Contract invariants that apply to every route:

- The adapter never computes candidate hashes.
- Secrets the adapter inserts: the adapter receives a secret reference and resolves it in memory for the duration of one transport request, for transport authentication only. The adapter never writes the resolved value into anything it produces: not logs, raw output it constructs, probe records, the manifest, or any exported file, and it drops the value when the request completes. This is the guarantee the engine can make, because it controls these outputs.
- Secrets a server echoes: a response body or header may contain a credential the server chose to repeat, or other sensitive content. The engine cannot prevent that and does not claim to. Before persistence, the adapter scrubs the response of every secret value it resolved for that request, by exact-match substitution with a fixed marker, and records in the attempt row that scrubbing ran and how many substitutions it made. Beyond the adapter's own resolved values, response content is treated as potentially sensitive: it stays in the local journal, is never exported, and is never committed. The contract test for this case verifies the scrub, not a production guarantee of secret-free storage.
- The adapter never follows redirects to a different host, never substitutes an endpoint, and never changes billing mode or route to recover from an error. A constraint that cannot be verified means no dispatch.
- The adapter never executes a returned tool call. In the alpha the request carries no tool definitions.
- Raw output is preserved before any parsing, with exactly one transformation applied: the secret scrub above. The journal stores the scrubbed output as the attempt's raw output, together with the scrub count, so the record is honest about what was changed. The journal is local and never committed; the public evidence export in section 7 never includes raw output, only the redacted probe export. Journal storage and public export are separate controls.
- Each adapter ships with a contract test suite run against a fake endpoint, covering: success, timeout, malformed body, truncated body, each non-complete status, auth failure, 5xx, connection reset mid-body, identity mismatch, and a fake server that echoes the request credential (the test asserts the persisted output contains the marker and not the value, and that the scrub count is one).

## 2. OpenAI-compatible route: endpoint profiles

One adapter serves every OpenAI-compatible endpoint. Differences between endpoints live in an endpoint profile plus the probe record, never in code paths per vendor.

Endpoint profile fields: `name`, `base_url`, `auth` (secret reference, or none), `extra_headers` (names only in logs; values are secret references), `declared_location` (local, lan, corporate, cloud), `declared_execution` (local, remote, unknown), `declared_billing` (none, flat, metered, unknown; declared by the operator, not inferred from location), `network_dependency` (none, vpn, other, as a label), `dialect` (`chat_completions_v1` is the only value in the alpha), `max_tokens_field` (`max_tokens` or `max_completion_tokens`, set by the probe), `models` (optional allowlist), and `terms_note` (free text).

Dialect subset used by the alpha: `POST {base_url}/chat/completions` with `model`, `messages` (system, user, assistant), the probed max-tokens field, and `stream: false`. Optional parameters (temperature, stop, response_format, seed) are sent only when the profile enables them and the probe confirmed acceptance. `GET {base_url}/models` is used for discovery only; a listed tag is not an identity.

The probe records, per endpoint and model: whether `/models` exists and what it returns; which max-tokens field is accepted; whether `usage` is returned; the finish-reason vocabulary observed and its mapping to normalized status; whether `system` role is honored; whether an over-length prompt errors or is silently truncated (diagnostic marker test plus any documented behavior; silent truncation makes the participant ineligible for a required reviewer role per v3 section 5); and response latency at a fixed small prompt, for pool scheduling.

Operational states added for this route: `unreachable` (connection refused, DNS failure, or a network dependency not met), `unauthorized`, and `model_unavailable`. These are participant status, not verdicts, and they produce INCOMPLETE only if a required participant remains in one of them at the deadline.

Routing policy: an endpoint with `declared_location` of corporate or cloud is a remote destination. A local-only profile rejects it. A mixed profile must name it explicitly. The engine sends full peer transcripts to every participant in a phase, so every endpoint in a roster receives every other participant's text; the policy check runs against the whole roster before dispatch, as v3 section 4 requires. Operator-configured corporate endpoints are permitted when the roster's routing policy names them. Remote location alone does not establish metered billing; `declared_billing` does, and a `metered` endpoint is subject to the spending ceiling in v2 section 6 before it can be dispatched. Using an employer-provided gateway for a personal project is a terms question for the operator, outside this design, recorded in `terms_note`.

Anthropic's native Messages API is a different dialect and is out of scope for the alpha. Claude participates in the alpha only through an OpenAI-compatible gateway. Whether Anthropic exposes its own compatible `chat/completions` endpoint is unverified in this delta and should be checked before any such profile is written.

## 3. CLI route: obligations written now, implemented later

The CLI route is not in the alpha, but its obligations are fixed here so the contract does not assume a one-shot HTTP lifecycle.

- `probe` must launch the binary in a clean controlled working directory with sanitized configuration, per v2 section 8, and confirm that no repository hooks, plugins or MCP servers load. For Claude Code this means an explicitly pinned non-bare configuration that preserves subscription login, failing closed if the observed billing mode is not the expected one. Codex App Server and Pi each need their own startup-isolation probe before holding any role; Pi has had none and is not assumed equivalent to the other two.
- `complete` must deliver the full message array through the binary's documented machine interface (stream-JSON, App Server protocol, or equivalent), never through terminal paste, and must bind the returned text to the attempt envelope exactly as the API route does, mapping the client's native outcome to the normalized completion status in section 1.
- `cancel` semantics are specified per client and per pinned version in that client's adapter, from its documentation and a contract test, and recorded in the probe. For Claude Code, the headless documentation checked on October 8, 2026 distinguishes SIGINT (turn ended, result recorded) from SIGTERM (turn abandoned, no result). That is evidence for one client, not a rule for all of them.
- A CLI participant used as a reviewer runs with no write or exec tools. A CLI participant used as an author in a coding turn runs only inside the execution backend's isolated checkout, which remains out of scope until the coding milestone.

## 4. Alpha scope

In scope:

1. Config: a TOML roster file with participants, endpoint profiles, and two presets, `solo` and `review`. Secrets are references to environment variables or a local secret store, never literal values in the file.
2. One OpenAI-compatible adapter with `probe`, `complete`, `cancel` and `usage`, non-streaming.
3. Engine as a separate long-running process with a local control socket. The TUI attaches to it. Detach or TUI crash never cancels a turn. The socket protocol is specified in a one-page document as the first task of the engine milestone, frozen before engine code is written; it must state detach-never-cancels and reattach semantics.
4. SQLite journal (section 5) and append-only event log.
5. Review protocol: one author and a configurable set of required reviewers, with the shipped `review` preset at one reviewer. One reviewer is the preset, not an alpha limit; the engine supports v3's configurable reviewer sets from the start, and the N-agent acceptance items in v3 section 7 that concern review (N=2 review, N=3 and N=5 role configurations, oversize roster rejected by budget) are alpha acceptance items. Deliberate-mode acceptance items are deferred with deliberate mode. Verdicts use the envelope in section 6. INCOMPLETE on deadline, missing reviewer, hash mismatch, identity mismatch, non-complete status, unreachable participant, or a second malformed verdict. Human override journaled as HUMAN OVERRIDE.
6. Solo as an explicitly selected mode, visibly unreviewed, with its own terminal state (section 5). No output scanning in the alpha.
7. Deadlines: per-attempt and whole-turn, both displayed; whole-turn includes queue time. One active request per pool by default.
8. TUI: input, reviewed-answer pane, status strip (mode, phase, elapsed, each participant's operational state), transcript tab with full peer messages and dissent, and commands `/mode`, `/participants`, `/resume`, `/cancel`, `/why`.
9. Tests: a fake adapter driving every negative case in v2 section 10 and section 1 above, run in CI before any real endpoint is used.

Out of scope for the alpha, unchanged in design: deliberate mode, output-checked solo, streaming, coding and worktrees, Herdr or cmux integration beyond running the TUI inside a pane, the CLI route, shipped hosted-API profiles (none ships in the alpha; an operator may configure a `metered` endpoint, which then requires the v2 section 6 ceiling), and Odonian handoff.

Default mode: the shipped default preset is `review`. Solo is entered by explicit `/mode solo` for a session, or by the per-project quick profile from v2 section 11, which is selected once per project, persists visibly, and is shown in the status strip on every turn it governs. No global config key silently makes solo the default. The status strip always shows which mode a turn ran in.

## 5. Journal schema minimum

Tables, with the invariants they carry. Exact DDL is a task deliverable. No table stores a secret value that the adapter itself resolved; manifests and probe records hold references only, and raw output is scrubbed of the request's resolved values before it is written. Response content beyond that is stored as potentially sensitive, local-only data.

- `runs`: run id, created time, frozen manifest (JSON: roster, roles, presets, probe records with secret references only, policy, budgets, destinations), manifest hash. Immutable after dispatch; a change creates a new run revision.
- `turns`: turn id, run id, mode, whole-turn deadline, terminal state. Terminal states: `approved` (review: all required reviewers approved the exact candidate), `solo_complete` (solo: author's answer delivered, visibly unreviewed, no approval claimed), `incomplete`, `overridden`, `cancelled`.
- `messages`: message id, turn id, phase, sender, recipient set, sequence, content, engine-computed SHA-256, byte length, parent ids. Immutable. Identity for replay is message id plus sender and phase; identical content from different senders is legal.
- `attempts`: attempt id, turn id, participant, phase, epoch, dispatched at, deadline, raw output (scrubbed per section 1), scrub count, observed model identity, normalized completion status, native finish reason, transport status, cancellation state, `repair_of` (attempt id when this attempt is the format-repair request for an earlier malformed verdict). Output from an epoch that is not the current one is stored and marked quarantined.
- `candidates`: candidate id, turn id, content, SHA-256 over content plus manifest hash plus evidence ids. Approvals bind to this hash.
- `verdicts`: verdict id, attempt id, candidate hash echoed by the model, parsed verdict (`approve` or `block`), reason, and for a block: block id, severity, evidence, requested remedy; validation status (`valid`, `rejected`, `malformed`, `revision_request`). A verdict whose echoed candidate hash or attempt envelope does not match is stored with validation status `rejected` and never counts. Only `valid` counts toward approval.
- `blocks`: block id, verdict id, candidate hash, severity, evidence, remedy, state (`open`, `withdrawn` against a named new candidate hash, `overridden`). Blocks persist across candidate revisions per v2 section 4.
- `events`: append-only operational log (dispatch, state changes, deadline expiry, cancellation requested, cancellation confirmed, override).

Durable intent precedes dispatch: the `attempts` row is committed before the HTTP request is sent, and the completion is committed after the response is fully read. A format-repair request is a new `attempts` row with `repair_of` set, against the unchanged candidate hash. Crash recovery reads attempts with no completion and marks them unknown; it never re-sends automatically.

## 6. Verdict envelope v1

The reviewer is asked to return exactly one block:

```
VERDICT v1
candidate: <hash supplied by the engine>
attempt: <attempt id supplied by the engine>
verdict: approve | block
severity: low | medium | high        (block only)
block_id: <reviewer-chosen short id>  (block only)
evidence: <what in the candidate the block rests on, one or more lines>  (block only)
remedy: <what change would withdraw the block, one or more lines>  (block only)
reason: <free text, one or more lines>
END VERDICT
```

`approve-with-changes` is not a verdict value in the alpha. Under v2 section 4 it is a revision request, and the alpha has no revision loop; a reviewer who wants changes blocks with a remedy, and the turn ends INCOMPLETE with the block visible. It returns with the revision loop in the coding milestone. If a reviewer nonetheless writes `verdict: approve-with-changes` in an otherwise well-formed block, the engine classifies it as a revision request, not as a malformed envelope: the turn ends INCOMPLETE immediately, the block is stored with validation status `revision_request` and shown in the transcript, and no format-repair attempt is sent. Repair is for format faults only.

Format repair never re-opens judgment. The repair request re-sends the unchanged candidate and the envelope format, states that the reviewer's earlier output is preserved in the transcript, and asks only for the same review in the required format. It does not ask the reviewer to reconsider, and it states that a changed judgment is a protocol violation. The engine does not compare the repaired verdict against the original prose, because that would mean parsing prose for approval words, which this section forbids. Instead the original malformed output and the repaired envelope are stored on linked attempt rows and shown side by side in the transcript tab, with the repair marked as such, so a changed judgment is visible to the human. A repaired `approve` therefore counts, but it is never silent.

Parsing rules. The engine finds the first line that is exactly `VERDICT v1` and the first subsequent line that is exactly `END VERDICT`; those delimit the block, and the first `END VERDICT` terminates it regardless of content, so a crafted `reason` cannot extend the block. Everything outside the block is preserved as the reviewer's commentary, shown in the transcript tab, and never parsed for approval words. Within the block: every required field present exactly once; no unknown fields; no unknown values; `candidate` and `attempt` equal to the values the engine supplied; for `block`, all block-only fields present; for `approve`, none of them present. A second `VERDICT v1` line anywhere in the output makes the whole output malformed. A valid block is a complete verdict: it never triggers format repair and never becomes approval.

A missing or malformed block permits one format-repair attempt that re-sends the unchanged candidate and the format; a second malformed result is INCOMPLETE. JSON mode is not used, because the envelope must work on any endpoint that returns plain text.

## 7. First milestone, revised

Gate A from v2 section 10 is reordered. Subscription-auth probes move after the alpha. The first real adapters are API routes.

This delta supersedes the Ollama-first sequence and the open language choice in Codex's `docs/first-milestone-proposal.md`. The mixed API/CLI acceptance test in `docs/api-cli-routes.md` (CLI author with API reviewer, and its mirror) moves to the post-alpha CLI milestone; that document's per-participant routing requirements are retained and are satisfied by sections 1 and 2 here.

The work is three milestones, not three task batches. Each milestone is subdivided into Haiku-sized tasks registered two or three at a time under the standing Odonian limit, serialized where tasks touch the same files, with independent review and human merge per task.

Milestone A, foundations: repository scaffold with Go module, CI and lint; TOML config schema and loader with secret references; journal schema and migrations including the `blocks` table and terminal states; fake adapter implementing the contract with scripted failure modes for every status and negative case; OpenAI-compatible adapter with probe and redacted export; contract test suite for both adapters.

Milestone B, engine: control-socket one-page spec (first task, frozen before engine code); review protocol with configurable reviewer sets; verdict envelope parser with the section 6 rules; deadlines and pools; crash recovery; the negative test suite from section 4 item 9 and the N-configuration acceptance items.

Milestone C, TUI: Bubble Tea TUI, attach and detach, commands, transcript and dissent view, quick-profile display, and the plain-terminal acceptance scenario.

Conformance runs, by Brian on his own machines after milestone A: the Spark endpoint for each model he intends to use, and inference.nvidia.com. Redacted probe exports are committed to the repository as evidence; raw records stay on his machine. The alpha is not called an alpha until at least one real endpoint has a committed redacted probe export and a passing contract test run.

Acceptance for the alpha, in addition to v3 section 7's review-related items: a review turn completes end to end on a real endpoint; a solo turn ends `solo_complete` and is displayed as unreviewed; disconnecting the reviewer mid-turn yields INCOMPLETE, not approval; a reviewer returning prose with the word "approve" and no envelope yields a repair attempt and then INCOMPLETE; a reviewer whose `reason` contains `END VERDICT` is parsed only up to the first delimiter; a reviewer returning `verdict: approve-with-changes` ends the turn INCOMPLETE with no repair sent; a repaired envelope is shown beside the original malformed output; a valid block ends the turn INCOMPLETE with the block visible and open; a fake server that echoes the credential yields persisted output containing the marker, not the value; killing the TUI mid-turn and reattaching shows the turn's final state; killing the engine mid-turn and restarting marks the attempt unknown and does not re-send; a verdict echoing a stale candidate hash is rejected; an endpoint profile declared corporate is rejected by a local-only roster before dispatch; a committed probe export contains no secret value and no raw header.

## 8. Open points resolved

All four open points from revision 1 were answered identically by both reviewers and are adopted: the socket protocol is a one-page spec as milestone B's first task; `approve-with-changes` is removed from the alpha; the roster format is TOML; no hosted profile ships in the alpha, with Codex's refinement that operator-configured corporate endpoints are permitted under routing policy and that billing is declared, not inferred from location.

## 9. Disposition of revision 1 review

Codex 1 (reconcile existing drafts): adopted, section 7. Codex 2 (N and solo semantics): adopted, section 4 item 5 and `solo_complete` in section 5. Codex 3 (verdict semantics): adopted, sections 5 and 6, including block fields, duplicate and unknown rejection, repair as a new attempt, valid block never repaired. Codex 4 (published evidence): adopted, section 1 probe paragraph and secrets invariant. Codex 5 (transport outcomes): adopted, normalized completion status in section 1, per-client cancellation in section 3. Codex 6 (batch sizing): adopted, section 7 milestones.

Muse §6+§4(5) (remove approve-with-changes): adopted, section 6. Muse §1+§5 (no secret values in probe or manifest): adopted, sections 1 and 5. Muse §1 (call-time resolution in memory only): adopted, section 1. Muse §6 (END VERDICT in reason): adopted, section 6 parsing rules. Muse §4 (shipped default is review): adopted in part. The shipped default is `review` and no config key silently flips it. Fabel does not adopt "explicit selection each run": v2 section 11 already settled a per-project quick profile that persists visibly, and per-run re-selection would re-open the bypass-rate concern recorded there. Codex's cross-relay reply concurs. Dissent preserved if Muse maintains it.

Revision 2 terminal votes. Muse: APPROVE, dissent on §4 withdrawn. Codex: BLOCK on two points, both adopted in revision 3. Codex §§1/5 (secret-free storage was over-promised): the guarantee is narrowed to values the adapter itself resolves, a pre-persistence scrub with a recorded count handles server echo, and other response content is acknowledged as potentially sensitive local-only data (section 1 invariants, section 5 preamble and `attempts`). Codex §6 (approve-with-changes misclassified as format fault): a well-formed `approve-with-changes` is now a `revision_request` ending INCOMPLETE with no repair; repair is format-only, its prompt prohibits changed judgment, and original and repaired outputs are shown side by side rather than semantically compared by the engine (section 6, `verdicts` statuses).

Cross-relay qualifications from Codex (all five adopted): revision request yields INCOMPLETE and repair never pressures a reviewer (section 6); review ships as default with a persistent local solo setting (section 4); raw output is journal-only and separate from public export (section 1 invariants); per-client cancellation validation (section 3); metered operator endpoints keep the spending ceiling (section 2). Muse's cross-relay clarifications are consistent with the above and need no further change.

## Sources

This delta introduces no new external claims. Provider behavior it relies on was checked on October 8 and October 10, 2026, and is cited in v2 section 9 and the v3 addendum. The claim that inference.nvidia.com exposes a standard OpenAI-compatible interface is Brian's and is unverified until probed.
