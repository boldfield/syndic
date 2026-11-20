# syndic

A local chat and coding TUI where every turn is reviewed by independent AI participants before it is final. One participant authors, one or more reviewers approve or block the exact candidate by hash, and the human merges.

## Documentation

- `docs/design/`: the approved design, four layered documents with their SHA-256 identities.
- `docs/evidence/`: redacted endpoint probe exports.

## Design identities

| Document | Bytes | SHA-256 |
|---|---|---|
| `docs/design/v2-candidate.md` | 13003 | `4eeb705560325a1b7a63f47cfbe2b9afc5127a33ef5a30f0e8043e7b9a513321` |
| `docs/design/v3-addendum.md` | 13827 | `c5cac3dfff38bc5953ef17264f6be9977d536ecfc1b96bb2042317f6e584114a` |
| `docs/design/v3-delta-api-alpha.md` | 28049 | `23ab2be67bbad01391852cfe0e14e1f78399ca63170c805d3c952c8bcee12a42` |
| `docs/design/v4-delta-engine-tui.md` | 93851 | `5881352b88087d1eaaaa26eee0d7d1afbd287a01ca9fee43bd630770acf39ba0` |

Check with `shasum -a 256 docs/design/*.md`. The design is the hashed text.

## Development

- `make check` runs formatting checks, linting, and tests. The linter (`make lint`) runs golangci-lint at the pinned version via `go run`; the first run downloads and builds the linter.
- `make lint` runs golangci-lint alone and can be used to check only linting.

## Status

Milestone A (foundations) is complete. See [`docs/evidence/README.md`](docs/evidence/README.md) for redacted endpoint probe exports.

## License

Apache-2.0.
