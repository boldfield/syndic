---
key: A-06
title: "A-06: OpenAI-compatible adapter: complete and cancel"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-04, A-02]
---

## Goal

The one HTTP adapter the alpha ships: Chat Completions, non-streaming, parameterized entirely by an endpoint profile. It must pass the A-04 contract harness against an `httptest` server. No vendor-specific code paths.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 2 (dialect subset, endpoint profile fields, finish-reason mapping) and section 1 (secrets, no redirects, scrub). `docs/design/v3-addendum.md` section 3 (do not force optional fields; HTTP success is not semantic completion).

## Deliverables

Package `internal/transport/openai`.

1. `New(profile config.EndpointProfile, resolver config.SecretResolver, opts ...Option) *Adapter` implementing `transport.Adapter` for `Complete`, `Cancel`, `Usage`. `Probe` is a stub returning `ErrNotImplemented` until A-07.
2. `Complete`:
   - Builds `POST {BaseURL}/chat/completions` with `model`, `messages` (roles system, user, assistant only; reject others), `stream: false`, and the max-tokens field named by `profile.MaxTokensField` (if empty, use `max_tokens`; A-07's probe sets the right one). No temperature, stop, seed or response_format unless the profile's optional-parameter allowlist includes them.
   - Resolves `profile.Auth` and each `ExtraHeaders` value through the resolver immediately before the request, holds them only in the request object, and passes the resolved values to `transport.Scrub` on the response. Never logs headers.
   - Uses an `http.Client` whose `CheckRedirect` returns an error for any redirect to a different host, and a per-request context derived from `req.Deadline`.
   - Maps `finish_reason`: `stop` → `complete`; `length` → `truncated`; `content_filter` → `refused`; anything else or missing → `error` with the native value preserved in `NativeFinish`. A context deadline or connection error mid-body → `interrupted`. Non-2xx → `error` with `TransportStatus` = HTTP status and the body preserved (scrubbed) in `RawOutput`.
   - Observed identity: if the response carries `model`, record it as `ModelIdentity{Kind: "revision", Value: model}` only when it differs in form from a bare tag (contains a digest-like or version suffix); otherwise `unknown`. Do not invent a digest. If the profile's expected model is set and the observed value is a different tag, return the result with the identity populated so the engine can raise `ErrIdentityMismatch`; the adapter itself does not fail the call.
   - Echoes `req.Envelope` unchanged into the result.
   - No retries. One request per `Complete`.
3. `Cancel`: cancels the in-flight request's context. `ServerConfirmed` is `unknown` (HTTP gives no evidence of server-side termination). Records nothing else.
4. `Usage`: returns the last response's `usage` block if present, else `ErrUsageUnavailable`.
5. `contracttest.Scenario` implementation backed by `net/http/httptest` so the A-04 harness runs against this adapter, including a second `httptest` server for the redirect case and a handler that echoes the `Authorization` header into the body for the secret-echo case.

## Acceptance

- `contracttest.Run` passes for every case.
- Test: request body contains exactly the fields the dialect subset allows and no others, for a profile with no optional parameters enabled.
- Test: `max_completion_tokens` is used when the profile says so.
- Test: `Authorization` header value never appears in any log output captured during a test (wrap the standard logger).
- Test: a 302 to a second host results in zero requests reaching the second server.
- Test: `finish_reason: "length"` yields `truncated`, and a verdict-shaped body with that status is still `truncated` (status is not inferred from content).

## Out of scope

Streaming. Probe (A-07). Any dialect other than Chat Completions. Retries, backoff or failover. Any real endpoint.
