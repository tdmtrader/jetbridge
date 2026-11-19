package concourse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The seam this file defends:
//
//	Core may name the agentic layer exactly once, at a composition root.
//	Nothing else in the tree may import it.
//
// `core` was cut from v0.2.130 to be a clean base CI platform, with the v1/v2/v3
// agentic platform stripped out and a v4 to be built later. The owner's second
// stated goal is to "keep separate the k8s native features that power jetbridge
// from the agentic ones we want to add". The way that separation is lost is
// never a decision -- it is one import at a time, each locally reasonable, until
// the platform cannot be reasoned about without the agent layer in scope.
//
// So state the rule as a test. Today it has one subject and one allowed
// importer, which is exactly when a rule like this is cheap to adopt.

const modulePrefix = "github.com/concourse/concourse/"

// agenticPackages are the packages that exist to serve automated agents rather
// than the CI platform itself.
//
// Every entry must exist. A package listed here that `go list` does not know
// about fails the test rather than silently shrinking it -- so removing MCP is
// a deliberate edit to this list, not a rule that quietly stops applying.
var agenticPackages = []string{
	// Model Context Protocol: a JSON-RPC surface whose entire purpose is to let
	// an LLM drive Concourse. Deliberately kept when the rest of the agentic
	// platform was stripped.
	"atc/api/mcpserver",
	// Dedicated agent executables are part of the agentic layer, not core
	// composition roots. Neither is imported by the CI platform.
	"cmd/jb",
	"cmd/jb-review-worker",
	"atc/mcp",
	"internal/mcpclient",
	"cmd/jb-mcp-client",
	// Local schema/client experiment is part of the MCP surface, not core.
	"hack/mcp-schema-probe",
}

// agenticPrefixes are reserved for v4. Nothing lives under them yet; listing
// them now means the rule already binds when the first package arrives, instead
// of being remembered and re-derived at the moment it is least convenient.
//
// Unlike agenticPackages, these are NOT required to be non-empty.
var agenticPrefixes = []string{
	"agent/",
	"atc/agent/",
}

// wiringPoints are the packages permitted to import the agentic layer, each
// with the reason it is allowed. A handler has to be registered somewhere; the
// point of the allowlist is that "somewhere" is one named place with a stated
// justification, rather than wherever it was convenient.
//
// There is exactly one entry, which is the rule stated at the top of this file
// holding rather than the rule going unexercised, and it carries two
// independent reasons.
//
// It has not always been this package, and the allowlist has not always had
// anything in it. atc/api held the only entry while the MCP tool surface and
// its route were registered there; removing that surface emptied the list,
// and what is left of mcpserver is transport with no Concourse imports of its
// own. An empty allowlist was never the goal -- an agentic layer nothing
// constructs is a layer nothing runs -- and it is not the state now: the web
// composition root mounts MCP on its own routes, and the run_pipeline step's
// admitter is assembled in the same place.
//
// Adding an entry here is the moment to ask whether the dependency should be
// inverted instead -- the agentic side depending on core costs nothing, and
// core depending on the agentic side is what made v1/v2/v3 inseparable. That
// question was asked for the entry below and answered by inverting everything
// that could be inverted: atc/exec declares the shape of the admitter it needs
// and names nothing agentic, atc/engine takes that interface as an option, and
// atc/agent/composition reaches back into core through atc/runs. What is left
// is a constructor and a struct-to-struct translation, which is the
// irreducible part -- something has to build the object graph.
//
// The second reason is MCP. The web composition root mounts the MCP handler on
// the handful of paths MCP owns -- its endpoint and its OAuth metadata --
// beside the web handler rather than around it, because MCP authenticates with
// its own bearer credentials and consent cookies and an MCP 401 must not clear
// an unrelated web login's. What it mounts is an adapter that cannot fetch
// data directly or bypass API authorization and auditing. Both reasons name
// the same package, so they are recorded together rather than as two entries.
var wiringPoints = map[string]string{
	"atc/atccmd": "the composition root: it constructs runs.NewAdmitter and " +
		"composition.NewService and adapts the result to exec.ChildRunAdmitter " +
		"for the run_pipeline step, in atc/atccmd/child_run_admitter.go and " +
		"nowhere else. See docs/superpowers/specs/2026-09-08-run-pipeline-step-design.md. " +
		"It is also where the web composition root mounts the MCP handler on " +
		"the paths MCP owns, beside the web handler rather than around it, in " +
		"atc/atccmd/wiring_api.go; the adapter behind it cannot fetch data " +
		"directly or bypass API authorization and auditing.",
}

type goListPackage struct {
	ImportPath     string   `json:"ImportPath"`
	Imports        []string `json:"Imports"`
	TestImports    []string `json:"TestImports"`
	XTestImports   []string `json:"XTestImports"`
	Incomplete     bool     `json:"Incomplete"`
	DepsErrorCount int      `json:"-"`
}

// importGraph holds production and test edges separately, because the two rules
// below need different ones.
//
// "Core must not import the agentic layer" counts test imports: a test
// dependency couples the packages just as firmly, and it is the easier one to
// add without thinking.
//
// "The agentic layer must not reach further into core" counts production
// imports only. A test reaching for a helper is not a coupling v4 would
// inherit, and pinning it here would just be noise.
type importGraph struct {
	prod map[string][]string
	all  map[string][]string
}

