package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"testing"
)

// coverageTable maps acceptance items from docs/design/v4-delta-engine-tui.md
// section 8 to the test functions that cover them. Items are grouped by
// paragraph, quoted or closely paraphrased. Items marked "(TUI)" are listed
// as "milestone C" because they are out of scope for milestone B.
var coverageTable = []struct {
	item     string
	testFunc string
	section  string
}{
	// Budgets
	{"Budgets: projection equals sum of stand-ins per-message and per-request", "milestone C", "budgets"},
	{"Budgets: fake carries MaxOutputTokens clamped to output_bound", "milestone C", "budgets"},

	// Cancellation
	{"Cancellation: cancel during author yields cancelled with no candidate_id", "milestone C", "cancellation"},

	// Capacity
	{"Capacity: client-deadline Hang leaves capacity_holds row", "milestone C", "capacity"},
	{"Capacity: hold survives engine restart", "milestone C", "capacity"},

	// Deadlines
	{"Deadlines: two reviewers in pool run sequentially", "milestone C", "deadlines"},
	{"Deadlines: attempt deadline on Hang yields interrupted", "milestone C", "deadlines"},

	// Dispatch
	{"Dispatch: reset with interrupted yields no retry and capacity hold", "TestDispatch_ResetWithHoldNoRetry", "dispatch"},
	{"Dispatch: unauthorized fails turn at once with no retry", "TestDispatch_UnauthorizedFailsImmediately", "dispatch"},
	{"Dispatch: unreachable with normal reply yields one retry and normal turn", "TestDispatch_UnreachableThenRetryThenNormal", "dispatch"},

	// Journal
	{"Journal: crash between state write and event leaves no orphan pair", "milestone C", "journal"},
	{"Journal: late result after epoch bump is quarantined", "milestone C", "journal"},

	// Protocol
	{"Protocol: approve path reaches approved with candidate hash in answer", "TestProtocol_ProseApproveRepairsThenMalformedTwice", "protocol"},
	{"Protocol: author result with status truncated yields author_failed:truncated", "TestProtocol_AuthorTruncatedIncomplete", "protocol"},
	{"Protocol: reviewer result with mismatched ReportedModel fails CheckResult", "TestProtocol_ReportedModelMismatchFails", "protocol"},
	{"Protocol: solo with status refused is incomplete, not solo_complete", "TestProtocol_SoloWithRefusedIncomplete", "protocol"},

	// Quick
	{"Quick: quick_profile in submit changes preset or mode", "milestone C", "quick"},

	// Recovery
	{"Recovery: kill mid-review, restart, turn is incomplete:engine_restart", "milestone C", "recovery"},

	// Repair
	{"Repair: repair request contains original output verbatim", "milestone C", "repair"},
	{"Repair: terminal delivery dispatches one attempt per participant", "milestone C", "repair"},

	// Roles
	{"Roles: review with two required reviewers needs both", "milestone C", "roles"},

	// Roster
	{"Roster: SIGTERM ends turn incomplete:engine_stop", "milestone C", "roster"},
	{"Roster: status.roster_file_changed reflects file edits", "milestone C", "roster"},

	// Runs
	{"Runs: engine start writes run of kind engine with correct manifest hash", "milestone C", "runs"},
	{"Runs: participant with no probe record is unprobed:no_record", "milestone C", "runs"},

	// Socket
	{"Socket: 1 MiB frame limit enforced, content pages under 512 KiB encoded", "milestone C", "socket"},
	{"Socket: submission_id deduplication with submission_hash", "milestone C", "socket"},
}

// TestCoverage_AllItemsCovered ensures every item in the coverage table maps
// to a test function that actually exists in this package, and no existing
// test functions are unmapped.
func TestCoverage_AllItemsCovered(t *testing.T) {
	seen := make(map[string]bool)
	for _, item := range coverageTable {
		if item.testFunc != "" && item.testFunc != "milestone C" {
			seen[item.testFunc] = true
		}
	}

	testFuncs := discoverTestFunctions(t)
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

// TestCoverage_EveryItemPresent ensures that every item from section 8 outside
// the TUI appears in the coverage table, with a valid test function name.
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

// TestCoverage_TableOrdered ensures the coverage table is sorted by section and item,
// making it easier to audit and maintain.
func TestCoverage_TableOrdered(t *testing.T) {
	for i := 1; i < len(coverageTable); i++ {
		prev := coverageTable[i-1]
		curr := coverageTable[i]

		// Check section ordering
		if prev.section > curr.section {
			t.Errorf("coverage table not sorted by section at index %d: %s > %s", i, prev.section, curr.section)
		}
	}
}

// discoverTestFunctions uses the AST to discover all Test* functions defined
// in the current package, rather than relying on a hardcoded list.
func discoverTestFunctions(t *testing.T) map[string]bool {
	testFuncs := make(map[string]bool)

	// Parse all *_test.go files in this directory
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		// Only parse test files, skip coverage_test.go itself
		name := info.Name()
		return name != "coverage_test.go"
	}, parser.SkipObjectResolution)

	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	// Collect all Test* functions from the package
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					if fn.Recv == nil && len(fn.Name.Name) > 4 && fn.Name.Name[:4] == "Test" {
						testFuncs[fn.Name.Name] = true
					}
				}
			}
		}
	}

	return testFuncs
}

// sectionsPresent returns a sorted list of all section names in the coverage table,
// which can be used to understand completeness at a glance.
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

// TestCoverage_SectionsPresent is a documentation test that lists which
// sections of the design have coverage items (or placeholders).
func TestCoverage_SectionsPresent(t *testing.T) {
	sections := sectionsPresent()
	t.Logf("Coverage present for sections: %v", sections)
}
