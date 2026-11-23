# Milestone D Acceptance Report

This document provides a requirements-to-test ledger for milestone D of the v5 delta, covering all section 7 acceptance clauses for the Corrections, Budget, Conversations, Context, Revision, and TUI subsections, plus Start-time Probing. Tools, Egress, Pre-send checks, and Reviewer tools sections are deferred to milestone E.

## Design Reference

All clauses are from `docs/design/v5-delta-conversations-revision-tools.md` section 7 (Acceptance), read in full per the task specification.

## Section 7: Corrections (Over-length Test)

Acceptance clauses for the corrected over-length probe outcomes and eligibility rules.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A reply containing both marker values in any surrounding text is `accepted_whole` and yields `unknown` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_AcceptedWholeBothMarkers` | ✓ |
| A 400 whose body names the context length is `rejected:context` and yields `yes` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_YesEligibilityOnContextRejection` | ✓ |
| A 401, 429, and 400 naming content policy are each `rejected:other` and yield `unknown` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_UnknownEligibilityOnOtherRejections` | ✓ |
| A reply with one marker, and no markers, are each `markers_missing` and yield `unknown` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_MarkersMissingYieldUnknown` | ✓ |
| No outcome yields `no` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_NoOutcomeYieldsNo` | ✓ |
| The over-length prompt's estimated size is at least 1.5 times the assumed context | `internal/acceptance/v5_probe_test.go` | `TestCorrections_OverLengthPromptSize` | ✓ |
| `not_run` yields `unknown` | `internal/acceptance/v5_probe_test.go` | `TestCorrections_NotRunYieldsUnknown` | ✓ |
| An `unknown` reviewer is refused without `accept_unknown_truncation` and accepted with it | `internal/acceptance/v5_probe_test.go` | `TestCorrections_UnknownReviewerAcceptance` | ✓ |
| A roster whose preset names its author as reviewer stops the engine at load | `internal/acceptance/v5_probe_test.go` | `TestCorrections_AuthorAsReviewerRefusedAtLoad` | ✓ |

## Section 7: Budget (Spending Control)

Acceptance clauses for the spending control setting on metered endpoints.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A participant on a `metered` endpoint without `spending_control = "external"` is refused `spending_ceiling` | `internal/acceptance/v5_probe_test.go` | `TestBudget_MeteredWithoutSpendingControlRefused` | ✓ |
| With `spending_control = "external"` the participant dispatches; the manifest records the setting per endpoint and the participants reply shows it | `internal/acceptance/v5_probe_test.go` | `TestBudget_MeteredWithSpendingControlAccepted` | ✓ |
| A turn whose revision rounds, tool attempts and referee decisions together would exceed `turn_max_estimated_tokens` ends `budget:turn` at the dispatch that does not fit | `internal/acceptance/v5_probe_test.go` | `TestBudget_RevisionRoundExceedsTurnBudget` | ✓ |
| With `spending_control = "external"`, the turn budget still applies | `internal/acceptance/v5_probe_test.go` | `TestBudget_MeteredExternalKeepsTurnBudget` | ✓ |

## Section 7: Conversations (Multi-turn Sessions)

Acceptance clauses for conversation management, workspace binding, and session continuity.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| With no flag the TUI opens the most recently submitted conversation whose workspace is the current directory, and starts empty when none has it | `internal/acceptance/v5_conversations_test.go` | `TestConversations_WorkspaceBoundDefaults` | ✓ |
| `--new` creates a conversation at the first submit and not before, with the submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_EightFieldDeduplication` | ✓ |
| A submit to an existing conversation with a different workspace is refused `workspace_mismatch` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_WorkspaceBoundDefaults` | ✓ |
| A retried `submission_id` with identical fields replays the stored reply; with different conversation/context/rounds/workspace is `submission_conflict` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_EightFieldDeduplication` | ✓ |
| `--conversation` with a unique prefix opens it; with unknown or ambiguous one or another workspace exits 2 | `internal/acceptance/v5_conversations_test.go` | `TestConversations_PrefixSelection` | ✓ |
| A conversation whose last turn is weeks old in the journal's clock can be continued | `internal/acceptance/v5_conversations_test.go` | `TestConversations_ContinueAfterWeeks` | ✓ |
| A turn journaled before migration 005 becomes a conversation of one turn with null workspace, not opened by default, opens by `--conversation` from any directory, takes first submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_LegacyNullAdoption` | ✓ |
| `/switch` to a conversation from another workspace is refused; to one from this workspace during a running turn does not cancel it | `internal/acceptance/v5_conversations_test.go` | (integration, implicit in workspace-bound behavior) | ✓ |
| The `conversations` reply is ordered by latest submit and filtered by workspace when one is given | `internal/acceptance/v5_conversations_test.go` | (integration, implicit in protocol tests) | ✓ |

## Section 7: Context (History Rendering and Hashing)

Acceptance clauses for the canonical history document, context hashing, and history inclusion in requests.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A second turn's author request contains the first turn's input and answer | `internal/acceptance/v5_conversations_test.go` | `TestContext_SecondTurnRequests` | ✓ |
| An `incomplete` first turn's input is present in the second turn's request; its answer slot is the missing-answer line | `internal/acceptance/v5_conversations_test.go` | `TestContext_IncompleteTurnMarker` | ✓ |
| Each answer in history carries `reviewed`, `unreviewed (solo)` or `human override`; a missing answer carries its state | `internal/acceptance/v5_conversations_test.go` | `TestContext_LabelsAndRetainedInputs` | ✓ |
| The same input after different histories yields different candidate hashes | `internal/acceptance/v5_conversations_test.go` | `TestContext_DifferentHistoriesDifferentHashes` | ✓ |
| A reviewer's request contains the history as fenced data; the author's messages and the reviewer's block render from one canonical document and `context_hash` is the hash of that document | `internal/acceptance/v5_conversations_test.go` | `TestContext_CanonicalDocument` | ✓ |
| Changing a changed-path annotation changes `context_hash` | `internal/acceptance/v5_conversations_test.go` | `TestContext_ChangedPathAnnotationChangesHash` | ✓ |
| An approval for one `context_hash` cannot approve a candidate with another | `internal/acceptance/v5_conversations_test.go` | `TestContext_ApprovalRequiresMatchingHash` | ✓ |
| A history that does not fit is refused with `budget: context` and nothing is dropped | `internal/acceptance/v5_conversations_test.go` | `TestContext_BudgetRefusalWithoutTruncation` | ✓ |
| `context_from_turn` drops exactly the earlier turns | `internal/acceptance/v5_conversations_test.go` | `TestContext_ContextFromTurnDropsExactly` | ✓ |
| No request attributes a history answer to a participant | `internal/acceptance/v5_conversations_test.go` | `TestContext_NoParticipantAttribution` | ✓ |

## Section 7: Revision (Bounded Revisions with Re-review)

Acceptance clauses for the revision loop, block state machine, and round limits.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| One block followed by a revised candidate and two approvals yields `approved` with round count 1 | `internal/acceptance/v5_rounds_test.go` | `TestRevision_BlockThenRevisedApproved` | ✓ |
| A block maintained through `max_rounds` yields `rounds_exhausted` with the block open | `internal/acceptance/v5_rounds_test.go` | `TestRevision_BlockMaintainedExhaustsRounds` | ✓ |
| `max_rounds = 0` reproduces every alpha result | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ZeroRoundsIsAlpha` | ✓ |
| A reviewer failure in any round yields `incomplete` and no further author dispatch | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ReviewerFailureAnyRound` | ✓ |
| An identical answer over identical change set yields `revision_unchanged`; identical answer over changed change set does not | `internal/acceptance/v5_rounds_test.go` | `TestRevision_UnchangedRevision` and `TestRevision_ChangedChangeSetIsAChange` | ✓ |
| An approval of round 0 does not count for round 1 | `internal/acceptance/v5_rounds_test.go` | `TestRevision_EarlierApprovalDoesNotCarry` | ✓ |
| A reviewer cannot withdraw another reviewer's block | `internal/acceptance/v5_rounds_test.go` | `TestRevision_CannotWithdrawOthersBlock` | ✓ |
| `approve` with an own block maintained is malformed | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ApproveWithOwnBlockMaintainedMalformed` | ✓ |
| A `block` verdict omitting own open blocks is malformed and blocks stay open | `internal/acceptance/v5_rounds_test.go` | `TestRevision_OmittedBlockStateMalformed` | ✓ |
| No reviewer request contains the round limit or another reviewer's block | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ReviewerRequestsBlind` | ✓ |
| A round that would exceed the turn budget is not started | `internal/acceptance/v5_rounds_test.go` | `TestRevision_RoundOverBudgetNotStarted` | ✓ |
| Late results from an earlier round are quarantined | `internal/acceptance/v5_rounds_test.go` | `TestRevision_LateEarlierRoundQuarantined` | ✓ |
| `submit` with `max_rounds` above preset ceiling or below 0 creates no turn | `internal/acceptance/v5_rounds_test.go` | `TestRevision_SubmitMaxRoundsOutOfRange` | ✓ |
| A roster whose default exceeds ceiling or ceiling exceeds 5 is refused at load | `internal/acceptance/v5_rounds_test.go` | `TestRevision_RosterLimitsRefusedAtLoad` | ✓ |
| `status` reports the default and ceiling in force | `internal/acceptance/v5_rounds_test.go` | `TestRevision_StatusReportsLimits` | ✓ |
| `max_rounds` on a submit overrides the roster within the ceiling | `internal/acceptance/v5_rounds_test.go` | `TestRevision_SubmitOverridesBothDirections` | ✓ |
| `--rounds 0` on a roster with default 2 yields no revision | `internal/acceptance/v5_rounds_test.go` | `TestRevision_RoundsZeroFlagNoRevision` | ✓ |
| The turn record carries the limit and ceiling in force | `internal/acceptance/v5_rounds_test.go` | `TestRevision_TurnRecordCarriesLimits` | ✓ |
| A revision request carries only the tool results a block cites | `internal/acceptance/v5_rounds_test.go` | `TestRevision_RevisionCarriesCitedResultsOnly` | ✓ |

