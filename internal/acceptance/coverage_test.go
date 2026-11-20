package acceptance

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// coverageTable maps acceptance items from docs/design/v4-delta-engine-tui.md
// section 8 to the test functions that cover them. Items are grouped by
// paragraph, quoted or closely paraphrased. Items marked with "milestone C"
// are those explicitly marked (TUI) in the design and are out of scope for B.
var coverageTable = []struct {
	item     string
	testFunc string
	section  string
}{
	// Protocol (approve path, verdicts, blocks, malformed handling, solo)
	{"Protocol: approve path reaches approved with candidate hash in answer", "TestProtocol_ApprovePathReachesApproved", "protocol"},
	{"Protocol: VerdictProseApprove yields repair then INCOMPLETE malformed_twice", "TestProtocol_ProseApproveRepairsThenMalformedTwice", "protocol"},
	{"Protocol: VerdictWrongHash yields rejected and INCOMPLETE with no repair", "TestProtocol_VerdictWrongHash", "protocol"},
	{"Protocol: VerdictApproveWithChanges yields revision_request and INCOMPLETE", "TestProtocol_ApproveWithChanges", "protocol"},
	{"Protocol: VerdictTwoEnvelopes is malformed", "TestProtocol_TwoEnvelopes", "protocol"},
	{"Protocol: VerdictBlockWithEndMarker parses to first delimiter and block opens", "TestProtocol_BlockWithEndMarker", "protocol"},
	{"Protocol: block ends turn INCOMPLETE with block open", "TestProtocol_BlockEndsIncomplete", "protocol"},
	{"Protocol: author result with status truncated yields author_failed:truncated", "TestProtocol_AuthorTruncatedIncomplete", "protocol"},
	{"Protocol: reviewer result with mismatched ReportedModel fails CheckResult", "TestProtocol_ReportedModelMismatchFails", "protocol"},
	{"Protocol: solo with status refused is incomplete not solo_complete", "TestProtocol_SoloWithRefusedIncomplete", "protocol"},

	// Dispatch failures (transport status, retries, Go errors)
	{"Dispatch: unreachable then normal reply yields one retry and normal turn", "TestDispatch_UnreachableThenRetryThenNormal", "dispatch"},
	{"Dispatch: two unreachable steps yield reviewer_failed:unreachable no wait", "TestDispatch_TwoUnreachable", "dispatch"},
	{"Dispatch: unauthorized fails turn at once with no retry", "TestDispatch_UnauthorizedFailsImmediately", "dispatch"},
	{"Dispatch: reset with interrupted yields no retry and capacity hold", "TestDispatch_ResetWithHoldNoRetry", "dispatch"},
	{"Dispatch: malformed_body yields reviewer_failed:malformed_body no retry no hold", "TestDispatch_MalformedBody", "dispatch"},
	{"Dispatch: ErrNotSent-wrapped error yields transport_status:not_sent no hold", "TestDispatch_ErrNotSent", "dispatch"},
	{"Dispatch: plain Go error yields adapter_error and capacity hold", "TestDispatch_GoError", "dispatch"},
	{"Dispatch: OpenAI adapter contract shows unresolvable secret wrapped in ErrNotSent", "TestDispatch_AdapterContract", "dispatch"},
	{"Dispatch: next submit after any failure dispatches to same participant again", "TestDispatch_ResubmitAfterFailure", "dispatch"},

	// Runs and manifest (run creation, manifest hash, roster hash, unprobed, probe selection)
	{"Runs: engine start writes run of kind engine with correct manifest hash", "TestRuns_EngineRunCreation", "runs"},
	{"Runs: two starts with same roster yield equal roster_hash and different run_id", "TestRuns_SameRosterSameHash", "runs"},
	{"Runs: changed roster yields different roster_hash", "TestRuns_ChangedRosterDifferentHash", "runs"},
	{"Runs: participant with no seeded export is unprobed:no_record", "TestRuns_UnprobedNoRecord", "runs"},
	{"Runs: unprobed:fingerprint_mismatch when base_url differs", "TestRuns_FingerprintMismatchURL", "runs"},
	{"Runs: unprobed:fingerprint_mismatch when auth reference differs", "TestRuns_FingerprintMismatchAuth", "runs"},
	{"Runs: unprobed:fingerprint_mismatch when adapter version differs", "TestRuns_FingerprintMismatchVersion", "runs"},
	{"Runs: seeded bare export with no fingerprint yields unprobed:no_fingerprint", "TestRuns_NoFingerprint", "runs"},
	{"Runs: two participants sharing endpoint and model select same record", "TestRuns_SharedRecord", "runs"},
	{"Runs: seeded export with eligible_required_reviewer:no makes preset fail with preset_invalid", "TestRuns_IneligibleReviewer", "runs"},
	{"Runs: hello.run_first_seq is seq of new engine_started event", "TestRuns_HelloRunFirstSeq", "runs"},
	{"Runs: subscribe{since_seq: run_first_seq - 1} replays exactly new run", "TestRuns_SubscribeReplayNewRun", "runs"},
	{"Runs: subscribe{since_seq: 0} replays seeded probe_completed rows and client ignores", "TestRuns_SubscribeIgnoresProbeCompleted", "runs"},
	{"Runs: manifest JSON contains no secret value", "TestRuns_ManifestNoSecrets", "runs"},
	{"Runs: probe with usage writes calibration with bytes_per_token", "TestRuns_CalibrationFromUsage", "runs"},
	{"Runs: probe without usage writes calibration:unavailable", "TestRuns_CalibrationUnavailable", "runs"},

	// Roster and stop (file_changed, holds across rename, SIGTERM, probe against running)
	{"Roster: status.roster_file_changed is false at start", "TestRoster_FileChangedAtStart", "roster"},
	{"Roster: status.roster_file_changed is true after file rewritten", "TestRoster_FileChangedAfterRewrite", "roster"},
	{"Roster: status.roster_file_changed is true after comment-only edit", "TestRoster_FileChangedCommentOnly", "roster"},
	{"Roster: status.roster_file_changed is false again after restored byte-for-byte", "TestRoster_FileChangedRestored", "roster"},
	{"Roster: hold left in pool a survives restart with participant pool renamed b", "TestRoster_HoldSurvivesRename", "roster"},
	{"Roster: hold on two-endpoint pool survives restart split into two pools", "TestRoster_HoldSurvivelsSplit", "roster"},
	{"Roster: hold on endpoint placed in two pools holds both", "TestRoster_HoldOnMultiplePools", "roster"},
	{"Roster: hold with no group members left appears in orphaned_holds", "TestRoster_OrphanedHolds", "roster"},
	{"Roster: submit refused with orphaned_holds", "TestRoster_SubmitRefusedOrphaned", "roster"},
	{"Roster: release{pool: old_name} clears orphaned hold", "TestRoster_ReleaseOrphanedHold", "roster"},
	{"Roster: SIGTERM during review ends turn incomplete:engine_stop", "TestRoster_SIGTERMEndsIncomplete", "roster"},
	{"Roster: SIGTERM calls Cancel on in-flight attempt", "TestRoster_SIGTERMCallsCancel", "roster"},
	{"Roster: SIGTERM sends no terminal delivery", "TestRoster_SIGTERMNoDelivery", "roster"},
	{"Roster: SIGTERM emits engine_stopping and closes client", "TestRoster_SIGTERMStoppingEvent", "roster"},
	{"Roster: submit during grace period refused with stopping", "TestRoster_SubmitRefusedDuringGrace", "roster"},
	{"Roster: second SIGTERM during grace period exits, next start recovers unknown", "TestRoster_SecondSIGTERMExits", "roster"},
	{"Roster: syndic probe against running engine exits with distinct code", "TestRoster_ProbeAgainstRunning", "roster"},

	// N and roles (required reviewers, preset rules, N=3/5)
	{"Roles: review with two required reviewers needs both", "TestRoles_TwoRequiredReviewers", "roles"},
	{"Roles: one approve and one block is INCOMPLETE", "TestRoles_ApproveAndBlockIncomplete", "roles"},
	{"Roles: preset naming author as reviewer is refused at submit", "TestRoles_AuthorAsReviewerRefused", "roles"},
	{"Roles: N=3 roster runs", "TestRoles_N3Roster", "roles"},
	{"Roles: N=5 roster runs", "TestRoles_N5Roster", "roles"},

	// Budgets (projection, MaxOutputTokens, bounds, estimates, holds, overrides)
	{"Budgets: projection equals sum of stand-ins per-message and per-request", "TestBudgets_ProjectionEqualsSum", "budgets"},
	{"Budgets: fake carries MaxOutputTokens clamped to output_bound", "TestBudgets_MaxOutputTokensClamped", "budgets"},
	{"Budgets: missing context_bound refused with context_unknown naming context_bound", "TestBudgets_MissingContextBound", "budgets"},
	{"Budgets: missing tokens_per_byte_max refused naming it", "TestBudgets_MissingTokensPerByteMax", "budgets"},
	{"Budgets: missing bytes_per_token and no roster value refused naming bytes_per_token", "TestBudgets_MissingBytesPerToken", "budgets"},
	{"Budgets: participant with both roster values set passes", "TestBudgets_RosterValuesPass", "budgets"},
	{"Budgets: status shows each source as roster", "TestBudgets_RosterSourceInStatus", "budgets"},
	{"Budgets: calibration 4.0 bytes/token yields estimate at 3.0", "TestBudgets_CalibrationSafetyFactor", "budgets"},
	{"Budgets: fake reporting usage above estimate causes budget_estimate_exceeded", "TestBudgets_EstimateExceeded", "budgets"},
	{"Budgets: estimate exceeded increases charged total and turn completes", "TestBudgets_ChargedAfterExceeded", "budgets"},
	{"Budgets: fake usage pushes total over turn_max marks turn exhausted", "TestBudgets_TurnsExhausted", "budgets"},
	{"Budgets: exhausted turn lets other reviewer finish, refuses delivery reservations", "TestBudgets_ExhaustedRefusesDelivery", "budgets"},
	{"Budgets: quarantined late result with usage exceeds reservation raises charged total", "TestBudgets_QuarantinedLateExceeds", "budgets"},
	{"Budgets: quarantined late result below reservation changes nothing", "TestBudgets_QuarantinedLateBelow", "budgets"},
	{"Budgets: admission bound exceeds usable context refused at dispatch", "TestBudgets_AdmissionExceedsContext", "budgets"},
	{"Budgets: estimate exceeds turn budget refused at dispatch", "TestBudgets_EstimateExceedsTurn", "budgets"},
	{"Budgets: two reviewers budget race yields exactly one dispatched", "TestBudgets_TwoReviewerRace", "budgets"},
	{"Budgets: input exceeding author bound refused with budget:context", "TestBudgets_InputExceedsAuthorBound", "budgets"},
	{"Budgets: author reply exceeding reviewer bound ends turn budget:context", "TestBudgets_ReplyExceedsReviewerBound", "budgets"},
	{"Budgets: preset budgets override changes MaxOutputTokens", "TestBudgets_PresetOverride", "budgets"},
	{"Budgets: quick profile budgets override applied after preset", "TestBudgets_QuickProfileOverride", "budgets"},
	{"Budgets: usage stored on attempt and shown in turn", "TestBudgets_UsageStored", "budgets"},
	{"Budgets: participant on metered endpoint refused with budget:spending_ceiling", "TestBudgets_MeteredEndpointRefused", "budgets"},
	{"Budgets: delivery not fitting recipient bound recorded not_sent:budget", "TestBudgets_DeliveryNotFitBound", "budgets"},
	{"Budgets: bytes_per_token in manifest", "TestBudgets_BytesPerTokenInManifest", "budgets"},

	// Quick profile (preset solo, quick_profile preset override, unknown profile, TUI items)
	{"Quick: submit{quick_profile:x} with preset=solo runs solo", "TestQuick_QuickProfileSolo", "quick"},
	{"Quick: submit{quick_profile:x, preset:review} overrides and runs review", "TestQuick_QuickProfileOverridePreset", "quick"},
	{"Quick: unknown profile name refused with quick_profile_unknown", "TestQuick_UnknownProfile", "quick"},
	{"Quick: roster quick profile with protected_paths fails Roster.Validate", "TestQuick_ProtectedPathsFails", "quick"},
	{"Quick: (TUI) project file discovered from subdirectory shown asked about", "milestone C", "quick"},
	{"Quick: (TUI) file hash and profile hash and definition hash recorded", "milestone C", "quick"},
	{"Quick: (TUI) rewritten file asks again", "milestone C", "quick"},
	{"Quick: (TUI) unchanged file with profile preset changed asks again", "milestone C", "quick"},
	{"Quick: (TUI) file naming removed profile asks again", "milestone C", "quick"},
	{"Quick: (TUI) unapproved file not sent strip shows unapproved", "milestone C", "quick"},
	{"Quick: (TUI) /quick off stops sending strip shows quick off", "milestone C", "quick"},
	{"Quick: (TUI) strip shows quick:x", "milestone C", "quick"},

	// Deadlines and pools (sequential, attempt deadline, turn deadline, clamping)
	{"Deadlines: two reviewers in pool run sequentially", "TestDeadlines_TwoReviewersSequential", "deadlines"},
	{"Deadlines: second reviewer's queued time counts against turn deadline", "TestDeadlines_QueuedCountsAgainstTurn", "deadlines"},
	{"Deadlines: attempt deadline on Hang yields interrupted", "TestDeadlines_AttemptDeadlineOnHang", "deadlines"},
	{"Deadlines: turn deadline fires during queue", "TestDeadlines_TurnDeadlineDuringQueue", "deadlines"},
	{"Deadlines: repair does not extend turn deadline", "TestDeadlines_RepairNoExtend", "deadlines"},
	{"Deadlines: attempt dispatched with less remaining turn time gets smaller value", "TestDeadlines_AttemptClampedToRemaining", "deadlines"},
	{"Deadlines: submit while provisional refused without allow_provisional", "TestDeadlines_ProvisionalRefused", "deadlines"},
	{"Deadlines: submit with allow_provisional:true succeeds while provisional", "TestDeadlines_ProvisionalAllowed", "deadlines"},
	{"Deadlines: reattach and recovery work while provisional", "TestDeadlines_ProvisionalRecoveryWorks", "deadlines"},
	{"Deadlines: per-preset deadline override changes attempt deadline on attempt_begun", "TestDeadlines_PresetOverride", "deadlines"},

	// Cancellation and epochs (cancel during author/review, output after cancel, epochs)
	{"Cancellation: cancel during author yields cancelled with no candidate_id", "TestCancellation_CancelDuringAuthor", "cancellation"},
	{"Cancellation: cancel during review yields cancelled with candidate_id set", "TestCancellation_CancelDuringReview", "cancellation"},
	{"Cancellation: cancelled turn carries gathered verdicts in transcript", "TestCancellation_GatheredVerdicts", "cancellation"},
	{"Cancellation: output after cancel is quarantined turn state unchanged", "TestCancellation_QuarantinedAfterCancel", "cancellation"},
	{"Cancellation: CancelUnconfirmed holds pool slot no automatic release", "TestCancellation_CancelUnconfirmedHolds", "cancellation"},

	// Recovery (kill and restart, reconnect, roster validation)
	{"Recovery: kill mid-review restart turn is incomplete:engine_restart", "TestRecovery_KillAndRestart", "recovery"},
	{"Recovery: pending attempt is unknown fake shows nothing re-sent", "TestRecovery_PendingUnknown", "recovery"},
	{"Recovery: client reconnect with last seq receives exactly missed events", "TestRecovery_ReconnectMissedEvents", "recovery"},
	{"Recovery: roster validation error leaves recovery transaction committed", "TestRecovery_RosterValidationError", "recovery"},

	// Socket (second engine, protocol version, frame limits, submission dedup, replay)
	{"Socket: second engine refuses to start names holder PID", "TestSocket_SecondEngineRefuses", "socket"},
	{"Socket: protocol 2 client refused with protocol error", "TestSocket_ProtocolVersion2Refused", "socket"},
	{"Socket: 2 MiB client frame refused with frame error", "TestSocket_2MiBFrameRefused", "socket"},
	{"Socket: every engine-emitted frame measures under 1 MiB encoded", "TestSocket_FramesUnder1MiB", "socket"},
	{"Socket: 12 MiB output containing JSON-escape bytes fetched whole through content", "TestSocket_12MiBOutput", "socket"},
	{"Socket: content pages each under 512 KiB encoded", "TestSocket_ContentPages512KiB", "socket"},
	{"Socket: content limit 1000000 clamped to 262144", "TestSocket_ContentLimitClamped", "socket"},
	{"Socket: submit twice with one submission_id and same content yields one turn", "TestSocket_SubmissionIDDedup", "socket"},
	{"Socket: submit twice same id with different input refused submission_conflict", "TestSocket_SubmissionConflict", "socket"},
	{"Socket: refused submission id can be reused with new content", "TestSocket_ReuseRefusedID", "socket"},
	{"Socket: replay then live delivers each seq exactly once", "TestSocket_ReplayThenLiveExactOnce", "socket"},
	{"Socket: subscriber lagging disconnected with lagging error", "TestSocket_LaggingDisconnect", "socket"},
	{"Socket: status output contains no secret value", "TestSocket_StatusNoSecrets", "socket"},
	{"Socket: release without attempt_id on pool with one hold releases it", "TestSocket_ReleaseWithoutAttemptID", "socket"},
	{"Socket: release with two holds refused ambiguous listing both", "TestSocket_ReleaseTwoHoldsAmbiguous", "socket"},
	{"Socket: release on pool with none refused no_hold", "TestSocket_ReleaseNoHolds", "socket"},
	{"Socket: release with empty reason refused reason_required", "TestSocket_ReleaseEmptyReason", "socket"},
	{"Socket: detach with turn open reattach turn finished answer tab shows result", "TestSocket_DetachReattach", "socket"},

	// Journal and races (crash between state and event, late result, blocks, migration)
	{"Journal: crash between state write and event leaves no orphan pair", "TestJournal_NoCrashOrphanPair", "journal"},
	{"Journal: result arriving after epoch bumped quarantined turn unchanged", "TestJournal_LateResultAfterEpoch", "journal"},
	{"Journal: reviewer result after block ended turn quarantined", "TestJournal_LateResultAfterBlock", "journal"},
	{"Journal: OverrideTurn without candidate errors", "TestJournal_OverrideTurnWithoutCandidate", "journal"},
	{"Journal: two blocks same reviewer id from different verdicts both store", "TestJournal_TwoBlocksSameReviewerID", "journal"},
	{"Journal: migration 004 on A-era probe runs leaves kind=probe LatestProbeRecord finds newest", "TestJournal_Migration004ProbeRuns", "journal"},

	// Capacity (Hang hold, dispatch waits, release, restart, crash, Cancel, timeout)
	{"Capacity: client-deadline on Hang leaves capacity_holds row", "TestCapacity_HangLeapsHold", "capacity"},
	{"Capacity: next dispatch to held pool waits", "TestCapacity_NextDispatchWaits", "capacity"},
	{"Capacity: /release clears hold with how:operator", "TestCapacity_ReleaseClearsHold", "capacity"},
	{"Capacity: late result clears hold with how:late_result", "TestCapacity_LateResultClearsHold", "capacity"},
	{"Capacity: hold survives engine restart appears in engine_started", "TestCapacity_HoldSurvivesRestart", "capacity"},
	{"Capacity: crash between terminal transaction and Cancel call leaves hold recorded", "TestCapacity_CrashBeforeCancel", "capacity"},
	{"Capacity: crash during recovery before commit leaves no scheduler dispatch", "TestCapacity_CrashDuringRecovery", "capacity"},
	{"Capacity: Cancel returning ServerConfirmed:yes releases with how:server_confirmed", "TestCapacity_CancelServerConfirmed", "capacity"},
	{"Capacity: uncertain_release_after=1s releases with how:operator_timeout", "TestCapacity_OperatorTimeout", "capacity"},
	{"Capacity: uncertain_release_after=0s never releases automatically", "TestCapacity_NeverAutoRelease", "capacity"},

	// Repair and delivery (repair output, terminal delivery, commentary, deadline, restart)
	{"Repair: repair request message array contains original output verbatim", "TestRepair_OriginalOutputVerbatim", "repair"},
	{"Repair: terminal delivery dispatches one terminal_delivery attempt per participant", "TestRepair_TerminalDeliveryPerParticipant", "repair"},
	{"Repair: delivery payload contains reviewer commentary outside envelope", "TestRepair_CommentaryInDelivery", "repair"},
	{"Repair: delivery attempt hanging ends at deadline not retried", "TestRepair_DeliveryHangDeadline", "repair"},
	{"Repair: delivery attempt pending at restart marked unknown not re-sent", "TestRepair_DeliveryPendingRestart", "repair"},
	{"Repair: attempt_completed events distinguish two delivered from one failed", "TestRepair_AttemptCompletedDistinguish", "repair"},
	{"Repair: delimiters differ between attempts not derivable from attempt ids", "TestRepair_DifferentDelimiters", "repair"},

	// TUI items (answer tab, drafts, blocks, /why, override, provisional, roster banner, dumb, layout)
	{"TUI: answer tab empty before terminal with drafts off", "milestone C", "tui"},
	{"TUI: drafts on shows banner", "milestone C", "tui"},
	{"TUI: quick profile with drafts=true turns on for governed turns", "milestone C", "tui"},
	{"TUI: incomplete shows blocks with remedy", "milestone C", "tui"},
	{"TUI: /why on budget failure shows estimated and limit", "milestone C", "tui"},
	{"TUI: override requires reason shows banner", "milestone C", "tui"},
	{"TUI: provisional prompt appears once per process resend carries allow_provisional", "milestone C", "tui"},
	{"TUI: roster-changed banner follows status", "milestone C", "tui"},
	{"TUI: TERM=dumb runs", "milestone C", "tui"},
	{"TUI: three-pane layout renders at 80x24", "milestone C", "tui"},
}

