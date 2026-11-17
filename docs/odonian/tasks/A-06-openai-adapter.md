---
key: A-06
title: "A-06: OpenAI-compatible adapter: complete, cancel, usage"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-04b, A-02]
---

## Goal

The one HTTP adapter the alpha ships: Chat Completions, non-streaming, parameterized entirely by an endpoint profile. It must pass the contract harness against an `httptest` server. No vendor-specific code paths.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 2 (dialect subset, operational states) and section 1 (secrets, no redirects, scrub, raw output preserved before parsing). `docs/design/v3-addendum.md` section 3 (do not force optional fields; HTTP success is not semantic completion).

## Deliverables

Package `internal/transport/openai`. Standard library plus this repository's packages. Do not change `internal/transport`, `internal/transport/contracttest` or `internal/config`; if any is insufficient, stop and report.

1. A constructor taking a `config.EndpointProfile` and a `config.SecretResolver`, returning an adapter that implements `transport.Adapter`. `Probe` returns `transport.ErrNotImplemented` until A-07a.
2. `Complete` request:
   - `POST {BaseURL}/chat/completions`. The JSON body has exactly four keys: `model` (from the request), `messages`, `stream` set to false, and the max-tokens key named by the profile's `MaxTokensField` (`max_tokens` when that is empty). No temperature, stop, seed, response_format, tools or any other key.
   - A message whose role is not system, user or assistant makes `Complete` return a Go error without sending anything.
   - The profile's `Auth` and each `ExtraHeaders` value are resolved through the resolver immediately before the request and used only to set headers (`Authorization: Bearer <value>` for `Auth`). A resolver error makes `Complete` return a Go error without sending anything. Resolved values are not stored on the adapter and are not logged; header values are never logged.
   - The HTTP client refuses any redirect whose target host differs from the profile's host, and uses a per-request context bounded by the request deadline.
   - Exactly one HTTP request per `Complete`. No retry, backoff or failover.
3. `Complete` result. `RawOutput` is the full response body as received, scrubbed with the values resolved for this request; `ScrubCount` is the number of substitutions in it. `Text` is the first choice's message content, parsed from the unscrubbed body and then scrubbed separately. The envelope is copied from the request. Mapping:

   | Observation | Status | Transport status |
   |---|---|---|
   | 2xx, parseable, `finish_reason` `stop` | `complete` | `ok` |
   | 2xx, parseable, `finish_reason` `length` | `truncated` | `ok` |
   | 2xx, parseable, `finish_reason` `content_filter` | `refused` | `ok` |
   | 2xx, parseable, any other or missing `finish_reason` | `error` | `ok` |
   | 2xx, body not parseable or no choices | `error` | `malformed_body` |
   | body larger than the limit (option, default 16 MiB) | `error` | `body_too_large` |
   | 401 or 403 | `error` | `unauthorized` |
   | 404 | `error` | `model_unavailable` |
   | any other non-2xx | `error` | `http_<code>` |
   | redirect to another host refused | `error` | `http_<code>` of the redirect |
   | connection refused or DNS failure | `error` | `unreachable` |
   | deadline passed before the body was fully read | `interrupted` | `timeout` |
   | cancelled through `Cancel` | `interrupted` | `cancelled` |
   | connection lost mid-body | `interrupted` | `reset` |

   The native `finish_reason` is preserved verbatim in `NativeFinish` whenever the body carried one. Status is never inferred from the content of the answer.
4. Identity. `ReportedModel` is the response's `model` field verbatim, or empty. `Identity` is always unknown in this task: no field of the Chat Completions response is treated as a digest or revision (see `docs/design/open-questions.md`, OQ-1). Do not derive an identity from the model name.
5. `Cancel(attemptID)`: cancels that attempt's in-flight request. `Requested` is true when an in-flight request was found; `ServerConfirmed` is always `unknown`, because HTTP gives no evidence of server-side termination. Safe to call concurrently with `Complete`.
6. `Usage`: the most recent response's `usage` block if it had one, else `transport.ErrUsageUnavailable`. Each `CompleteResult` also carries its own usage when present.
7. A `contracttest.Backend` implementation in this package's test file, backed by `net/http/httptest`: one handler behaviour per case, a second server as the foreign host for `redirect_other_host`, and for `secret_echo` a handler that puts the received bearer token into the answer content.

## Acceptance

- `contracttest.Run` passes for every case.
- The request body for a default profile has exactly the four keys above; with `MaxTokensField` set to `max_completion_tokens` that key is used instead of `max_tokens`.
- A 302 to a second `httptest` server results in zero requests reaching it.
- `finish_reason` `length` with answer content shaped like an approving verdict envelope is still `truncated`.
- A table test covers each row of the mapping table that the contract harness does not (404, missing `finish_reason`, no choices, oversize body, connection refused, cancel).
- With the standard logger redirected to a buffer for the whole test run, the bearer value never appears in it.
- `go test -race` passes for this package.

## Out of scope

Streaming. Probe (A-07a). Optional request parameters. Any dialect other than Chat Completions. Any real endpoint.
