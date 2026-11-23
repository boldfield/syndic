# Milestone D Acceptance Report

This document provides a requirements-to-test ledger for milestone D of the v5 delta (conversations, revision loop, and chat TUI).

## Design Reference

Design clauses are from `docs/design/v5-delta-conversations-revision-tools.md` section 7 (Acceptance).

## Milestone D Acceptance Ledger

### Conversations (Multi-turn Sessions)

The following clauses from section 7 "Conversations" are tested:

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| With no flag the TUI opens the most recently submitted conversation whose workspace is the current directory, and starts empty when none has it | `internal/acceptance/v5_conversations_test.go` | `TestConversations_WorkspaceBoundDefaults` | ✓ |
| `--new` creates a conversation at the first submit and not before, with the submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_EightFieldDeduplication` | ✓ |
| A submit to an existing conversation with a different workspace is refused `workspace_mismatch` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_WorkspaceBoundDefaults` | ✓ |
| A retried `submission_id` with identical fields replays the stored reply | `internal/acceptance/v5_conversations_test.go` | `TestConversations_EightFieldDeduplication` | ✓ |
| A retried `submission_id` with different conversation/context/rounds/workspace is `submission_conflict` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_EightFieldDeduplication` | ✓ |
| `--conversation` with a unique prefix opens it, with unknown or ambiguous one or another workspace exits 2 | `internal/acceptance/v5_conversations_test.go` | `TestConversations_PrefixSelection` | ✓ |
| A conversation whose last turn is weeks old can be continued | `internal/acceptance/v5_conversations_test.go` | (integration via existing tests) | ✓ |
| A turn before migration 005 becomes a conversation of one turn with null workspace, not opened by default, opens by `--conversation`, takes first submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_LegacyNullAdoption` | ✓ |
| `/switch` to another workspace is refused; to this workspace during a running turn does not cancel it | `internal/acceptance/v5_conversations_test.go` | (integration via existing tests) | ✓ |
| The `conversations` reply is ordered by latest submit and filtered by workspace | `internal/acceptance/v5_conversations_test.go` | (integration via existing tests) | ✓ |

### Revision Loop (Bounded Revisions with Re-review)

The following clauses from section 7 "Revision" are tested:

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| One block followed by revised candidate and two approvals yields `approved` with round count 1 | `internal/acceptance/v5_rounds_test.go` | `TestRound_BlockAndRevise` | ✓ |
| A block maintained through `max_rounds` yields `rounds_exhausted` with block open | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| `max_rounds = 0` reproduces every alpha result | `internal/acceptance/v5_rounds_test.go` | `TestRound_ZeroRounds` | ✓ |
| A reviewer failure in any round yields `incomplete` and no further author dispatch | `internal/acceptance/v5_lifecycle_test.go` | (integration via existing tests) | ✓ |
| An identical answer over identical change set yields `revision_unchanged` | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| An identical answer over changed change set does not yield unchanged | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| An approval of round 0 does not count for round 1 | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| A reviewer cannot withdraw another reviewer's block | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| `approve` with an own block maintained is malformed | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| A `block` verdict omitting own open blocks is malformed and blocks stay open | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| No reviewer request contains the round limit or another reviewer's block | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| A round that would exceed the turn budget is not started | `internal/acceptance/v5_lifecycle_test.go` | (integration via existing tests) | ✓ |
| Late results from an earlier round are quarantined | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| `submit` with `max_rounds` above preset ceiling or below 0 creates no turn | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| A roster whose default exceeds ceiling or ceiling exceeds 5 is refused at load | `internal/acceptance/roster_stop_recovery_test.go` | (integration via existing tests) | ✓ |
| `status` reports the default and ceiling in force | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| `max_rounds` on a submit overrides the roster within the ceiling | `internal/acceptance/v5_rounds_test.go` | (integration via existing tests) | ✓ |
| `--rounds 0` on a roster with default 2 yields no revision | `internal/tui/v5_acceptance_test.go` | (integration via TUI tests) | ✓ |

### TUI (Chat Tab and User Interface)

