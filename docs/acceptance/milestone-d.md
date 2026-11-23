# Milestone D acceptance ledger

This ledger maps each clause of the D paragraphs of section 7 ("Acceptance") of
`docs/design/v5-delta-conversations-revision-tools.md` (ratified revision 5) to
the executing tests that prove it, and the behavior v4 retains to the tests
that already held it. Milestone D is the set of section 8 tasks listed under
"D, in dependency order"; the paragraphs that belong to milestone E are listed
at the end and are not claimed here.

Every row names tests as `<package directory>.<TestName>`. Each named test
exists, runs under `go test -short` and none of them calls `Skip`:
`TestV5LedgerNamesRealTests` in `internal/tui/v5_acceptance_test.go` reads this
file, requires every table row to name at least one test, finds each named test
in its package directory and fails on a missing test or a `Skip` call. A clause
without a test would appear under "Open gaps"; there are none.

All of the tests use the fake adapter, `httptest`, temporary files and
temporary Unix sockets. None contacts a real endpoint or needs a credential.

Run the whole ledger:

    go test -short -count=1 ./internal/acceptance ./internal/tui ./internal/engine ./internal/server ./cmd/syndic

## Corrections

| Clause | Tests |
| --- | --- |
| A reply containing both marker values in any surrounding text is `accepted_whole` and yields `unknown` | `internal/acceptance.TestCorrections_UnknownEligibilityOnProbe` (subtest `both_markers_in_surrounding_text`) |
| A 400 whose body names the context length is `rejected:context` and yields `yes` | `internal/acceptance.TestCorrections_YesEligibilityOnContextRejection`, `internal/transport/openai.TestOverLengthClassificationMatrix` |
| A 401, a 429 and a 400 naming content policy are each `rejected:other` and yield `unknown` | `internal/acceptance.TestCorrections_UnknownEligibilityOnOtherRejections` |
| A reply with one marker, and a reply with none, are each `markers_missing` and yield `unknown` | `internal/acceptance.TestCorrections_UnknownEligibilityOnProbe` (subtests `one_marker`, `no_markers`) |
| No outcome yields `no` | `internal/acceptance.TestCorrections_NoOutcomeYieldsNo` |
| The over-length prompt's estimated size is at least 1.5 times the assumed context | `internal/acceptance.TestCorrections_OverLengthPromptSize` |
| `not_run` yields `unknown` | `internal/acceptance.TestCorrections_UnknownEligibilityOnProbe` (subtest `not_run`) |
| An `unknown` reviewer is refused without `accept_unknown_truncation` and accepted with it | `internal/acceptance.TestCorrections_UnknownReviewerAcceptance` |
| A roster whose preset names its author as reviewer stops the engine at load | `internal/acceptance.TestCorrections_AuthorAsReviewerRefusedAtLoad` |

## Budget

| Clause | Tests |
| --- | --- |
| A participant on a `metered` endpoint without `spending_control = "external"` is refused `spending_ceiling` | `internal/acceptance.TestBudget_MeteredWithoutSpendingControlRefused`, `internal/engine.TestBudgetMeteredEndpointRefused` |
| With it the participant dispatches, the manifest records the setting per endpoint and the participants reply shows it | `internal/acceptance.TestBudget_MeteredWithSpendingControlAccepted`, `internal/tui.TestParticipantDetailShowsExternalSpendingControl` |
| A turn whose revision rounds together would exceed `turn_max_estimated_tokens` ends `budget:turn` at the dispatch that does not fit, and that dispatch is not sent | `internal/acceptance.TestBudget_RevisionRoundExceedsTurnBudget`, `internal/acceptance.TestBudget_MeteredExternalKeepsTurnBudget` |

The same clause also names tool attempts and referee decisions. The engine has
neither before milestone E, so only the revision-round part is a D clause; the
other parts are listed under "Deferred to E".

## Conversations