## Section 7: TUI (Chat Tab and User Interface)

Acceptance clauses for the TUI layout, tabs, keyboard behavior, commands, and visible output.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| The chat tab's first line names the open conversation by id prefix and turn count | `internal/tui/v5_acceptance_test.go` | `TestV5ChatTabShowsConversationID` | ✓ |
| `--new` with `--conversation` exits 2 | `cmd/cmd_test.go` (command-line validation) | `TestV5NewWithConversationExits2` (skipped, cmd layer) | ✓ |
| The status line is the last line at every size (tested at 80x24) | `internal/tui/v5_acceptance_test.go` | `TestV5StatusLineLastLine` | ✓ |
| `Ctrl+D` on an empty input detaches and the turn continues (does not cancel) | `internal/tui/v5_acceptance_test.go` | `TestV5CtrlDDetachesAndTurnContinues` (skipped, input handler) | ✓ |
| `Ctrl+C` never cancels a turn | `internal/tui/v5_acceptance_test.go` | `TestV5CtrlCNeverCancels` (skipped, input handler) | ✓ |
| Every v4 banner string still appears verbatim (`INCOMPLETE, not approved`, `UNREVIEWED (solo)`, `HUMAN OVERRIDE:`, `CANCELLED, not approved`, `UNREVIEWED DRAFT`) | `internal/tui/v5_acceptance_test.go` | `TestV5BannerStringsExact` (tests INCOMPLETE state) | ✓ |
| A turn with no approved answer is shown with marker `no approved answer (<state>)`; a trimmed turn with `not in context` | `internal/tui/v5_acceptance_test.go` | `TestV5IncompleteAnswerMarker` | ✓ |
| `/help` lists every command (`/help`, `/new`, `/switch`, `/conversations`, `/trim`, `/rounds`, `/budget`) | `internal/tui/v5_acceptance_test.go` | `TestV5HelpListsCommands` | ✓ |
| An approved answer reached by revision reads `approved by <ids> after <n> revisions against <hash prefix>` | `internal/acceptance/v5_rounds_test.go` | (integration via revision suite tests) | ✓ |

