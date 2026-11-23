# Milestone D Acceptance Report

This document provides a comprehensive clause-by-clause requirements-to-test ledger for milestone D of the v5 delta: conversations, revision loop, and chat TUI. All acceptance tests are deterministic with `fake.Adapter`; there is no real endpoint in CI.

## Design Reference

All tests verify clauses stated in `docs/design/v5-delta-conversations-revision-tools.md` section 7 (Acceptance), organized by topic. The ratified document SHA-256 is `6cb570b01ee5c40744e3f97c986d33095b9c056d205efd332b024b713b005577` at commit `a6a9c63f4993f0d2057b42ab20a6805a56d3c977`.

## Requirements-to-Test Ledger

### 1. Corrections (Probe Outcomes)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| A reply containing both marker values in any surrounding text is `accepted_whole` and yields `unknown` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_MarkersBothPresentYieldsUnknown` | ✓ |
| A 400 whose body names the context length is `rejected:context` and yields `yes` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_ContextOverflowYieldsYes` | ✓ |
| A 401, 429, and 400 naming content policy are each `rejected:other` and yield `unknown` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_OtherRejectionsYieldUnknown` | ✓ |
| A reply with one marker or none is `markers_missing` and yields `unknown` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_IncompleteMarkersYieldUnknown` | ✓ |
| No outcome yields `no` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_NoOutcomeYieldsNo` | ✓ |
| The over-length prompt's estimated size is at least 1.5 times the assumed context | `internal/acceptance/v5_probing_test.go` | `TestCorrections_FillerSizeMinimum` | ✓ |
| `not_run` yields `unknown` | `internal/acceptance/v5_probing_test.go` | `TestCorrections_NotRunYieldsUnknown` | ✓ |
| An `unknown` reviewer is refused without `accept_unknown_truncation` and accepted with it | `internal/acceptance/v5_probing_test.go` | `TestCorrections_UnknownReviewerControlledByAcceptFlag` | ✓ |
| A roster whose preset names its author as reviewer stops the engine at load | `internal/acceptance/v5_roster_test.go` | `TestRoster_AuthorAsReviewerRefused` | ✓ |

### 2. Budget (Spending Control)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| A participant on a `metered` endpoint without `spending_control = "external"` is refused `spending_ceiling` | `internal/acceptance/v5_budget_test.go` | `TestBudget_MeteredWithoutSpendingControlRefused` | ✓ |
| With `spending_control = "external"` the participant dispatches, manifest records the setting, and participants reply shows it | `internal/acceptance/v5_budget_test.go` | `TestBudget_ExternalSpendingControlAllows` | ✓ |
| A turn whose revision rounds, tool attempts and referee decisions together would exceed `turn_max_estimated_tokens` ends `budget:turn` and that dispatch is not sent | `internal/acceptance/v5_budget_test.go` | `TestBudget_TurnTokensExceededEndsWithoutDispatch` | ✓ |

### 3. Conversations (Multi-turn Sessions)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| With no flag the TUI opens the most recently submitted conversation whose workspace is the current directory, and starts empty when none has it | `internal/acceptance/v5_conversations_test.go` | `TestConversations_DefaultOpensLatest` | ✓ |
| `--new` creates a conversation at the first submit and not before, with the submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_NewCreatesAtFirstSubmit` | ✓ |
| A submit to an existing conversation with a different workspace is refused `workspace_mismatch` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_WorkspaceMismatchRefused` | ✓ |
| A retried `submission_id` with identical fields replays the stored reply | `internal/acceptance/v5_conversations_test.go` | `TestConversations_IdenticalRetryReplays` | ✓ |
| A retried `submission_id` with different conversation/context/rounds/workspace is `submission_conflict` | `internal/acceptance/v5_conversations_test.go` | `TestConversations_DifferentRetryIsConflict` | ✓ |
| `--conversation` with a unique prefix opens it, with unknown or ambiguous one or another workspace exits 2 sending nothing | `internal/acceptance/v5_conversations_test.go` | `TestConversations_FlagValidation` | ✓ |
| A conversation whose last turn is weeks old can be continued | `internal/acceptance/v5_conversations_test.go` | `TestConversations_OldConversationContinues` | ✓ |
| A turn before migration 005 becomes a conversation of one turn with null workspace, not opened by default, opens by `--conversation`, takes first submit's workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_LegacyNullAdoption` | ✓ |
| `/switch` to another workspace is refused; to this workspace during a running turn does not cancel it | `internal/acceptance/v5_conversations_test.go` | `TestConversations_SwitchValidation` | ✓ |
| The `conversations` reply is ordered by latest submit and filtered by workspace | `internal/acceptance/v5_conversations_test.go` | `TestConversations_ListOrdering` | ✓ |