| Clause | Tests |
| --- | --- |
| With no flag the TUI opens the most recently submitted conversation whose workspace is the current directory | `cmd/syndic.TestSelectLatestForTheWorkspace`, `cmd/syndic.TestSelectLatestFollowsPagesAndSkipsNullWorkspace`, `internal/acceptance.TestConversations_WorkspaceBoundDefaults` |
| ...and starts empty when none has it | `cmd/syndic.TestSelectLatestWithNoneStartsEmpty`, `internal/tui.TestChatHeaderOfAnEmptyConversation` |
| `--new` creates a conversation at the first submit and not before, with the submit's workspace | `cmd/syndic.TestSelectNewOpensNothing`, `internal/tui.TestSubmitCarriesConversationAndRounds`, `internal/engine.TestSubmitConversationAdoptionAndWorkspace` |
| A submit to an existing conversation with a different workspace is refused `workspace_mismatch` | `internal/acceptance.TestConversations_WorkspaceBoundDefaults`, `internal/engine.TestSubmitConversationAdoptionAndWorkspace` |
| A retried `submission_id` with a different `conversation_id`, `context_from_turn`, `max_rounds` or `workspace` is `submission_conflict`, and with all identical replays the stored reply | `internal/acceptance.TestConversations_EightFieldDeduplication`, `internal/engine.TestSubmitReplaysSameSubmissionAndConflictsOnAnyChangedMember` |
| `--conversation` with a unique prefix opens it | `cmd/syndic.TestSelectByPrefix`, `internal/acceptance.TestConversations_PrefixSelection` |
| ...and with an unknown or ambiguous prefix, or one from another workspace, exits 2 having sent nothing | `cmd/syndic.TestSelectByPrefix`, `internal/server.TestConversationUnknownAndAmbiguousSendNoSubmit`, `internal/acceptance.TestConversations_PrefixSelection` |
| A conversation whose last turn is weeks old in the journal's clock can be continued | `internal/acceptance.TestConversations_ContinueAfterWeeks` |
| A turn journaled before migration 005 appears as a conversation of one turn with a null workspace, is not opened by default, opens by `--conversation` from any directory and takes the workspace of the first submit | `internal/acceptance.TestConversations_LegacyNullAdoption`, `internal/engine.TestSubmitNullWorkspaceConversationAdoptsFirstWorkspace`, `internal/engine.TestSubmitReplaysPreConversationTurn` |
| `/switch` to a conversation from another workspace is refused | `internal/tui.TestConversationsAndSwitch` |
| `/switch` to one from this workspace during a running turn does not cancel it | `internal/tui.TestNewAndSwitchLeaveARunningTurnAlone` |
| The `conversations` reply is ordered by latest submit and filtered by workspace when one is given | `internal/acceptance.TestConversations_WorkspaceBoundDefaults`, `internal/server.TestConversationListPagesByCursor`, `cmd/syndic.TestListConversationsFiltersToWorkspace` |

## Context

| Clause | Tests |
| --- | --- |
| A second turn's author request contains the first turn's input and answer | `internal/acceptance.TestContext_SecondTurnRequests` |
| An `incomplete` first turn's input is present in the second turn's request and its answer slot is the missing-answer line | `internal/acceptance.TestContext_IncompleteTurnMarker` |
| The same input after different histories yields different candidate hashes | `internal/acceptance.TestContext_DifferentHistoriesDifferentHashes` |
| A reviewer's request contains the history as fenced data | `internal/acceptance.TestContext_SecondTurnRequests` |
| The author's messages and the reviewer's block render from one canonical document and `context_hash` is the hash of that document | `internal/acceptance.TestContext_CanonicalDocument`, `internal/history.TestRenderForAuthorExact`, `internal/history.TestRenderForReviewer` |
| Changing a changed-path annotation changes `context_hash` | `internal/acceptance.TestContext_ChangedPathAnnotationChangesHash`, `internal/history.TestApplyAnnotationsChangeHash` |
| An approval for one `context_hash` cannot approve a candidate with another | `internal/acceptance.TestContext_ApprovalRequiresMatchingHash`, `internal/engine.TestCandidateIdentityBindsFrozenContextHash` |
| A history that does not fit is refused with `budget: context` and nothing is dropped | `internal/acceptance.TestContext_BudgetRefusalWithoutTruncation`, `internal/engine.TestSubmitOversizeHistoryRefusedWithoutTruncation` |
| `context_from_turn` drops exactly the earlier turns | `internal/acceptance.TestContext_ContextFromTurnDropsExactly`, `internal/engine.TestSubmitContextFromTurnTrimsExactly` |
| No request attributes a history answer to a participant | `internal/acceptance.TestContext_NoParticipantAttribution` |

## Revision

