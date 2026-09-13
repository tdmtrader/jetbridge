package concourse

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
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

// hangarGCSPackage is the Hangar OUTPUT plane's Cloud Storage seam: the object
// adapter its four roles share, and the bucket-metadata source the attestor
// reads. It is a daemon-side implementation detail, and this is the second half
// of the rule stated in hangar/architecture_test.go: that one keeps the cloud
// client out of package hangar, this one keeps it out of everything that is not
// a daemon.
//
// The cost of losing it is measurable rather than theoretical. hangar is
// imported by atc/runtime, atc/atccmd and atc/worker/jetbridge; while the GCS
// store lived in package hangar it linked cloud.google.com/go/storage into
// ./cmd/concourse, taking that binary from 1347 to 1515 packages and from
// 126,836,146 to 146,976,274 bytes. One convenient import from atc puts all of
// it back.
const hangarGCSPackage = "hangar/gcs"

// hangarStorePackage is the artifact daemon's strict-input Hangar store, which
// was in hangar/gcs until the third round of one finding.
//
// It is a separate package because of GCSStore.DeleteTree. While the store sat
// beside the output object seam, the output daemon, the inventory controller
// and the attestor -- all of which link that seam -- could name GCSStore, point
// a GCSConfig at the output bucket and delete a key from a root that is not the
// reclaimer. That is the "key-only delete route" Req 55 asks the guards to
// reject, and no import guard about hangar/gcs could see it, because naming
// GCSStore WAS naming hangar/gcs.
const hangarStorePackage = "hangar/gcsstore"

// hangarStoreImporters: exactly one binary, and its own specs.
var hangarStoreImporters = map[string]string{
	"cmd/artifact-daemon": "the artifact daemon is the only process that talks to the cache " +
		"and strict-input buckets, and the only one that has any business holding a store " +
		"whose DeleteTree takes a key",
}

// hangarGCSImporters are the packages allowed to name it, each with the reason.
//
// The list grew with the output plane, and the entries are deliberately three
// binaries-or-harnesses rather than a package layer. The role packages
// (hangar/output/publisher and its siblings) are NOT here and must not be:
// they depend on hangar/objectstore, which names no cloud SDK type, and that
// is what keeps the cloud client out of anything that links a role.
var hangarGCSImporters = map[string]string{
	"cmd/hangar-output-daemon": "the output daemon is the only process that talks to the output " +
		"bucket, under its own service account; a Kubernetes service account is Pod-wide, so this " +
		"is a second binary precisely so the first one's identity gains no output role",
	"cmd/hangar-output-inventory": "the inventory controller is the list/get principal, and the " +
		"only workload in this system whose cloud identity holds bucket-wide list",
	"hangar/gcsdelete": "the object-delete capability's own package. It names the cloud client " +
		"because it IS the adapter, and it shares hangar/gcs's 404/412/403 split rather than " +
		"re-deriving it -- a second reading of those three codes is where a delete eventually " +
		"gets told that 412 means \"already gone\". Which binaries link it is the subject of " +
		"TestOnlyTheReclaimerBinaryCanInvokeAnOutputDelete, and the answer is one",
	"cmd/hangar-output-policy-attestor": "the attestor is the bucket-metadata principal: it " +
		"reads lifecycle and IAM and holds no object permission, which is why its compromise " +
		"costs the assessment rather than the data",
	"hangar/output/conformance": "the tier-2 conformance suite drives the real adapter against " +
		"fake-gcs-server, because a conformance claim proved through a hand-written fake is a " +
		"claim about the fake. It is a test-only import: the package has no non-test file that " +
		"names hangar/gcs, and TestTheConcourseBinaryLinksNoCloudStorageClient below is what " +
		"makes the consequence -- ./cmd/concourse -- checkable rather than argued",
	"atc/hangaroutput": "TEST-ONLY: the managed-read specs stat the published object through " +
		"the real client against the emulator the daemon published into. The package's " +
		"production code declares ExactStat as a port and names no cloud SDK; " +
		"testOnlyGCSImporters below checks that rather than believing it",
}

// testOnlyGCSImporters are the exemptions above whose reason says TEST-ONLY, and
// the check that follows is what makes the words true.
var testOnlyGCSImporters = map[string]bool{
	"hangar/output/conformance": true,
	"atc/hangaroutput":          true,
}

// outputRolePackages are the four cloud-facing roles of the Hangar output plane.
//
// Each is a separate binary with a separate Kubernetes service account, and the
// isolation only means something while no other process links one. The
// existing artifact daemon is the case that matters: its identity holds the
// cache and strict-input roles, and a service account is Pod-wide, so an
// import here would give that identity an output role no code in that process
// could give back.
var outputRolePackages = []string{
	"hangar/output/publisher",
	"hangar/output/inventory",
	"hangar/output/reclaimer",
	"hangar/output/policy",
}