### 4. Context (History Rendering and Hashing)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| A second turn's author request contains the first turn's input and answer | `internal/acceptance/v5_context_test.go` | `TestContext_SecondTurnIncludesHistory` | ✓ |
| An `incomplete` first turn's input is present, its answer slot shows the missing-answer line | `internal/acceptance/v5_context_test.go` | `TestContext_IncompleteTurnShowsMissingLine` | ✓ |
| The same input after different histories yields different candidate hashes | `internal/acceptance/v5_context_test.go` | `TestContext_HistoryAffectsHash` | ✓ |
| A reviewer's request contains the history as fenced data | `internal/acceptance/v5_context_test.go` | `TestContext_ReviewerSeesHistory` | ✓ |
| The author's messages and reviewer's block render from one canonical document with `context_hash` | `internal/acceptance/v5_context_test.go` | `TestContext_CanonicalHistoryDocument` | ✓ |
| Changing a changed-path annotation changes `context_hash` | `internal/acceptance/v5_context_test.go` | `TestContext_AnnotationChangesHash` | ✓ |
| An approval for one `context_hash` cannot approve a candidate with another | `internal/acceptance/v5_context_test.go` | `TestContext_ApprovalHashBinding` | ✓ |
| A history that does not fit is refused with `budget: context` and nothing is dropped | `internal/acceptance/v5_context_test.go` | `TestContext_HistoryBudgetExceeded` | ✓ |
| `context_from_turn` drops exactly the earlier turns | `internal/acceptance/v5_context_test.go` | `TestContext_ContextFromTurnDropsEarlier` | ✓ |
| No request attributes a history answer to a participant | `internal/acceptance/v5_context_test.go` | `TestContext_AnonymousHistory` | ✓ |

### 5. Revision (Bounded Loop with Block State)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| One block followed by revised candidate and two approvals yields `approved` with round count 1 | `internal/acceptance/v5_rounds_test.go` | `TestRevision_BlockAndReviseAndApprove` | ✓ |
| A block maintained through `max_rounds` yields `rounds_exhausted` with block open | `internal/acceptance/v5_rounds_test.go` | `TestRevision_BlockedMaxRoundsExhausted` | ✓ |
| `max_rounds = 0` reproduces every alpha result | `internal/acceptance/v5_rounds_test.go` | `TestRevision_MaxRoundsZero` | ✓ |
| A reviewer failure in any round yields `incomplete` and no further author dispatch | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ReviewerFailureEndsIncomplete` | ✓ |
| An identical answer over identical change set yields `revision_unchanged` | `internal/acceptance/v5_rounds_test.go` | `TestRevision_IdenticalAnswerAndChanges` | ✓ |
| An identical answer over changed change set does not yield unchanged | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ChangedChangesNotUnchanged` | ✓ |
| An approval of round 0 does not count for round 1 | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ApprovalRoundBinding` | ✓ |
| A reviewer cannot withdraw another reviewer's block | `internal/acceptance/v5_rounds_test.go` | `TestRevision_CannotWithdrawOthersBlock` | ✓ |
| `approve` with an own block maintained is malformed | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ApproveWithOwnBlockMalformed` | ✓ |
| A `block` verdict omitting own open blocks is malformed and blocks stay open | `internal/acceptance/v5_rounds_test.go` | `TestRevision_OmittedBlocksMalformed` | ✓ |
| No reviewer request contains the round limit or another reviewer's block | `internal/acceptance/v5_rounds_test.go` | `TestRevision_ReviewerNoRoundLimitOrOtherBlocks` | ✓ |
| A round that would exceed the turn budget is not started | `internal/acceptance/v5_rounds_test.go` | `TestRevision_BudgetExceededNoRound` | ✓ |
| Late results from an earlier round are quarantined | `internal/acceptance/v5_rounds_test.go` | `TestRevision_LateResultsQuarantined` | ✓ |
| `submit` with `max_rounds` above preset ceiling or below 0 creates no turn | `internal/acceptance/v5_rounds_test.go` | `TestRevision_InvalidMaxRoundsRefused` | ✓ |
| A roster whose default exceeds ceiling or ceiling exceeds 5 is refused at load | `internal/acceptance/v5_roster_test.go` | `TestRoster_RoundLimitValidation` | ✓ |
| `status` reports the default and ceiling in force | `internal/acceptance/v5_rounds_test.go` | `TestRevision_StatusReportsLimits` | ✓ |
| `max_rounds` on a submit overrides the roster within the ceiling | `internal/acceptance/v5_rounds_test.go` | `TestRevision_SubmitOverridesDefault` | ✓ |
| `--rounds 0` on a roster with default 2 yields no revision | `internal/acceptance/v5_rounds_test.go` | `TestRevision_FlagOverridesDefault` | ✓ |
| The turn record carries the limit and ceiling in force | `internal/acceptance/v5_rounds_test.go` | `TestRevision_TurnRecordsLimits` | ✓ |
| A revision request carries only tool results a block cites | `internal/acceptance/v5_rounds_test.go` | `TestRevision_AuthorReceivesCitedResults` | ✓ |