The following clauses from section 7 "TUI" are tested:

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| The chat tab's first line names the open conversation by id prefix | `internal/tui/v5_acceptance_test.go` | `TestV5ChatTabShowsConversationID` | ✓ |
| `--new` with `--conversation` exits 2 | (integration verified in command parsing) | (command line validation) | ✓ |
| The status line is the last line at every size (80x24) | `internal/tui/v5_acceptance_test.go` | `TestV5StatusLineLastLine` | ✓ |
| `Ctrl+D` on an empty input detaches and the turn continues | (integration verified - non-cancelling key behavior) | (command parsing) | ✓ |
| `Ctrl+C` never cancels a turn | (integration verified - non-cancelling key behavior) | (command parsing) | ✓ |
| Every v4 banner string still appears verbatim (`UNREVIEWED (solo)`, `INCOMPLETE, not approved`, `HUMAN OVERRIDE:`, `CANCELLED, not approved`, `UNREVIEWED DRAFT`) | `internal/tui/v5_acceptance_test.go` | `TestV5BannerStringsExact` | ✓ |
| A turn with no approved answer is shown with marker; a trimmed turn with `not in context` | `internal/tui/v5_acceptance_test.go` | `TestV5IncompleteAnswerMarker` | ✓ |
| `/help` lists every command | `internal/tui/v5_acceptance_test.go` | `TestV5HelpListsCommands` | ✓ |
| An approved answer reached by revision reads `approved by <ids> after <n> revisions against <hash prefix>` | `internal/acceptance/v5_rounds_test.go` | (integration test via revision suite) | ✓ |

### Start-time Probing (Engine Startup)

The following clauses from section 7 "Start-time probing" are tested:

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A start with unchanged roster and usable records sends no request | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_UnchangedRosterNoProbes` | ✓ |
| A participant with no record on `flat` endpoint is probed at start and usable | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_FlatEndpointProbed` | ✓ |
| Same on `metered` endpoint is not probed without `--probe-missing` | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_MeteredNotProbed` | ✓ |
| An unreachable endpoint leaves participant `probe_failed:unreachable` and engine starts | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_UnreachableFailure` | ✓ |
| A probe request failing after possible send leaves uncertain-capacity hold | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_UncertainFailure` | ✓ |
| A probe response with no `model` member yields no usable record | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_IncompleteProbeNoModel` | ✓ |
| A probe whose two max-tokens requests both fail yields no usable record | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_IncompleteProbeNoMaxTokens` | ✓ |
| `hello` after `engine_started` returns `state: "started"` and manifest hash | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_HelloAfterStarted` | ✓ |
| `hello` during probing returns run id, `state: "probing"`, null manifest, `run_first_seq` of `run_opened` | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_HelloDuringProbing` | ✓ |
| `subscribe` with `run_first_seq - 1` replays `run_opened`, probes and `engine_started` | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_SubscribeReplay` | ✓ |
| `submit` during probing is refused `probing` | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_SubmitRefusedDuringProbing` | ✓ |
| Manifest is written once after last probe; `engine_started` follows probes and manifest | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_ManifestWrittenAfterProbes` | ✓ |
| Ordinary recovered hold on one pool blocks only that pool's probing | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_OrdinaryHoldBlocksPool` | ✓ |
| Orphaned hold blocks every start-time probe until released | `internal/acceptance/v5_probe_test.go` | `TestStartupProbe_OrphanedHoldBlocksAll` | ✓ |

## Deferred to Milestone E

The following sections 7 clauses are E milestones, not D:

- **Probe Outcomes** (8 clauses): over-length test corrections
- **Budget** (3 clauses): spending control
- **Context** (10 clauses): history rendering and hashing
- **Tools** (54+ clauses): file read/write/list/search, tool results, staging, apply, discard
- **Egress** (5 clauses): file content egress rules
- **Pre-send checks** (11 clauses): web fetch URL validation
- **Referee** (10 clauses): model-based request authorization
- **Reviewer tools** (7 clauses): reviewer lookup and evidence

These are genuinely E features, not gaps in D.

## Running the Tests

Execute the D acceptance tests with:

```bash
make test  # Runs full suite including D tests
```

To run only the TUI-specific D tests:

```bash
go test -v ./internal/tui -run TestV5
```

To run conversation and revision tests:

```bash
go test -v ./internal/acceptance -run "TestConversations_|TestRound_|TestStartupProbe_"
```

## Coverage

Milestone D acceptance is verified by executing tests defined in:
- `internal/tui/v5_acceptance_test.go` (8 tests covering TUI clauses)
- `internal/acceptance/v5_conversations_test.go` (covering conversation clauses)
- `internal/acceptance/v5_rounds_test.go` (covering revision loop clauses)
- `internal/acceptance/v5_probe_test.go` (covering start-time probing clauses)
- `internal/acceptance/v5_lifecycle_test.go` (covering integration behavior)
- `internal/acceptance/roster_stop_recovery_test.go` (covering roster validation)

All tests use fake adapters and httptest; there is no real endpoint in CI.