// outputRoleImporters are the packages allowed to link one, with the reason.
var outputRoleImporters = map[string]string{
	"cmd/hangar-output-daemon":          "the output daemon is the publisher principal",
	"cmd/hangar-output-inventory":       "the inventory controller is the inventory principal",
	"cmd/hangar-output-reclaimer":       "the reclaimer is the reclaimer principal",
	"cmd/hangar-output-policy-attestor": "the attestor is the policy principal",
	"hangar/output/conformance": "the shared conformance suite drives all four roles against " +
		"both substrate tiers; it is a test-only package that links into no binary",
	"atc/hangaroutput": "TEST-ONLY: the managed-read specs admit a read against the REAL " +
		"publisher's exact-generation stat over the same bucket the daemon published into, " +
		"because requirement 35 is about the object rather than about a fixture's opinion of " +
		"it. The package's production code declares ExactStat as a port and links nothing",

	// The three pass packages. Each is one principal's bounded unit of work,
	// lifted out of `package main` so the composition can be driven -- which
	// is what the Phase 7 review found nothing was doing. Each links EXACTLY
	// ONE role and is linked by exactly one binary, so the principal boundary
	// is unchanged: the guard below over cmd/ roots is what keeps that true,
	// and it reads the real build graph rather than these words.
	"atc/hangaroutput/inventorypass": "the inventory controller's bounded unit, lifted out of " +
		"its main so it can be driven; it links the inventory role and no other, and only " +
		"cmd/hangar-output-inventory links it",
	"atc/hangaroutput/reclaimpass": "the reclaimer's two bounded units, lifted out of its " +
		"main so they can be driven; they link the reclaimer role and no other, and only " +
		"cmd/hangar-output-reclaimer links them",
	"atc/hangaroutput/attestpass": "the attestor's bounded unit, lifted out of its main so it " +
		"can be driven; it links the policy role and no other, and only " +
		"cmd/hangar-output-policy-attestor links it",

	"atc/db": "TEST-ONLY: the controller-pass specs drive the real inventory and reclaimer " +
		"roles against real PostgreSQL and the tier-1 store, because the composition -- which " +
		"record precedes which effect -- is what the phase shipped unwired. The package's " +
		"production code links no role",
}

// testOnlyRoleImporters are the exemptions above whose reason says TEST-ONLY.
//
// The reason is CHECKED rather than believed: an exemption stated in a comment
// is one a later edit can quietly turn into a production import, and the whole
// point of the principal boundary is that a Pod's identity is Pod-wide. The
// check reads the non-test import graph, so a production file that named one of
// these would fail here even though the exemption is still listed.
var testOnlyRoleImporters = map[string]bool{
	"hangar/output/conformance": true,
	"atc/hangaroutput":          true,
	"atc/db":                    true,
}

// TestTheOutputRolesAreLinkedOnlyByTheirOwnPrincipals is the import half of the
// principal boundary.
func TestTheOutputRolesAreLinkedOnlyByTheirOwnPrincipals(t *testing.T) {
	graph := loadImportGraph(t)

	for _, role := range outputRolePackages {
		if _, ok := graph.all[role]; !ok {
			t.Fatalf("%s does not exist; this rule would pass vacuously", role)
		}
	}
	for importer := range outputRoleImporters {
		if _, ok := graph.all[importer]; !ok {
			t.Errorf("allowed importer %q does not exist; the exemption is stale", importer)
		}
	}

	forbidden := map[string]bool{}
	for _, role := range outputRolePackages {
		forbidden[role] = true
	}

	linked := 0
	for pkg, imports := range graph.all {
		for _, imported := range imports {
			if !forbidden[imported] {
				continue
			}
			if pkg == imported {
				// A role's own external test package (`package policy_test`)
				// importing the role. It is the same directory and the same
				// principal; counting it would make every role package an
				// extra importer of itself the moment it grew a test.
				continue
			}
			linked++
			if reason, ok := outputRoleImporters[pkg]; ok {
				t.Logf("allowed: %s links %s — %s", pkg, imported, reason)

				continue
			}
			t.Errorf("%s links %s.\n\nThe four output roles are four Kubernetes service "+
				"accounts. A Pod's identity is Pod-wide, so a process that links a role has "+
				"that role's cloud permission for everything else it does -- which is exactly "+
				"why cmd/artifact-daemon, the ATC and the web node link none of them. Depend "+
				"on the hangar/output interface, or add %s above with the reason it must be a "+
				"principal.", pkg, imported, pkg)
		}
	}
	if linked == 0 {
		t.Error("nothing links any output role at all, including the output daemon. Either the " +
			"roles moved or the graph is not being read, and in both cases this rule is " +
			"guarding nothing.")
	}

	// Every TEST-ONLY exemption is verified to be one.
	for importer := range testOnlyRoleImporters {
		if _, ok := outputRoleImporters[importer]; !ok {
			t.Errorf("%s is listed as a test-only exemption and is not an exemption at all; the "+
				"two lists have drifted", importer)
		}
		for _, imported := range graph.prod[importer] {
			if forbidden[imported] {
				t.Errorf("%s is exempted as a TEST-ONLY importer of an output role and its "+
					"PRODUCTION code imports %s. A Pod's identity is Pod-wide: an exemption for "+
					"a spec is not an exemption for a process.", importer, imported)
			}
		}
	}

	// The case this rule exists for, stated by name so a green says it was
	// checked rather than merely not violated.
	for _, role := range outputRolePackages {
		for _, imported := range graph.all["cmd/artifact-daemon"] {
			if imported == role {
				t.Errorf("cmd/artifact-daemon links %s. Its service account holds the cache and "+
					"strict-input roles; adding an output role to that Pod is the one thing the "+
					"second binary exists to prevent.", role)
			}
		}
	}
}

