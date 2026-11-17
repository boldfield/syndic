---
key: A-07
title: "A-07: endpoint probe, raw evidence record and redacted export"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-06]
---

## Goal

Implement `Probe` for the OpenAI-compatible adapter, producing the capability record the design calls for, and the evidence package that splits what stays on the operator's machine from what may be committed to a public repository.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 (`probe` contract; raw stays local, redacted export is committed, export says it is redacted), section 2 (what the probe records), section 7 (committed export contains no secret value and no raw header). `docs/design/v3-addendum.md` sections 4 and 5 (local-only is a declaration; silent truncation disqualifies a required reviewer; marker test is diagnostic only).

## Deliverables

1. In `internal/transport/openai`, `Probe(ctx, endpoint, model)` performing, in order, each a separate small request with its own timeout:
   - `GET /models`: exists or not; whether `model` is listed; raw list kept in `Raw`.
   - Max-tokens field: send a 1-token request with `max_tokens`; if the server rejects it with an error naming the field, retry once with `max_completion_tokens`. Record which worked. (This is the one place a second request is allowed, and it is a different request, not a retry of the same one.)
   - Usage presence: from the response above.
   - System role: send system "Reply with the single word PING" plus user "hello"; record whether the reply contains PING. Diagnostic, not proof.
   - Finish-reason vocabulary: record the values seen across the probe's requests and their mapping.
   - Silent truncation marker test: build a prompt of repeated filler to roughly 1.5 times the profile's declared context bound (or a documented default when unknown), with a unique marker at the head and another at the tail, ask the model to repeat both markers. Record: errored (good), returned both (inconclusive), returned one or neither (silent truncation likely). Per v3 addendum section 5, "silent truncation likely" sets `EligibleRequiredReviewer: false` in the record. Returning both markers does not set it true; it stays `unknown` unless the server documents its behavior.
   - Latency: wall time of the 1-token request.
   - Observed identity from any response `model` field, per A-06's rule.
   The record's `Raw` field holds every response body and header map, unredacted, for local use.
2. Package `internal/evidence`:
   - `Redact(rec transport.CapabilityRecord) (Export, error)`: produces an `Export` struct that contains the structured fields, the endpoint profile name (not URL host if the profile marks location corporate or cloud; keep the host for local and lan), model name, observed identity, the mapping table, latency, the marker-test outcome, timestamps and versions. It contains none of `Raw`, no header map, no response body, no URL query strings, no secret references' values (names only). The `Export` carries `Redacted: true` and `RedactionRule: "evidence/v1"`.
   - The redaction rule is documented in `internal/evidence/RULE.md` in plain language: what is kept, what is dropped, and why. This file is the "documented redaction rule" the design requires.
   - `WriteRaw(rec, dir)` writes the full record to `dir/<endpoint>-<model>-<timestamp>.raw.json`. `WriteExport(exp, dir)` writes `dir/<endpoint>-<model>-<timestamp>.json`.
3. A guard test in `internal/evidence` that feeds a record whose `Raw` contains a known sentinel token in a header and in a body, runs `Redact`, serializes the export, and asserts the sentinel is absent. This is the acceptance item "a committed probe export contains no secret value and no raw header".

## Acceptance

- Against an `httptest` server scripted for each branch (models present/absent, `max_tokens` rejected, truncating vs erroring on long input), `Probe` returns the expected record fields.
- Export guard test passes.
- `RULE.md` exists and matches what `Redact` does (reviewer checks by reading both).
- No probe step retries a failed request except the documented max-tokens field switch.

## Out of scope

Running against a real endpoint (that is Brian, with A-08). Committing anything to `docs/evidence/`. Deciding eligibility beyond the one rule stated.
