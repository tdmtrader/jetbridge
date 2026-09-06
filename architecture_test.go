package concourse

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
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
// It is empty, which is the end state worth defending: core does not name the
// agentic layer at all. atc/api held the only entry until the MCP tool surface
// and its route were removed; what remains of mcpserver is transport with no
// Concourse imports and no registration.
//
// Adding an entry here is the moment to ask whether the dependency should be
// inverted instead -- the agentic side depending on core costs nothing, and
// core depending on the agentic side is what made v1/v2/v3 inseparable.
var wiringPoints = map[string]string{}

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
	cmd := exec.Command("go", "list", "-e", "-json", "./...")
	out, err := cmd.Output()
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