// cloudStorageModule is the dependency the rule above exists to keep out of the
// ATC binary.
const cloudStorageModule = "cloud.google.com/go/storage"

// TestTheConcourseBinaryLinksNoCloudStorageClient measures the consequence.
//
// The import allowlist above is a rule about who may name a package, and every
// entry added to it makes that rule weaker. This one is not a rule about names
// at all: it asks the toolchain what ./cmd/concourse actually links, so an
// allowlist entry that turned out to matter is caught by the fact it was
// supposed to prevent rather than by a reviewer noticing.
func TestTheConcourseBinaryLinksNoCloudStorageClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./cmd/concourse").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps failed: %v", err)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(deps) < 500 {
		t.Fatalf("go list -deps reported %d packages for ./cmd/concourse, which is far too few "+
			"to be the ATC; the listing failed and this check would pass vacuously", len(deps))
	}

	for _, dep := range deps {
		if dep == cloudStorageModule || strings.HasPrefix(dep, cloudStorageModule+"/") {
			t.Errorf("./cmd/concourse links %s.\n\nThe GCS client is a daemon-side detail. "+
				"While it lived in package hangar it took this binary from 1347 to 1515 packages "+
				"and from 126,836,146 to 146,976,274 bytes. Something in the ATC's dependency "+
				"graph now names hangar/gcs, or a package that does: depend on the "+
				"hangar.Store or hangar/objectstore interface instead.", dep)
		}
	}
}

func TestHangarGCSStoreIsImportedOnlyByTheDaemon(t *testing.T) {
	graph := loadImportGraph(t)

	if _, ok := graph.all[hangarGCSPackage]; !ok {
		t.Fatalf("%s does not exist; this rule would pass vacuously", hangarGCSPackage)
	}
	for importer := range hangarGCSImporters {
		imports, ok := graph.all[importer]
		if !ok {
			t.Errorf("allowed importer %q does not exist; the exemption is stale", importer)
			continue
		}
		// An exemption that is no longer used is a rule that got weaker for
		// free. cmd/artifact-daemon sat on this list after its store moved to
		// hangar/gcsstore and nothing said so, which is how an allowlist stops
		// describing the tree it guards.
		if !slices.Contains(imports, hangarGCSPackage) {
			t.Errorf("%s is exempted to import %s and does not import it. Delete the entry: an "+
				"exemption nobody uses is a hole nobody is watching.", importer, hangarGCSPackage)
		}
	}
	for importer := range testOnlyGCSImporters {
		if _, ok := hangarGCSImporters[importer]; !ok {
			t.Errorf("%s is listed as a test-only exemption and is not an exemption at all; the "+
				"two lists have drifted", importer)
		}
		for _, imported := range graph.prod[importer] {
			if imported == hangarGCSPackage {
				t.Errorf("%s is exempted as a TEST-ONLY importer of %s and its PRODUCTION code "+
					"imports it. The cloud client is a daemon-side detail, and an exemption for "+
					"a spec is not an exemption for a process.", importer, hangarGCSPackage)
			}
		}
	}

	// Test imports count. A test dependency links the package into nothing,
	// but it is how the production import arrives a week later.
	for pkg, imports := range graph.all {
		if pkg == hangarGCSPackage {
			continue
		}
		for _, imported := range imports {
			if imported != hangarGCSPackage {
				continue
			}
			if reason, ok := hangarGCSImporters[pkg]; ok {
				t.Logf("allowed: %s imports %s — %s", pkg, hangarGCSPackage, reason)
				continue
			}
			t.Errorf("%s imports %s. The GCS client is a daemon-side detail: depend on the "+
				"hangar.Store interface instead, or add %s above with the reason it must link a "+
				"cloud client.", pkg, hangarGCSPackage, pkg)
		}
	}
}

