---
key: A-02
title: "A-02: config: TOML roster, endpoint profiles, presets, secret references"
model: haiku
review_models: [opus]
escalate: true
agent_merge: true
track: build
depends_on: [A-01]
---

## Goal

Load and validate the syndic roster file. The roster names participants, endpoint profiles and presets. Secrets appear only as references.

## Design reference

`docs/design/v3-delta-api-alpha.md` sections 1 (participant fields), 2 (endpoint profile fields and the "Routing policy" paragraph), 4 item 1 and the "Default mode" paragraph. `docs/design/v3-addendum.md` sections 1 (invalid configurations) and 4 (local-only rejects declared cloud or unknown execution routing).

## Deliverables

Package `internal/config`. This is the only milestone A task besides A-03a that adds a module dependency.

1. Types, with TOML tags:
   - `Roster{ DefaultPreset string; Participants []Participant; Endpoints []EndpointProfile; Presets map[string]Preset; Policy RoutingPolicy }`
   - `Participant{ ID, Model, Route ("api"|"cli"), Endpoint (endpoint profile name, api route), Binary (path, cli route), EligibleRoles []Role, Pool string }`
   - `EndpointProfile{ Name, BaseURL, Auth SecretRef (optional), ExtraHeaders map[string]SecretRef, DeclaredLocation ("local"|"lan"|"corporate"|"cloud"), DeclaredExecution ("local"|"remote"|"unknown"), DeclaredBilling ("none"|"flat"|"metered"|"unknown"), NetworkDependency ("none"|"vpn"|"other"), Dialect ("chat_completions_v1"), MaxTokensField ("max_tokens"|"max_completion_tokens"|""), Models []string, TermsNote string }`. There is no optional-parameter field: the alpha sends no optional request parameters.
   - `Preset{ Mode ("solo"|"review"), Author string, RequiredReviewers []string }`
   - `RoutingPolicy{ LocalOnly bool; AllowedDestinations []string }`. `AllowedDestinations` holds endpoint profile names.
   - `Role` is `author` or `reviewer`.
   - `SecretRef{ Source ("env"|"file"), Name string }`. A `SecretRef` has a `String()` that prints `env:NAME` or `file:NAME` and never a value.
2. `Load(path string) (*Roster, error)` parsing TOML with `github.com/BurntSushi/toml`, pinned. Unknown keys in the file are an error.
3. `(*Roster) Validate() error` returning every violation, not just the first, as a joined error. Violations:
   - duplicate participant ID; duplicate endpoint name;
   - a participant names an endpoint that does not exist; a preset names a participant that does not exist;
   - `api` participant without endpoint, or `cli` participant without binary;
   - a participant whose model is not in its endpoint's `Models` allowlist, when that allowlist is non-empty;
   - preset whose author is also in its required reviewers;
   - preset with mode `review` and zero required reviewers; preset with mode `solo` and any required reviewer;
   - preset naming a participant not eligible for that role;
   - `DefaultPreset` is empty, names a preset that does not exist, or names a preset whose mode is not `review` (delta section 4: the shipped default is review; a roster may define solo presets but may not make one the default);
   - `LocalOnly` true and any participant's endpoint has `DeclaredLocation` corporate or cloud, or `DeclaredExecution` not `local`;
   - `LocalOnly` false and any participant's endpoint has `DeclaredLocation` corporate or cloud without that endpoint's name appearing in `AllowedDestinations` (delta section 2: a mixed profile must name a remote destination explicitly). The check covers every participant in the roster, not only those in the default preset;
   - `AllowedDestinations` names an endpoint that does not exist;
   - any endpoint with `DeclaredBilling` of `metered`. The message says that metered endpoints cannot be dispatched until the spending ceiling exists (delta section 2, v2 section 6) and that no ceiling is implemented in the alpha;
   - unknown enum values anywhere.
4. `SecretResolver` interface `{ Resolve(ref SecretRef) (string, error) }` and one implementation, `EnvFileResolver`, that reads `env` from the process environment and `file` from a file of that name under a configured directory (reject names containing a path separator; trim one trailing newline). An unset variable or missing file is an error naming the reference, never an empty string. Results are never cached and never logged.
5. A documented example roster at `internal/config/testdata/example.toml` with two API participants on one local endpoint, one `review` preset (author plus one reviewer), one `solo` preset, and `DefaultPreset = "review"`.

## Acceptance

- Tests load the example roster and `Validate()` returns nil.
- A table-driven test produces each violation above from a minimal bad roster and asserts the error message names the offending ID or field.
- A test with two violations asserts both appear in the returned error.
- A test asserts that `fmt.Sprintf("%v", roster)` and `SecretRef.String()` never contain a resolved secret value, using a resolver seeded with a sentinel string.
- `go vet` and lint clean.

## Out of scope

Resolving secrets at request time (that is the adapter's job, A-06). Any network call. CLI route specifics beyond the `Binary` field. A spending ceiling.
