package hangaroutput_test

// The commit boundary, asserted structurally.
//
// The behavioural specs beside this one count signer calls and prove a
// rolled-back admission reaches none. That is the right assertion and it is not
// sufficient: it covers the paths the specs drive, and the rule requirement 35
// states is about every path there is -- "only after that transaction commits
// may the control plane mint and deliver a usable lease-bound warrant".
//
// So this reads the source. A function that holds an open transaction and signs
// inside it is minting under a transaction that may still roll back, and that
// is exactly the shape a later edit reintroduces by moving one call two lines.
// The guard cannot be satisfied by a comment.
//
// IT FOLLOWS ONE STEP OF INDIRECTION. A guard matching only a literal `Sign`
// selector in the same function body as `Begin` is narrower than its own name:
// a mint reached through a helper on the same receiver -- `admission.mint(...)`
// inside `commitLease` -- would walk straight past it. So the functions each
// file declares are resolved against each other and "signs" is transitive.
// Across files it is not, which is why every file that mints is in the list
// below rather than only the one that started out that way.
//
// It asserts it FOUND something first. A guard that scanned nothing and
// reported nothing is the silent-skip failure this tree warns about elsewhere,
// and it is the reason AC 20 says architecture guards must prove they scanned
// at least one file.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// mintingFiles is every file in this package that reaches a warrant signer.
// read.go admits a read; leasecontrol.go re-mints the warrant a renewal produces.
var mintingFiles = []string{"read.go", "leasecontrol.go"}

func TestNoFunctionBothOpensATransactionAndSigns(t *testing.T) {
	totalScanned, totalTransacting, totalSigning := 0, 0, 0

	for _, path := range mintingFiles {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		// What the two halves look like as calls.
		opensTransaction := func(name string) bool { return name == "Begin" }
		signs := func(name string) bool { return name == "Sign" }

		type body struct {
			name    string
			begins  []string
			mints   []string
			calls   []string
			signing bool
		}

		var functions []*body
		byName := map[string]*body{}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			found := &body{name: function.Name.Name}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					switch {
					case opensTransaction(fun.Sel.Name):
						found.begins = append(found.begins, render(fileSet, call))
					case signs(fun.Sel.Name):
						found.mints = append(found.mints, render(fileSet, call))
					default:
						// A method on this file's own receiver, resolved below.
						found.calls = append(found.calls, fun.Sel.Name)
					}
				case *ast.Ident:
					found.calls = append(found.calls, fun.Name)
				}

				return true
			})
			found.signing = len(found.mints) > 0
			functions = append(functions, found)
			byName[found.name] = found
		}

		// Transitive closure over the calls this file declares: a function that
		// reaches a mint through a helper is a function that mints.
		for changed := true; changed; {
			changed = false
			for _, function := range functions {
				if function.signing {
					continue
				}
				for _, called := range function.calls {
					if target, ok := byName[called]; ok && target.signing {
						function.signing = true
						function.mints = append(function.mints, "via "+called)
						changed = true

						break
					}
				}
			}
		}

		for _, function := range functions {
			totalScanned++
			if len(function.begins) > 0 {
				totalTransacting++
			}
			if function.signing {
				totalSigning++
			}
			if len(function.begins) > 0 && function.signing {
				t.Errorf("atc/hangaroutput/%s: %s both opens a transaction (%s) and signs (%s).\n\n"+
					"A warrant minted inside a transaction is a warrant minted under a commit that may "+
					"still roll back, and requirement 35 admits a usable warrant only after the commit "+
					"is authoritative. The mint belongs in a function that holds no transaction and "+
					"takes a value only a committed row can produce.",
					path, function.name, strings.Join(function.begins, ", "),
					strings.Join(function.mints, ", "))
			}
		}
	}

	if totalScanned == 0 {
		t.Fatalf("this guard parsed %v and found no functions, so it is passing vacuously",
			mintingFiles)
	}
	if totalTransacting == 0 {
		t.Error("this guard found no function that opens a transaction; either the admission " +
			"stopped using one or it moved, and either way the rule is no longer being checked " +
			"where it is written")
	}
	if totalSigning == 0 {
		t.Errorf("this guard found no function that signs; the mint moved out of %v and the "+
			"rule is no longer being checked where it is written", mintingFiles)
	}
}

func render(fileSet *token.FileSet, node ast.Node) string {
	position := fileSet.Position(node.Pos())

	return "line " + itoa(position.Line)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}

	return string(digits)
}