// TestTheHangarStoreIsLinkedOnlyByTheArtifactDaemon is the delete-shaped half
// of the rule above.
//
// hangar/gcsstore.GCSStore.DeleteTree takes a TreeRef and a config, and the
// config names the bucket. A root that can construct one can delete a key in
// any bucket it can name -- including the output bucket -- which is the
// "key-only delete route" Req 55 asks the guards to reject. It is measured at
// the ROOT, like the capability guard below, because the question is which
// binaries can make the call rather than which packages may say the name.
func TestTheHangarStoreIsLinkedOnlyByTheArtifactDaemon(t *testing.T) {
	graph := loadImportGraph(t)

	if _, ok := graph.all[hangarStorePackage]; !ok {
		t.Fatalf("%s does not exist; this rule would pass vacuously", hangarStorePackage)
	}
	for importer := range hangarStoreImporters {
		imports, ok := graph.all[importer]
		if !ok {
			t.Errorf("allowed importer %q does not exist; the exemption is stale", importer)
			continue
		}
		if !slices.Contains(imports, hangarStorePackage) {
			t.Errorf("%s is exempted to import %s and does not import it; the exemption is stale",
				importer, hangarStorePackage)
		}
	}

	for pkg, imports := range graph.all {
		if pkg == hangarStorePackage {
			continue
		}
		for _, imported := range imports {
			if imported != hangarStorePackage {
				continue
			}
			if reason, ok := hangarStoreImporters[pkg]; ok {
				t.Logf("allowed: %s imports %s — %s", pkg, hangarStorePackage, reason)
				continue
			}
			t.Errorf("%s imports %s.\n\nThat package's store deletes by key: DeleteTree takes a "+
				"TreeRef and the bucket comes from the config, so any package that can construct "+
				"one can remove an object from any bucket it can name. It is the artifact "+
				"daemon's, and the output plane's roles reach objects through "+
				"hangar/gcs and hangar/objectstore instead.", pkg, hangarStorePackage)
		}
	}

	// And the reason the split exists, stated so a green says it was checked:
	// the roots that link the OUTPUT seam must not link the store.
	for _, root := range []string{
		"./cmd/hangar-output-daemon", "./cmd/hangar-output-inventory",
		"./cmd/hangar-output-policy-attestor", "./cmd/hangar-output-reclaimer",
	} {
		if linksPackage(t, root, modulePrefix+hangarStorePackage) {
			t.Errorf("%s links %s, whose DeleteTree is a delete by key from a root that is not "+
				"the reclaimer", root, hangarStorePackage)
		}
	}
}

// The second seam this file defends, added by the Hangar output-publication
// track under the owner's 2026-09-04 ruling on loupe finding hangar-2:
//
//	The durable cache tier and Hangar are separate stores, in both directions.
//
// cmd/artifact-daemon/durable is a fail-open, name-keyed cache. Its own file
// says so: it "takes the key as given and never inspects it"
// (cmd/artifact-daemon/durable_tier.go:19), the node-local copy "stays a cache
// with a TTL" (:30), and "Nothing here may fail a build ... Every method
// swallows its errors" (:33-35). That is exactly right for a resource cache,
// which is re-derivable by re-running the get step — and exactly wrong for a
// durable result, whose whole promise is that losing it is not recoverable by
// re-running anything.
//
// So the tier never carries a Hangar or v4 result record, and Hangar never
// reaches for the tier. The direction that actually protects the tier is the
// second one: a Hangar import inside it is how a store whose errors are
// swallowed acquires a caller who cannot tolerate that.
const durableCacheTier = "cmd/artifact-daemon/durable"

// durableTierFile is the file declaring DurableTier. It is checked separately
// from the import graph because it is package main in cmd/artifact-daemon and
// therefore unimportable — a graph clause naming it would be vacuous by
// language rule, not by accident.
const durableTierFile = "cmd/artifact-daemon/durable_tier.go"

// excludedTree is one side of the rule.
type excludedTree struct {
	// name is the package path, which also matches everything beneath it.
	name string
	// requireNonEmpty says whether the tree must exist. hangar/ does and must:
	// a rule about a tree that vanished is a rule that stopped applying.
	// agent/ and atc/agent/ do not, matching the convention agenticPrefixes
	// states above -- they are reserved for v4, and listing them now means the
	// rule already binds when the first package arrives.
	requireNonEmpty bool
	why             string
}

