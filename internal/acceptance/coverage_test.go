package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	coverageMu sync.Mutex
	coverage   = map[[2]string]string{} // {paragraph, item} -> test name
)

// cover records that the named test covers one item of a section 8 paragraph.
// item is the design's clause, quoted or closely paraphrased.
// test is a function name in this package or <import_suffix>.<TestName>.
func cover(paragraph, item, test string) {
	coverageMu.Lock()
	defer coverageMu.Unlock()
	key := [2]string{paragraph, item}
	if existing, ok := coverage[key]; ok && existing != test {
		panic("duplicate coverage: " + paragraph + ": " + item + " covered by both " + existing + " and " + test)
	}
	coverage[key] = test
}

// TestCoverage_NamedTestsExist verifies that every registered test exists,
// that no two items name the same test, and that every Test<Paragraph> function
// is registered by some item.
func TestCoverage_NamedTestsExist(t *testing.T) {
	coverageMu.Lock()
	registered := make(map[string]bool)
	itemsByTest := make(map[string][]string) // test -> items
	for key, testName := range coverage {
		item := key[1]
		registered[testName] = true
		itemsByTest[testName] = append(itemsByTest[testName], item)
	}
	coverageMu.Unlock()

	// Check for duplicate tests
	for testName, items := range itemsByTest {
		if len(items) > 1 && !strings.Contains(testName, ".") {
			// Only report duplicates for local tests, not external ones
			t.Errorf("test %s registered by multiple items: %v", testName, items)
		}
	}

	// Parse all test files to find test functions
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	definedTests := make(map[string]bool)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			definedTests[fd.Name.Name] = true
		}
	}

	// Check that every registered local test exists
	for testName := range registered {
		if strings.Contains(testName, ".") {
			// External test reference like openai.TestContractOpenAI
			continue
		}
		if !definedTests[testName] {
			t.Errorf("registered test %s does not exist", testName)
		}
	}

	// Check that every Test<Paragraph>_ function in this package is registered
	for testName := range definedTests {
		if strings.HasPrefix(testName, "TestProtocol_") || strings.HasPrefix(testName, "TestDispatch_") {
			if !registered[testName] {
				t.Errorf("test function %s is not registered by any item", testName)
			}
		}
	}
}

// TestCoverage_Gaps logs every test that calls gap with its message.
func TestCoverage_Gaps(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	gapCount := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		// Find all functions and check if they contain gap() calls
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			msg := extractGapMessage(fd)
			if msg != "" {
				t.Logf("%s: gap: %s", fd.Name.Name, msg)
				gapCount++
			}
		}
	}
	if gapCount == 0 {
		t.Log("no gaps")
	}
}

// extractGapMessage extracts the message from a gap() call in a function,
// or returns empty string if no gap call is found.
func extractGapMessage(fd *ast.FuncDecl) string {
	// Look for a gap call that's the first statement or only statement
	msg := gapMessageFromStmts(fd.Body.List)
	return msg
}

// gapMessageFromStmts recursively extracts gap message from statements.
func gapMessageFromStmts(stmts []ast.Stmt) string {
	for _, stmt := range stmts {
		if msg := gapMessageFromStmt(stmt); msg != "" {
			return msg
		}
	}
	return ""
}

// gapMessageFromStmt extracts gap message from a single statement.
func gapMessageFromStmt(node ast.Stmt) string {
	switch n := node.(type) {
	case *ast.ExprStmt:
		if ce, ok := n.X.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "gap" {
				// Extract the message argument (second arg)
				if len(ce.Args) >= 2 {
					if lit, ok := ce.Args[1].(*ast.BasicLit); ok {
						// Remove quotes from string literal
						msg := lit.Value
						if len(msg) >= 2 && msg[0] == '"' && msg[len(msg)-1] == '"' {
							return msg[1 : len(msg)-1]
						}
					}
				}
				return "gap called but message not extracted"
			}
		}
	case *ast.IfStmt:
		if msg := gapMessageFromStmt(n.Body); msg != "" {
			return msg
		}
		if n.Else != nil {
			if msg := gapMessageFromStmt(n.Else); msg != "" {
				return msg
			}
		}
	case *ast.BlockStmt:
		if n != nil {
			return gapMessageFromStmts(n.List)
		}
	}
	return ""
}

// TestCoverage_NoBareSkips fails if any file calls t.Skip, t.Skipf, or
// t.SkipNow except inside gap.
func TestCoverage_NoBareSkips(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			if hasBareSkip(fd) {
				t.Errorf("%s: contains bare t.Skip/t.Skipf/t.SkipNow (not in gap)", fd.Name.Name)
			}
		}
	}
}

// hasBareSkip checks if a function has a bare skip (not inside gap).
// Skips are only allowed inside gap calls.
func hasBareSkip(fd *ast.FuncDecl) bool {
	return hasBareSkinStmt(fd.Body.List, false)
}

// hasBareSkinStmt recursively checks for bare skips, tracking whether we're inside a gap call.
func hasBareSkinStmt(stmts []ast.Stmt, inGap bool) bool {
	for _, stmt := range stmts {
		// Check if this statement is a gap call
		if !inGap && isGapCall(stmt) {
			// Skip is allowed inside gap, so we don't recurse with inGap=true
			continue
		}
		if hasBareSkinFromStmt(stmt, inGap) {
			return true
		}
	}
	return false
}

// hasBareSkinFromStmt checks a single statement for bare skips.
func hasBareSkinFromStmt(node ast.Stmt, inGap bool) bool {
	switch n := node.(type) {
	case *ast.ExprStmt:
		return !inGap && isSkipCall(n)
	case *ast.IfStmt:
		if n.Body != nil {
			if hasBareSkinStmt(n.Body.List, inGap) {
				return true
			}
		}
		if n.Else != nil {
			if hasBareSkinStmt([]ast.Stmt{n.Else}, inGap) {
				return true
			}
		}
	case *ast.BlockStmt:
		if n != nil {
			return hasBareSkinStmt(n.List, inGap)
		}
	case *ast.ForStmt:
		if n.Body != nil {
			return hasBareSkinStmt(n.Body.List, inGap)
		}
	case *ast.RangeStmt:
		if n.Body != nil {
			return hasBareSkinStmt(n.Body.List, inGap)
		}
	case *ast.SwitchStmt:
		if n.Body != nil {
			return hasBareSkinStmt(n.Body.List, inGap)
		}
	}
	return false
}

// isGapCall checks if a statement is a gap() call.
func isGapCall(node interface{}) bool {
	es, ok := node.(*ast.ExprStmt)
	if !ok {
		return false
	}
	ce, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := ce.Fun.(*ast.Ident)
	return ok && id.Name == "gap"
}

// isSkipCall checks if a statement is a skip call (t.Skip, t.Skipf, t.SkipNow).
func isSkipCall(node interface{}) bool {
	es, ok := node.(*ast.ExprStmt)
	if !ok {
		return false
	}
	ce, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	// Check for t.Skip, t.Skipf, t.SkipNow
	if se, ok := ce.Fun.(*ast.SelectorExpr); ok {
		if id, ok := se.X.(*ast.Ident); ok && id.Name == "t" {
			return se.Sel.Name == "Skip" || se.Sel.Name == "Skipf" || se.Sel.Name == "SkipNow"
		}
	}
	return false
}
