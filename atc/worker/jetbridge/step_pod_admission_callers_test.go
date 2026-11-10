package jetbridge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// composePod returns a pod nothing has admitted. Its only callers are the
// two that admit what they submit: buildPod (the exec runtime's pod) and
// buildStepJob (the finished step Job template). A third caller could hand
// the API server a pod that skipped the step pod admission and its privilege
// gate, so this rule fails on one, and on either admitting caller that stops
// calling admitStepPod.
func TestComposePodHasOnlyItsTwoAdmittingCallers(t *testing.T) {
	want := []string{"buildPod", "buildStepJob"}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var callers []string
	admits := map[string]bool{}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch callee := call.Fun.(type) {
				case *ast.SelectorExpr:
					if callee.Sel.Name == "composePod" {
						callers = append(callers, fn.Name.Name)
					}
				case *ast.Ident:
					if callee.Name == "admitStepPod" {
						admits[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no production file scanned; the rule would pass vacuously")
	}
	sort.Strings(callers)
	if !reflect.DeepEqual(callers, want) {
		t.Errorf("composePod is called from %v, want exactly %v: every other path to a pod must go through one of them", callers, want)
	}
	for _, caller := range want {
		if !admits[caller] {
			t.Errorf("%s calls composePod but not admitStepPod: the pod it submits is never admitted", caller)
		}
	}
}