var durableTierExcludedTrees = []excludedTree{
	{
		name:            "hangar",
		requireNonEmpty: true,
		why: "Hangar is the durable result plane. Its promise is that an exact tree survives " +
			"payload reclamation and node loss; a tier that swallows its errors cannot make it",
	},
	{name: "agent", why: "reserved for v4"},
	{name: "atc/agent", why: "reserved for v4"},
}

func inTree(pkg string, tree excludedTree) bool {
	return pkg == tree.name || strings.HasPrefix(pkg, tree.name+"/")
}

// durableTierSeparation is the rule, as a pure function over an injected graph,
// so that TestDurableTierSeparationGuardIsNotVacuous can drive it with a
// fixture that violates it. An assertion that nothing was found is also what a
// broken listing reports.
func durableTierSeparation(graph importGraph, trees []excludedTree) []string {
	var problems []string

	if len(graph.all) == 0 {
		return []string{"the import graph is empty; every clause below would pass vacuously"}
	}
	if len(trees) == 0 {
		return []string{"no excluded tree is declared; this rule describes nothing"}
	}

	for _, tree := range trees {
		members := 0
		for pkg := range graph.all {
			if inTree(pkg, tree) {
				members++
			}
		}
		if tree.requireNonEmpty && members == 0 {
			problems = append(problems, "no package under "+tree.name+"/ is in the import graph, "+
				"but this rule requires one. Either it was renamed or the listing failed; either "+
				"way the clauses below would pass vacuously.")
		}
	}

	// (a) Nothing in an excluded tree may reach the tier. Test edges count: a
	// test dependency is how the production import arrives a week later.
	for pkg, imports := range graph.all {
		for _, tree := range trees {
			if !inTree(pkg, tree) {
				continue
			}
			for _, imported := range imports {
				if imported != durableCacheTier {
					continue
				}
				problems = append(problems, pkg+" imports "+durableCacheTier+": "+tree.why+". "+
					"The durable tier is a fail-open cache; a durable result plane must not be "+
					"built on a store whose every method swallows its errors.")
			}
		}
	}

	// (b) And the tier may not reach back. This is the direction that protects
	// the tier: a Hangar import inside it gives a store designed to fail open a
	// caller that cannot tolerate failing open.
	for _, imported := range graph.all[durableCacheTier] {
		for _, tree := range trees {
			if !inTree(imported, tree) {
				continue
			}
			problems = append(problems, durableCacheTier+" imports "+imported+": the tier is a "+
				"resource cache and must stay one. Keeping a "+tree.name+" record in it is how "+
				"a swallowed error becomes a lost result.")
		}
	}

	return problems
}

func TestDurableTierAndHangarAreSeparateStores(t *testing.T) {
	graph := loadImportGraph(t)

	if _, ok := graph.all[durableCacheTier]; !ok {
		t.Fatalf("%s is not in the import graph; this rule would pass vacuously", durableCacheTier)
	}

	scanned := 0
	for pkg := range graph.all {
		for _, tree := range durableTierExcludedTrees {
			if inTree(pkg, tree) {
				scanned++
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no package matched any excluded tree; the rule scanned nothing")
	}
	t.Logf("import graph has %d packages, %d of them in an excluded tree", len(graph.all), scanned)

	for _, problem := range durableTierSeparation(graph, durableTierExcludedTrees) {
		t.Errorf("%s", problem)
	}
}

// TestDurableTierFileDoesNotImportHangar covers the half the import graph
// cannot: DurableTier is declared in package main, which nothing can import, so
// go list reports its edges under cmd/artifact-daemon along with the whole
// daemon's -- including its legitimate hangar import. The file is therefore
// read directly.
func TestDurableTierFileDoesNotImportHangar(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), durableTierFile, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", durableTierFile, err)
	}
	if len(file.Imports) == 0 {
		t.Fatalf("%s declares no import at all; this check would pass vacuously", durableTierFile)
	}

	// The file must still be the one that declares DurableTier, or this test is
	// guarding a path that moved.
	body, err := os.ReadFile(durableTierFile)
	if err != nil {
		t.Fatalf("reading %s: %v", durableTierFile, err)
	}
	if !strings.Contains(string(body), "type DurableTier struct") {
		t.Fatalf("%s no longer declares DurableTier. Point durableTierFile at the file that "+
			"does, so this check keeps guarding the tier rather than a filename.", durableTierFile)
	}

	checked := 0
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Errorf("unquoting an import in %s: %v", durableTierFile, err)
			continue
		}
		checked++
		if !strings.HasPrefix(path, modulePrefix) {
			continue
		}
		short := strings.TrimPrefix(path, modulePrefix)
		for _, tree := range durableTierExcludedTrees {
			if inTree(short, tree) {
				t.Errorf("%s imports %s: the tier is a resource cache whose every method "+
					"swallows its errors. %s", durableTierFile, path, tree.why)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no import in %s was readable; this check passed over nothing", durableTierFile)
	}
}

