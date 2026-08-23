package main_test

// AC9 — every request URL becomes a key in exactly ONE place.
//
// The containment rule is only as good as the number of places it can be
// forgotten. Six escapes happened because five handlers each derived a key
// themselves; this asserts that cannot come back.
//
// The scanned token is `r.URL.Path`, NOT `strings.TrimPrefix(r.URL.Path` — a
// synonym (strings.CutPrefix, or slicing) would defeat the narrower token while
// leaving four derivations unguarded.
//
// "Exactly one enclosing function" is a structural invariant, not the exact
// count AGENTS.md forbids a guard from asserting. That rule forbids asserting
// how many items are on an allowlist; there is no allowlist here, and this
// tightens rather than loosens as the package grows.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecture_RequestPathIsDerivedInExactlyOnePlace(t *testing.T) {
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	// Test files legitimately reference r.URL.Path when standing up fake peers
	// (preemption_test.go, mirror_test.go). The exclusion is required and is
	// stated here rather than left implicit.
	var scanned int
	holders := map[string]string{} // enclosing func -> file

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Path" {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok || inner.Sel.Name != "URL" {
					return true
				}
				holders[fn.Name.Name] = filepath.Base(name)
				return true
			})
			return true
		})
	}

	// The scan must be able to fail. Zero files scanned, or zero matches, means
	// the guard is asserting nothing.
	if scanned == 0 {
		t.Fatal("guard scanned no production files — it cannot fail and protects nothing")
	}
	if len(holders) == 0 {
		t.Fatalf("guard found no r.URL.Path in %d production files — the token moved and this "+
			"guard is now inert", scanned)
	}

	if len(holders) != 1 {
		t.Errorf("r.URL.Path is derived in %d functions, want exactly 1 (the single accessor). "+
			"Each extra one is a place the containment rule can be forgotten:\n  %v",
			len(holders), holders)
	}
	for fn := range holders {
		if fn != "requestKey" {
			t.Errorf("r.URL.Path is derived in %q; it belongs only in requestKey", fn)
		}
	}
	t.Logf("scanned %d production files; r.URL.Path derived only in %v", scanned, holders)
}