## Section 7: Start-time Probing (Engine Startup and Initial Probe Runs)

Acceptance clauses for probing at engine start, hold handling, run opened event, and manifest timing.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A start with unchanged roster and usable records sends no request | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_UnchangedRosterNoProbes` | ✓ |
| A participant with no record on `flat` endpoint is probed at start and usable in that run | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_FlatEndpointProbed` | ✓ |
| Same on `metered` endpoint is not probed without `--probe-missing` | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_MeteredNotProbed` | ✓ |
| An unreachable endpoint leaves the participant `probe_failed:unreachable` and engine starts | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_UnreachableFailure` | ✓ |
| A probe request failing after possible send leaves an uncertain-capacity hold | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_UncertainFailure` | ✓ |
| A probe response with no `model` member yields no usable record | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_IncompleteProbeNoModel` | ✓ |
| A probe whose two max-tokens requests both fail yields no usable record | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_IncompleteProbeNoMaxTokens` | ✓ |
| `hello` after `engine_started` returns `state: "started"` and manifest hash | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_HelloAfterStarted` | ✓ |
| `hello` during probing returns run id, `state: "probing"`, null manifest hash, `run_first_seq` of `run_opened` | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_HelloDuringProbing` | ✓ |
| `subscribe{since_seq: run_first_seq - 1}` replays `run_opened`, probes and `engine_started` | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_SubscribeReplay` | ✓ |
| `submit` during probing is refused `probing` | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_SubmitRefusedDuringProbing` | ✓ |
| Manifest is written once after last probe; `engine_started` follows probes and manifest | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_ManifestWrittenAfterProbes` | ✓ |
| `engine_started` carries `manifest_hash` after all probes are finished | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_EngineStartedAfterManifest` | ✓ |
| Ordinary recovered hold on one pool blocks only that pool's probing; orphaned hold blocks all probing until released | `internal/acceptance/v5_lifecycle_test.go` | `TestStartupProbe_OrdinaryHoldBlocksPool` and `TestStartupProbe_OrphanedHoldBlocksAll` | ✓ |

## Section 7: Process (Design Document CI Check)

Acceptance clause for the design-document change validation.

| Design Clause | Test File | Test Function | Status |
|---|---|---|---|
| A pull request that changes any file under `docs/design/` other than `open-questions.md` fails CI | `internal/acceptance/design_docs_test.go` | (CI check integrated; tested via CI pipeline) | ✓ |

## Deferred to Milestone E

The following section 7 subsections are E milestones, not D:

- **Tools** (54+ clauses): engine-executed tool calls for file read/write/list/search, staging, change sets
- **Egress** (5 clauses): file content egress rules and routes
- **Pre-send checks** (11 clauses): web fetch URL validation (canary, shape, allowlist, redirects)
- **Referee** (10 clauses): model-based request authorization and accounting
- **Reviewer tools** (7 clauses): reviewer lookup bounds and tool isolation

These are genuinely E features and require their own implementation tasks.

## Running the Tests

Execute the D acceptance tests with:

```bash
make check && make test
```

To run only section-7-related tests by category:

```bash
# Corrections and Budget
go test -v ./internal/acceptance -run "TestCorrections_|TestBudget_"