// loadImportGraph returns, for every package in the module, the in-module
// packages it imports.
//
// `go list` rather than golang.org/x/tools/go/packages: the latter is only an
// indirect dependency here and its richer modes type-check, which costs far
// more than this test is worth. Direct edges are the whole check -- transitive
// reachability is meaningless when nearly everything reaches atc/api anyway.
//
// One consequence to know before you trust a green: the graph comes from a
// subprocess, and Go's test cache does not track it. Edit a package these rules
// cover and `go test .` can report `ok ... (cached)` from the previous graph,
// which reads exactly like the guard declining to fire. Use `go test -count=1 .`
// -- or ginkgo, which compiles and runs the binary every time and is what
// `make test-unit` does -- when you are checking whether a guard still bites.
func loadImportGraph(t *testing.T) importGraph {
	t.Helper()

	// -e so a package that fails to load is reported rather than aborting the
	// whole listing.
	out, err := goList("-e", "-json", "./...")
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}

	graph := importGraph{
		prod: map[string][]string{},
		all:  map[string][]string{},
	}

	internal := func(groups ...[]string) []string {
		var out []string
		for _, group := range groups {
			for _, imp := range group {
				if strings.HasPrefix(imp, modulePrefix) {
					out = append(out, strings.TrimPrefix(imp, modulePrefix))
				}
			}
		}
		sort.Strings(out)

		return out
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decoding go list output: %v", err)
		}

		short := strings.TrimPrefix(pkg.ImportPath, modulePrefix)

		graph.prod[short] = internal(pkg.Imports)
		graph.all[short] = internal(pkg.Imports, pkg.TestImports, pkg.XTestImports)
	}

	// A graph this small means go list silently listed nothing useful, and
	// every assertion below would pass by vacuum.
	if len(graph.all) < 50 {
		t.Fatalf("go list returned only %d packages; this repo has well over 100. "+
			"The listing failed and every check below would pass vacuously.", len(graph.all))
	}

	return graph
}

func isAgentic(pkg string) bool {
	for _, a := range agenticPackages {
		if pkg == a || strings.HasPrefix(pkg, a+"/") {
			return true
		}
	}
	for _, p := range agenticPrefixes {
		if strings.HasPrefix(pkg, p) {
			return true
		}
	}

	return false
}

