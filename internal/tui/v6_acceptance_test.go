package tui

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestV6PresentationLedgerNamesRealTests keeps docs/acceptance/v6-presentation.md
// honest: every Test function a ledger row names exists in the Go package it
// names, none of them calls Skip, and every row names at least one.
func TestV6PresentationLedgerNamesRealTests(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docs", "acceptance", "v6-presentation.md"))
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]map[string]*ast.FuncDecl{}
	funcs := func(dir string) map[string]*ast.FuncDecl {
		if m, ok := cache[dir]; ok {
			return m
		}
		m := map[string]*ast.FuncDecl{}
		cache[dir] = m
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if err != nil || len(matches) == 0 {
			t.Errorf("no test files in %s", dir)
			return m
		}
		for _, name := range matches {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					m[fn.Name.Name] = fn
				}
			}
		}
		return m
	}
	ref := regexp.MustCompile("`((?:cmd|internal)/[a-z/]+)\\.(Test[A-Za-z0-9_]+)`")
	rows := 0
	for n, line := range bytes.Split(raw, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("| ")) || bytes.HasPrefix(line, []byte("| Clause")) || bytes.HasPrefix(line, []byte("| Behavior")) || bytes.HasPrefix(line, []byte("| ---")) {
			continue
		}
		if bytes.Contains(line, []byte("V6-E:")) {
			continue
		}
		rows++
		found := ref.FindAllSubmatch(line, -1)
		if len(found) == 0 {
			t.Errorf("v6-presentation.md:%d: row names no test: %.80s", n+1, line)
		}
		for _, m := range found {
			dir, name := string(m[1]), string(m[2])
			fn, ok := funcs(dir)[name]
			if !ok {
				t.Errorf("v6-presentation.md:%d: %s has no test %s", n+1, dir, name)
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow") {
						t.Errorf("v6-presentation.md:%d: %s.%s calls %s", n+1, dir, name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if rows == 0 {
		t.Error("the ledger has no data rows")
	}
}
