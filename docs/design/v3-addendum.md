# Configurable agents and local model support

Proposal dated October 10, 2026. This addendum extends candidate v2, SHA-256 `4eeb705560325a1b7a63f47cfbe2b9afc5127a33ef5a30f0e8043e7b9a513321`. It governs the specific changes below; all other v2 requirements remain. Peer review of this proposal does not authorize implementation or Odonian task creation. Human adoption remains pending.

## 1. Agents and roles

Support a configurable roster of N named agent instances, including all-local and mixed local/subscription configurations. N counts active logical participants, not vendors, models, OS processes, or concurrent requests. A registered agent need not participate in every turn. No fixed trio or arbitrary maximum of five is built into the protocol; admission is bounded by configured resources and context limits.

Configuration separates participant ID, adapter and endpoint reference, requested model and observed revision, eligible roles, effective sampling parameters, context/output limits, routing policy, and resource pool. Credentials are secret references, excluded from transcripts and exported configuration. Multiple participants may use the same model and endpoint. Each receives its own engine-owned context, with no cross-participant state sharing before the specified exchange phase. Stateless HTTP calls carry explicit message arrays; no conversation-ID feature is assumed. Server-side isolation remains part of the server trust contract.

Freeze a run manifest before dispatch: roster, proposer set P, integrator, required reviewer set R, advisory roles, adapter capabilities, model identities, policy, budgets, and routing destinations. Duplicate participant IDs, an author approving its own candidate, or a missing required role are invalid configurations. Candidate identity includes this manifest and the relevant discussion/evidence. Material configuration or observed model-revision changes create a new run revision and invalidate affected approvals. Model tags are not immutable identities. Record a server-provided digest/revision where available, otherwise identity is unknown; do not manufacture a digest from a model name. Observe available model identity for every attempt and bind the observations to its output. Prefer an immutable model selector and response-bound revision evidence where supported. Otherwise record pre-dispatch and post-completion observations, including observation gaps; matching tag lookups do not prove which weights served the request. A verdict with an observed identity different from the frozen manifest cannot count as approval. If request-specific identity cannot be established, label it unknown/attested under the selected policy instead of claiming immutable execution. Policies can require known identity, but unknown identity is visible rather than silently equated with pinning.

## 2. N-agent protocols

N=1 supports solo, visibly unreviewed. Review and deliberate require at least two distinct instances and at least one final reviewer other than the candidate author. Two-agent deliberation is legal and labeled as having only one non-author final reviewer. Separate instances do not establish statistical independence; record shared model/family provenance.

Review has one author and one or more configured reviewers. Deliberate has at least two proposers: collect blind proposals, deliver complete peer proposals for critique, then deliver complete critiques before integration. The integrator and final reviewers receive the complete proposal/critique transcript and exact candidate. Terminal verdicts go to all participants without acknowledgment recursion. Every declared proposer must complete its proposal and critique; missing participation produces INCOMPLETE.

Presets start small: review uses one author plus one required reviewer; deliberate uses three proposers, one of whom integrates and the other two review. Users may change P and R explicitly. Extra configured participants are never silently demoted to advisers. All required reviewers must approve the exact candidate. No majority vote, timeout quorum, or reduced roster is inferred from N. Future threshold policies require a separate explicit protocol design.

Advisers do not contribute approval votes. Their objections remain visible, with disposition recorded before candidate freeze; a later material objection creates a new revision under v2. Required reviewer blocks retain v2 withdrawal/override rules. Human override is authorization, not a model vote or proof of correctness.

## 3. Local HTTP adapter

Add a first-class OpenAI-compatible HTTP transport. Ollama is the first conformance target, as chosen by the user. Base URL, model, optional endpoint-specific auth reference, and capability profile are configurable. Local inference over an API does not imply paid cloud inference. Subscription transports remain separate; paid fallback is never automatic.

The first dialect is text Chat Completions. Start with a bounded non-streaming request; streaming is an additional tested capability. Responses, tools, images, and server-managed conversations are outside the initial HTTP adapter. The engine supplies explicit role/content messages and supported output bounds. Do not force temperature, stop sequences, schema flags, or other optional fields on incompatible servers. Record effective parameters and unknown defaults. No returned tool call is executed.

Capability evidence is scoped to adapter/server version, model revision where available, and configuration. It covers text roles, response completion and finish reasons, output limits, context behavior, optional streaming, cancellation observability, auth/routing, and usage availability. A successful ordinary answer does not establish all these capabilities. Interrupted, truncated, refused, or malformed required output cannot become an approval. HTTP success or socket EOF alone is not semantic completion.

Verdicts use a strict versioned envelope validated by the engine. Trusted dispatch binds the result to participant, turn, phase, attempt, and candidate; echoed identity fields must match the supplied values. The model is never asked to compute candidate hashes. Preserve raw output. Do not search arbitrary prose with a regex for approval words. A malformed verdict permits a bounded repair request against the unchanged candidate, then INCOMPLETE. JSON-mode support is optional; strict validation is mandatory.

## 4. Routing and locality

Record observed endpoint facts and operator declarations separately: loopback/LAN/remote address, declared execution location, declared cloud routing, and evidence/unknowns. Never label inference verified-local merely because the URL is localhost. A local proxy can forward requests elsewhere.