// TestAgenticLayerIsImportedOnlyAtItsWiringPoint is the rule.
func TestAgenticLayerIsImportedOnlyAtItsWiringPoint(t *testing.T) {
	graph := loadImportGraph(t)

	// Every declared agentic package must actually exist. Without this, a
	// rename or a deletion turns the rule into a no-op that still reports
	// success -- the failure mode that makes architecture tests worthless.
	for _, pkg := range agenticPackages {
		if _, ok := graph.all[pkg]; !ok {
			t.Errorf("agenticPackages lists %q, which is not in the import graph.\n"+
				"It was renamed or removed. Update the list -- the seam should "+
				"change because someone decided to change it, not because a "+
				"path moved.", pkg)
		}
	}

	// And every wiring point must exist, for the same reason: an allowlist
	// entry pointing at nothing silently widens nothing, but it also stops
	// documenting anything.
	for pkg := range wiringPoints {
		if _, ok := graph.all[pkg]; !ok {
			t.Errorf("wiringPoints allows %q to import the agentic layer, but that "+
				"package does not exist. Remove the entry.", pkg)
		}
	}

	var violations []string
	usedWiringPoint := map[string]bool{}

	for pkg, imports := range graph.all {
		if isAgentic(pkg) {
			// The agentic layer may import itself freely.
			continue
		}

		for _, imp := range imports {
			if !isAgentic(imp) {
				continue
			}

			if _, allowed := wiringPoints[pkg]; allowed {
				usedWiringPoint[pkg] = true
				continue
			}

			violations = append(violations, pkg+" -> "+imp)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("core package imports the agentic layer: %s\n"+
			"Core must not depend on the agent surface -- that dependency is what "+
			"made v1/v2/v3 impossible to separate. Either invert it (have the "+
			"agentic side depend on core), or, if this genuinely is a composition "+
			"root, add it to wiringPoints with the reason.", v)
	}

	// An allowlist entry that no longer grants anything is stale. Left alone it
	// accumulates into a list nobody trusts, which is how allowlists stop being
	// read.
	for pkg := range wiringPoints {
		if _, exists := graph.all[pkg]; !exists {
			continue // already reported above
		}
		if !usedWiringPoint[pkg] {
			t.Errorf("wiringPoints allows %q to import the agentic layer, but it no "+
				"longer does. Remove the entry so the allowlist keeps describing "+
				"what is actually true.", pkg)
		}
	}
}

// TestAgenticLayerDoesNotReachIntoCoreInternals is the other direction.
//
// The rule above stops core depending on the agent surface. This one bounds how
// far the agent surface reaches back, because every core package it touches is
// one v4 inherits a coupling to -- and mcpserver is already the cautionary
// example: it talks to atc/db directly rather than going through core's own
// handlers, which is why its behaviour has drifted from the REST API it
// shadows.
//
// This is a ratchet, not a prohibition. The pins record today's reach so that
// widening it is a visible edit.
var agenticCoreReach = map[string][]string{
	// Nothing. What is left of mcpserver is the JSON-RPC / Streamable-HTTP
	// transport: no Concourse imports at all, which is why it was worth keeping
	// when the tool surface went.
	//
	// It reached into atc and atc/db until then -- talking to the database
	// directly rather than through core's handlers, which is exactly how its
	// behaviour drifted from the REST API it shadowed until it no longer
	// enforced the same authorization. When v4 rebuilds the tools, they should
	// compose with core's handlers rather than re-implement them, and this pin
	// should stay empty.
	"atc/api/mcpserver": {},
	// Shared authorization-kind classification, never direct domain execution.
	"atc/mcp":               {"atc", "atc/api/accessor", "atc/api/auth", "skymarshal/mcpauth"},
	"hack/mcp-schema-probe": {},
	"internal/mcpclient":    {},
	"cmd/jb-mcp-client":     {},

	// v4's first package, and the first consumer of core's run-admission port.
	//
	// Exactly two: atc for shared value types, atc/runs for admission. That is
	// the whole point of the port -- CreateRunInTx is an atc/db seam, and a
	// package that called it directly would pin atc/db here on its first
	// commit and inherit the coupling mcpserver's entry above exists to warn
	// about.
	//
	// The set is exact in both directions: an import that is not listed fails,
	// and a listed import that is gone fails. So widening this is an edit with
	// a reason, not an accident.
	"atc/agent/composition": {"atc", "atc/runs"},
	// Artifact-only review contract: it does not reach into the CI runtime.
	"agent/review": {},
	// Sealed Git capture shared by every workload's input format. Git and the
	// filesystem only.
	"agent/capture": {},
	// The provider session: tmpfs credential staging, the pinned Codex, the
	// closed event vocabulary. It runs inside the worker image and knows
	// nothing of Runs.
	"agent/session": {},
	// Test support: the pinned Codex against a scripted model. Like the
	// session it serves, no core reach.
	"agent/session/codextest": {},
	// Artifact-only implementation contract: snapshot, edit-only worker,
	// patch and local apply. Like agent/review, no core reach.
	"agent/implement": {},
	// The workload-neutral Run client: public Run values, including
	// the Run observation every workload's MCP status tool returns, and the
	// shared canonical archive verifier. No storage client.
	"agent/runclient": {"atc", "hangar"},
	// The review and implement adapters over agent/runclient. Handles, Run
	// status, the MCP status projection and archive verification all come
	// through the shared client, so neither names a core package itself.
	"agent/review/client":    {},
	"agent/implement/client": {},
	// The command reuses the saved platform login; the shared client consumes
	// only public wire types and never reaches the Run database or scheduler.
	"cmd/jb":               {"fly/rc"},
	"cmd/jb-review-worker": {},
}

// unpinnedAgenticPackages closes the opt-in hole in the ratchet below.
//
// The pins are a map, and the loop that reads them iterates over its keys. So
// an agentic package nobody remembered to pin is not a failure -- it is simply
// never visited. Creating atc/agent/<anything> and forgetting the pin was
// therefore legal and silent, which is the opposite of what a ratchet is for:
// the first commit of a new agentic package is exactly when its reach is
// cheapest to bound, and exactly when it is easiest to forget.
//
// This walks the graph from the other end. Every package the classifier calls
// agentic must appear as a key in the pins, whether or not anyone thought to
// add it.
//
// It takes the graph, the classifier and the pins as arguments rather than
// reading the package-level globals, so that A6 can drive it with a fixture and
// prove it objects to a scan that classified nothing. A guard that silently
// matches zero packages passes forever (AGENTS.md, Repo conventions); this one
// reports the empty scan as its first problem. It deliberately asserts no exact
// number of agentic packages -- only that there is at least one, and that each
// of them is pinned.
func unpinnedAgenticPackages(graph importGraph, agentic func(string) bool, pins map[string][]string) []string {
	var (
		problems   []string
		classified []string
	)

	// Production edges only, matching the scope of the ratchet this feeds.
	for pkg := range graph.prod {
		if agentic(pkg) {
			classified = append(classified, pkg)
		}
	}
	sort.Strings(classified)

	if len(classified) == 0 {
		problems = append(problems,
			"the agentic classification matched no package in the import graph. "+
				"Either agenticPackages/agenticPrefixes stopped describing the tree, "+
				"or the listing failed -- and every pin check below would pass "+
				"vacuously either way.")

		return problems
	}

	for _, pkg := range classified {
		if _, pinned := pins[pkg]; !pinned {
			problems = append(problems, "agentic package "+pkg+" is not pinned in "+
				"agenticCoreReach. Every core package the agent layer touches is a "+
				"coupling v4 inherits, so a new agentic package declares its reach on "+
				"its first commit -- when it is one line -- rather than accumulating "+
				"couplings nobody recorded. Add the pin, listing exactly the in-module "+
				"core packages it imports.")
		}
	}

	return problems
}

func TestAgenticLayerDoesNotReachIntoCoreInternals(t *testing.T) {
	graph := loadImportGraph(t)

	// Every agentic package must be pinned, not just every pin visited. Without
	// this the loop below is opt-in and an unpinned package is invisible to it.
	for _, problem := range unpinnedAgenticPackages(graph, isAgentic, agenticCoreReach) {
		t.Errorf("%s", problem)
	}

	for pkg, pinned := range agenticCoreReach {
		imports, ok := graph.prod[pkg]
		if !ok {
			t.Errorf("agenticCoreReach pins %q, which is not in the import graph. "+
				"Update the pin.", pkg)
			continue
		}

		pinnedSet := map[string]bool{}
		for _, p := range pinned {
			pinnedSet[p] = true
		}

		actualSet := map[string]bool{}
		for _, imp := range imports {
			// Only production imports. A test reaching for a helper is not the
			// coupling this guards.
			if isAgentic(imp) {
				continue
			}
			actualSet[imp] = true
		}

		var added []string
		for imp := range actualSet {
			if !pinnedSet[imp] {
				added = append(added, imp)
			}
		}
		sort.Strings(added)

		for _, imp := range added {
			t.Errorf("agentic package %s reaches into a new core package: %s\n"+
				"Every core package the agent layer touches is a coupling v4 "+
				"inherits. Prefer core's published API over its internals; if the "+
				"dependency is genuinely needed, pin it in agenticCoreReach.",
				pkg, imp)
		}

		for _, p := range pinned {
			if !actualSet[p] {
				t.Errorf("agenticCoreReach pins %s -> %s, but that import is gone. "+
					"Remove the pin so it keeps describing reality.", pkg, p)
			}
		}
	}
}

// TestUnpinnedAgenticPackagesGuardFailsOnAnEmptyScan is A6, and it is the reason
// the D9 assertion is a pure function over an injected graph and classifier
// rather than a loop over package-level globals.
//
// The failure this guards against is the one AGENTS.md names: a structural test
// that silently matches zero packages passes forever. If `agenticPrefixes` were
// mistyped, or `go list` returned a listing in which nothing classified as
// agentic, the "every agentic package is pinned" rule would be vacuously true
// and would report success for the rest of its life. So the helper is driven
// here with a classifier that deliberately matches nothing, and it must object.
//
// The fixture is the test's own -- no `go list`, no edit to the tree -- which is
// what makes this committable, unlike A5, A7, A8 and A9.
func TestUnpinnedAgenticPackagesGuardFailsOnAnEmptyScan(t *testing.T) {
	graph := importGraph{
		prod: map[string][]string{
			"atc/agent/one":   {"atc", "atc/runs"},
			"atc/agent/two":   {"atc"},
			"atc/agent/three": {"atc"},
			"atc/db":          {"atc"},
		},
		all: map[string][]string{},
	}

	matchesNothing := func(string) bool { return false }
	matches := func(pkgs ...string) func(string) bool {
		set := map[string]bool{}
		for _, p := range pkgs {
			set[p] = true
		}

		return func(pkg string) bool { return set[pkg] }
	}

	t.Run("objects when the classification matched no package", func(t *testing.T) {
		problems := unpinnedAgenticPackages(graph, matchesNothing, map[string][]string{})
		if len(problems) == 0 {
			t.Fatalf("the guard passed on a scan that classified no package at all. " +
				"That is the vacuous green this assertion exists to prevent.")
		}
		if !strings.Contains(problems[0], "matched no package") {
			t.Errorf("first problem should name the empty scan, got: %q", problems[0])
		}
	})

	t.Run("is silent when every classified package is pinned", func(t *testing.T) {
		problems := unpinnedAgenticPackages(graph,
			matches("atc/agent/one", "atc/agent/two"),
			map[string][]string{
				"atc/agent/one": {"atc", "atc/runs"},
				"atc/agent/two": {"atc"},
			})
		if len(problems) != 0 {
			t.Errorf("expected no problems, got %v", problems)
		}
	})

	// The same, with a different number of packages. The guard must not encode
	// how many agentic packages there are -- only that there is at least one and
	// that each of them is pinned.
	t.Run("is silent for a different number of pinned packages", func(t *testing.T) {
		problems := unpinnedAgenticPackages(graph,
			matches("atc/agent/one", "atc/agent/two", "atc/agent/three"),
			map[string][]string{
				"atc/agent/one":   {"atc", "atc/runs"},
				"atc/agent/two":   {"atc"},
				"atc/agent/three": {"atc"},
			})
		if len(problems) != 0 {
			t.Errorf("expected no problems, got %v", problems)
		}
	})

	t.Run("names a classified package that is not pinned", func(t *testing.T) {
		problems := unpinnedAgenticPackages(graph,
			matches("atc/agent/one", "atc/agent/two"),
			map[string][]string{"atc/agent/one": {"atc", "atc/runs"}})
		if len(problems) != 1 {
			t.Fatalf("expected exactly the one unpinned package to be reported, got %v", problems)
		}
		if !strings.Contains(problems[0], "atc/agent/two") {
			t.Errorf("problem should name the unpinned package, got: %q", problems[0])
		}
	})
}

// THE RUN CONTRACT'S SINGLE PATHS, STATED AS SOURCE PROPERTIES.
//
// Durable Run cancellation rests on four structural claims, none of which any
// one Run can observe (durable_run_cancellation_control T5):
//
//   - one function writes a Run's status: the v2 terminalizer. A second is a
//     second way to publish an outcome, which is what a "cancelling" or
//     API-only status change would be;
//   - one function records the cancellation request and fence. The API and the
//     deadline follow-on are callers of it, not further entry points, so this
//     counts writers, never callers;
//   - core's run packages cannot kill anything Kubernetes-specifically: they do
//     not link a Kubernetes client or the JetBridge runtime, so stopping work is
//     only ever the executor-neutral node protocol behind an interface;
//
// Each check is a pure function over what the scan found, so each is driven
// below with a scan that finds nothing and must object, like
// TestUnpinnedAgenticPackagesGuardFailsOnAnEmptyScan.

// goSourceFile is one parsed production Go file of the root module.
type goSourceFile struct {
	rel  string // slash-separated, relative to the repository root
	file *ast.File
}

// productionGoFiles parses every non-test Go file of the root module that
// include accepts. Nested modules are other modules -- the brine suite's step
// fixtures write Run rows directly on purpose -- so their trees are skipped.
func productionGoFiles(t *testing.T, include func(rel string) bool) []goSourceFile {
	t.Helper()

	root := repositoryRoot()
	fset := token.NewFileSet()
	var files []goSourceFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", ".claude", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relative)
		if !include(rel) {
			return nil
		}
		// Comments are left out: prose may name the seam it keeps.
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files = append(files, goSourceFile{rel: rel, file: file})

		return nil
	})
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	return files
}

