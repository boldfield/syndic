# Handoff: syndic, from design to Odonian

Written November 17, 2026 by Fabel (Claude, in the claude.ai desktop app) for a CLI Fable instance that will carry this project into Odonian. You have no memory of the design sessions. Everything you need is in this directory. Read this file, then `docs/design/v3-delta-api-alpha.md`, then `docs/odonian/`.

## What syndic is

A local, personal chat and coding TUI in which every turn goes through a review protocol across several AI participants before the answer is final. One participant authors, one or more independent reviewers approve or block the exact candidate by hash, and the human merges. The design is the product of a three-way discussion between Codex (OpenAI), Muse (Meta) and Fabel (Claude), with Brian as the human owner, held October 8 through November 17, 2026.

The name is from Le Guin's Anarres, where a syndic acts on behalf of a syndicate. Odonian, Brian's task board, is from the same book.

## How Brian works, and what is yours to do

Brian keeps design and peer consensus in the claude.ai app (this document's author). Implementation goes through Odonian: a Project, a Document, and Haiku-sized tasks with escalation on, independent review, and human merge. You are the bridge. Your job is to:

1. Create the repository at `github.com/boldfield/syndic` if Brian has not, public, Apache-2.0. Do not push without his say-so.
2. Create the Odonian Project and register the design Document. The payloads are in `docs/odonian/`.
3. Register milestone A tasks two or three at a time, in the batches given in `docs/odonian/milestone-a.md`, each batch only after Brian approves it.
4. Keep the design documents in this directory unchanged. If a design question comes up that the documents do not settle, write it down in `docs/design/open-questions.md` and tell Brian; he will take it back to the peer discussion. Do not resolve design questions on your own and do not let a task do it.

Brian's instruction on November 17 at 13:56 PT was to prepare this handoff so a CLI Fable can "carry on to Odonian". Treat that as direction to proceed with steps 1 and 2 and to prepare step 3. Each task batch registration still needs his explicit approval; that is the standing rule, not a formality.

## The design, in brief

Three documents, each approved by all three peers with hash-bound votes. They layer; a later one governs only what it changes.

- `docs/design/v2-candidate.md` (13003 bytes, SHA-256 `4eeb7055…513321`), October 8. The core: review and deliberate protocols, immutable messages with engine-computed hashes, verdicts bound to candidate hashes, blocks that persist until withdrawn, INCOMPLETE on any missing reviewer, timeout or crash, human override labeled as such, one isolated writer per coding unit, no automatic paid fallback, no browser automation.
- `docs/design/v3-addendum.md` (13827 bytes, SHA-256 `c5cac3df…584114a`), October 10. Configurable N participants, OpenAI-compatible HTTP transport, model identity observed per attempt, local-only routing policy as declaration not proof, resource pools, strict verdict envelope, Herdr as optional host.
- `docs/design/v3-delta-api-alpha.md` (28049 bytes, SHA-256 `23ab2be6…12a42`), November 17, revision 3. The one you will work from. Participant transport contract (`probe`, `complete`, `cancel`, `usage`) with `api` and `cli` routes behind one abstraction; OpenAI-compatible endpoint profiles; API-only alpha scope; journal schema; verdict envelope v1; three milestones; acceptance list. Section 9 records how every peer objection was resolved.

`docs/review/2026-11-17-v3-delta-review.md` is the full transcript of the three review rounds on the delta, including the two Codex blocks on revision 2 that produced revision 3. Read it if you want to know why a clause is worded the way it is.

Recompute the three hashes before you rely on the files. If any differs, stop and tell Brian; the design is the hashed text, not whatever is on disk.

## Decisions already made

These are Brian's, recorded in delta section 0, and are not open:

- Go, with Bubble Tea for the TUI. SQLite via a pure-Go driver (modernc) so the binary is static.
- API routes first. The alpha has no CLI route. The CLI route (Codex App Server, Claude Code headless, Pi) comes after, behind the same transport contract, and its obligations are already written in delta section 3.
- OpenAI-compatible Chat Completions, non-streaming, is the only dialect in the alpha. Target endpoints are Brian's Spark (local inference host, several models) and inference.nvidia.com (his employer's gateway; its compatibility is his belief, unverified until probed).
- TOML for the roster file.
- Repository is public under `boldfield/syndic`, Apache-2.0.
- Shipped default mode is review. Solo is explicit, per session or per project, never a silent global default.
- No hosted paid API profile ships. An operator may configure a metered endpoint; it then needs the v2 spending ceiling.

## What the alpha must prove

From delta section 7, the alpha is not called an alpha until: at least one real endpoint has a committed redacted probe export and a passing contract test run; a review turn completes end to end on it; and every negative case in the acceptance list holds against the fake adapter. The negative cases are the point. A harness that approves when it should not is worse than no harness.

## Milestones

- **A, foundations** (this handoff specifies it fully): scaffold, config, journal, transport contract and scrub, fake adapter, OpenAI-compatible adapter, probe with redacted export, `syndic probe` command.
- **B, engine**: control-socket spec first, then review protocol, verdict parser, deadlines and pools, crash recovery, negative tests. Task specs are written against merged milestone A code, not now.
- **C, TUI**: Bubble Tea, attach and detach, commands, transcript and dissent view, plain-terminal acceptance.

After A merges, Brian runs `syndic probe` against the Spark and inference.nvidia.com on his own machine and commits the redacted exports to `docs/evidence/`. Raw probe records never leave his machine.

## Conventions for tasks

- Every task is Haiku-sized: one or two files, one concern, a test that fails before and passes after. If a spec needs more than that, split it; do not grow it.
- `escalate: true` on every task. `review_models: ["opus"]`. Human merge; `agent_merge` stays false.
- Tasks that touch the same file are serialized with `depends_on`.
- A task never changes a design document. If it cannot be implemented as specified, it stops and reports.
- Tests use the fake adapter and `net/http/httptest`. No task calls a real endpoint. No task needs a secret.
- Commit messages reference the task key, for example `A-04: transport contract and scrub`.

## What is not here

- Codex wrote two earlier drafts in its own workspace, `docs/first-milestone-proposal.md` and `docs/api-cli-routes.md`. The delta supersedes the first and defers the second to the CLI milestone. They are not in this directory; ask Brian if you need them.
- Nothing from the Herdr or cmux evaluations is needed for the alpha. Both are optional hosts; the TUI must work in a plain terminal.
- No code. This directory is documentation only. The scaffold is task A-01.

## Provenance of this handoff

The design files are byte-identical to the texts the peers voted on; Codex independently recomputed each hash from its own copy at vote time. The review record was transcribed by Fabel from the Codex and Meta AI desktop apps during the November 17 session, since Fabel was both author and courier that day; the peers' votes themselves are what they are, but the transcription is one party's. If a quoted peer position looks wrong, the apps still hold the originals.