// TestDurableTierSeparationGuardIsNotVacuous drives the rule with fixtures, the
// way TestUnpinnedAgenticPackagesGuardFailsOnAnEmptyScan does for D9.
func TestDurableTierSeparationGuardIsNotVacuous(t *testing.T) {
	trees := []excludedTree{{name: "hangar", requireNonEmpty: true, why: "because"}}

	t.Run("objects to an empty graph", func(t *testing.T) {
		if problems := durableTierSeparation(importGraph{}, trees); len(problems) == 0 {
			t.Fatal("the rule passed over an empty import graph. That is the vacuous green " +
				"this assertion exists to prevent.")
		}
	})

	t.Run("objects when the required tree is absent", func(t *testing.T) {
		graph := importGraph{all: map[string][]string{
			durableCacheTier: {},
			"atc/db":         {"atc"},
		}}
		problems := durableTierSeparation(graph, trees)
		if len(problems) == 0 {
			t.Fatal("the rule passed over a graph with no hangar package at all")
		}
		if !strings.Contains(problems[0], "no package under hangar/") {
			t.Errorf("first problem should name the missing tree, got: %q", problems[0])
		}
	})

	t.Run("catches hangar reaching for the tier", func(t *testing.T) {
		graph := importGraph{all: map[string][]string{
			durableCacheTier: {},
			"hangar":         {},
			"hangar/output":  {durableCacheTier},
		}}
		problems := durableTierSeparation(graph, trees)
		if len(problems) != 1 || !strings.Contains(problems[0], "hangar/output imports") {
			t.Fatalf("expected exactly the hangar/output edge to be reported, got %v", problems)
		}
	})

	t.Run("catches the tier reaching for hangar", func(t *testing.T) {
		graph := importGraph{all: map[string][]string{
			durableCacheTier: {"hangar"},
			"hangar":         {},
		}}
		problems := durableTierSeparation(graph, trees)
		if len(problems) != 1 || !strings.Contains(problems[0], durableCacheTier+" imports hangar") {
			t.Fatalf("expected exactly the tier's own edge to be reported, got %v", problems)
		}
	})

	t.Run("is silent on the shape core actually has", func(t *testing.T) {
		graph := importGraph{all: map[string][]string{
			durableCacheTier:      {},
			"hangar":              {},
			"hangar/gcs":          {"hangar"},
			"cmd/artifact-daemon": {"hangar", "hangar/gcs", durableCacheTier},
		}}
		if problems := durableTierSeparation(graph, trees); len(problems) != 0 {
			t.Errorf("expected no problems, got %v", problems)
		}
	})

	// The trees that are deliberately allowed to be empty must not be reported
	// as missing, or the convention agenticPrefixes states would be broken the
	// moment this rule adopted it.
	t.Run("does not require the reserved v4 trees to exist", func(t *testing.T) {
		reserved := []excludedTree{
			{name: "hangar", requireNonEmpty: true, why: "because"},
			{name: "agent", why: "reserved for v4"},
			{name: "atc/agent", why: "reserved for v4"},
		}
		graph := importGraph{all: map[string][]string{
			durableCacheTier: {},
			"hangar":         {},
		}}
		if problems := durableTierSeparation(graph, reserved); len(problems) != 0 {
			t.Errorf("expected no problems, got %v", problems)
		}
	})
}

// The delete capability, measured at the ROOT rather than at the import.
//
// Every guard above is a rule about which package may NAME another. This one
// asks the toolchain what each binary actually links, which is the question the
// requirement is about: GCS IAM cannot require a caller to send a generation
// precondition once delete permission exists (Req 55), so the boundary has to be
// that exactly one process can make the call at all. An allowlist entry that
// turned out to matter is caught here by the fact it was supposed to prevent
// rather than by a reviewer noticing.
//
// It is deliberately not parameterised over "the roots we remembered": it
// discovers every main package in cmd/ and checks all of them, so a binary added
// next year inherits the rule without anyone adding it to a list.
const outputDeleteRole = "github.com/concourse/concourse/hangar/output/reclaimer"

// outputDeleteCapability is the package that can construct an object delete
// over a real cloud client, and the only one.
//
// It is a separate subject from the role because the role was the WRONG thing
// to measure, demonstrated: `objectstore.Handle` carried Delete, so the adapter
// handed to the daemon, the inventory controller and the reclaimer alike
// carried the capability, and a live `objects.delete` added to
// cmd/hangar-output-daemon -- no reclaimer import anywhere -- built and passed
// every guard in this file. Linking a role is a fact about imports. Being able
// to delete is a fact about which package's constructor is in the binary, and
// that is what this asks.
const outputDeleteCapability = "github.com/concourse/concourse/hangar/gcsdelete"