// pipelineRunUpdate captures the SET list of an UPDATE of the Run header.
var pipelineRunUpdate = regexp.MustCompile(`(?is)\bUPDATE\s+pipeline_runs\b(?:\s+(?:AS\s+)?[a-z_]+)??\s+SET\s+(.*?)(?:\bWHERE\b|\bFROM\b|\bRETURNING\b|$)`)

// runHeaderColumns names Run header columns two ways: inside a SQL SET list,
// and as the bare column name a query builder's Set takes.
type runHeaderColumns struct{ inSQL, name *regexp.Regexp }

var (
	runStatusColumn = runHeaderColumns{
		inSQL: regexp.MustCompile(`(?i)(?:^|[\s,.])status\s*=`),
		name:  regexp.MustCompile(`^status$`),
	}
	runFenceColumns = runHeaderColumns{
		inSQL: regexp.MustCompile(`(?i)(?:^|[\s,.])(?:cancel_requested_at|cancel_requested_by|cancel_reason)\s*=`),
		name:  regexp.MustCompile(`^(?:cancel_requested_at|cancel_requested_by|cancel_reason)$`),
	}
)

// stringArgument returns call's first argument when it is a string literal.
func stringArgument(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	return text, err == nil
}

// localAssignments records every expression assigned to each local name in
// decl, so a builder or a map held in a variable can be followed back.
func localAssignments(decl ast.Decl) map[string][]ast.Expr {
	locals := map[string][]ast.Expr{}
	ast.Inspect(decl, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for k, lhs := range n.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						locals[ident.Name] = append(locals[ident.Name], n.Rhs[k])
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for k, ident := range n.Names {
					locals[ident.Name] = append(locals[ident.Name], n.Values[k])
				}
			}
		}
		return true
	})
	return locals
}

