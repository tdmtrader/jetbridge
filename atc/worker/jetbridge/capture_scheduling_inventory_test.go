package jetbridge

// Every production site where a selected capture changes what this runtime does.
//
// The Phase 8 scheduling box asks for a source inventory that "fails if it
// matches no capture-producing path, or if a new capture path bypasses the
// common adapter". Both halves are here, and the second is the one that needs a
// mechanism rather than a promise: a capture changes a Pod in several specific
// ways -- an init container, a volume, an affinity term, an admission refusal --
// and a NEW way that did not go through the common predicate would be invisible
// to every behavioural test that did not happen to cover it.
//
// The predicate is `ExecutionControl.HasDurableOutputCapture`, and the rule is
// that every production read of it is listed with a reason. A site that is not
// listed fails; a listed site that no longer exists fails too, so the list
// cannot rot into exemptions for code that was deleted.
//
// NARROWED by decision F13. The job, one-off and check worker paths that carry
// ExecutionControl WITHOUT a capture belong to the sibling track
// `exact_execution_control`; this inventory is over capture-selected executions,
// which is what spec Reqs 3-6 and 58 require of this track.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// capturePredicate is the one question a capture-aware path asks.
const capturePredicate = "HasDurableOutputCapture"

// captureSites are the production reads of that predicate, keyed
// file:enclosingFunction and carrying the reason.
//
// The enclosing function is in the key deliberately. Keying on the file alone
// would let a new capture path pass by being written next to an existing one,
// which is exactly the edit this inventory exists to catch.
var captureSites = map[string]string{
	"container.go:buildPod": "the ADMISSION refusal. A worker whose output facet is not " +
		"enabled builds no capture pod at all, and says so: durable output capture never " +
		"degrades into an ordinary step, because a pod that ran and captured nothing would " +
		"leave the predeclared handoff unresolved until its deadline",
	"capture_control.go:buildCaptureControlInitContainer": "the control init that establishes " +
		"the provisional source hold. It is index 0 of the init slice, before every writer",
	"capture_control.go:captureReservedDirectory": "a READ of the daemon-issued directory the " +
		"selected output's volume must resolve to. There is deliberately no function that " +
		"BUILDS one: Req 7 says no API accepts a caller-chosen path",
	"capture_control.go:captureSelectedOutputName": "which declared output was selected",
	"capture_control.go:captureSelectedOutputPath": "where that output lives in the container",
	"storage_daemonset.go:BuildAffinity": "the two ready labels and the reserving node. A " +
		"capture pod requires BOTH labels and the node by name; the labels pick a cohort whose " +
		"daemons could acknowledge a hold, and the node is where the reservation's directory " +
		"actually is",
	"process_control.go:capturing": "the supervisor's own question, which is what makes " +
		"an exact finish or stop acknowledgement required rather than optional",
}

func TestEveryCaptureAwarePathIsInventoried(t *testing.T) {
	if len(captureSites) == 0 {
		t.Fatal("the capture inventory is empty; this rule would pass vacuously")
	}

	found := map[string]bool{}
	scanned := 0

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
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

		var enclosing string
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				enclosing = typed.Name.Name
			case *ast.SelectorExpr:
				if typed.Sel.Name != capturePredicate {
					return true
				}
				found[name+":"+enclosing] = true
			}

			return true
		})
	}

	if scanned < 10 {
		t.Fatalf("scanned only %d production files in this package; the walk failed and this "+
			"rule would pass vacuously", scanned)
	}
	if len(found) == 0 {
		t.Fatalf("no production file in this package reads %s. This inventory's whole subject "+
			"is capture-selected executions, and a runtime where nothing asks the question is "+
			"one where the predicate was renamed and the rule is guarding a string",
			capturePredicate)
	}

	var unlisted, stale []string
	for site := range found {
		if _, ok := captureSites[site]; !ok {
			unlisted = append(unlisted, site)
		}
	}
	for site := range captureSites {
		if !found[site] {
			stale = append(stale, site)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)

	for _, site := range unlisted {
		t.Errorf("%s asks whether this execution selected a capture and is not in "+
			"captureSites.\n\nA capture changes a Pod in specific ways -- the control init, "+
			"the reserved volume, two affinity labels, the reserving node, an admission "+
			"refusal -- and a new way that nobody wrote down is one no behavioural test "+
			"covers by accident. Add it with the reason, or route it through an existing "+
			"site.", site)
	}
	for _, site := range stale {
		t.Errorf("captureSites lists %s and no production file has it. A list that keeps "+
			"entries for deleted code stops being an inventory and becomes a set of "+
			"exemptions.", site)
	}
}

// The common adapter stays singular on the capture path too.
//
// `SelectCapture` is the one production function that can put a capture on an
// execution. A second one would be a second way for a capture to reach a Pod,
// and every guard above reads the RESULT rather than the act.
func TestExactlyOneProductionFunctionSelectsACapture(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	// A write to the Capture field is the act. Reading the field is not: half
	// this runtime reads it, and the sites are inventoried above.
	var writers []string
	scanned := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", "node_modules", "brine":
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return nil
		}
		scanned++

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		var enclosing string
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				enclosing = typed.Name.Name
			case *ast.AssignStmt:
				for _, target := range typed.Lhs {
					selector, ok := target.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "Capture" {
						continue
					}
					// runtime.ExecutionControl's own Capture field, not the
					// half-dozen other types with one. The receiver's package
					// is what tells them apart, and the only declaration site
					// that matters is in atc/runtime.
					if !strings.HasPrefix(filepath.ToSlash(relative), "atc/runtime/") {
						continue
					}
					writers = append(writers, filepath.ToSlash(relative)+":"+enclosing)
				}
			}

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if scanned < 500 {
		t.Fatalf("parsed only %d production Go files; the walk failed", scanned)
	}

	sort.Strings(writers)
	expected := []string{"atc/runtime/executioncontrol.go:SelectCapture"}
	if len(writers) != len(expected) || (len(writers) == 1 && writers[0] != expected[0]) {
		t.Errorf("the sites that write ExecutionControl.Capture are %v, and there is exactly "+
			"one: %v.\n\nSelectCapture is the common adapter. It is where the phase check, "+
			"the version check and the validation live, and a second writer is a capture that "+
			"reached a Pod without them.", writers, expected)
	}
}
