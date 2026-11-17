---
key: A-01
title: "A-01: repository scaffold, CI and license"
model: haiku
review_models: [opus]
escalate: true
agent_merge: false
track: build
depends_on: []
---

## Goal

Create the Go module skeleton for syndic so that later tasks have a place to put code and a CI that runs their tests. No application logic.

## Design reference

`docs/design/v3-delta-api-alpha.md` section 0 (name, repository, license) and `docs/odonian/milestone-a.md` (package layout).

## Deliverables

1. `go.mod` with module path `github.com/boldfield/syndic` and the Go version currently installed in the environment (do not pin a newer one).
2. Directories with a `doc.go` containing a one-paragraph package comment each: `cmd/syndic`, `cmd/syndicd`, `internal/config`, `internal/journal`, `internal/transport`, `internal/transport/fake`, `internal/transport/openai`, `internal/evidence`.
3. `cmd/syndic/main.go` and `cmd/syndicd/main.go`: each prints its name and version placeholder and exits 0. No flags yet.
4. `LICENSE`: the Apache License 2.0 text, verbatim, as published at https://www.apache.org/licenses/LICENSE-2.0.txt. If the environment cannot fetch it, stop and report rather than typing it from memory.
5. `NOTICE`: one line, `syndic. Copyright 2026 Brian Oldfield.`
6. `README.md`: name, one-sentence description ("a local chat and coding TUI where every turn is reviewed by independent AI participants before it is final"), a Design section linking the three files in `docs/design/` with their SHA-256 values as given in `docs/HANDOFF.md`, and a Status line saying milestone A is in progress. Keep the existing `docs/` tree untouched.
7. `.gitignore`: Go defaults plus `*.sqlite`, `*.db`, `/evidence-raw/`, `.env`.
8. `.github/workflows/ci.yml`: on push and pull request, run `go build ./...`, `go vet ./...`, `go test ./...`, and `golangci-lint` (pin a version). CI must pass on this commit.
9. `Makefile` with targets `build`, `test`, `lint`, `probe` (the last runs `go run ./cmd/syndic probe "$(ENDPOINT)"`; it will fail until A-08 and that is fine).

## Acceptance

- `go build ./...`, `go vet ./...`, `go test ./...` all exit 0 on a clean checkout.
- CI workflow runs the same and is green.
- `go run ./cmd/syndic` and `go run ./cmd/syndicd` each exit 0 with one line of output.
- `LICENSE` is byte-identical to the Apache 2.0 text from apache.org.
- No file under `docs/` is modified.

## Out of scope

Any dependency beyond the standard library and golangci-lint. Any application code. Any change to the design documents.