// builderTable follows a query builder chain (psql.Update("t").Set(...)...)
// back to its Update call and returns the table, through builders held in
// local variables. It reports false when the chain has no visible Update.
func builderTable(expr ast.Expr, locals map[string][]ast.Expr, seen map[string]bool) (string, bool) {
	for {
		switch e := expr.(type) {
		case *ast.CallExpr:
			selector, ok := e.Fun.(*ast.SelectorExpr)
			if !ok {
				return "", false
			}
			if selector.Sel.Name == "Update" {
				return stringArgument(e)
			}
			expr = selector.X
		case *ast.Ident:
			if seen[e.Name] {
				return "", false
			}
			seen[e.Name] = true
			for _, assigned := range locals[e.Name] {
				if table, ok := builderTable(assigned, locals, seen); ok {
					return table, true
				}
			}
			return "", false
		default:
			return "", false
		}
	}
}

// setMapNames reports whether a SetMap argument may assign a column column
// matches: a map literal (a map[string]any, an sq.Eq) is read key by key,
// through a local variable if need be; a map the scan cannot see may set
// anything, so it counts.
func setMapNames(expr ast.Expr, locals map[string][]ast.Expr, column *regexp.Regexp) bool {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		for _, element := range e.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.BasicLit)
			if !ok || key.Kind != token.STRING {
				return true
			}
			if name, err := strconv.Unquote(key.Value); err == nil && column.MatchString(name) {
				return true
			}
		}
		return false
	case *ast.Ident:
		assigned := locals[e.Name]
		if len(assigned) == 0 {
			return true
		}
		for _, value := range assigned {
			literal, ok := value.(*ast.CompositeLit)
			if !ok || setMapNames(literal, nil, column) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// runHeaderWriters names every declaration ("file:Func") that updates
// pipeline_runs and assigns a column column matches: in a SQL string literal,
// or through a Set or SetMap on a query builder chained from
// Update("pipeline_runs"). A column name or map the scan cannot read counts.
func runHeaderWriters(files []goSourceFile, column runHeaderColumns) []string {
	var writers []string
	for _, source := range files {
		for _, decl := range source.file.Decls {
			name := "<package scope>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = fn.Name.Name
			}
			writes := false
			locals := localAssignments(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				// A query builder writes the column only when this Set or
				// SetMap belongs to an Update of pipeline_runs.
				if call, ok := node.(*ast.CallExpr); ok && len(call.Args) > 0 {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && (selector.Sel.Name == "Set" || selector.Sel.Name == "SetMap") {
						if table, ok := builderTable(selector.X, locals, map[string]bool{}); ok && table == "pipeline_runs" {
							if selector.Sel.Name == "Set" {
								if text, ok := stringArgument(call); !ok || column.name.MatchString(text) {
									writes = true
								}
							} else if setMapNames(call.Args[0], locals, column.name) {
								writes = true
							}
						}
					}
				}
				lit, ok := node.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, update := range pipelineRunUpdate.FindAllStringSubmatch(text, -1) {
					if column.inSQL.MatchString(update[1]) {
						writes = true
					}
				}

				return true
			})
			if writes {
				writers = append(writers, source.rel+":"+name)
			}
		}
	}
	sort.Strings(writers)

	return writers
}

// exactlyOneWriter is the rule both single-path guards share.
func exactlyOneWriter(what string, scanned int, writers []string, want string) []string {
	switch {
	case scanned == 0:
		return []string{fmt.Sprintf("the %s scan matched no file; the rule would pass vacuously", what)}
	case len(writers) == 0:
		return []string{fmt.Sprintf("no declaration writes the %s. %s is expected to; if it moved, "+
			"the pattern is now guarding a string", what, want)}
	case len(writers) > 1 || writers[0] != want:
		return []string{fmt.Sprintf("the %s has %d writers, %v; exactly one, %s, is allowed", what, len(writers), writers, want)}
	}

	return nil
}

const (
	runStatusWriter = "atc/db/pipeline_run_result.go:finalizeOutputRun"
	runFenceWriter  = "atc/db/pipeline_run_cancellation.go:acceptRunCancellation"
)

func TestExactlyOneFunctionWritesARunsStatusAndOneRecordsItsCancellationFence(t *testing.T) {
	files := productionGoFiles(t, func(string) bool { return true })
	if len(files) < 500 {
		t.Fatalf("parsed only %d production Go files; the walk failed", len(files))
	}

	for _, problem := range exactlyOneWriter("v2 Run status", len(files), runHeaderWriters(files, runStatusColumn), runStatusWriter) {
		t.Error(problem + ". A Run's status changes once, in the one terminal publication; " +
			"cancellation and ordinary completion both publish through it.")
	}
	for _, problem := range exactlyOneWriter("Run cancellation request and fence", len(files), runHeaderWriters(files, runFenceColumns), runFenceWriter) {
		t.Error(problem + ". The API and any deadline scanner call the one accept/fence " +
			"operation; a second writer is a second way to stop a Run.")
	}
}