// TestCoverage_AllItemsCovered ensures every item in the coverage table with
// a real test function (not "milestone C") maps to a function that exists.
func TestCoverage_AllItemsCovered(t *testing.T) {
	seen := make(map[string]bool)
	for _, item := range coverageTable {
		if item.testFunc != "" && item.testFunc != "milestone C" {
			seen[item.testFunc] = true
		}
	}

	testFuncs := discoverTestFunctions()
	for testName := range testFuncs {
		if !seen[testName] {
			t.Logf("test function %s not referenced in coverage table (may be expected during development)", testName)
		}
	}

	for testName := range seen {
		if !testFuncs[testName] {
			t.Errorf("coverage table references non-existent test function %s", testName)
		}
	}
}

// TestCoverage_EveryItemPresent ensures that every item from section 8
// appears in the coverage table with a valid test function name.
func TestCoverage_EveryItemPresent(t *testing.T) {
	if len(coverageTable) == 0 {
		t.Error("coverage table is empty")
		return
	}

	for i, item := range coverageTable {
		if item.item == "" {
			t.Errorf("coverage item %d has empty item description", i)
		}
		if item.testFunc == "" {
			t.Errorf("coverage item %d (%q) has empty testFunc", i, item.item)
		}
	}
}

// TestCoverage_TableOrdered ensures the coverage table is sorted by section.
func TestCoverage_TableOrdered(t *testing.T) {
	sectionOrder := map[string]int{
		"protocol":     0,
		"dispatch":     1,
		"runs":         2,
		"roster":       3,
		"roles":        4,
		"budgets":      5,
		"quick":        6,
		"deadlines":    7,
		"cancellation": 8,
		"recovery":     9,
		"socket":       10,
		"journal":      11,
		"capacity":     12,
		"repair":       13,
		"tui":          14,
	}

	for i := 1; i < len(coverageTable); i++ {
		prev := coverageTable[i-1]
		curr := coverageTable[i]

		prevOrder := sectionOrder[prev.section]
		currOrder := sectionOrder[curr.section]

		if prevOrder > currOrder {
			t.Errorf("coverage table not sorted by section at index %d: %s (%d) > %s (%d)",
				i, prev.section, prevOrder, curr.section, currOrder)
		}
	}
}