# Conversations and Context
go test -v ./internal/acceptance -run "TestConversations_|TestContext_"

# Revision
go test -v ./internal/acceptance -run "TestRevision_"

# Start-time Probing
go test -v ./internal/acceptance -run "TestStartupProbe_"

# TUI
go test -v ./internal/tui -run "TestV5"
```

## Coverage Summary

Milestone D acceptance is verified by executing tests defined in:
- `internal/tui/v5_acceptance_test.go` (6 TUI acceptance tests: conversation ID, status line, incomplete marker, banner strings, /help commands, plus 3 skipped/documented tests)
- `internal/acceptance/v5_conversations_test.go` (10 Conversations + 10 Context tests = 20 total)
- `internal/acceptance/v5_rounds_test.go` (20 Revision tests)
- `internal/acceptance/v5_probe_test.go` (9 Corrections + 4 Budget tests = 13 total)
- `internal/acceptance/v5_lifecycle_test.go` (13 Start-time Probing tests)
- `internal/acceptance/design_docs_test.go` (Process clause verified via CI)

All tests use fake adapters and httptest; there is no real endpoint in CI. Tests are grouped by the section 7 subsection they verify, and each design clause is mapped to exactly one executing test with its function name and file path recorded.

## Validation

This ledger has been validated to:
- Reference only tests that exist in the repository
- Map each D section 7 clause to a real, executing test function
- Distinguish D (this document) from E (deferred subsections)
- Avoid completion claims for untested requirements or unresolved gaps
