---
key: A-04a
title: "A-04a: transport contract: types, adapter interface, secret scrub, result check"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-01]
---

## Goal

The interface every participant route implements, the shared types and vocabulary, the secret scrub, and the engine-side check that decides whether a result may count. Every later transport task is written against this package, so names and values here are exact, not suggestions.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 in full, and the "Operational states" paragraph of section 2. `docs/design/v3-addendum.md` section 1 (model identity is observed, never manufactured from a model name).

## Deliverables

Package `internal/transport`. Standard library only.

1. Vocabulary:
   - `CompletionStatus`: `complete`, `truncated`, `refused`, `interrupted`, `error`.
   - `Tri`: `yes`, `no`, `unknown`.
   - Transport status string constants: `ok`, `unauthorized`, `model_unavailable`, `unreachable`, `timeout`, `reset`, `cancelled`, `malformed_body`, `body_too_large`, and a helper producing `http_<code>` for any other non-2xx response.
   - `ScrubMarker = "[REDACTED-SECRET]"`.
2. Types:
   - `ModelIdentity{ Kind ("digest"|"revision"|"unknown"); Value string }`. Constructors: one for unknown; one each for digest and revision that take the server-provided value and the requested model name, and return an error when the value is empty or equals the requested model name. Comparable with `==`.
   - `Message{ Role ("system"|"user"|"assistant"); Content string }`.
   - `AttemptEnvelope{ Participant, TurnID, Phase string; Epoch int; AttemptID string; CandidateHash string (may be empty) }`. Comparable with `==`.
   - `CompleteRequest{ Envelope; Model string; Messages []Message; MaxOutputTokens int; Deadline time.Time }`.
   - `CompleteResult{ Envelope; RawOutput string; Text string; ScrubCount int; Identity ModelIdentity; ReportedModel string; Status CompletionStatus; NativeFinish string; Usage *Usage; TransportStatus string }`.
     - `RawOutput` is the transport's response exactly as received, with only the scrub applied. It is what the journal stores.
     - `Text` is the participant's answer extracted from it, also scrubbed. Empty when nothing could be extracted.
     - `ScrubCount` counts substitutions made in `RawOutput`.
     - `ReportedModel` is whatever model name the server put in its response, verbatim, or empty. It is a tag, not an identity.
     - `Envelope` is copied from the request by the adapter.
   - `Usage{ Prompt, Completion int }`.
   - `CancelStatus{ Requested bool; ServerConfirmed Tri }`.
   - `CapabilityRecord`: `Endpoint`, `Model`; `AuthMode` (`api-key`, `subscription`, `none`, `unknown`); `Identity`; `ReportedModel`; `ContextBound`, `OutputBound` (pointer, nil means unknown); `Streaming`, `CancelObservable`, `UsageAvailable`, `ModelsEndpoint`, `ModelListed`, `SystemRoleHonored`, `EligibleRequiredReviewer` (all `Tri`); `MaxTokensField` string; `FinishReasons map[string]CompletionStatus` (native value observed to normalized status); `OverLength` (`errored`, `both_markers_returned`, `silent_truncation_likely`, `not_run`); `LatencyMillis` (pointer); `ToolRestrictions []string`; `ServerVersion`, `AdapterVersion` string; `ObservedAt time.Time`; `Raw json.RawMessage`. `Raw` is the unredacted evidence; its doc comment says it is local-only and never exported or committed.
   - `Expected{ Identity ModelIdentity; ReportedModel string }`: what the frozen manifest recorded for this participant at probe time.
3. Interface `Adapter` with four methods, each taking a `context.Context` first:
   - `Probe(endpoint, model string)` returning a `CapabilityRecord`;
   - `Complete(CompleteRequest)` returning a `CompleteResult`;
   - `Cancel(attemptID string)` returning a `CancelStatus`;
   - `Usage()` returning `*Usage`, or `ErrUsageUnavailable` when the endpoint does not expose it.
   `Complete` returns a Go error only for a request the adapter refuses to send (for example a message with an unsupported role). Every transport outcome, including failures, is a `CompleteResult` with the appropriate status.
4. Sentinel errors: `ErrUsageUnavailable`, `ErrNotImplemented`, `ErrEnvelopeMismatch`, `ErrIdentityMismatch`, `ErrStatusNotComplete`, `ErrUnreachable`, `ErrUnauthorized`, `ErrModelUnavailable`.
5. `Scrub(output string, secrets []string) (scrubbed string, count int)`: exact-match substitution of each non-empty secret with `ScrubMarker`, longest secret first so a secret that is a prefix of another is not left half-replaced. `count` is the number of substitutions. Pure function, no logging.
6. `CheckResult(req CompleteRequest, res CompleteResult, expected Expected) error`: the single gate a result must pass before it can carry a verdict that counts. Returns a joined error containing:
   - `ErrEnvelopeMismatch` when `res.Envelope != req.Envelope`;
   - `ErrIdentityMismatch` when `res.Identity != expected.Identity` or `res.ReportedModel != expected.ReportedModel`;
   - `ErrStatusNotComplete` when `res.Status` is not `complete`.
   Nil only when none apply. Unknown identity equals unknown identity; this function does not decide whether unknown is acceptable, which is roster policy.
7. Package comment restating the invariants from delta section 1 that this package enforces or defines (adapters never compute candidate hashes, scrub before persistence, no redirects to another host, no tool execution, no retry or failover) and the one it cannot (a server echoing sensitive content other than the resolved secrets).

## Acceptance

- `Scrub` table test: no secrets, one secret once, one secret three times (count 3), a secret that is a prefix of another, secret absent, empty secret ignored.
- `CheckResult` table test: clean result returns nil; each of the three faults alone returns its sentinel (`errors.Is`); all three together return all three; unknown-versus-unknown identity with equal reported model returns nil; reported model differing while identity is unknown on both sides returns `ErrIdentityMismatch`.
- Identity constructor test: digest and revision constructors reject an empty value and a value equal to the requested model name.
- `go vet` and lint clean.

## Out of scope

The contract test harness (A-04b). Any HTTP. Any journal write. The engine's use of these types.
