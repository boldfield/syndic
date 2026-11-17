# syndic

A local chat and coding TUI where every turn is reviewed by independent AI participants before it is final. One participant authors, one or more reviewers approve or block the exact candidate by hash, and the human merges.

This repository currently holds design documentation only. Code arrives through Odonian tasks; see `docs/HANDOFF.md`.

## Start here

- `docs/HANDOFF.md`: what this is, what has been decided, what to do next. Read first.
- `docs/design/`: the approved design, three layered documents with their voted SHA-256 identities.
- `docs/odonian/`: Odonian Project and Document payloads and the milestone A task specs, ready to register in batches.
- `docs/review/`: the peer review record for the current design revision.
- `docs/evidence/`: redacted endpoint probe exports, added by the operator after milestone A.

## Design identities

| Document | Bytes | SHA-256 |
|---|---|---|
| `docs/design/v2-candidate.md` | 13003 | `4eeb705560325a1b7a63f47cfbe2b9afc5127a33ef5a30f0e8043e7b9a513321` |
| `docs/design/v3-addendum.md` | 13827 | `c5cac3dfff38bc5953ef17264f6be9977d536ecfc1b96bb2042317f6e584114a` |
| `docs/design/v3-delta-api-alpha.md` | 28049 | `23ab2be67bbad01391852cfe0e14e1f78399ca63170c805d3c952c8bcee12a42` |

Check with `shasum -a 256 docs/design/*.md`. The design is the hashed text.

## Status

Design approved November 17, 2026. Milestone A (foundations) specified; Odonian project and design document registered, tasks not yet. No code.

## License

Apache-2.0. `LICENSE` and `NOTICE` are added by task A-01.
