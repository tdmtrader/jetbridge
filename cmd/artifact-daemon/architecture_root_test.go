package main

// Structural guards for the handle-based containment.
//
// Each fails when its scan matches nothing, and each states its own blind
// spot. A guard that hides its approximation is the shape this proposal keeps
// finding — an earlier round produced exactly that defect ("N6 defeatable AST
// guard"), and the fix was to say in the test what the guard cannot see.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func productionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			// Not fatal: unparseable debris is the build's problem, and
			// Fataling here turns unrelated mess into a guard failure.
			t.Logf("skipping unparseable %s: %v", n, err)
			continue
		}
		out[n] = f
	}
	if len(out) == 0 {
		t.Fatal("scanned no production files — this guard cannot fail")
	}
	return out
}

// AC4 — the duplicate extractor is gone and must not come back. server.go
// consumes archives only through the shared extractor.
func TestArchitecture_ServerHasNoTarReader(t *testing.T) {
	files := productionFiles(t)

	var readers []string
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewReader" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "tar" {
				readers = append(readers, name)
			}
			return true
		})
	}

	if len(readers) == 0 {
		t.Fatal("found no tar.NewReader anywhere — the token moved and this guard is inert")
	}
	for _, name := range readers {
		if name == "server.go" {
			t.Errorf("server.go builds a tar.NewReader again; archives are consumed through "+
				"extractTarToRoot. Found in: %v", readers)
		}
	}
	t.Logf("tar.NewReader present in %v; server.go is not among them", readers)
}

// AC7 — a symlink is never created without the target being validated first.
// os.Root contains traversal THROUGH a link but will happily create one
// pointing out, which is Track 1's finding and the reason this guard exists.
//
// Blind spot, stated: this checks that each function creating a symlink also
// calls the validator. It does not prove the call happens on the same entry,
// or first. A syntactic guard cannot; the ordering is covered by the
// behavioural tests instead.
func TestArchitecture_SymlinkCreationIsValidated(t *testing.T) {
	files := productionFiles(t)

	creators := map[string]bool{}
	validators := map[string]bool{}

	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name == "Symlink" {
						creators[name+":"+fn.Name.Name] = true
					}
				case *ast.Ident:
					if fun.Name == "validateSymlinkTarget" {
						validators[name+":"+fn.Name.Name] = true
					}
				}
				return true
			})
			return true
		})
	}

	if len(creators) == 0 {
		t.Fatal("found no symlink creation in production code — this guard is inert")
	}
	for where := range creators {
		if !validators[where] {
			t.Errorf("%s creates a symlink without calling validateSymlinkTarget in the same "+
				"function; os.Root will create an outward link", where)
		}
	}
	t.Logf("symlink creators: %v — all validated", keysOf(creators))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
