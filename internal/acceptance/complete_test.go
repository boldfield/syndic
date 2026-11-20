package acceptance

import "testing"

func init() {
	// TUI paragraph items (milestone C) - these are entirely TUI
	cover("TUI", "answer tab is empty before terminal state with drafts off", "milestone C")
	cover("TUI", "drafts on shows the banner", "milestone C")
	cover("TUI", "a quick profile with `drafts = true` turns it on for governed turns", "milestone C")
	cover("TUI", "incomplete shows blocks with remedy", "milestone C")
	cover("TUI", "`/why` on a budget failure shows estimated and limit", "milestone C")
	cover("TUI", "override requires a reason and shows the banner", "milestone C")
	cover("TUI", "the provisional prompt appears once per process and the resend carries `allow_provisional`", "milestone C")
	cover("TUI", "the roster-changed banner follows `status`", "milestone C")
	cover("TUI", "`TERM=dumb` runs", "milestone C")
	cover("TUI", "the three-pane layout renders at 80×24", "milestone C")

	// Quick profile items marked "(TUI)" (milestone C)
	cover("Quick profile", "(TUI) a project file is found from a subdirectory, shown, asked about once, recorded by path, file hash, profile name and definition hash, and sent on the next submit", "milestone C")
	cover("Quick profile", "(TUI) a rewritten file asks again", "milestone C")
	cover("Quick profile", "(TUI) an unchanged file whose profile's `preset` changed in the roster (a different `definition_sha256` in `status.quick_profiles`) asks again", "milestone C")
	cover("Quick profile", "(TUI) a file naming a profile the manifest no longer has asks again and is not sent", "milestone C")
	cover("Quick profile", "(TUI) an unapproved file is not sent and the strip shows `quick: unapproved`", "milestone C")
	cover("Quick profile", "(TUI) `/quick off` stops sending it for that process", "milestone C")
	cover("Quick profile", "(TUI) the strip shows `quick:x`", "milestone C")

	// Socket items marked "(TUI)" - the answer tab part of detach/reattach
	cover("Socket", "(TUI) detach with a turn open, reattach, turn finished meanwhile, answer tab shows the result", "milestone C")
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
		"TUI":                     tuiCount,
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