// outputDeleteRoot is the one binary allowed to link either.
const outputDeleteRoot = "./cmd/hangar-output-reclaimer"

func TestOnlyTheReclaimerBinaryCanInvokeAnOutputDelete(t *testing.T) {
	roots := commandRoots(t)
	if len(roots) < 4 {
		t.Fatalf("found %d command roots, which is far too few to be this repository's cmd/ "+
			"directory; the discovery failed and this rule would pass vacuously", len(roots))
	}

	// The controls FIRST: the reclaimer really does link both. Without them
	// every assertion below would also pass for a tree in which the delete
	// client had been deleted entirely.
	for _, subject := range []string{outputDeleteRole, outputDeleteCapability} {
		if !linksPackage(t, outputDeleteRoot, subject) {
			t.Fatalf("%s does not link %s, so this rule is guarding nothing",
				outputDeleteRoot, subject)
		}
	}

	for _, root := range roots {
		if root == outputDeleteRoot {
			continue
		}
		if linksPackage(t, root, outputDeleteCapability) {
			t.Errorf("%s links %s.\n\nThat package is the object-delete CAPABILITY: it is the "+
				"only place in this repository that can construct a deleter over a real cloud "+
				"client, and a binary that links it can issue objects.delete whether or not it "+
				"names a role. GCS IAM cannot require a caller to send a generation precondition "+
				"once delete permission exists, so the boundary is that exactly one process can "+
				"make the call at all.", root, outputDeleteCapability)
		}
		if linksPackage(t, root, outputDeleteRole) {
			t.Errorf("%s links %s.\n\nOnly the isolated reclaimer workload may import or invoke "+
				"the output delete client. A Kubernetes service account is Pod-wide, so a "+
				"second binary that linked this would be a second identity holding "+
				"storage.objects.delete for everything else it does. This is the second line: "+
				"the capability guard above is the first.", root, outputDeleteRole)
		}
	}

	// And the property the two guards exist for, stated so a green says it was
	// checked: the shared object seam every root holds has no delete on it at
	// all. A Delete method here would put the capability back into the type
	// three of four binaries take, which is the shape that was demonstrated.
	assertSharedHandleHasNoDelete(t)

	// The third line, and the one the first two were blind to.
	assertNoCommandRootNamesTheStorageSDK(t)
}

// storageSDKExemptions are the packages under cmd/ allowed to name the Cloud
// Storage SDK, with the reason. There is one.
var storageSDKExemptions = map[string]string{
	"cmd/artifact-daemon/durable": "the durable CACHE tier's own GCS backend, which predates the " +
		"output plane and is a different store over a different bucket: it is fail-open, " +
		"name-keyed and re-derivable, and durableTierSeparation above is what keeps it and " +
		"Hangar from becoming each other. It builds its own client with the SDK because it IS a " +
		"backend; it is not a command root, and no main package is exempt",
}

// assertNoCommandRootNamesTheStorageSDK is the arm that closes the route the
// first two guards certified against and did not measure.
//
// Both of those ask the build graph which PACKAGES a binary links. Neither can
// see the cheapest delete there is, because it needs no package: while
// hangar/gcs.NewStorageClient was exported, three non-reclaimer roots held the
// raw *storage.Client in a local variable, and
//
//	storageClient.Bucket(bucket).Object("any/key").Delete(ctx)
//
// compiled in cmd/hangar-output-daemon, cmd/hangar-output-inventory and
// cmd/hangar-output-policy-attestor alike -- no new import, nothing added to any
// dependency graph, every guard in this file green. That was demonstrated in all
// three. A method call on an already-typed value is invisible to an import rule.
//
// What makes that line a compile error now is that no root can obtain the value:
// the client is opened by hangar/internal/gcsclient, which Go's internal rule
// puts out of reach of everything outside hangar/, and each capability package
// hands back an interface carrying only the operations its role may issue. This
// arm is the belt to that brace -- it stops a root reaching around the
// capability packages to storage.NewClient directly -- and it applies to the
// RECLAIMER too: the one binary allowed to delete should be doing it through
// hangar/gcsdelete, not through the SDK.
func assertNoCommandRootNamesTheStorageSDK(t *testing.T) {
	t.Helper()

	root := filepath.Join(repositoryRoot(), "cmd")
	fileSet := token.NewFileSet()
	scanned, exercised := 0, map[string]bool{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot(), path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		pkg := filepath.ToSlash(filepath.Dir(relative))

		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", relative, err)
		}
		scanned++

		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: unquoting %s: %w", relative, spec.Path.Value, err)
			}
			if imported != cloudStorageModule && !strings.HasPrefix(imported, cloudStorageModule+"/") {
				continue
			}
			if reason, ok := storageSDKExemptions[pkg]; ok {
				exercised[pkg] = true
				t.Logf("allowed: %s names %s — %s", relative, cloudStorageModule, reason)
				continue
			}
			t.Errorf("%s names %s.\n\nA file under cmd/ that can reach the SDK can open its own "+
				"client, and a *storage.Client is an arbitrary, unconditional, key-only "+
				"objects.delete one method call away -- with no further import, invisible to "+
				"every import-graph rule in this file. That exact line was demonstrated in three "+
				"roots. Take the capability from hangar/gcs, hangar/gcsstore or "+
				"hangar/gcsdelete, each of which opens and owns its own client behind a "+
				"(ctx, endpoint) constructor and returns only the operations the role may "+
				"issue. The reclaimer is not an exception: it reaches delete through "+
				"%s.", relative, cloudStorageModule, outputDeleteCapability)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("scanning cmd/: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d files under cmd/, which is far too few to be this "+
			"repository's command roots; the walk failed and this rule would pass vacuously",
			scanned)
	}
	for pkg := range storageSDKExemptions {
		if !exercised[pkg] {
			t.Errorf("%s is exempted to name %s and no file in it does; the exemption is stale",
				pkg, cloudStorageModule)
		}
	}
}