| Clause | Tests |
| --- | --- |
| One block followed by a revised candidate and two approvals yields `approved` with round count 1 | `internal/acceptance.TestRevision_BlockThenRevisedApproved` |
| A block maintained through `max_rounds` yields `rounds_exhausted` with the block open | `internal/acceptance.TestRevision_BlockMaintainedExhaustsRounds` |
| `max_rounds = 0` reproduces every alpha result | `internal/acceptance.TestRevision_ZeroRoundsIsAlpha` |
| A reviewer failure in any round yields `incomplete` and no further author dispatch | `internal/acceptance.TestRevision_ReviewerFailureAnyRound`, `internal/engine.TestRevisionNeverStartsOnReviewerFailure` |
| An identical answer over an identical change set yields `revision_unchanged` | `internal/acceptance.TestRevision_UnchangedRevision`, `internal/engine.TestRevisionUnchangedDetection` |
| An identical answer over a changed change set does not | `internal/acceptance.TestRevision_ChangedChangeSetIsAChange`, `internal/engine.TestSameRevision` |
| An approval of round 0 does not count for round 1 | `internal/acceptance.TestRevision_EarlierApprovalDoesNotCarry` |
| A reviewer cannot withdraw another reviewer's block | `internal/acceptance.TestRevision_CannotWithdrawOthersBlock` |
| `approve` with an own block maintained is malformed | `internal/acceptance.TestRevision_ApproveWithOwnBlockMaintainedMalformed` |
| A `block` verdict that omits one of the reviewer's own open blocks is malformed and the omitted block stays open | `internal/acceptance.TestRevision_OmittedBlockStateMalformed` |
| No reviewer request contains the round limit or another reviewer's block | `internal/acceptance.TestRevision_ReviewerRequestsBlind` |
| A round that would exceed the turn budget is not started | `internal/acceptance.TestRevision_RoundOverBudgetNotStarted`, `internal/engine.TestRevisionProjectionMustFit` |
| Late results from an earlier round are quarantined | `internal/acceptance.TestRevision_LateEarlierRoundQuarantined` |
| `submit` with `max_rounds` above the preset's ceiling or below 0 creates no turn | `internal/acceptance.TestRevision_SubmitMaxRoundsOutOfRange`, `internal/engine.TestRevisionClientLimit` |
| A roster whose default exceeds its ceiling, or whose ceiling exceeds 5, is refused at load | `internal/acceptance.TestRevision_RosterLimitsRefusedAtLoad`, `internal/config.TestResolveRevisionDefaultBreaksCeiling` |
| `status` reports the default and the ceiling in force | `internal/acceptance.TestRevision_StatusReportsLimits` |
| `max_rounds` on a submit overrides the roster in both directions within the ceiling | `internal/acceptance.TestRevision_SubmitOverridesBothDirections` |
| `--rounds 0` yields no revision on a roster whose default is 2 | `internal/acceptance.TestRevision_RoundsZeroFlagNoRevision`, `internal/tui.TestSubmitCarriesConversationAndRounds` |
| The turn record carries the limit and the ceiling in force | `internal/acceptance.TestRevision_TurnRecordCarriesLimits` |
| A revision request carries the tool results a block cites and no other reviewer result | `internal/acceptance.TestRevision_RevisionCarriesCitedResultsOnly` |

## TUI

| Clause | Tests |
| --- | --- |
| The chat tab's first line names the open conversation (id prefix and turn count) | `internal/tui.TestV5ChatFirstLineNamesTheConversation`, `internal/tui.TestChatHeaderNamesTheOpenConversation` |
| `--new` with `--conversation` exits 2 | `cmd/syndic.TestCmdTUIUsageErrorsExit2`, `cmd/syndic.TestParseStartupFlags` |
| The status line is the last line at every size | `internal/tui.TestV5StatusLineIsLastAtEverySize` (seven sizes and resizes, all three tabs), `internal/tui.TestChatStatusLineIsLastAt80x24` |
| `Ctrl+D` on an empty input detaches and the turn continues | `internal/tui.TestV5CtrlDAndCtrlCDetachWithoutCancellingARunningTurn` (key pressed while the author is held; asserts no cancel, the turn open after the TUI exits, then approved), `internal/tui.TestCtrlDDetachesOnlyOnEmptyInput` |
| `Ctrl+C` never cancels a turn | `internal/tui.TestV5CtrlDAndCtrlCDetachWithoutCancellingARunningTurn`, `internal/tui.TestV5CtrlCWithTextClearsAndSendsNothing`, `internal/tui.TestCtrlCClearsThenDetaches` |
| Every v4 banner string still appears verbatim (`UNREVIEWED (solo)`, `INCOMPLETE, not approved`, `HUMAN OVERRIDE:`, `CANCELLED, not approved`, `UNREVIEWED DRAFT`) | `internal/tui.TestV5EveryV4BannerStringIsVerbatim`, `internal/tui.TestChatShowsEachTurnsInputThenOutcome` |
| A turn with no approved answer is shown with `no approved answer (<state>)` | `internal/tui.TestV5NoApprovedAnswerAndNotInContextMarkers`, `internal/tui.TestChatMissingAnswerAndTrimmedTurnsAreDistinct` |
| A trimmed turn is shown with `not in context` | `internal/tui.TestV5NoApprovedAnswerAndNotInContextMarkers`, `internal/tui.TestReopenedTrimmedConversationMarksTurnsNotInContext` |
| `/help` lists every command | `internal/tui.TestV5HelpListsEveryCommandAndKey` (every entry of the dispatch table, every key), `internal/tui.TestHelpListsCommandsAndKeys` |
| An approval reached by revision reads `approved by <ids> after <n> revisions against <hash>` (section 5, through a real engine) | `internal/tui.TestV5ApprovedAfterRevisionShowsIdsCountAndHash`, `internal/tui.TestChatApprovedByRevisionShowsTheCount` |

