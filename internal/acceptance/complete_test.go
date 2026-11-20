package acceptance

import (
	"testing"
)

const (
	tuiParagraph = "TUI"
)

func init() {
	// TUI paragraph items (milestone C) - these are entirely TUI
	cover(tuiParagraph, "answer tab is empty before terminal state with drafts off", "TestTUI_AnswerTabEmptyWithDraftsOff")
	cover(tuiParagraph, "drafts on shows the banner", "TestTUI_DraftsOnBanner")
	cover(tuiParagraph, "a quick profile with `drafts = true` turns it on for governed turns", "TestTUI_QuickProfileDraftsDefault")
	cover(tuiParagraph, "incomplete shows blocks with remedy", "TestTUI_IncompleteShowsBlocks")
	cover(tuiParagraph, "`/why` on a budget failure shows estimated and limit", "TestTUI_WhyBudgetFailure")
	cover(tuiParagraph, "override requires a reason and shows the banner", "TestTUI_OverrideRequiresReason")
	cover(tuiParagraph, "the provisional prompt appears once per process and the resend carries `allow_provisional`", "TestTUI_ProvisionalPrompt")
	cover(tuiParagraph, "the roster-changed banner follows `status`", "TestTUI_RosterChangedBanner")
	cover(tuiParagraph, "`TERM=dumb` runs", "TestTUI_TermDumb")
	cover(tuiParagraph, "the three-pane layout renders at 80×24", "TestTUI_ThreePaneLayout")

	// Quick profile items marked "(TUI)" (milestone C) - will be counted as part of "Quick profile" paragraph
	cover("Quick profile", "(TUI) a project file is found from a subdirectory, shown, asked about once, recorded by path, file hash, profile name and definition hash, and sent on the next submit", "TestTUI_ProjectFileDiscovery")
	cover("Quick profile", "(TUI) a rewritten file asks again", "TestTUI_RewrittenFileAsksAgain")
	cover("Quick profile", "(TUI) an unchanged file whose profile's `preset` changed in the roster (a different `definition_sha256` in `status.quick_profiles`) asks again", "TestTUI_ProfileDefChangedAsksAgain")
	cover("Quick profile", "(TUI) a file naming a profile the manifest no longer has asks again and is not sent", "TestTUI_MissingProfileAsksAgain")
	cover("Quick profile", "(TUI) an unapproved file is not sent and the strip shows `quick: unapproved`", "TestTUI_UnapprovedFileNotSent")
	cover("Quick profile", "(TUI) `/quick off` stops sending it for that process", "TestTUI_QuickOffStopsSending")
	cover("Quick profile", "(TUI) the strip shows `quick:x`", "TestTUI_StripShowsQuickName")

	// Socket items marked "(TUI)" - the answer tab part of detach/reattach
	// will be counted as part of "Socket" paragraph
	cover("Socket", "(TUI) detach with a turn open, reattach, turn finished meanwhile, answer tab shows the result", "TestTUI_AnswerTabAfterReattach")
}

// TestTUI_AnswerTabEmptyWithDraftsOff verifies the TUI shows the answer tab empty
// before terminal state when drafts are off.
func TestTUI_AnswerTabEmptyWithDraftsOff(t *testing.T) {
	gap(t, "TUI implementation (milestone C): answer tab display logic")
}

// TestTUI_DraftsOnBanner verifies the TUI shows a banner when drafts are on.
func TestTUI_DraftsOnBanner(t *testing.T) {
	gap(t, "TUI implementation (milestone C): drafts toggle banner")
}

// TestTUI_QuickProfileDraftsDefault verifies a quick profile with drafts=true
// sets the default for governed turns.
func TestTUI_QuickProfileDraftsDefault(t *testing.T) {
	gap(t, "TUI implementation (milestone C): quick profile drafts default")
}

// TestTUI_IncompleteShowsBlocks verifies the TUI shows blocks with remedy on
// incomplete turn.
func TestTUI_IncompleteShowsBlocks(t *testing.T) {
	gap(t, "TUI implementation (milestone C): incomplete blocks display")
}

// TestTUI_WhyBudgetFailure verifies /why command shows estimated and limit on
// budget failure.
func TestTUI_WhyBudgetFailure(t *testing.T) {
	gap(t, "TUI implementation (milestone C): /why command")
}

// TestTUI_OverrideRequiresReason verifies override requires a reason and shows
// the banner.
func TestTUI_OverrideRequiresReason(t *testing.T) {
	gap(t, "TUI implementation (milestone C): override banner")
}

// TestTUI_ProvisionalPrompt verifies provisional prompt appears once per
// process and resend carries allow_provisional.
func TestTUI_ProvisionalPrompt(t *testing.T) {
	gap(t, "TUI implementation (milestone C): provisional deadline prompt")
}

