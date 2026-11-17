---
key: A-02
title: "A-02: config: TOML roster, endpoint profiles, presets, secret references"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: [A-01]
---

## Goal

Load and validate the syndic roster file. The roster names participants, endpoint profiles and presets. Secrets appear only as references.

## Design reference

`docs/design/v3-delta-api-alpha.md` sections 1 (participant fields), 2 (endpoint profile fields), 4 items 1 and the "Default mode" paragraph. `docs/design/v3-addendum.md` section 1 (configuration separates participant ID, endpoint, model, roles, pool, routing policy; invalid configurations).

## Deliverables

Package `internal/config`.

1. Types, with TOML tags:
   - `Roster{ DefaultPreset string; Participants []Participant; Endpoints []EndpointProfile; Presets map[string]Preset; Policy RoutingPolicy }`
   - `Participant{ ID, Model, Route ("api"|"cli"), Endpoint (name, api route) or Binary (path, cli route), EligibleRoles []Role, Pool string }`
   - `EndpointProfile{ Name, BaseURL, Auth SecretRef (optional), ExtraHeaders map[string]SecretRef, DeclaredLocation ("local"|"lan"|"corporate"|"cloud"), DeclaredExecution ("local"|"remote"|"unknown"), DeclaredBilling ("none"|"flat"|"metered"|"unknown"), NetworkDependency string, Dialect ("chat_completions_v1"), MaxTokensField ("max_tokens"|"max_completion_tokens"|""), Models []string, TermsNote string }`
   - `Preset{ Mode ("solo"|"review"), Author string, RequiredReviewers []string }`
   - `RoutingPolicy{ LocalOnly bool; AllowedDestinations []string }`
   - `Role` is `author` or `reviewer`.
   - `SecretRef{ Source ("env"|"file"), Name string }`. A `SecretRef` has a `String()` that prints `env:NAME` or `file:NAME` and never a value.
2. `Load(path string) (*Roster, error)` parsing TOML (use `github.com/BurntSushi/toml` or `github.com/pelletier/go-toml/v2`; pick one and pin it).
3. `(*Roster) Validate() error` returning every violation, not just the first, as a joined error. Violations:
   - duplicate participant ID; duplicate endpoint name;
   - participant references an endpoint or preset that does not exist;
   - `api` participant without endpoint, or `cli` participant without binary;
   - preset whose author is also in its required reviewers;
   - preset with mode `review` and zero required reviewers;
   - preset naming a participant not eligible for that role;
   - `DefaultPreset` is empty or names a preset whose mode is not `review` (delta section 4: the shipped default is review; a roster may define solo presets but may not make one the default);
   - `LocalOnly` true and any participant's endpoint has `DeclaredLocation` corporate or cloud, or `DeclaredExecution` not `local`;
   - unknown enum values anywhere.
4. `SecretResolver` interface `{ Resolve(ref SecretRef) (string, error) }` and one implementation, `EnvFileResolver`, that reads `env` from the process environment and `file` from a path under a configured directory. Resolution results are never cached and never logged.
5. A documented example roster at `internal/config/testdata/example.toml` with two API participants on one local endpoint, one `review` preset (author plus one reviewer), one `solo` preset, and `DefaultPreset = "review"`.

## Acceptance

- Tests load the example roster and `Validate()` returns nil.
- A table-driven test produces each violation above from a minimal bad roster and asserts the error message names the offending ID or field.
- A test asserts that `fmt.Sprintf("%v", roster)` and `SecretRef.String()` never contain a resolved secret value, using a resolver seeded with a sentinel string.
- `go vet` and lint clean.

## Out of scope

Resolving secrets at request time (that is the adapter's job, A-04 and A-06). Any network call. CLI route specifics beyond the `Binary` field.
