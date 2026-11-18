# Evidence Redaction Rule v1

This document specifies the allowlist rule `evidence/v1` for redacting probe capability records.

## Overview

A probe's raw capability record stays on the operator's machine and is never committed to the repository. The public export is built from an explicit allowlist of fields — secrets and infrastructure details are excluded by design, not by removal.

The redaction is an allowlist: the export includes only fields enumerated here. Everything else stays local.

## Fields Included

The public export contains the following fields from the raw record:

- `redacted` (always `true`)
- `redaction_rule` (always `"evidence/v1"`)
- **Endpoint profile fields:**
  - Endpoint name
  - Declared location (local, lan, corporate, cloud)
  - Declared execution (local, remote, unknown)
  - Declared billing (none, flat, metered, unknown)
  - Network dependency label (none, vpn, other)
  - **Host and port** — extracted from the endpoint's base URL, **but only when declared location is `local` or `lan`**. For corporate and cloud locations, the host is omitted entirely. The exported value contains host and port only; scheme, path, query string, and user info are never included.

- **Model information:**
  - Model name as requested in the probe
  - Model identity (server-provided digest or revision, or unknown; never manufactured)
  - Reported model (as returned by the server)

- **Capability flags (all Tri values: yes, no, unknown):**
  - Streaming support
  - Cancellation observability
  - Usage reporting availability
  - Models endpoint exists
  - Model is listed in models endpoint
  - System role honored
  - Eligible for required reviewer role

- **Bounds and configuration:**
  - Context bound (maximum input tokens, or null if unlimited)
  - Output bound (maximum output tokens, or null if unlimited)
  - Max-tokens field name (the field accepted by this endpoint: "max_tokens" or "max_completion_tokens")

- **Response mapping:**
  - Finish reasons and their mapping to normalized completion status
  - Over-length outcome (how the endpoint handles over-length inputs: errored, both_markers_returned, silent_truncation_likely, or not_run)

- **Performance and versioning:**
  - Latency in milliseconds (from a fixed small probe request)
  - Adapter version
  - Tool restrictions in effect

- **Timing:**
  - Observed-at timestamp (UTC)

- **Authentication (by reference):**
  - Auth mode (api-key, subscription, none, unknown)
  - Auth secret reference in `env:NAME` or `file:NAME` form (by reference only, never the resolved value)
  - Extra header secret references in `env:NAME` or `file:NAME` form (by reference only, never the resolved value)

## Fields Excluded

The following fields from the raw record are NOT included in the export:

- **Raw probe evidence** (`Raw` field): The complete unredacted probe evidence including server responses, model listings, and infrastructure detail. This stays local only.

- **Response headers** (`ServerVersion` and others): Response headers may contain infrastructure detail and are never exported.

- **Response bodies**: Raw response content beyond what is explicitly captured in normalized fields.

- **Models list** (`ModelsEndpoint` returns `yes` or `no`, but the actual list of models is not included).

## Why

- **Secrets and credentials**: Secret values are never included; only references to environment variables or files are exported.

- **Infrastructure detail**: Host names for corporate and cloud endpoints, response header values, and server version strings are sensitive infrastructure information that may reveal internal details about the operator's setup or the endpoint's configuration. They stay local.

- **Response bodies and raw evidence**: The raw probe response may contain sensitive content and is not suitable for public repository commit.

- **Model inventories**: The models list is infrastructure detail and stays local; only whether the endpoint supports model discovery is exported.

- **Host name control by location**: The redaction rule treats local and LAN endpoints differently from corporate and cloud endpoints. Local and LAN endpoints are assumed to be under the operator's direct control, so their host names and ports are safe to export. Corporate and cloud endpoints may be shared or multi-tenant infrastructure, so their host names are excluded.

## Implementation

The `Redact` function in `internal/evidence` implements this rule field-by-field. The function validates that the record's endpoint matches the profile's name and then builds the export by selecting only the fields listed above. The resulting export is a faithful representation of what can be safely committed.
