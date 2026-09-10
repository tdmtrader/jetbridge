package hangaroutput_test

// The commit boundary, asserted structurally.
//
// The behavioural spec beside this one counts signer calls and proves a
// rolled-back admission reaches none. That is the right assertion and it is not
// sufficient: it covers the paths the spec drives, and the rule requirement 35
// states is about every path there is -- "only after that transaction commits
// may the control plane mint and deliver a usable lease-bound grant".
//
// So this reads the source. A function that holds an open transaction and signs
// inside it is minting under a transaction that may still roll back, and that
// is exactly the shape a later edit reintroduces by moving one call two lines.
// The guard cannot be satisfied by a comment.
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

func TestNoFunctionBothOpensATransactionAndSigns(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "read.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing read.go: %v", err)
	}

	// What the two halves look like as calls.
	opensTransaction := func(name string) bool { return name == "Begin" }
	signs := func(name string) bool { return name == "Sign" }

	scanned, transacting, signing := 0, 0, 0
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		scanned++

		var begins, mints []string
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case opensTransaction(selector.Sel.Name):
				begins = append(begins, render(fileSet, call))
			case signs(selector.Sel.Name):
				mints = append(mints, render(fileSet, call))
			}

			return true
		})

		if len(begins) > 0 {
			transacting++
		}
		if len(mints) > 0 {
			signing++
		}
		if len(begins) > 0 && len(mints) > 0 {
			t.Errorf("atc/hangaroutput/read.go: %s both opens a transaction (%s) and signs (%s).\n\n"+
				"A grant minted inside a transaction is a grant minted under a commit that may "+
				"still roll back, and requirement 35 admits a usable grant only after the commit "+
				"is authoritative. The mint belongs in a function that holds no transaction and "+
				"takes a value only a committed row can produce.",
				function.Name.Name, strings.Join(begins, ", "), strings.Join(mints, ", "))
		}
	}

	if scanned == 0 {
		t.Fatal("this guard parsed read.go and found no functions, so it is passing vacuously")
	}
	if transacting == 0 {
		t.Error("this guard found no function that opens a transaction; either the admission " +
			"stopped using one or it moved, and either way the rule is no longer being checked " +
			"where it is written")
	}
	if signing == 0 {
		t.Error("this guard found no function that signs; the mint moved out of read.go and the " +
			"rule is no longer being checked where it is written")
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
