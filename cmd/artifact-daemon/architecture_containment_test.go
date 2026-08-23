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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestArchitecture_RequestPathIsDerivedInExactlyOnePlace(t *testing.T) {
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var scanned int
	holders := map[string]string{} // "file:func" -> what it touched

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			// Do NOT Fatal. Unrelated debris in the package would otherwise
			// turn this guard into a hard failure instead of a scan it can
			// reason about — and an unparseable file is the build's problem,
			// not this guard's.
			t.Logf("skipping unparseable %s: %v", name, err)
			continue
		}
		scanned++

		// Walk EVERY function-like node, not just FuncDecl. A handler written
		// as a package-level `var h = func(w, r) {...}` is a FuncLit and the
		// first version of this guard never looked inside one.
		record := func(where string, body ast.Node) {
			ast.Inspect(body, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// r.RequestURI — a genuine alternate source, and it carries the
				// UNDECODED path, so it is more dangerous than r.URL.Path.
				if sel.Sel.Name == "RequestURI" {
					holders[name+":"+where] = "RequestURI"
					return true
				}
				// r.URL — matched at the URL level, not at .Path, so that
				// `u := r.URL; u.Path` is caught too. The first version matched
				// only SelectorExpr{X: SelectorExpr}, so binding r.URL to a
				// local defeated it.
				//
				// Without type information this cannot tell r.URL (the request
				// field) from url.URL (the package type), so the net/url
				// package qualifier is excluded by name. That is a real
				// limitation: a local variable named `url` holding a request
				// would slip through. It is narrower than the hole it closes,
				// and stated rather than hidden.
				if sel.Sel.Name == "URL" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "url" {
						return true
					}
					holders[name+":"+where] = "URL"
				}
				return true
			})
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				recv := ""
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					if st, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
						if id, ok := st.X.(*ast.Ident); ok {
							recv = id.Name + "."
						}
					}
				}
				if fn.Body != nil {
					record(recv+fn.Name.Name, fn.Body)
				}
			case *ast.FuncLit:
				record(fmt.Sprintf("func-literal@%d", fset.Position(fn.Pos()).Line), fn.Body)
			}
			return true
		})
	}

	if scanned == 0 {
		t.Fatal("guard scanned no production files — it cannot fail and protects nothing")
	}
	if len(holders) == 0 {
		t.Fatalf("guard found no request-URL access in %d production files — the token moved "+
			"and this guard is now inert", scanned)
	}

	const accessor = "server.go:Server.requestKey"
	for where, what := range holders {
		if where != accessor {
			t.Errorf("request URL (%s) is read in %q; it belongs only in %s", what, where, accessor)
		}
	}
	t.Logf("scanned %d production files; request URL read only in %v", scanned, holders)
}