### 6. TUI (User-Observable Chat Interface)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| The chat tab's first line names the open conversation | `internal/tui/v5_acceptance_test.go` | `TestV5ChatTabShowsConversationID` | ✓ |
| `--new` with `--conversation` exits 2 | `internal/cmd/cmd_test.go` | `TestCmd_NewWithConversationExits2` | ✓ |
| The status line is the last line at every size | `internal/tui/v5_acceptance_test.go` | `TestV5StatusLineEverySize` | ✓ |
| `Ctrl+D` on an empty input detaches and the turn continues | `internal/tui/v5_acceptance_test.go` | `TestV5CtrlDDetaches` | ✓ |
| `Ctrl+C` never cancels a turn | `internal/tui/v5_acceptance_test.go` | `TestV5CtrlCNeverCancels` | ✓ |
| Every v4 banner string still appears verbatim: `UNREVIEWED (solo)`, `INCOMPLETE, not approved`, `HUMAN OVERRIDE:`, `CANCELLED, not approved`, `UNREVIEWED DRAFT` | `internal/tui/v5_acceptance_test.go` | `TestV5BannerStringsVerbatim` | ✓ |
| A turn with no approved answer is shown with marker; a trimmed turn with `not in context` | `internal/tui/v5_acceptance_test.go` | `TestV5IncompleteAndTrimMarkers` | ✓ |
| `/help` lists every command | `internal/tui/v5_acceptance_test.go` | `TestV5HelpListsCommands` | ✓ |

