package hangaroutput_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// The list in leaseowners.go is a claim about the WIRING, so it is checked
// against the wiring rather than against itself.
//
// A hand-maintained list of "kinds somebody runs" goes stale the moment a
// Runner is added or removed, and the failure is silent in both directions: a
// new worker whose liveness nothing watches, or five permanent warnings about
// workers that do not exist. This reads the `Kind:` assignments out of the
// command roots' own source, which is where a controller.Runner is actually
// given its kind.
//
// It parses rather than imports because cmd/* are main packages and cannot be
// imported at all.
func TestTheOwnedOperationKindsAreExactlyTheOnesAWorkloadClaims(t *testing.T) {
	wired := kindsAssignedToARunner(t)
	if len(wired) < 3 {
		t.Fatalf("found %d operation kinds assigned to a Runner across cmd/; the scan failed "+
			"and this rule would pass vacuously: %v", len(wired), wired)
	}

	declared := map[output.OperationKind]bool{}
	for _, kind := range hangaroutput.OwnedOperationKinds() {
		if declared[kind] {
			t.Errorf("OwnedOperationKinds lists %s twice", kind)
		}
		declared[kind] = true
	}

	for kind, where := range wired {
		if !declared[kind] {
			t.Errorf("%s assigns a controller.Runner the kind %s, and OwnedOperationKinds does "+
				"not list it.\n\nThat worker takes a real lease, and nothing publishes or "+
				"alerts on whether it is holding one. Add it, so its liveness is watched the "+
				"way the other four are.", where, kind)
		}
	}
	for kind := range declared {
		if _, ok := wired[kind]; !ok {
			t.Errorf("OwnedOperationKinds lists %s and no file under cmd/ assigns it to a "+
				"controller.Runner.\n\nThe status publisher will emit -1 for it forever and "+
				"HangarOutputOperationLeaseUnheld will say its controller is not running, "+
				"about a controller that does not exist. Either wire it or move it to "+
				"UnownedOperationKinds with the reason.", kind)
		}
	}
}

// Every kind is classified, exactly once. A tenth kind added to the schema
// lands in neither list and fails here rather than quietly acquiring or losing
// an alert.
func TestEveryOperationKindIsEitherOwnedOrRecordedAsUnowned(t *testing.T) {
	owned := map[output.OperationKind]bool{}
	for _, kind := range hangaroutput.OwnedOperationKinds() {
		owned[kind] = true
	}
	unowned := hangaroutput.UnownedOperationKinds()

	all := output.OperationKinds()
	if len(all) == 0 {
		t.Fatalf("the operation vocabulary is %d kinds; it collapsed and this rule would pass "+
			"over almost nothing", len(all))
	}

	for _, kind := range all {
		reason, recorded := unowned[kind]
		switch {
		case owned[kind] && recorded:
			t.Errorf("%s is in both OwnedOperationKinds and UnownedOperationKinds", kind)
		case !owned[kind] && !recorded:
			t.Errorf("%s is in neither OwnedOperationKinds nor UnownedOperationKinds.\n\n"+
				"Whether a kind has a worker decides whether its lease term is a signal or a "+
				"permanent false alert, so a new kind has to say which it is. If nothing runs "+
				"it, record it as unowned with the reason; if something does, wire it.", kind)
		case recorded && strings.TrimSpace(reason) == "":
			t.Errorf("%s is recorded as unowned with an empty reason; the record is the point",
				kind)
		}
	}

	for kind := range unowned {
		if !slicesContainsKind(all, kind) {
			t.Errorf("UnownedOperationKinds records %s, which is not an operation kind; the "+
				"entry is stale", kind)
		}
	}
	for kind := range owned {
		if !slicesContainsKind(all, kind) {
			t.Errorf("OwnedOperationKinds lists %s, which is not an operation kind; the entry "+
				"is stale", kind)
		}
	}
}

func slicesContainsKind(all []output.OperationKind, want output.OperationKind) bool {
	for _, kind := range all {
		if kind == want {
			return true
		}
	}

	return false
}

// kindsAssignedToARunner finds every `Kind: output.OperationX` in a non-test
// file under cmd/, with the file it was found in.
func kindsAssignedToARunner(t *testing.T) map[output.OperationKind]string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	repository := filepath.Join(filepath.Dir(thisFile), "..", "..")

	byConstant := map[string]output.OperationKind{}
	for _, kind := range output.OperationKinds() {
		byConstant[constantNameFor(kind)] = kind
	}

	found := map[output.OperationKind]string{}
	scanned := 0
	err := filepath.WalkDir(filepath.Join(repository, "cmd"),
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parsing %s: %v", path, parseErr)
			}
			scanned++
			relative, relErr := filepath.Rel(repository, path)
			if relErr != nil {
				relative = path
			}

			ast.Inspect(file, func(node ast.Node) bool {
				pair, isPair := node.(*ast.KeyValueExpr)
				if !isPair {
					return true
				}
				key, isIdent := pair.Key.(*ast.Ident)
				if !isIdent || key.Name != "Kind" {
					return true
				}
				selector, isSelector := pair.Value.(*ast.SelectorExpr)
				if !isSelector {
					return true
				}
				pkg, isPkg := selector.X.(*ast.Ident)
				if !isPkg || pkg.Name != "output" {
					return true
				}
				if kind, known := byConstant[selector.Sel.Name]; known {
					found[kind] = filepath.ToSlash(relative)
				}

				return true
			})

			return nil
		})
	if err != nil {
		t.Fatalf("scanning cmd/: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("parsed only %d non-test files under cmd/; the walk failed", scanned)
	}

	return found
}

// constantNameFor maps a kind's value back to the Go constant that declares it
// -- "reclaim_admission" to "OperationReclaimAdmission" -- so the scan matches
// on the identifier a main package actually writes.
func constantNameFor(kind output.OperationKind) string {
	parts := strings.Split(string(kind), "_")
	name := "Operation"
	for _, part := range parts {
		if part == "" {
			continue
		}
		name += strings.ToUpper(part[:1]) + part[1:]
	}

	return name
}

// A guard on the mapping above: every kind's derived constant name must be one
// the vocabulary really declares, or the scan silently matches nothing and
// TestTheOwnedOperationKindsAreExactlyTheOnesAWorkloadClaims reports an empty
// wiring.
func TestEveryOperationKindMapsToADeclaredConstant(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "hangar", "output", "operations.go")

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing operations.go: %v", err)
	}

	names := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, isValue := node.(*ast.ValueSpec)
		if !isValue {
			return true
		}
		for _, ident := range spec.Names {
			names[ident.Name] = true
		}

		return true
	})
	if len(names) < 9 {
		t.Fatalf("parsed %d declared names out of operations.go; the parse failed", len(names))
	}

	var missing []string
	for _, kind := range output.OperationKinds() {
		if !names[constantNameFor(kind)] {
			missing = append(missing, string(kind)+" -> "+constantNameFor(kind))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these kinds do not map to a constant declared in hangar/output/operations.go: "+
			"%s.\n\nThe wiring scan matches on the constant identifier, so a kind whose "+
			"constant is named differently is one the scan cannot see -- and its absence from "+
			"the wiring reads as \"nothing runs it\".", strings.Join(missing, ", "))
	}
}