// TestTUI_RosterChangedBanner verifies roster-changed banner follows status.
func TestTUI_RosterChangedBanner(t *testing.T) {
	gap(t, "TUI implementation (milestone C): roster changed banner")
}

// TestTUI_TermDumb verifies the TUI runs in TERM=dumb mode.
func TestTUI_TermDumb(t *testing.T) {
	gap(t, "TUI implementation (milestone C): TERM=dumb support")
}

// TestTUI_ThreePaneLayout verifies the three-pane layout renders at 80×24.
func TestTUI_ThreePaneLayout(t *testing.T) {
	gap(t, "TUI implementation (milestone C): three-pane layout at 80×24")
}

// TestTUI_ProjectFileDiscovery verifies quick profile project file discovery.
func TestTUI_ProjectFileDiscovery(t *testing.T) {
	gap(t, "TUI implementation (milestone C): quick profile file discovery and approval")
}

// TestTUI_RewrittenFileAsksAgain verifies rewritten quick profile file asks
// again.
func TestTUI_RewrittenFileAsksAgain(t *testing.T) {
	gap(t, "TUI implementation (milestone C): rewritten file re-asks")
}

// TestTUI_ProfileDefChangedAsksAgain verifies changed profile definition asks
// again.
func TestTUI_ProfileDefChangedAsksAgain(t *testing.T) {
	gap(t, "TUI implementation (milestone C): profile definition change re-asks")
}

// TestTUI_MissingProfileAsksAgain verifies missing profile asks again.
func TestTUI_MissingProfileAsksAgain(t *testing.T) {
	gap(t, "TUI implementation (milestone C): missing profile re-asks")
}

// TestTUI_UnapprovedFileNotSent verifies unapproved file is not sent.
func TestTUI_UnapprovedFileNotSent(t *testing.T) {
	gap(t, "TUI implementation (milestone C): unapproved file handling")
}

// TestTUI_QuickOffStopsSending verifies /quick off stops sending profile.
func TestTUI_QuickOffStopsSending(t *testing.T) {
	gap(t, "TUI implementation (milestone C): /quick off command")
}

// TestTUI_StripShowsQuickName verifies status strip shows quick profile name.
func TestTUI_StripShowsQuickName(t *testing.T) {
	gap(t, "TUI implementation (milestone C): status strip quick profile display")
}

// TestTUI_AnswerTabAfterReattach verifies answer tab shows result after
// reattach when turn finished.
func TestTUI_AnswerTabAfterReattach(t *testing.T) {
	gap(t, "TUI implementation (milestone C): answer tab display after reattach")
}

// TestCoverage_ParagraphCounts verifies that every clause in section 8 of the
// design is registered as a coverage item and that the number of registered
// items matches the number of clauses in the design.
func TestCoverage_ParagraphCounts(t *testing.T) {
	const (
		protocolCount       = 10
		dispatchCount       = 9
		runsManifestCount   = 13
		rosterStopCount     = 10
		nAndRolesCount      = 5
		budgetsCount        = 20
		quickProfileCount   = 11 // 4 non-TUI + 7 TUI
		deadlinesCount      = 7
		cancellationCount   = 4
		recoveryCount       = 3
		socketCount         = 18 // 17 non-TUI + 1 TUI (answer tab part)
		journalCount        = 6
		capacityCount       = 9
		repairDeliveryCount = 7
		tuiCount            = 10
	)

	type paraCount struct {
		name string
		want int
	}

	var tests []paraCount
	for _, e := range registry {
		found := false
		for i, t := range tests {
			if t.name == e.paragraph {
				tests[i].want++
				found = true
				break
			}
		}
		if !found {
			tests = append(tests, paraCount{name: e.paragraph, want: 0})
		}
	}

	expected := map[string]int{
		"Protocol":                protocolCount,
		"Dispatch failures":       dispatchCount,
		"Runs and manifest":       runsManifestCount,
		"Roster and stop":         rosterStopCount,
		"N and roles":             nAndRolesCount,
		"Budgets":                 budgetsCount,
		"Quick profile":           quickProfileCount,
		"Deadlines and pools":     deadlinesCount,
		"Cancellation and epochs": cancellationCount,
		"Recovery":                recoveryCount,
		"Socket":                  socketCount,
		"Journal and races":       journalCount,
		"Capacity":                capacityCount,
		"Repair and delivery":     repairDeliveryCount,
		tuiParagraph:              tuiCount,
	}

	for para, want := range expected {
		got := 0
		for _, e := range registry {
			if e.paragraph == para {
				got++
			}
		}
		if got != want {
			t.Errorf("paragraph %q: registered %d items, section 8 specifies %d", para, got, want)
		}
	}
}