// assertSharedHandleHasNoDelete reads the interface declaration rather than the
// build graph, because this half is about a TYPE: it is the one that makes the
// reviewer's demonstration -- `objects.Object(bucket, key).Delete(ctx)` in a
// non-reclaimer root -- fail to compile rather than merely fail a guard.
func assertSharedHandleHasNoDelete(t *testing.T) {
	t.Helper()

	path := filepath.Join(repositoryRoot(), "hangar", "objectstore", "objectstore.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Handle" {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		found = true
		for _, method := range iface.Methods.List {
			for _, name := range method.Names {
				if name.Name == "Delete" {
					t.Errorf("objectstore.Handle declares Delete.\n\nEvery root that takes an "+
						"object adapter then holds the delete capability, whatever role it "+
						"links -- which is exactly the state a live objects.delete was "+
						"demonstrated from cmd/hangar-output-daemon in. Deletion belongs on "+
						"objectstore.DeleteHandle, whose only implementation over a real cloud "+
						"client is %s.", outputDeleteCapability)
				}
			}
		}

		return false
	})
	if !found {
		t.Fatal("objectstore.Handle was not found; this rule would pass vacuously")
	}
}

// commandRoots lists every main package under cmd/.
func commandRoots(t *testing.T) []string {
	t.Helper()

	out, err := exec.Command("go", "list", "-f", "{{if eq .Name \"main\"}}{{.Dir}}{{end}}",
		"./cmd/...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list ./cmd/... failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list ./cmd/... failed: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(thisFile)

	var roots []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		relative, err := filepath.Rel(repoRoot, strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("relativising %q: %v", line, err)
		}
		roots = append(roots, "./"+filepath.ToSlash(relative))
	}

	return roots
}

// linksPackage asks the toolchain whether a root's transitive dependencies
// include a package. It reads the real build graph rather than source imports,
// so a package reached through three intermediaries is still found.
func linksPackage(t *testing.T, root, pkg string) bool {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps", root).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps %s failed: %v\n%s", root, err, ee.Stderr)
		}
		t.Fatalf("go list -deps %s failed: %v", root, err)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(dep) == pkg {
			return true
		}
	}

	return false
}

// TestEachOutputControllerLinksOnlyItsOwnRole is the same measurement for the
// other three principals.
//
// The isolation only means anything while each binary links ONE role. A
// controller that linked two would be one Kubernetes service account holding
// two sets of cloud permissions, and no care inside the process takes that back.
func TestEachOutputControllerLinksOnlyItsOwnRole(t *testing.T) {
	const prefix = "github.com/concourse/concourse/hangar/output/"

	expected := map[string]string{
		"./cmd/hangar-output-daemon":          prefix + "publisher",
		"./cmd/hangar-output-inventory":       prefix + "inventory",
		"./cmd/hangar-output-reclaimer":       prefix + "reclaimer",
		"./cmd/hangar-output-policy-attestor": prefix + "policy",
	}
	all := []string{
		prefix + "publisher", prefix + "inventory", prefix + "reclaimer", prefix + "policy",
	}

	for root, own := range expected {
		if !linksPackage(t, root, own) {
			t.Errorf("%s does not link its own role %s", root, own)
		}
		for _, role := range all {
			if role == own {
				continue
			}
			if linksPackage(t, root, role) {
				t.Errorf("%s links %s as well as its own %s. One binary, one service account, "+
					"one role: a process holding two is one cloud identity with two sets of "+
					"permissions.", root, role, own)
			}
		}
	}
}
