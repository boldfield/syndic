package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestV6Addendum1Ledger keeps docs/acceptance/v6-addendum-1.md honest: its two
// tables have 11 and 20 rows, and every test function the file names exists in
// the Go package it names.
func TestV6Addendum1Ledger(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docs", "acceptance", "v6-addendum-1.md"))
	if err != nil {
		t.Fatal(err)
	}
	ledger := string(raw)
	cache := map[string]map[string]bool{}
	funcs := func(dir string) map[string]bool {
		if m, ok := cache[dir]; ok {
			return m
		}
		m := map[string]bool{}
		cache[dir] = m
		files, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if len(files) == 0 {
			t.Errorf("no test files in %s", dir)
		}
		for _, name := range files {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
					m[fn.Name.Name] = true
				}
			}
		}
		return m
	}
	ref := regexp.MustCompile("`((?:cmd|internal)/[a-z/]+)\\.(Test[A-Za-z0-9_]+)`")
	check := func(where, text string) int {
		found := ref.FindAllStringSubmatch(text, -1)
		for _, m := range found {
			if !funcs(m[1])[m[2]] {
				t.Errorf("%s: %s has no test %s", where, m[1], m[2])
			}
		}
		return len(found)
	}

	sections := []struct {
		heading string
		rows    int
	}{{"## Section 2:", 11}, {"## Section 4:", 20}}
	for _, s := range sections {
		_, body, ok := strings.Cut(ledger, "\n"+s.heading)
		if !ok {
			t.Fatalf("the ledger has no %q section", s.heading)
		}
		if body, _, ok = strings.Cut(body, "\n## "); !ok && s.heading != "## Section 4:" {
			t.Fatalf("section %q is not closed", s.heading)
		}
		rows := 0
		for _, line := range strings.Split(body, "\n") {
			cells := splitRow(line)
			if len(cells) != 2 || cells[0] == "Line" || strings.HasPrefix(cells[0], "---") {
				continue
			}
			rows++
			if check(s.heading+" row "+short(cells[0]), cells[1]) == 0 {
				t.Errorf("%s row %s names no test", s.heading, short(cells[0]))
			}
		}
		if rows != s.rows {
			t.Errorf("%s has %d rows, want %d", s.heading, rows, s.rows)
		}
	}

	_, changed, ok := strings.Cut(ledger, "\n## Clauses changed in earlier designs")
	if !ok {
		t.Fatal("the ledger has no section on the clauses changed in earlier designs")
	}
	if check("changed clauses", changed) == 0 {
		t.Error("the changed clauses name no test")
	}
}