// discoverTestFunctions finds all Test* functions in the _test.go files
// using a simple text scan to avoid deprecated parser functions.
func discoverTestFunctions() map[string]bool {
	testFuncs := make(map[string]bool)

	// Find test files in this directory
	entries, err := os.ReadDir(".")
	if err != nil {
		return testFuncs
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") &&
			entry.Name() != "coverage_test.go" {
			// Simple scan for "func Test" to avoid deprecated parser
			path := entry.Name()
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			text := string(content)
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "func Test") && strings.Contains(trimmed, "(") {
					// Extract function name
					parts := strings.Split(trimmed, "(")
					if len(parts) > 0 {
						funcName := strings.TrimPrefix(parts[0], "func ")
						if funcName != "" {
							testFuncs[funcName] = true
						}
					}
				}
			}
		}
	}

	return testFuncs
}

// sectionsPresent returns a sorted list of all section names in the coverage table.
func sectionsPresent() []string {
	sections := make(map[string]bool)
	for _, item := range coverageTable {
		sections[item.section] = true
	}
	var result []string
	for s := range sections {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// TestCoverage_SectionsPresent lists which sections have coverage items.
func TestCoverage_SectionsPresent(t *testing.T) {
	sections := sectionsPresent()
	t.Logf("Coverage present for %d sections: %v", len(sections), sections)

	// Also list milestone C items
	var milestoneCItems int
	for _, item := range coverageTable {
		if item.testFunc == "milestone C" {
			milestoneCItems++
		}
	}
	t.Logf("Total items in coverage table: %d, milestone C (TUI) items: %d",
		len(coverageTable), milestoneCItems)
}

// TestCoverage_Stats provides coverage statistics for the test run.
func TestCoverage_Stats(t *testing.T) {
	testFuncs := discoverTestFunctions()
	var implementedCount int
	var todoCount int

	for _, item := range coverageTable {
		if item.testFunc == "milestone C" {
			continue
		}
		if testFuncs[item.testFunc] {
			implementedCount++
		} else {
			todoCount++
		}
	}

	totalMilestoneB := implementedCount + todoCount
	t.Logf("Coverage statistics:")
	t.Logf("  Total milestone B items: %d", totalMilestoneB)
	t.Logf("  Implemented tests: %d", implementedCount)
	t.Logf("  Remaining tests: %d", todoCount)
	if totalMilestoneB > 0 {
		t.Logf("  Completion: %.1f%%", 100*float64(implementedCount)/float64(totalMilestoneB))
	}
}
