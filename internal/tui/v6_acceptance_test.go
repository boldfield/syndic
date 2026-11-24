package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const v6DesignSHA256 = "1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7"

// ledgerPackage is one parsed Go package directory: its non-test declarations
// and every function declared in its test files.
type ledgerPackage struct {
	decls map[string]map[string]bool // file -> "Name" and "Recv.Name" declared there
	tests map[string]*ast.FuncDecl   // every top-level function of the test files
}

// TestV6PresentationLedger keeps docs/acceptance/v6-presentation.md honest. It
// proves what a machine can prove: the design file is the ratified one, every
// D-side row names an implementation `file.go:Symbol` that is declared in
// non-test code and a test that exists, and no named test (or same-package test
// helper it calls) skips or returns early under testing.Short(). Every E-side
// row names a V6-E task as implementer and accepter, and the validation
// evidence names a commit. Whether a test proves its clause is a review
// judgement the ledger records, not something this checks.
func TestV6PresentationLedger(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docs", "acceptance", "v6-presentation.md"))
	if err != nil {
		t.Fatal(err)
	}
	ledger := string(raw)

	design, err := os.ReadFile(filepath.Join(root, "docs", "design", "v6-delta-tui-presentation.md"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(design)
	if got := hex.EncodeToString(sum[:]); got != v6DesignSHA256 {
		t.Errorf("design file SHA-256 = %s, the ledger is for %s", got, v6DesignSHA256)
	}
	if !strings.Contains(ledger, v6DesignSHA256) {
		t.Errorf("the ledger does not state the design SHA-256 %s", v6DesignSHA256)
	}

	pkgs := map[string]*ledgerPackage{}
	load := func(dir string) *ledgerPackage {
		if p, ok := pkgs[dir]; ok {
			return p
		}
		p := &ledgerPackage{decls: map[string]map[string]bool{}, tests: map[string]*ast.FuncDecl{}}
		pkgs[dir] = p
		files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if len(files) == 0 {
			t.Errorf("no Go files in %s", dir)
		}
		for _, name := range files {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(name, "_test.go") {
				for _, d := range f.Decls {
					if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
						p.tests[fn.Name.Name] = fn
					}
				}
				continue
			}
			set := map[string]bool{}
			p.decls[filepath.Base(name)] = set
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil || len(d.Recv.List) == 0 {
						set[d.Name.Name] = true
						continue
					}
					set[receiverName(d.Recv.List[0].Type)+"."+d.Name.Name] = true
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							set[s.Name.Name] = true
						case *ast.ValueSpec:
							for _, n := range s.Names {
								set[n.Name] = true
							}
						}
					}
				}
			}
		}
		return p
	}

	implRef := regexp.MustCompile("`((?:cmd|internal)/[a-z/]+)/([a-z0-9_]+\\.go):([A-Za-z_][A-Za-z0-9_.]*)`")
	testRef := regexp.MustCompile("`((?:cmd|internal)/[a-z/]+)\\.(Test[A-Za-z0-9_]+)`")
	checked := map[string]bool{}
	checkTest := func(where, dir, name string) {
		key := dir + "." + name
		if checked[key] {
			return
		}
		checked[key] = true
		p := load(dir)
		fn, ok := p.tests[name]
		if !ok {
			t.Errorf("%s: %s has no test %s", where, dir, name)
			return
		}
		for _, problem := range skipsOrShortReturns(fn, p.tests, map[string]bool{}) {
			t.Errorf("%s: %s.%s %s", where, dir, name, problem)
		}
	}

	d, e, ok := strings.Cut(ledger, "\n## E-side clauses")
	if !ok {
		t.Fatal("the ledger has no E-side section")
	}
	_, d, ok = strings.Cut(d, "\n## D-side clauses")
	if !ok {
		t.Fatal("the ledger has no D-side section")
	}
	rows := 0
	for _, line := range strings.Split(d, "\n") {
		cells := splitRow(line)
		if len(cells) != 3 || cells[0] == "Clause" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		rows++
		where := "row " + short(cells[0])
		impls, tests := implRef.FindAllStringSubmatch(cells[1], -1), testRef.FindAllStringSubmatch(cells[2], -1)
		if len(impls) == 0 {
			t.Errorf("%s: no implementation file:Symbol", where)
		}
		if len(tests) == 0 {
			t.Errorf("%s: no test", where)
		}
		for _, m := range impls {
			dir, file, sym := m[1], m[2], m[3]
			set, ok := load(dir).decls[file]
			if !ok {
				t.Errorf("%s: %s/%s is not a non-test file", where, dir, file)
			} else if !set[sym] {
				t.Errorf("%s: %s/%s declares no %s", where, dir, file, sym)
			}
		}
		for _, m := range tests {
			checkTest(where, m[1], m[2])
		}
	}
	if rows < 100 {
		t.Errorf("only %d D-side rows; the ledger lost clauses", rows)
	}

	// E-side rows name V6-E tasks, never tests.
	eIDs := regexp.MustCompile(`V6-E\d\d [^()]+ \([0-9a-f]{8}\)`)
	eRows := 0
	evidence := ""
	e, evidence, _ = strings.Cut(e, "\n## Gaps and decisions")
	_ = evidence
	for _, line := range strings.Split(e, "\n") {
		cells := splitRow(line)
		if len(cells) != 3 || cells[0] == "Clause" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		eRows++
		if !eIDs.MatchString(cells[1]) || !eIDs.MatchString(cells[2]) {
			t.Errorf("E row %s: implementer and accepter must each name a V6-E task", short(cells[0]))
		}
	}
	if eRows < 20 {
		t.Errorf("only %d E-side rows", eRows)
	}

	// Validation evidence is a commit and observed results, not a prediction.
	ev := regexp.MustCompile("(?m)^Tree: `([0-9a-f]{40})`").FindStringSubmatch(ledger)
	if ev == nil {
		t.Error("the validation evidence names no 40-digit commit as `Tree:`")
	}
	for _, want := range []string{"make check: pass", "make test: pass"} {
		if !strings.Contains(ledger, want) {
			t.Errorf("the validation evidence lacks %q", want)
		}
	}
}

