# Decisions log

One line per decision, with who made it and where it is recorded. Design decisions by the peers are hash-bound in the documents; product decisions are Brian's.

| Date | Decision | By | Recorded in |
|---|---|---|---|
| 2026-10-08 | Review and deliberate are separate protocols; review is the default | peers | v2 §1–2 |
| 2026-10-08 | Approvals bind to candidate hash; any mutation voids them; blocks persist until withdrawn or overridden | peers | v2 §4 |
| 2026-10-08 | Missing reviewer, timeout, crash, parser failure → INCOMPLETE; never quorum, never paid fallback | peers | v2 §6 |
| 2026-10-08 | Strict default plus explicitly selected per-project quick profile; scanners escalate, never certify | peers, dissent recorded | v2 §5, §11 |
| 2026-10-08 | Browser automation excluded; manual courier is advisory and courier-attested | peers | v2 §9, §11 |
| 2026-10-08 | Clean-room subscription reviewer is a release blocker for CLI adapters | peers | v2 §9, §11 |
| 2026-10-10 | N participants configurable; no fixed trio; proposer and reviewer sets separate | peers | v3 §1–2 |
| 2026-10-10 | OpenAI-compatible HTTP transport is first-class; Ollama first conformance target | Brian, peers | v3 §3 |
| 2026-10-10 | Model identity observed per attempt and bound to output; mismatch cannot approve | peers | v3 §1 |
| 2026-10-10 | Local-only is a routing policy on declarations, not proof of no egress | peers | v3 §4 |
| 2026-11-04 | Herdr first supported host, cmux shortlisted, both optional; plain terminal is an acceptance scenario | peers | terminal assessments; v3 §6 |
| 2026-11-17 | API routes first; CLI routes later behind the same abstraction | Brian | delta §0 |
| 2026-11-17 | Go with Bubble Tea | Brian | delta §0 |
| 2026-11-17 | Project name syndic; public repo `boldfield/syndic`; Apache-2.0 | Brian | delta §0 |
| 2026-11-17 | TOML roster; socket spec is milestone B's first task; no hosted profile ships; `approve-with-changes` removed from alpha | peers | delta §8 |
| 2026-11-17 | Shipped default is review; solo via `/mode` or per-project quick profile; no silent global flip | peers | delta §4 |
| 2026-11-17 | Secret guarantee narrowed to adapter-resolved values; scrub before persistence; raw evidence local, redacted export committed | peers (Codex block) | delta §1, §5 |
| 2026-11-17 | Format repair is format-only; original and repaired outputs shown side by side; engine never semantically compares | peers (Codex block) | delta §6 |
| 2026-11-17 | Design and peer consensus stay in the claude.ai app; implementation handoff to a CLI Fable and Odonian | Brian | HANDOFF.md |
