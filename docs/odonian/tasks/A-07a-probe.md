---
key: A-07a
title: "A-07a: OpenAI-compatible endpoint probe"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-06]
---

## Goal

Implement `Probe` for the OpenAI-compatible adapter, producing the capability record the design calls for. The record is evidence about one endpoint and model; unknowns stay unknown.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 (`probe` contract) and section 2 (the paragraph beginning "The probe records"). `docs/design/v3-addendum.md` section 5 (silent truncation disqualifies a required reviewer; the marker test is diagnostic only).

## Deliverables

In `internal/transport/openai`, replacing A-06's stub. Do not change the behaviour of `Complete`, `Cancel` or `Usage`, or any other package; if something is insufficient, stop and report.

`Probe(ctx, endpoint, model)` returns an error if `endpoint` is not the adapter's profile name. Otherwise it performs the steps below in order, each its own HTTP request with its own timeout (option, default 60 seconds), under the same rules as `Complete`: secrets resolved per request, no redirect to another host, no retry. It fills a `transport.CapabilityRecord`.

1. **Models.** `GET {BaseURL}/models`. `ModelsEndpoint` is yes on 2xx with a parseable list, no on 404, unknown otherwise. `ModelListed` is yes or no when the list was obtained, unknown otherwise. A listed name is never used as an identity.
2. **Max-tokens field.** A one-token completion using `max_tokens`. If the server answers 400, send one further request using `max_completion_tokens`. Record the field that worked in `MaxTokensField`; leave it empty if neither did. This second request is a different request, not a retry, and is the only place the probe sends a follow-up.
3. **Reachability and auth, from step 2's outcome.** Connection refused or DNS failure: return `transport.ErrUnreachable`. 401 or 403: return `transport.ErrUnauthorized`. 404: return `transport.ErrModelUnavailable`. In each case also return the partial record gathered so far. `AuthMode` is `api-key` when the profile has `Auth` and the request succeeded, `none` when the profile has no `Auth` and it succeeded, `unknown` otherwise.
4. **Usage.** `UsageAvailable` is yes when step 2's successful response carried a `usage` block, no when it did not.
5. **Latency.** Wall time of step 2's successful request, in `LatencyMillis`.
6. **System role.** System message "Reply with the single word PING", user message "hello". `SystemRoleHonored` is yes when the answer contains PING, no when it does not, unknown when the request failed. Diagnostic, not proof.
7. **Over-length behaviour.** Skipped, with `OverLength` set to `not_run`, when the caller disables it by option. Otherwise: build a prompt with a unique marker at the head, filler, and a second unique marker at the tail, sized to 1.5 times an assumed context length (option in tokens, default 131072, converted at 4 characters per token; the conversion is a documented estimate), and ask for both markers back.
   - server returned an error: `errored`;
   - answer contains both markers: `both_markers_returned`;
   - answer contains one or neither: `silent_truncation_likely`.
8. **Eligibility.** `EligibleRequiredReviewer` is no when `OverLength` is `silent_truncation_likely`, and unknown in every other case. This task never sets it to yes: returning both markers is not proof of retention.
9. **Finish reasons.** `FinishReasons` maps each native `finish_reason` seen in any probe response to the status `Complete` would give it.
10. **Remaining fields.** `Identity` unknown and `ReportedModel` from step 2's response, exactly as `Complete` reports them. `Streaming` unknown (not probed in the alpha). `CancelObservable` no. `ContextBound` and `OutputBound` nil unless the `/models` entry for this model states them in a field the code names explicitly. `ToolRestrictions` states that no tool definitions are sent. `ServerVersion` from a `Server` response header if present, else `unknown`. `AdapterVersion` a package constant. `ObservedAt` the time the probe started.
11. **Raw.** A JSON object holding, per step, the request path, response status, response headers and response body. Before it is stored in the record, the serialized `Raw` is passed through `transport.Scrub` with every secret value resolved during the probe. Request headers are not recorded.

## Acceptance

All against `httptest` servers.

- A fully cooperative server yields a record with every field above set as specified.
- Table test over server behaviours: `/models` 404; `max_tokens` rejected with 400 then `max_completion_tokens` accepted; both rejected; no `usage` block; system role ignored; long input answered with an error; long input answered with only the head marker; over-length step disabled.
- 401 on step 2 returns `transport.ErrUnauthorized`; a closed port returns `transport.ErrUnreachable`; 404 on step 2 returns `transport.ErrModelUnavailable`.
- A server that echoes the bearer token in a response header and body: the serialized `Raw` does not contain the token.
- Request counting shows no step is repeated except the documented max-tokens follow-up.
- The contract harness from A-06 still passes.

## Out of scope

Redaction and file output (A-07b). Running against a real endpoint. Streaming probes. Deciding eligibility beyond the one rule stated.
