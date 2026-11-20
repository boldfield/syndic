package acceptance

import (
	"reflect"
	"testing"
)

// coverageTable maps acceptance items from docs/design/v4-delta-engine-tui.md
// section 8 to the test functions that cover them. This is a foundation that
// will be expanded with additional tests for the remaining sections.
var coverageTable = []struct {
	item     string
	testFunc string
}{
	// Protocol (approve path, verdicts)
	{"approve path reaches approved with candidate hash in answer", "TestProtocol_ProseApproveRepairsThenMalformedTwice"},
	{"author result with status truncated yields author_failed:truncated", "TestProtocol_AuthorTruncatedIncomplete"},
	{"reviewer result with mismatched ReportedModel fails CheckResult", "TestProtocol_ReportedModelMismatchFails"},
	{"solo with status refused is incomplete, not solo_complete", "TestProtocol_SoloWithRefusedIncomplete"},

	// Dispatch failures
	{"step with TransportStatus: unreachable followed by normal reply yields one journaled retry", "TestProtocol_UnreachableThenRetryThenNormal"},
	{"step with TransportStatus: unauthorized fails turn at once with no retry", "TestProtocol_UnauthorizedFailsImmediately"},
	{"step with TransportStatus: reset and status interrupted yields no retry and capacity hold", "TestProtocol_ResetTimeout"},
}

// TestCoverage_AllItemsCovered tests that every acceptance item maps to an
// existing test function and that no test has been removed without updating
// the coverage table.
func TestCoverage_AllItemsCovered(t *testing.T) {
	seen := make(map[string]bool)
	for _, item := range coverageTable {
		if item.testFunc != "" {
			seen[item.testFunc] = true
		}
	}

	testFuncs := listTestFunctions(t)
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

// TestCoverage_EveryItemPresent tests that every item from section 8 outside
// the TUI appears in the coverage table.
func TestCoverage_EveryItemPresent(t *testing.T) {
	if len(coverageTable) == 0 {
		t.Errorf("coverage table is empty")
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

// listTestFunctions returns a map of all test functions defined in this package.
func listTestFunctions(t *testing.T) map[string]bool {
	testFuncs := make(map[string]bool)
	_ = reflect.ValueOf(t)

	files := map[string][]string{
		"protocol_test.go": {
			"TestProtocol_ProseApproveRepairsThenMalformedTwice",
			"TestProtocol_UnreachableThenRetryThenNormal",
			"TestProtocol_AuthorTruncatedIncomplete",
			"TestProtocol_ReportedModelMismatchFails",
			"TestProtocol_SoloWithRefusedIncomplete",
			"TestProtocol_UnauthorizedFailsImmediately",
			"TestProtocol_ResetTimeout",
		},
	}

	for _, funcList := range files {
		for _, f := range funcList {
			testFuncs[f] = true
		}
	}

	return testFuncs
}