A local-only profile accepts explicitly configured local execution destinations and rejects declared cloud or unknown execution routing. It applies to the whole data flow: proposer, reviewer, integrator, peer messages, artifacts, exports, logs, and telemetry. Mixed rosters require a policy allowing every destination that will receive the content. The initial HTTP adapter does not follow redirects or silently substitute endpoints; endpoint credentials are scoped to the configured destination. Full-transcript deliberation that violates routing policy is rejected before dispatch, not repaired by silently omitting peer text.

Local-only is a routing policy based on declarations and observed configuration, not proof against a dishonest or misconfigured server. Strong no-egress assurance needs separately enforced server/network isolation. Record that distinction. An Ollama check must establish the selected model is configured for local execution and record cloud settings; it must not assume every model served by a local Ollama endpoint runs locally.

## 5. Budgets and scheduling

N and concurrency are independent. Resource pools group requests sharing an endpoint, GPU, or subscription quota, including multiple endpoints sharing one GPU. Default to one active request per pool; allow explicit overrides. Queueing, model loading when observable, running, waiting for login/permission, and cancellation status are operational states separate from review verdicts.

Whole-turn deadlines include queue time. Per-attempt execution deadlines start at dispatch and cannot extend the turn deadline. Display both. No queued participant is silently skipped to reach approval. Client cancellation records cancellation requested; server termination is confirmed only when the transport supplies evidence. Quarantine late responses by attempt epoch. If server work may still occupy a pool, hold or mark its capacity uncertain until reconciled; do not promise a concurrency ceiling while blindly launching replacements.

Preflight each recipient's context and the aggregate turn budget before dispatch, using configured output bounds and room for instructions, evidence, critiques, and verdicts. Recheck actual payloads at every phase. Exact token counts may be unavailable: use a documented conservative bound and safety margin, disclose estimation, and stop when a supported safe envelope cannot be established. All-proposer exchange has approximately quadratic aggregate text growth; distinct P and R roles reduce some duplication but do not remove the scaling cost.

Never summarize or truncate active-turn peer content to fit. Exceeding a limit preserves the artifact and stops or asks for a separately authorized split/replacement. Marker-echo tests are diagnostic only; returning markers is not proof of complete retention. Known silent-truncation configurations are ineligible for required review until that behavior is disabled and checked. Unobservable provider retention remains honestly labeled under v2's explicit policy for unavailable readback.

## 6. Quality and host integration

Transport eligibility and model quality are separate. Record seeded-defect evaluation results by task category and model/configuration revision, including false approvals and reviewer-added defects. Mark uncalibrated models. Do not invent an accuracy threshold or assume a large/cloud model is better. A user-selected policy may require a specified evaluation threshold. Same-model rosters remain legal without claims of diverse reasoning.

Herdr is the first supported workspace host through an external extension; ordinary-terminal operation remains an acceptance scenario. The separately running engine owns durable state and structured adapter transports. Herdr receives coarse status, not authority to decide review outcomes from terminal text. UI detach or crash does not cancel a turn; engine crash invokes journal recovery, not automatic replay of uncertain effects. Commands and observations use the engine's own control surface. Herdr plugins are not sandboxes; worker capability isolation remains separate.

Artifact handoff, where supported, uses a narrow per-turn directory and frozen/hash-checked files. Treat supplied paths as untrusted and do not read outside the allowed boundary. Manual provenance remains courier-attested.

## 7. First milestone and acceptance

Review the roster/adapter contract first. Then build small fake-transport fixtures and an Ollama conformance probe; run credentialed subscription checks locally under user control without exporting credentials to the fleet. Herdr hosting and subscription probes remain separate work items. Language selection follows the smallest demonstrated integration, not Herdr's implementation language. Fake-adapter engine work can overlap remaining provider probes once the contract is accepted; live release requires the capabilities of its selected adapters, so an all-local text release need not wait for subscription adapters. Repository review and coding still require their stronger isolation gates.

Before calling the first text-review milestone usable, demonstrate:

- N=1 solo; invalid N=1 review; N=2 review and deliberate; N=3 and N=5 role configurations; a larger roster rejected because of an actual configured budget rather than a hardcoded trio.
- Two instances sharing one model/endpoint without cross-participant context; an all-Ollama roster, and later a mixed roster when a subscription adapter passes its gates.
- Missing reviewer, malformed verdict, wrong candidate/epoch, changed model revision, interruption, and known truncation cannot produce approval.
- Complete peer-message delivery, preserved dissent, queue-aware deadlines, cancellation with unknown server completion, and journal crash recovery.
- A localhost endpoint configured for cloud routing is rejected by local-only policy; no redirect, export, or paid fallback bypasses destination policy.
- Detach and reattach through Herdr and an ordinary terminal preserve the same engine state.

Tests are deterministic with fake adapters wherever possible; real Ollama probes record version, model identity, server configuration, and evidence. No model downloads, installations, inference runs, implementation tasks, or board mutations are authorized by this proposal.

## Sources

Checked October 10, 2026. These establish documented interfaces, not a working harness adapter.

- Ollama's [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility) describes a subset API, local-server cloud use, and separate model context configuration.
- LM Studio's [compatible endpoints](https://lmstudio.ai/docs/developer/openai-compat) support the generic endpoint direction; LM Studio is not the first test target.
- Herdr's [plugin contract at the inspected commit](https://github.com/herdrdev/herdr/blob/2563803dca97c040beaf3dc3acdcb5a3221b4238/docs/next/website/src/content/docs/plugins.mdx) separates host services from plugin-owned implementation/state and states that plugins are not sandboxed.
