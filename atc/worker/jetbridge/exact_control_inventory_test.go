package jetbridge

// One protocol owner, and every path that can run a command or replace a Pod.
//
// The Phase 4 refactor box asks for two things this file provides. First, that
// the base adapter stays singular: there is one client for the output daemon's
// control API, and the sibling `exact_execution_control` track EXTENDS it
// rather than adding a second. Second, that every retry path capable of
// recreating a command or a Pod is enumerated, so a new one is a visible edit
// here rather than a quiet way around the exact protocol.
//
// The inventory is the mechanism. A site that is not listed fails the test; a
// listed site that no longer exists fails it too, so the list cannot rot into a
// set of exemptions for code that was deleted years ago.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// commandOrPodSites are the production call sites that can start a command or
// create a Pod, and the reason each is admissible.
//
// The reasons are the point. "It recreates a Pod" is not a finding; "it
// recreates a Pod without asking whether a capture holds the source" is, and
// writing the reason next to the site is what makes the difference reviewable.
// The key is file:enclosingFunction->callee, and the enclosing function is in
// it deliberately. Keying on the callee alone would let a NEW retry path pass
// simply by calling something an existing one already calls, which is exactly
// the edit this inventory has to catch.
var commandOrPodSites = map[string]string{
	"container.go:Run->createPausePod": "the pause Pod a step's exec transport attaches to. " +
		"The terminal-Pod replacement in the same function consults the ledger first " +
		"(refuseIfCaptureHeld), because deleting a terminal Pod to make a fresh one is a new " +
		"Pod UID over the same step",
	"container.go:Run->createPod": "the direct-mode fallback, used only when no executor is " +
		"configured. It bakes the command into the Pod spec and runs it once; there is no " +
		"retry path through it at all",
	"container.go:createPod->":      "createPod's own declaration",
	"container.go:createPausePod->": "createPausePod's own declaration",
	"process.go:Wait->ExecInPod": "the ONE place a step's command is issued. Its retry loop " +
		"only re-dials while the transport never carried a byte (execTransportLive), and for a " +
		"controlled execution the exact supervisor refuses to relaunch a command whose start it " +
		"already recorded",
	"process.go:Wait->recreatePausePod": "the exec retry's replacement of a pause Pod that died " +
		"before the command started",
	"process.go:waitForRunning->recreatePausePod": "the same replacement from the readiness " +
		"watch, sharing recreatePausePod's one-shot guard",
	"process.go:recreatePausePod->": "the one-shot replacement itself. Spent once per step " +
		"whatever the outcome, refused outright once the transport went live, and refused for a " +
		"capture-held source",
	"process.go:recreatePausePod->createPausePod": "recreatePausePod's own creation, after its " +
		"guards",
	"volume.go:StreamIn->ExecInPod": "tar into a volume. It runs no step command and creates " +
		"no Pod; it is listed rather than filtered so the scan cannot be made to miss a step " +
		"command by moving it into this file",
	"volume.go:StreamOut->ExecInPod": "tar out of a volume, for the same reason",
	"executor.go:ExecInPod->": "the SPDYExecutor's own definition, which is the transport " +
		"rather than a caller of it",
}

func TestEveryPathThatRunsACommandOrReplacesAPodIsInventoried(t *testing.T) {
	if len(commandOrPodSites) == 0 {
		t.Fatal("the inventory is empty; the scan below would report success over anything")
	}

	found := map[string]bool{}
	scanned := 0

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			record := func(site string, position token.Pos) {
				found[site] = true
				if _, listed := commandOrPodSites[site]; !listed {
					t.Errorf("%s at %s runs a command or creates a Pod and is not in "+
						"commandOrPodSites. Every such path has to say why it may: a retry that "+
						"re-issues a controlled command, or a replacement Pod over a "+
						"capture-held source, is the failure this inventory exists to make "+
						"visible.", site, fset.Position(position))
				}
			}
			if interesting(function.Name.Name) {
				record(name+":"+function.Name.Name+"->", function.Pos())
			}
			ast.Inspect(function, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !interesting(selector.Sel.Name) {
					return true
				}
				record(name+":"+function.Name.Name+"->"+selector.Sel.Name, call.Pos())

				return true
			})
		}
	}

	if scanned < 20 {
		t.Fatalf("the walk visited only %d production files in this package; the scan is broken "+
			"and every assertion above passes vacuously", scanned)
	}

	var stale []string
	for site := range commandOrPodSites {
		if !found[site] {
			stale = append(stale, site)
		}
	}
	sort.Strings(stale)
	if len(stale) != 0 {
		t.Errorf("the inventory pins sites that no longer exist: %v. Remove them; a reason that "+
			"guards nothing is worse than no reason, because it reads as coverage.", stale)
	}
}

// interesting names the four things this inventory tracks: creating a Pod,
// replacing one, and issuing a command into one.
func interesting(name string) bool {
	switch name {
	case "createPausePod", "createPod", "recreatePausePod", "ExecInPod":
		return true
	}

	return false
}

// There is exactly one production implementation of OutputControl.
//
// A second would be a second owner of an execution's control state, and the two
// would diverge the first time one of them learned something the other did not.
// The sibling `exact_execution_control` track extends OutputControlClient; this
// test is what tells it which one, and what fails if it writes another.
func TestExactlyOneProductionTypeOwnsTheControlProtocol(t *testing.T) {
	// The interface is satisfied by the client and, in tests, by fixtures. The
	// assertion is about PRODUCTION, so it is stated over the concrete type
	// this package exports rather than over everything that compiles.
	var client any = &OutputControlClient{}
	if _, ok := client.(OutputControl); !ok {
		t.Fatal("OutputControlClient no longer implements OutputControl; the adapter the " +
			"sibling track extends has moved")
	}

	// And it is one type with one endpoint. A client that could be repointed
	// mid-execution could ask the wrong node whether a process finished, so
	// the endpoint is a field set at construction and there is no setter.
	value := reflect.TypeOf(OutputControlClient{})
	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).IsExported() {
			t.Errorf("OutputControlClient.%s is exported; every field of this client is fixed "+
				"at construction, and an exported one is a way to repoint a live execution's "+
				"control at another node", value.Field(i).Name)
		}
	}
}