// No step configuration can ask for an added capability or a host namespace,
// because nothing that builds a step pod sets one and a sidecar has no field
// to carry one (step_pod_privilege_gate, spec amendment B1 §1). This test
// keeps it so: it fails when production code in atc/worker/jetbridge sets
// Capabilities, HostNetwork, HostPID or HostIPC, or when SidecarConfig has a
// field that could carry a security request. The step pod admission refuses
// such a pod only under --kubernetes-step-privilege=granted; under any, the
// default, it checks only hostPath volumes and their mounts.
func TestNoStepPodCodeSetsACapabilityOrAHostNamespace(t *testing.T) {
	files := productionGoFiles(t, func(rel string) bool {
		return filepath.ToSlash(filepath.Dir(rel)) == "atc/worker/jetbridge"
	})
	for _, problem := range podEscalationFieldsSet(files) {
		t.Error(problem + ". Step pods never add capabilities or join a host namespace.")
	}

	sidecar := productionGoFiles(t, func(rel string) bool { return filepath.ToSlash(rel) == "atc/sidecar.go" })
	for _, problem := range sidecarSecurityFields(sidecar) {
		t.Error(problem + ". A sidecar's security comes from the runtime, never from its config.")
	}
}

var podEscalationField = map[string]bool{"Capabilities": true, "HostNetwork": true, "HostPID": true, "HostIPC": true}

// podEscalationFieldsSet reports every composite-literal key or assignment
// that sets a capability set or a host namespace.
func podEscalationFieldsSet(files []goSourceFile) []string {
	if len(files) == 0 {
		return []string{"the step pod scan matched no file; the rule would pass vacuously"}
	}
	var problems []string
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && podEscalationField[key.Name] {
					problems = append(problems, fmt.Sprintf("%s sets %s", source.rel, key.Name))
				}
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && podEscalationField[sel.Sel.Name] {
						problems = append(problems, fmt.Sprintf("%s sets %s", source.rel, sel.Sel.Name))
					}
				}
			}
			return true
		})
	}
	sort.Strings(problems)

	return problems
}

var securityFieldName = regexp.MustCompile(`(?i)privileg|capabilit|securitycontext|hostnetwork|hostpid|hostipc|runas|seccomp|apparmor|selinux`)

// sidecarSecurityFields reports a SidecarConfig field that could carry a
// security request.
func sidecarSecurityFields(files []goSourceFile) []string {
	var problems []string
	found := false
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || spec.Name.Name != "SidecarConfig" {
				return true
			}
			found = true
			if st, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						if securityFieldName.MatchString(name.Name) {
							problems = append(problems, fmt.Sprintf("%s: SidecarConfig.%s", source.rel, name.Name))
						}
					}
				}
			}
			return false
		})
	}
	if !found {
		return []string{"no SidecarConfig type was found; the rule would pass vacuously"}
	}

	return problems
}

// kubernetesReach reports every run package whose linked dependencies include
// a Kubernetes client or the JetBridge runtime. deps maps each run package to
// its `go list -deps` listing.
func kubernetesReach(deps map[string][]string) []string {
	if len(deps) == 0 {
		return []string{"the run-package dependency scan listed no package; the rule would pass vacuously"}
	}
	var problems []string
	for pkg, listed := range deps {
		if len(listed) == 0 {
			problems = append(problems, fmt.Sprintf("%s listed no dependency at all; the listing failed", pkg))
		}
		for _, dep := range listed {
			if strings.HasPrefix(dep, "k8s.io/") || strings.HasPrefix(dep, modulePrefix+"atc/worker/jetbridge") {
				problems = append(problems, fmt.Sprintf("%s links %s", pkg, dep))
			}
		}
	}
	sort.Strings(problems)

	return problems
}

func TestTheRunPackagesCannotKillAnythingKubernetesSpecifically(t *testing.T) {
	deps := map[string][]string{}
	for _, pkg := range []string{"atc/runs", "atc/api/pipelinerunserver", "atc/db"} {
		out, err := exec.Command("go", "list", "-deps", "./"+pkg).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("go list -deps %s failed: %v\n%s", pkg, err, ee.Stderr)
			}
			t.Fatalf("go list -deps %s failed: %v", pkg, err)
		}
		deps[pkg] = strings.Fields(string(out))
	}

	for _, problem := range kubernetesReach(deps) {
		t.Error(problem + ". Run cancellation stops work only through the executor-neutral " +
			"node protocol (runs.CancellationSourcePlane); a Pod deletion is not an " +
			"acknowledgement and has no business being reachable from core.")
	}
}

