---
key: A-04
title: "A-04: transport contract, completion status, secret scrub, contract test harness"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-01]
---

## Goal

The interface every participant route implements, the shared types, the secret scrub, and a reusable test harness that any adapter must pass. This is the keystone of milestone A; A-05 and A-06 are written against it without interpretation.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 in full. Read it twice. Every bullet under "Contract invariants" maps to something in this task.

## Deliverables

Package `internal/transport`.

1. Types:
   - `CompletionStatus` string enum: `complete`, `truncated`, `refused`, `interrupted`, `error`.
   - `CapabilityRecord{ Endpoint, Model string; AuthMode ("api-key"|"subscription"|"none"|"unknown"); Identity ModelIdentity; ContextBound, OutputBound *int (nil = unknown); Streaming, CancelObservable, UsageAvailable Tri (yes|no|unknown); ToolRestrictions []string; ServerVersion, AdapterVersion string; ObservedAt time.Time; Raw json.RawMessage }`. `Raw` is the unredacted evidence and is marked in a comment as local-only.
   - `ModelIdentity{ Kind ("digest"|"revision"|"unknown"); Value string }`. Constructors refuse to build a `digest` from a model name.
   - `Message{ Role ("system"|"user"|"assistant"); Content string }`.
   - `AttemptEnvelope{ Participant, TurnID, Phase string; Epoch int; AttemptID string; CandidateHash string (may be empty) }`.
   - `CompleteRequest{ Envelope AttemptEnvelope; Messages []Message; MaxOutputTokens int; Deadline time.Time }`.
   - `CompleteResult{ Envelope AttemptEnvelope; RawOutput string; ScrubCount int; Identity ModelIdentity; Status CompletionStatus; NativeFinish string; Usage *Usage; TransportStatus string }`. `Envelope` is echoed from the request by the adapter so the engine can check it.
   - `CancelStatus{ Requested bool; ServerConfirmed Tri }`.
   - `Usage{ Prompt, Completion int }` or nil.
2. Interface:
   ```go
   type Adapter interface {
       Probe(ctx context.Context, endpoint, model string) (CapabilityRecord, error)
       Complete(ctx context.Context, req CompleteRequest) (CompleteResult, error)
       Cancel(ctx context.Context, attemptID string) (CancelStatus, error)
       Usage(ctx context.Context) (*Usage, error) // nil, ErrUsageUnavailable when not exposed
   }
   ```
3. `Scrub(output string, secrets []string) (scrubbed string, count int)`: exact-match substitution of each non-empty secret with the fixed marker `[REDACTED-SECRET]`. Longest secrets first so a secret that is a prefix of another is not left half-replaced. Pure function, no logging.
4. `ErrUsageUnavailable`, `ErrEnvelopeMismatch`, `ErrIdentityMismatch` sentinel errors.
5. `contracttest` subpackage: `func Run(t *testing.T, newAdapter func(t *testing.T, srv Scenario) Adapter)` where `Scenario` describes what the fake server behind the adapter should do. The harness drives these cases and asserts the contract for each:
   - success: status `complete`, envelope echoed unchanged, `ScrubCount` 0;
   - deadline exceeded: status `interrupted`, returns before `Deadline` plus a small grace;
   - malformed body: status `error`, raw body preserved in `RawOutput`;
   - body cut mid-stream (connection reset): status `interrupted`;
   - each native "not finished" reason the scenario can emit maps to `truncated` or `refused`, never `complete`;
   - auth failure (401/403): `TransportStatus` names it, status `error`;
   - 5xx: status `error`, no retry performed by the adapter (count server hits);
   - identity mismatch: scenario reports model `X`, request expected `Y`; result carries observed identity and the harness asserts the engine-side check `ErrIdentityMismatch` fires;
   - redirect to another host: adapter does not follow (server records zero hits on the second host);
   - secret echo: scenario echoes the request's bearer token in the body; `RawOutput` contains `[REDACTED-SECRET]` exactly once and does not contain the token; `ScrubCount == 1`.
   `Scenario` is an interface; A-05 implements it in-process and A-06 implements it with `httptest`.

## Acceptance

- `Scrub` unit tests: no secrets, one secret, overlapping secrets, secret absent, empty secret ignored, count correct.
- `contracttest.Run` compiles and is exercised by at least one trivial in-package adapter stub so the harness itself is tested (the stub may fail cases; the test asserts the harness reports them).
- A test asserts `ModelIdentity` cannot be constructed as `digest` from a bare model name.
- Package comment restates the four invariants from delta section 1 that this package enforces (no hash computation, scrub before persistence, no redirects, no tool execution) and the one it cannot (server-side echo of anything other than resolved secrets).

## Out of scope

Any real HTTP. Any journal write. The engine's use of these types.
