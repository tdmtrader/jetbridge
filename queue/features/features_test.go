// Package features binds each Scenario title to an active Ginkgo It of the
// same text in queue/*/ (core, config), since this module carries no Gherkin
// runner. Only calls to It count: XIt, PIt and FIt (refused: a focused spec
// fails the suite) and anything in a comment do not.
package features

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func activeIts(t *testing.T) map[string]bool {
	files, _ := filepath.Glob("../*/*_test.go")
	its := map[string]bool{}
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && len(c.Args) > 0 {
				if id, _ := c.Fun.(*ast.Ident); id != nil && id.Name == "It" {
					if lit, _ := c.Args[0].(*ast.BasicLit); lit != nil && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						its[s] = true
					}
				}
			}
			return true
		})
	}
	return its
}

func TestEveryScenarioHasAMatchingIt(t *testing.T) {
	its, scenarios := activeIts(t), 0
	feats, _ := filepath.Glob("*.feature")
	for _, f := range feats {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			title, ok := strings.CutPrefix(strings.TrimSpace(line), "Scenario:")
			if !ok {
				continue
			}
			scenarios++
			if title = strings.TrimSpace(title); !its[title] {
				t.Errorf("scenario %q has no active It(%q) in queue/", title, title)
			}
		}
	}
	if scenarios == 0 {
		t.Fatal("no scenarios found in queue/features/*.feature")
	}
}