// Each guard above objects to a scan that found nothing, and recognises the
// thing it forbids in a fixture of its own.
func TestTheRunContractGuardsFailOnAnEmptyScan(t *testing.T) {
	parse := func(rel, src string) goSourceFile {
		file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing fixture: %v", err)
		}

		return goSourceFile{rel: rel, file: file}
	}

	t.Run("single writers", func(t *testing.T) {
		if problems := exactlyOneWriter("status", 0, nil, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule passed on a scan of no file")
		}
		if problems := exactlyOneWriter("status", 10, nil, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule passed when no writer was found at all")
		}

		files := []goSourceFile{
			parse("a.go", "package a\nfunc f() { _ = `UPDATE pipeline_runs SET status=$2,completed_at=$3 WHERE id=$1` }\n"),
			parse("b.go", "package b\nconst q = \"UPDATE pipeline_runs r SET\\n cancel_requested_at = now() WHERE r.id=$1\"\n"),
			parse("c.go", "package c\nfunc g() { _ = `UPDATE pipeline_runs SET reclaim_retry_after=$2 WHERE status='running'` }\n"),
			parse("d.go", "package d\nfunc h() { _ = `UPDATE pipeline_runs_other SET status=$1` }\n"),
			parse("e.go", "package e\nfunc k() { psql.Update(\"pipeline_runs\").Set(\"status\", 1) }\n"),
			parse("f.go", "package f\nfunc m() { psql.Update(\"pipelines\").Set(\"status\", 1) }\n"),
			parse("g.go", "package g\nfunc n() { psql.Update(\"pipeline_runs\").SetMap(map[string]any{\"status\": 1}) }\n"),
			parse("h.go", "package h\nfunc o() { psql.Update(\"pipeline_runs\").SetMap(sq.Eq{\"reclaim_retry_after\": 1}) }\n"),
			// Two statements in one function: each Set belongs to its own Update.
			parse("i.go", "package i\nfunc p() { psql.Update(\"pipeline_runs\").Set(\"reclaim_retry_after\", 1); psql.Update(\"builds\").Set(\"status\", 1) }\n"),
			// A map held in a local variable is resolved to its literal.
			parse("j.go", "package j\nfunc q() { values := sq.Eq{\"status\": 1}; psql.Update(\"pipeline_runs\").SetMap(values) }\n"),
			// A map the scan cannot see counts as a write: it may set any column.
			parse("k.go", "package k\nfunc r(values map[string]any) { psql.Update(\"pipeline_runs\").SetMap(values) }\n"),
			// A builder held in a local variable keeps its table.
			parse("l.go", "package l\nfunc s() { q := psql.Update(\"pipeline_runs\"); q = q.Set(\"status\", 1) }\n"),
			parse("m.go", "package m\nfunc t(values map[string]any) { psql.Update(\"builds\").SetMap(values) }\n"),
		}
		status := runHeaderWriters(files, runStatusColumn)
		wantStatus := []string{"a.go:f", "e.go:k", "g.go:n", "j.go:q", "k.go:r", "l.go:s"}
		if !slices.Equal(status, wantStatus) {
			t.Errorf("status writers = %v, want %v (a WHERE clause, another table or another column is not a write; an uninspectable map on pipeline_runs is)", status, wantStatus)
		}
		fence := runHeaderWriters(files, runFenceColumns)
		if wantFence := []string{"b.go:<package scope>", "k.go:r"}; !slices.Equal(fence, wantFence) {
			t.Errorf("fence writers = %v, want %v", fence, wantFence)
		}
		if problems := exactlyOneWriter("status", len(files), status, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule accepted a second writer")
		}
		if problems := exactlyOneWriter("status", len(files), status[:1], "a.go:f"); len(problems) != 0 {
			t.Errorf("the single-writer rule objected to the one allowed writer: %v", problems)
		}
	})

	t.Run("pod escalation fields", func(t *testing.T) {
		if problems := podEscalationFieldsSet(nil); len(problems) == 0 {
			t.Error("the escalation rule passed on a scan of no file")
		}
		for name, src := range map[string]string{
			"capabilities": "package a\nvar c = corev1.SecurityContext{Capabilities: &corev1.Capabilities{}}\n",
			"host network": "package a\nvar s = corev1.PodSpec{HostNetwork: true}\n",
			"assigned PID": "package a\nfunc f(s *corev1.PodSpec) { s.HostPID = true }\n",
		} {
			if problems := podEscalationFieldsSet([]goSourceFile{parse("a.go", src)}); len(problems) == 0 {
				t.Errorf("the escalation rule missed %s", name)
			}
		}
		if problems := sidecarSecurityFields(nil); len(problems) == 0 {
			t.Error("the sidecar rule passed with no SidecarConfig")
		}
		if problems := sidecarSecurityFields([]goSourceFile{parse("s.go", "package a\ntype SidecarConfig struct { Name string; Privileged bool }\n")}); len(problems) == 0 {
			t.Error("the sidecar rule missed a Privileged field")
		}
		if problems := sidecarSecurityFields([]goSourceFile{parse("s.go", "package a\ntype SidecarConfig struct { Name string; Image string }\n")}); len(problems) != 0 {
			t.Errorf("the sidecar rule objected to ordinary fields: %v", problems)
		}
	})

	t.Run("kubernetes reach", func(t *testing.T) {
		if problems := kubernetesReach(nil); len(problems) == 0 {
			t.Error("the Kubernetes rule passed on a listing of no package")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": nil}); len(problems) == 0 {
			t.Error("the Kubernetes rule passed on an empty dependency listing")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context", "k8s.io/client-go/kubernetes"}}); len(problems) == 0 {
			t.Error("the Kubernetes rule missed a linked Kubernetes client")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context", modulePrefix + "atc/worker/jetbridge"}}); len(problems) == 0 {
			t.Error("the Kubernetes rule missed the linked JetBridge runtime")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context"}}); len(problems) != 0 {
			t.Errorf("the Kubernetes rule objected to a clean listing: %v", problems)
		}
	})
}

// A landing queue admits Runs as its team with nothing to verify against
// (ADR-0011), so whoever can construct a queue principal can act as any team.
// Only the landing queue's own component may, and this is the pin: every
// non-test file that names runs.QueuePrincipal is atc/landing or atc/runs.
func TestTheQueuePrincipalIsConstructedOnlyByTheLandingQueue(t *testing.T) {
	root := repositoryRoot()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if strings.HasPrefix(rel, "atc/runs/") || strings.HasPrefix(rel, "atc/landing/") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "QueuePrincipal") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("runs.QueuePrincipal is constructed outside atc/landing: %v", offenders)
	}
}

// A cache or build artefact checked in by accident is never noticed in review:
// hack/__pycache__/*.pyc rode along for weeks. .gitignore keeps new ones out of
// `git add .`, but not out of `git add -f` or a file tracked before the ignore
// existed, so check what is tracked.
//
// In a checkout that means asking git. CI runs this against a `git archive` of
// the ref (hack/ci-check.sh, the pipeline's unit-tests task), which has no .git
// but contains exactly the tracked files, so there the tree itself is the list.
func TestNoBuildArtefactOrCacheIsTracked(t *testing.T) {
	files := trackedFiles(t)
	if len(files) < 1000 {
		t.Fatalf("only %d tracked files listed; the listing failed and this rule would pass vacuously", len(files))
	}

	for _, f := range files {
		segments := strings.Split(f, "/")
		if strings.HasSuffix(f, ".pyc") ||
			slices.Contains(segments, "__pycache__") ||
			slices.Contains(segments, "elm-stuff") ||
			slices.Contains(segments, "node_modules") {
			t.Errorf("%s is tracked; it is a cache or build artefact. `git rm --cached` it.", f)
		}
	}
}