func short(s string) string {
	if len(s) > 70 {
		return s[:70]
	}
	return s
}

// splitRow returns the cells of a Markdown table row, or nil.
func splitRow(line string) []string {
	if !strings.HasPrefix(line, "| ") {
		return nil
	}
	line = strings.TrimSuffix(strings.TrimPrefix(line, "| "), " |")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// receiverName is the type name of a method receiver, without pointer or
// type parameters.
func receiverName(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.IndexListExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// skipsOrShortReturns reports every Skip call, and every `if testing.Short()`
// branch that returns or skips, in fn and in the same-package test helpers it
// reaches by plain call.
func skipsOrShortReturns(fn *ast.FuncDecl, funcs map[string]*ast.FuncDecl, seen map[string]bool) []string {
	if seen[fn.Name.Name] || fn.Body == nil {
		return nil
	}
	seen[fn.Name.Name] = true
	isSkip := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && (sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow")
	}
	isShort := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, _ := sel.X.(*ast.Ident)
		return sel.Sel.Name == "Short" && pkg != nil && pkg.Name == "testing"
	}
	contains := func(root ast.Node, match func(ast.Node) bool) bool {
		found := false
		ast.Inspect(root, func(n ast.Node) bool {
			if n != nil && match(n) {
				found = true
			}
			return !found
		})
		return found
	}
	var problems []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt:
			if contains(n.Cond, isShort) && contains(n.Body, func(x ast.Node) bool {
				_, ret := x.(*ast.ReturnStmt)
				return ret || isSkip(x)
			}) {
				problems = append(problems, "returns or skips under testing.Short() in "+fn.Name.Name)
			}
		case *ast.CallExpr:
			if isSkip(n) {
				problems = append(problems, "calls Skip in "+fn.Name.Name)
			}
			if id, ok := n.Fun.(*ast.Ident); ok {
				if h, ok := funcs[id.Name]; ok {
					problems = append(problems, skipsOrShortReturns(h, funcs, seen)...)
				}
			}
		}
		return true
	})
	return problems
}