## Start-time probing

| Clause | Tests |
| --- | --- |
| A start with an unchanged roster and usable records sends no request | `internal/acceptance.TestStartupProbe_UnchangedRosterNoProbes`, `internal/engine.TestStartupProbeSendsNothingForUsableRecords` |
| A participant with no record on a `flat` endpoint is probed at start and is usable in that run | `internal/acceptance.TestStartupProbe_FlatEndpointProbed`, `internal/engine.TestStartupProbeMakesParticipantsUsableInThisRun` |
| The same on a `metered` endpoint is not probed without `--probe-missing` | `internal/acceptance.TestStartupProbe_MeteredNotProbed`, `internal/engine.TestStartupProbeBillingGate` |
| An unreachable endpoint leaves the participant `probe_failed:unreachable` and the engine starts | `internal/acceptance.TestStartupProbe_UnreachableFailure`, `internal/engine.TestStartupProbeFailureLeavesEngineAvailable` |
| A probe request that fails after a possible send leaves an uncertain-capacity hold | `internal/acceptance.TestStartupProbe_UncertainFailure`, `internal/engine.TestStartupProbePostSendFailureLeavesHoldAndRestartDoesNotResend` |
| A probe response with no `model` member yields no usable record | `internal/acceptance.TestStartupProbe_IncompleteProbeNoModel`, `internal/transport/openai.TestProbeMissingModelIsIncompleteNotUsable` |
| A probe whose two max-tokens requests both fail yields no usable record | `internal/acceptance.TestStartupProbe_IncompleteProbeNoMaxTokens`, `internal/transport/openai.TestProbeNoWorkingMaxTokensFieldIsIncomplete` |
| `hello` after `engine_started` returns `state: "started"` and the manifest hash | `internal/acceptance.TestStartupProbe_HelloAfterStarted` |
| `hello` during probing returns the run id, `state: "probing"`, a null `manifest_hash` and a `run_first_seq` equal to the `seq` of `run_opened` | `internal/acceptance.TestStartupProbe_HelloDuringProbing`, `internal/engine.TestStartupProbeHelloAndSubmitDuringProbing` |
| `subscribe{since_seq: run_first_seq - 1}` replays `run_opened`, every probe event and `engine_started` | `internal/acceptance.TestStartupProbe_SubscribeReplay` |
| `submit` during probing is refused `probing` | `internal/acceptance.TestStartupProbe_SubmitRefusedDuringProbing` |
| The manifest is written once, after the last probe | `internal/acceptance.TestStartupProbe_ManifestWrittenAfterProbes` |
| `engine_started` follows every `probe_finished` and the manifest and carries `manifest_hash` | `internal/acceptance.TestStartupProbe_EngineStartedAfterManifest` |
| An ordinary recovered hold on one pool blocks probing of that pool's participants only | `internal/acceptance.TestStartupProbe_OrdinaryHoldBlocksPool`, `internal/engine.TestStartupProbeOrdinaryHoldBlocksOnlyItsPool` |
| An orphaned hold blocks every start-time probe until released | `internal/acceptance.TestStartupProbe_OrphanedHoldBlocksAll`, `internal/engine.TestStartupProbeOrphanedHoldBlocksEveryProbe` |

## Process

