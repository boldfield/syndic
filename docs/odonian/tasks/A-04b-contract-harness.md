---
key: A-04b
title: "A-04b: transport contract test harness"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-04a]
---

## Goal

A reusable test harness that any `transport.Adapter` must pass. It is what makes "this adapter satisfies the contract" a checkable claim. A-05 (fake) and A-06 (HTTP) each plug into it.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1, last invariant (the list of cases every adapter's contract suite covers).

## How it fits together

There are two roles, and they are separate types:

- A **case** is a name for one situation the contract covers. The harness owns the list.
- A **backend** is supplied by the adapter under test. Given a case, it arranges whatever makes that situation happen for its adapter (the fake scripts itself; the HTTP adapter starts an `httptest` server) and hands back a ready adapter plus a few observation hooks.

The harness loops over the cases, asks the backend to arrange each one, sends one standard request through the returned adapter, and asserts the outcome.

## Deliverables

Package `internal/transport/contracttest`. Standard library only. It imports `testing`, so it must only be imported from test files.

1. `Case`: a string type with these constants: `success`, `deadline_exceeded`, `malformed_body`, `connection_reset`, `truncated`, `refused`, `auth_failure`, `server_error`, `identity_mismatch`, `redirect_other_host`, `secret_echo`. `AllCases()` returns them in that order.
2. `Fixture` struct returned by a backend for one case:
   - `Adapter transport.Adapter`;
   - `Secret string`: the secret value the adapter will resolve for this request (used by `secret_echo`);
   - `Expected transport.Expected`: what a correct response would report (used by `identity_mismatch`, where the arranged response reports something else);
   - `PrimaryHits func() int`: requests that reached the configured endpoint;
   - `ForeignHits func() int`: requests that reached any other host (used by `redirect_other_host`).
   Hooks a backend cannot observe are nil only where the table below does not use them.
3. `Backend` interface with one method: given `*testing.T` and a `Case`, return a `Fixture`. Cleanup is registered by the backend with `t.Cleanup`.
4. `Run(t *testing.T, backend Backend)`: one subtest per case. The standard request has a fixed envelope with every field non-empty, model `contract-model`, one system and one user message, a small output bound, and a deadline of 2 seconds (300 milliseconds for `deadline_exceeded`).
5. One exported assertion function per case, taking the request, the result, the fixture and the elapsed time and returning an error. `Run` calls them; they are exported so they can be tested directly. Expected outcomes:

   | Case | Must hold |
   |---|---|
   | every case | `Envelope` equals the request's; `Complete` returned a nil Go error |
   | `success` | status `complete`; transport status `ok`; `ScrubCount` 0; `Text` non-empty; `CheckResult` with the fixture's `Expected` is nil |
   | `deadline_exceeded` | status `interrupted`; transport status `timeout`; returned within the deadline plus 500 milliseconds |
   | `malformed_body` | status `error`; transport status `malformed_body`; `RawOutput` non-empty; `Text` empty |
   | `connection_reset` | status `interrupted`; transport status `reset` |
   | `truncated` | status `truncated`; `NativeFinish` non-empty |
   | `refused` | status `refused`; `NativeFinish` non-empty |
   | `auth_failure` | status `error`; transport status `unauthorized` |
   | `server_error` | status `error`; transport status starts with `http_5`; `PrimaryHits` is exactly 1 (no retry) |
   | `identity_mismatch` | status `complete`; `CheckResult` with the fixture's `Expected` is `ErrIdentityMismatch` |
   | `redirect_other_host` | status `error`; `ForeignHits` is 0 |
   | `secret_echo` | `RawOutput` and `Text` do not contain `Secret`; `RawOutput` contains `ScrubMarker` exactly once; `ScrubCount` is 1 |

   For every case except `success` and `identity_mismatch`, `CheckResult` must be non-nil: none of them may count toward approval.
6. A conforming in-memory stub adapter and backend in this package's own test file (not exported), returning hard-coded results per case, so `Run` is exercised here.

## Acceptance

- `Run` passes against the in-package stub for every case.
- A table test feeds each assertion function a deliberately wrong result (for example `complete` where `truncated` is required, a `ScrubCount` of 0 for `secret_echo`, a `PrimaryHits` of 2 for `server_error`) and asserts it returns an error naming the case.
- `go vet` and lint clean.

## Out of scope

Any real adapter. Any HTTP, including `httptest` (that belongs to A-06's backend). Changes to `internal/transport`; if a type there is insufficient, stop and report.
