---
key: A-07b
title: "A-07b: evidence: redaction rule, redacted export, raw record writer"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-04a, A-02]
---

## Goal

Split a probe's capability record into what stays on the operator's machine and what may be committed to a public repository. The redaction is an allowlist: the export is built from named fields, never by removing things from the raw record.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 1 (probe paragraph: raw evidence stays local, a redacted public export is produced by a documented rule and says it is redacted) and section 7 (acceptance: a committed probe export contains no secret value and no raw header).

## Deliverables

Package `internal/evidence`. Standard library plus `internal/transport` and `internal/config`. It works on a `transport.CapabilityRecord` value and does not depend on any adapter. Do not change other packages; if something is insufficient, stop and report.

1. `Export` struct with JSON tags, containing only:
   - `redacted` (always true) and `redaction_rule` (always `evidence/v1`);
   - endpoint profile name; declared location, execution and billing; network dependency label;
   - the endpoint host, only when declared location is `local` or `lan`; omitted for `corporate` and `cloud`. Host and port only: no scheme, path, query string or user info;
   - model name as requested;
   - from the record: auth mode, identity, reported model, context and output bounds, the `Tri` capability fields, max-tokens field, finish-reason mapping, over-length outcome, eligibility, latency, tool restrictions, adapter version, observed-at time;
   - secret references by name (`env:NAME` form) for auth and for each extra header.
   It has no field for `Raw`, response headers, response bodies, server version, or the models list.
2. `Redact(record transport.CapabilityRecord, profile config.EndpointProfile) (Export, error)`: builds the export field by field. Returns an error if the record's endpoint is not the profile's name.
3. `WriteRaw(record, dir) (path, error)`: writes the complete record, including `Raw`, as JSON to `<endpoint>-<model>-<UTC timestamp>.raw.json`, creating `dir` with mode 0700 and the file with mode 0600.
4. `WriteExport(export, dir) (path, error)`: writes the export as indented JSON to `<endpoint>-<model>-<UTC timestamp>.json`.
   In both file names, any character outside letters, digits, dot, dash and underscore is replaced with a dash. Neither function overwrites an existing file.
5. `internal/evidence/RULE.md`: the documented redaction rule, in plain language. What is kept, what is dropped, and why for each group (model inventories, infrastructure detail, response headers, corporate host names). It states that the rule is an allowlist and names its version, `evidence/v1`.

## Acceptance

- Guard test: a record whose `Raw` contains a sentinel string inside a header value and inside a body, and whose `ServerVersion` is a second sentinel; after `Redact` and JSON serialization neither sentinel appears. This is the design's acceptance item.
- A `corporate` profile's export contains no part of its base URL host; a `local` profile's export contains the host and nothing after it.
- The serialized export contains `"redacted": true` and the rule version.
- Reflection test: the `Export` type has no field of type `json.RawMessage` and no `map[string][]string`, so a raw blob or header map cannot be added without failing a test.
- `WriteRaw` produces a file with mode 0600; a second write to the same name errors; a model name containing `/` and `:` produces a file name without them.
- `RULE.md` exists and agrees with `Redact` (the reviewer checks by reading both).

## Out of scope

Running a probe. Writing into `docs/evidence/`. The journal. Any network.