| Clause | Tests |
| --- | --- |
| A pull request that changes any file under `docs/design/` other than `open-questions.md` fails CI | `internal/acceptance.TestDesignDocs_ProtectedDocsFail`, `internal/acceptance.TestDesignDocs_ModifyExistingFails`, `internal/acceptance.TestDesignDocs_MixedChangeFails`, `internal/acceptance.TestDesignDocs_OpenQuestionsPasses` |

## Retained v4 behavior

These held before D; each row names the test that still holds it.

| Behavior | Tests |
| --- | --- |
| Durable intent: a dispatch is recorded as pending before it is sent, and a restart does not resend it | `internal/engine.TestStartupProbeCrashLeavesPendingRequestThatRestartHolds`, `internal/engine.TestRestartPendingDeliveryIsUnknownAndNotResent`, `internal/journal.TestStoreRecoverPending` |
| State and event are one transaction | `internal/acceptance.TestJournal_StateAndEventAreOneTransaction` |
| No secret is persisted | `internal/acceptance.TestRunsManifest_ManifestContainsNoSecretValue`, `internal/acceptance.TestSocket_StatusHasNoSecret`, `cmd/syndic.TestProbeResolverSecretNotLeaked` |
| A late result is quarantined, not applied | `internal/acceptance.TestJournal_ResultAfterEpochBumpQuarantined`, `internal/acceptance.TestCancellation_LateOutputQuarantined` |
| The turn's original deadline is never extended (repair, revision) | `internal/acceptance.TestDeadlinesPools_RepairDoesNotExtendTurnDeadline`, `internal/engine.TestRevisionKeepsOriginalDeadline` |
| Pool holds: an uncertain dispatch holds capacity until released, across restarts | `internal/acceptance.TestCapacity_HoldSurvivesRestart`, `internal/acceptance.TestCapacity_NextDispatchWaits`, `internal/acceptance.TestRoster_OrphanedHolds` |
| Reviews are independent, and a turn is approved only when every required reviewer approves | `internal/acceptance.TestNAndRoles_TwoReviewersNeeded`, `internal/acceptance.TestNAndRoles_ApproveAndBlockIncomplete`, `internal/acceptance.TestRevision_ReviewerRequestsBlind` |
| An approval must name the candidate hash | `internal/acceptance.TestProtocol_ApproveWithHash`, `internal/acceptance.TestProtocol_WrongHashRejected` |
| Budgets: a dispatch that does not fit is refused and not sent | `internal/acceptance.TestBudgets_EstimateExceeded`, `internal/acceptance.TestBudgets_BudgetExhausted` |
| Detaching never cancels a turn; the turn is found again on reattach | `internal/acceptance.TestSocket_DetachReattach`, `internal/tui.TestQuitLeavesTurnOpen`, `internal/tui.TestReattachFindsOpenTurn` |
| The answer tab shows nothing of the author before a terminal state unless drafts are on | `internal/tui.TestAnswerOpenTurnShowsNoAuthorText`, `internal/tui.TestAnswerDraftsOnWhileReviewersRun` |
| A blocked turn shows its blocks and remedy | `internal/tui.TestAnswerBlockEndsIncomplete` |
| An override needs a reason and shows `HUMAN OVERRIDE:` | `internal/tui.TestOverrideNeedsAReason`, `internal/tui.TestAnswerOverriddenShowsHumanReason` |
| `/why` on a budget failure shows the estimate and the limit | `internal/tui.TestWhyBudget` |
| The provisional prompt appears once per process and the resend carries `allow_provisional` | `internal/tui.TestProvisionalYesResendsOnceAndIsRemembered` |
| The roster-changed banner follows `status` | `internal/tui.TestRosterChangedBanner` |
| `TERM=dumb` runs | `internal/tui.TestDumbTerminalSoloTurn` |
| Every tab fits at 80x24, and a terminal below the minimum shows one line asking for more | `internal/tui.TestEveryTabFitsAt80x24`, `internal/tui.TestResizeBelowMinimumAndBack` |
| The TUI refuses a redirected stdout | `cmd/syndic.TestTUIRefusesRedirectedStdout` |

## Open gaps

None for milestone D. Every clause above names an executing test.

## Deferred to E

Milestone E owns these paragraphs of section 7, none of which has an engine
feature to test yet: Tools, Egress, Pre-send checks, Referee and Reviewer tools.
They are not claimed by this ledger.

Within a D paragraph, one part waits for E: the Budget clause's tool attempts
and referee decisions (the revision rounds are covered above), and the
Revision clause's cited tool results, which `TestRevision_RevisionCarriesCitedResultsOnly`
covers only for the results that exist before tools land.
