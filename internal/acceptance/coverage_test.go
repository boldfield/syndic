package acceptance

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// entry is one design item and the test that covers it.
type entry struct {
	paragraph, item, test string
}

var registry []entry

// cover records that the named test covers one item of a section 8 paragraph.
// Each test file calls it from an init function for the items it owns. test
// is a function name in this package, or <import path suffix>.<TestName> for
// an item about a command binary that a test in that package already proves
// exactly, the suffix being the package directory below the module root (for
// example cmd/syndicd.TestSecondSignalExits), or "milestone C" for a TUI item
// not yet implemented.
func cover(paragraph, item, test string) {
	registry = append(registry, entry{paragraph, item, test})
}

// srcFile is a parsed Go source file.
type srcFile struct {
	name string
	fset *token.FileSet
	file *ast.File
}

// parseFiles parses every Go file in dir whose name matches pattern.
func parseFiles(dir, pattern string) ([]srcFile, error) {
	names, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []srcFile
	for _, name := range names {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		out = append(out, srcFile{name: name, fset: fset, file: f})
	}
	return out, nil
}

// testFuncs returns the names of the top-level Test functions of the files.
func testFuncs(files []srcFile) map[string]bool {
	out := map[string]bool{}
	for _, sf := range files {
		for _, d := range sf.file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

// moduleRoot finds the directory holding go.mod above dir.
func moduleRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if d == filepath.Dir(d) {
			return "", errors.New("no go.mod above " + dir)
		}
	}
}

// paragraphPrefix is the word tests of a paragraph start with: Test<Prefix>_.
func paragraphPrefix(paragraph string) string {
	return strings.Fields(paragraph)[0]
}

// namedTestProblems checks the registry against the tests that exist: every
// registered test exists (unless it's "milestone C"), no test covers two items,
// and no Test<Paragraph>_ function of this package is left unregistered.
// "milestone C" is accepted only for specific items: TUI paragraph, Quick profile
// items starting with "(TUI)", and Socket answer-tab item. external loads the test
// files of another package by the directory below the module root.
func namedTestProblems(reg []entry, own []srcFile, external func(dir string) ([]srcFile, error)) []string {
	var problems []string
	ownTests := testFuncs(own)
	used := map[string]entry{}
	prefixes := map[string]bool{}
	registered := map[string]bool{}

	// Define where "milestone C" is accepted
	isMilestoneCAllowed := func(paragraph, item string) bool {
		if paragraph == "TUI" {
			return true
		}
		if paragraph == "Quick profile" && strings.HasPrefix(item, "(TUI)") {
			return true
		}
		if paragraph == "Socket" && strings.HasPrefix(item, "(TUI)") {
			return true
		}
		return false
	}

	for _, e := range reg {
		prefixes[paragraphPrefix(e.paragraph)] = true

		// Check milestone C is only used where allowed
		if e.test == "milestone C" && !isMilestoneCAllowed(e.paragraph, e.item) {
			problems = append(problems, fmt.Sprintf("%q of %q: milestone C is not allowed for this item", e.item, e.paragraph))
		}

		if prev, dup := used[e.test]; dup && e.test != "milestone C" {
			problems = append(problems, fmt.Sprintf("%s names %s, which already covers %q of %q", e.item, e.test, prev.item, prev.paragraph))
		}
		used[e.test] = e

		// Skip external package check and existence check for "milestone C"
		if e.test == "milestone C" {
			continue
		}

		if i := strings.LastIndex(e.test, "."); i >= 0 {
			dir, name := e.test[:i], e.test[i+1:]
			files, err := external(dir)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%q of %q: package %s: %v", e.item, e.paragraph, dir, err))
			} else if !testFuncs(files)[name] {
				problems = append(problems, fmt.Sprintf("%q of %q: %s has no test %s", e.item, e.paragraph, dir, name))
			}
			continue
		}
		registered[e.test] = true
		if !ownTests[e.test] {
			problems = append(problems, fmt.Sprintf("%q of %q: no test %s in this package", e.item, e.paragraph, e.test))
		}
	}
	for name := range ownTests {
		for prefix := range prefixes {
			if strings.HasPrefix(name, "Test"+prefix+"_") && !registered[name] {
				problems = append(problems, fmt.Sprintf("%s is a %s test that no item registers", name, prefix))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// skipNames are the testing methods that skip.
var skipNames = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

// bareSkipProblems reports every call of Skip, Skipf or SkipNow in the files
// except inside the function gap, wherever it appears: in a test, a helper, a
// closure or a subtest.
func bareSkipProblems(files []srcFile) []string {
	var problems []string
	for _, sf := range files {
		for _, d := range sf.file.Decls {
			fn, isFunc := d.(*ast.FuncDecl)
			if isFunc && fn.Recv == nil && fn.Name.Name == "gap" {
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && skipNames[sel.Sel.Name] {
					problems = append(problems, fmt.Sprintf("%s: %s outside gap", sf.fset.Position(call.Pos()), sel.Sel.Name))
				}
				return true
			})
		}
	}
	return problems
}

// gapCall is a test that calls gap, with the message it gives.
type gapCall struct {
	test, message string
}

// gapCalls finds every call of gap inside a Test function.
func gapCalls(files []srcFile) []gapCall {
	var out []gapCall
	for _, sf := range files {
		for _, d := range sf.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "gap" {
					msg := "(message is not a string literal)"
					if len(call.Args) == 2 {
						if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if s, err := strconv.Unquote(lit.Value); err == nil {
								msg = s
							}
						}
					}
					out = append(out, gapCall{fn.Name.Name, msg})
				}
				return true
			})
		}
	}
	return out
}