### 7. Start-time Probing (Engine Startup)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| A start with unchanged roster and usable records sends no request | `internal/acceptance/v5_probing_test.go` | `TestProbing_UnchangedRosterNoRequest` | ✓ |
| A participant with no record on `flat` endpoint is probed at start and usable | `internal/acceptance/v5_probing_test.go` | `TestProbing_FlatEndpointProbedAtStart` | ✓ |
| Same on `metered` endpoint is not probed without `--probe-missing` | `internal/acceptance/v5_probing_test.go` | `TestProbing_MeteredNoProbingDefault` | ✓ |
| An unreachable endpoint leaves participant `probe_failed:unreachable` and engine starts | `internal/acceptance/v5_probing_test.go` | `TestProbing_UnreachableDoesNotBlockStart` | ✓ |
| A probe request failing after possible send leaves uncertain-capacity hold | `internal/acceptance/v5_probing_test.go` | `TestProbing_FailureAfterSendCreatesHold` | ✓ |
| A probe response with no `model` member yields no usable record | `internal/acceptance/v5_probing_test.go` | `TestProbing_NoModelNoRecord` | ✓ |
| A probe whose two max-tokens requests both fail yields no usable record | `internal/acceptance/v5_probing_test.go` | `TestProbing_MaxTokensFailureNoRecord` | ✓ |
| `hello` after `engine_started` returns `state: "started"` and manifest hash | `internal/acceptance/v5_probing_test.go` | `TestProbing_HelloStartedWithManifest` | ✓ |
| `hello` during probing returns run id, `state: "probing"`, null manifest, `run_first_seq` of `run_opened` | `internal/acceptance/v5_probing_test.go` | `TestProbing_HelloProbingWithRunId` | ✓ |
| `subscribe` with `run_first_seq - 1` replays `run_opened`, probes and `engine_started` | `internal/acceptance/v5_probing_test.go` | `TestProbing_SubscribeReplaysProbeEvents` | ✓ |
| `submit` during probing is refused `probing` | `internal/acceptance/v5_probing_test.go` | `TestProbing_SubmitDuringProbingRefused` | ✓ |
| Manifest is written once after last probe; `engine_started` follows probes and manifest | `internal/acceptance/v5_probing_test.go` | `TestProbing_ManifestAndStartedOrdering` | ✓ |
| Ordinary recovered hold on one pool blocks only that pool's probing | `internal/acceptance/v5_probing_test.go` | `TestProbing_HoldBlocksPool` | ✓ |
| Orphaned hold blocks every start-time probe until released | `internal/acceptance/v5_probing_test.go` | `TestProbing_OrphanedHoldBlocksAll` | ✓ |

### 8. Process (CI and Quality)

| Design Clause | Test File | Test Name | Status |
|---|---|---|---|
| A pull request that changes any file under `docs/design/` other than `open-questions.md` fails CI | `ci/check-design-edits.sh` | `design_edit_check` | ✓ |

## Deferred to Milestone E

Milestone E adds tools, referee, file egress, and advanced TUI features. The following sections 7 clauses are E milestones, not D:

- **Tools** (all 54+ clauses): file read/write/list/search, tool results, staging
- **Egress** (all 5 clauses): file content egress rules
- **Pre-send checks** (all 11 clauses): web fetch URL validation
- **Referee** (all 10 clauses): model-based request authorization
- **Reviewer tools** (all 7 clauses): reviewer lookup and evidence

These are genuinely E features and not gaps in D.

## Retained v4 Behavior

The following v4 requirements remain unchanged in D and are verified by existing acceptance tests in v4-compatible mode:

- Turn independence and sequential execution
- Two required reviewers with votes that determine approval
- Author may or may not use submitted candidate evidence
- Participant blindness (names not in requests)
- Durable journal integrity and recovery
- No secrets persisted
- Protocol version and change set hash binding

## Running the Tests

To run all D milestone acceptance tests:

```bash
# All corrections/probing and budget
make test 2>&1 | grep -E "TestCorrections|TestBudget|TestProbing" 

# All conversation and context tests
go test -race ./internal/acceptance -run "TestConversations|TestContext"

# All revision tests
go test -race ./internal/acceptance -run "TestRevision"

# All TUI tests with proper assertions
go test -race ./internal/tui -run "TestV5"

# Full suite
make test
```

Coverage is verified by ensure every D clause above maps to exactly one `TestXxx` name, and every test has a t.Fatalf/t.Errorf assertion that enforces the exact specification.

## Test Structure

Each test file adheres to this structure:
1. Use `fake.Adapter` with deterministic step sequences
2. Assert the exact clause text from section 7
3. No test contacts a real endpoint
4. No test skips due to unimplemented features in D
5. Required scenarios run under `-short` flag without timeout extension

All tests use the standard rosters:
- `enginetest.RosterTOML`: Two-participant (author and reviewer) mode
- `enginetest.RosterSOLO`: Solo mode
- Custom rosters for specific scenarios

The tests are maintainable: each is named after its clause, with one assertion per required behavior, so changes to either the test or the implementation are traceable to the design requirement.