// trackedFiles lists the repository's tracked files: `git ls-files` when the
// root is a checkout (.git is a directory, or a file in a worktree), otherwise
// a walk of the archive. The walk skips node_modules and elm-stuff, which a
// task may install or build into the archive before the tests run; a tracked
// copy of either is still caught by every local run.
func trackedFiles(t *testing.T) []string {
	t.Helper()

	if _, err := os.Stat(".git"); err == nil {
		out, err := exec.Command("git", "ls-files").Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("git ls-files failed: %v\n%s", err, ee.Stderr)
			}
			t.Fatalf("git ls-files failed: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	var files []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "elm-stuff":
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the archive: %v", err)
	}
	return files
}

// atc/imageresolver turns an image reference into a pinned digest for the web,
// the engine, lidar and the Kubernetes runtime alike. It can serve all of them
// only while it depends on none of them: one import of atc/db or a runtime
// package and the resolver joins whichever cycle that package is in. Today it
// imports `atc` (the shared types) and nothing else in the module.
func TestTheImageResolverStaysALeaf(t *testing.T) {
	const resolver = "atc/imageresolver"
	allowed := map[string]bool{"atc": true}

	graph := loadImportGraph(t)
	imports, ok := graph.prod[resolver]
	if !ok {
		t.Fatalf("%s is not in the import graph; this rule would pass vacuously. "+
			"If it moved, move the rule with it.", resolver)
	}

	for _, imp := range imports {
		if !allowed[imp] {
			t.Errorf("%s imports %s. The resolver is a leaf every layer imports; "+
				"pass what it needs in instead of importing it.", resolver, imp)
		}
	}
	for imp := range allowed {
		if !slices.Contains(imports, imp) {
			t.Errorf("%s no longer imports %s; drop it from the allowed set so the rule "+
				"keeps describing the package.", resolver, imp)
		}
	}
}

// Counterfeiter's generated fakes were removed with tools.go and the module
// requirement; the tree's doubles are real servers and real Postgres now. A
// go:generate line or a tools import would bring the generator back quietly,
// so ask the build graph, tests included, whether anything links it.
func TestNothingDependsOnCounterfeiter(t *testing.T) {
	out, err := exec.Command("go", "list", "-e", "-deps", "-test", "./...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps -test failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps -test failed: %v", err)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(deps) < 500 {
		t.Fatalf("go list -deps -test listed only %d packages; the listing failed and "+
			"this rule would pass vacuously", len(deps))
	}

	for _, dep := range deps {
		if strings.Contains(dep, "counterfeiter") {
			t.Errorf("%s is in the build graph. Counterfeiter fakes were removed; "+
				"use a real server or real Postgres instead.", dep)
		}
	}
}

// A lager.Logger built mid-wiring has no sink, or a sink of its own at a fixed
// level: its lines vanish (the migrator's did) or ignore --log-level and the
// atc.* session chain (the daemon client's did). A logger is made once, at a
// composition root, and passed down. This rule pins that: outside package
// main and test files, nothing calls lager.NewLogger. Nested modules (the
// brine suite) are other modules and are not scanned.
//
// newLoggerExceptions are test-support packages that build a whole web for a
// spec and so are its composition root.
var newLoggerExceptions = map[string]string{
	"atc/atctest/atctest.go": "the in-process ATC platform for integration specs is its own composition root",
}

func TestOnlyCompositionRootsBuildALagerLogger(t *testing.T) {
	var offenders []string
	inMain := 0
	for _, source := range productionGoFiles(t, func(string) bool { return true }) {
		lagerName := ""
		for _, spec := range source.file.Imports {
			if path, _ := strconv.Unquote(spec.Path.Value); path == "code.cloudfoundry.org/lager/v3" {
				lagerName = "lager"
				if spec.Name != nil {
					lagerName = spec.Name.Name
				}
			}
		}
		if lagerName == "" {
			continue
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewLogger" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != lagerName {
				return true
			}
			switch {
			case source.file.Name.Name == "main":
				inMain++
			case newLoggerExceptions[source.rel] != "":
			default:
				offenders = append(offenders, source.rel)
			}
			return true
		})
	}
	// cmd/artifact-daemon/main.go builds one: a scan that finds none in a
	// main package has stopped recognising the call and would pass vacuously.
	if inMain == 0 {
		t.Fatal("found no lager.NewLogger in any main package; the scan is not recognising the call")
	}
	if len(offenders) != 0 {
		t.Fatalf("lager.NewLogger outside a composition root: %v\n\nTake the caller's "+
			"logger (logger.Session(...)) instead; a logger built here has no sink or "+
			"ignores --log-level.", offenders)
	}
}

// goList runs `go list` once per distinct argument list for this test
// process. Several guards ask the toolchain the same question of the same
// tree (the full -json graph four times, `-deps <root>` once per role per
// root); each still parses the output and makes its own assertions. No test
// in this package changes directory or environment, so the arguments are the
// whole key. A failed listing is not kept, and every caller gets its own copy
// of the bytes.
func goList(args ...string) ([]byte, error) {
	entry, _ := goListResults.LoadOrStore(strings.Join(args, "\x00"), &goListResult{})
	result := entry.(*goListResult)
	result.once.Do(func() {
		result.out, result.err = exec.Command("go", append([]string{"list"}, args...)...).Output()
	})
	if result.err != nil {
		goListResults.CompareAndDelete(strings.Join(args, "\x00"), result)
	}
	return bytes.Clone(result.out), result.err
}

var goListResults sync.Map

type goListResult struct {
	once sync.Once
	out  []byte
	err  error
}