func ownFiles(t *testing.T) []srcFile {
	t.Helper()
	files, err := parseFiles(".", "*.go")
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	return files
}

func externalTests(t *testing.T) func(dir string) ([]srcFile, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := moduleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	return func(dir string) ([]srcFile, error) {
		files, err := parseFiles(filepath.Join(root, filepath.FromSlash(dir)), "*_test.go")
		if err == nil && len(files) == 0 {
			err = errors.New("no test files")
		}
		return files, err
	}
}

func TestCoverage_NamedTestsExist(t *testing.T) {
	for _, p := range namedTestProblems(registry, ownFiles(t), externalTests(t)) {
		t.Error(p)
	}
}

func TestCoverage_NoBareSkips(t *testing.T) {
	for _, p := range bareSkipProblems(ownFiles(t)) {
		t.Error(p)
	}
}

// TestCoverage_Gaps lists every test that calls gap, so the gaps are visible
// in test output.
func TestCoverage_Gaps(t *testing.T) {
	gaps := gapCalls(ownFiles(t))
	if len(gaps) == 0 {
		t.Log("no gaps: every registered test passes against the engine")
	}
	for _, g := range gaps {
		t.Logf("gap: %s: %s", g.test, g.message)
	}
}

// TestCoverage_ChecksCatchProblems feeds the checks sources with each kind of
// fault and requires a report, so a check that stopped looking would fail.
func TestCoverage_ChecksCatchProblems(t *testing.T) {
	parse := func(src string) []srcFile {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic_test.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		return []srcFile{{name: "synthetic_test.go", fset: fset, file: f}}
	}
	none := func(string) ([]srcFile, error) { return nil, errors.New("no such package") }
	files := parse(`package p
import "testing"
func TestProto_Registered(t *testing.T) {}
func TestProto_Forgotten(t *testing.T) {}
`)
	reg := []entry{
		{"Proto things", "an item", "TestProto_Registered"},
		{"Proto things", "another item", "TestProto_Registered"},
		{"Proto things", "a missing one", "TestProto_Missing"},
		{"Proto things", "an external one", "cmd/nowhere.TestX"},
	}
	problems := strings.Join(namedTestProblems(reg, files, none), "\n")
	for _, want := range []string{"already covers", "no test TestProto_Missing", "cmd/nowhere", "TestProto_Forgotten is a Proto test that no item registers"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems lack %q:\n%s", want, problems)
		}
	}
	if got := namedTestProblems([]entry{{"Proto things", "an item", "TestProto_Registered"}}, parse("package p\nfunc TestProto_Registered() {}\n"), none); len(got) != 0 {
		t.Errorf("a complete registry reported %v", got)
	}

	// Test milestone C handling
	mcBadReg := []entry{
		{"TUI", "a TUI item", "milestone C"},                  // allowed
		{"Quick profile", "(TUI) a TUI item", "milestone C"},  // allowed
		{"Socket", "(TUI) an answer tab item", "milestone C"}, // allowed
		{"Budgets", "an item", "milestone C"},                 // not allowed
	}
	mcProblems := strings.Join(namedTestProblems(mcBadReg, parse("package p"), none), "\n")
	if !strings.Contains(mcProblems, "milestone C is not allowed") {
		t.Errorf("milestone C check failed to catch disallowed use: %s", mcProblems)
	}

	skips := parse(`package p
import "testing"
func gap(t *testing.T, what string) { t.Skip(what) }
func TestA(t *testing.T) { t.Skip("x") }
func helper(t *testing.T) { t.Skipf("x") }
func TestB(t *testing.T) { t.Run("sub", func(t *testing.T) { t.SkipNow() }) }
func TestC(t *testing.T) { gap(t, "the engine does Y") }
`)
	if got := bareSkipProblems(skips); len(got) != 3 {
		t.Errorf("bare skips found = %v, want the three outside gap", got)
	}
	if got := gapCalls(skips); len(got) != 1 || got[0].test != "TestC" || got[0].message != "the engine does Y" {
		t.Errorf("gapCalls = %+v", got)
	}
}
