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
	"regexp"
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

// What this rule claims, and what it does not.
//
// It claims exactly one thing: NO COMMAND ROOT REACHES AN OBJECT DELETE THROUGH
// THE GO COMPOSITION. It is measured at the ROOT rather than at the import --
// every guard above is a rule about which package may NAME another; this one
// asks the toolchain what each binary actually links, so an allowlist entry that
// turned out to matter is caught by the fact it was supposed to prevent rather
// than by a reviewer noticing. It is not parameterised over "the roots we
// remembered": it discovers every main package in cmd/ and checks all of them,
// so a binary added next year inherits the rule without anyone adding it to a
// list.
//
// SOURCE GUARDS CANNOT BOUND A CREDENTIAL HOLDER; IAM IS THE CONTROL. A Go
// program holding application default credentials can do anything those
// credentials permit, and no rule written over imports can say otherwise. Round
// 4 of review demonstrated three routes that are green here and will stay green:
//
//	L  golang.org/x/oauth2/google.DefaultClient and a net/http DELETE to
//	   https://storage.googleapis.com/storage/v1/b/<bucket>/o/<key> -- fifteen
//	   lines, no go.mod change, no storage SDK named anywhere
//	M  os/exec of `gcloud storage rm gs://<bucket>/<key>`
//	N  github.com/aws/aws-sdk-go-v2/service/s3 DeleteObject -- a cloud storage
//	   SDK that was not on the list, already required at v1.107.1
//
// Chasing those with source rules is unbounded work ending in a guard nobody can
// satisfy. They are not defects in the composition; they are the fact that
// credentials are the boundary. Reqs 54 and 55 say so, and the control is IAM:
// the daemon, inventory and attestor principals hold no storage.objects.delete,
// and every one of L, M and N gets a 403. That half is made load-bearing by
// deploy/chart/tests/hangar_output_test.go's
// TestOnlyTheReclaimerPrincipalIsGrantedObjectDelete, which asserts the rendered
// Workload Identity annotations and the generated IAM documentation grant delete
// to the reclaimer principal and to no other, and by activation, which attests
// the real policy.
//
// What these rules buy is that the capability is not RE-ACQUIRED BY ACCIDENT
// through the composition: a refactor that hands the wrong adapter to the wrong
// binary, a helper that opens its own client, a role that grows a method. That
// is a real and recurring failure -- it happened three times in three rounds --
// and it is what is checked here. The three tripwires below
// (TestNoPackageOutsideTheCapabilityPackagesNamesACloudStorageSDK's widened SDK
// list, TestNoCommandRootShellsOutToACloudCLI and
// TestNoPackageAddressesACloudStorageEndpointOverRawHTTP) cover the
// plausible-accident shape of L, M and N without pretending to close them.
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

func TestNoCommandRootReachesAnObjectDeleteThroughTheGoComposition(t *testing.T) {
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
			t.Errorf("%s reaches an object delete through the Go composition: it links %s.\n\n"+
				"That package is the object-delete CAPABILITY: it is the only place in this "+
				"repository that can construct a deleter over a real cloud client, and a binary "+
				"that links it can issue objects.delete whether or not it names a role. This "+
				"rule does not claim the binary CANNOT delete -- a process holding credentials "+
				"can, and IAM is what stops it. It claims the composition does not hand it the "+
				"capability, which is the accident this has been three times.",
				root, outputDeleteCapability)
		}
		if linksPackage(t, root, outputDeleteRole) {
			t.Errorf("%s reaches an object delete through the Go composition: it links %s.\n\n"+
				"Only the isolated reclaimer workload may import or invoke the output delete "+
				"client. A Kubernetes service account is Pod-wide, so a second binary that "+
				"linked this would be a second Pod whose identity IAM would then have to be "+
				"trusted to keep delete away from. This is the second line: the capability "+
				"guard above is the first.", root, outputDeleteRole)
		}
	}

	// And the property the two guards exist for, stated so a green says it was
	// checked: the shared object seam every root holds has no delete on it at
	// all. A Delete method here would put the capability back into the type
	// three of four binaries take, which is the shape that was demonstrated.
	assertSharedHandleHasNoDelete(t)

	// The third line is no longer here: it is
	// TestNoPackageOutsideTheCapabilityPackagesNamesACloudStorageSDK below,
	// which is repository-wide. The arm that used to live here parsed files
	// under cmd/ and compared against ONE import path, so it stopped a root
	// reaching for the SDK directly and nothing else -- the same call written
	// in a package the root LINKS was invisible to it, and a sibling SDK with
	// a different path was invisible twice over. Both were demonstrated.
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

// activationTableWriters are the packages allowed to write the activation epoch
// row, and there is one.
//
// The epoch row is the plane's single authority: a node label is a hint, a
// daemon handshake is evidence, a Helm value is an intention, and none of them
// authorizes a capture, a receipt registration, a claim acquisition or a
// finalization. This row does. So the set of things that can move it is the set
// of things that can turn the plane on, and it is one internal command run as a
// one-shot Job under its own least-privilege PostgreSQL role.
//
// The rule is stated over SOURCE rather than over the build graph because what
// it is about is a STATEMENT: any package that can open a database handle can
// write any table, and no import rule can see that. What it can see is an
// UPDATE, an INSERT or a DELETE naming the table.
var activationTableWriters = map[string]string{
	"atc/hangaroutput/activation": "the activation command's own package. Its four guarded " +
		"transitions ARE the protocol, and the Jobs that run them hold a PostgreSQL role " +
		"distinct from the web pod's -- which is what makes this rule enforceable at the " +
		"credential as well as at the code",
	"atc/worker/jetbridge/brine/steps": "TEST HARNESS: the brine adapter's step definitions, " +
		"which arrange an activated plane so a scenario can start from one. They are ordinary " +
		"Go files rather than _test.go files because a brine adapter is a BINARY, so the " +
		"suffix rule cannot see them -- which is why the check below reads the nested go.mod " +
		"rather than believing this sentence",
}

// nestedTestModules are the exemptions above whose reason says TEST HARNESS,
// with the nested go.mod that makes the claim checkable.
//
// A comment saying "this is only a harness" is a comment a later edit can
// quietly make untrue. A separate module is not: the root module's build graph
// cannot reach it at all, so nothing this repository ships can link it.
var nestedTestModules = map[string]string{
	"atc/worker/jetbridge/brine/steps": "atc/worker/jetbridge/brine/go.mod",
}

const activationTable = "hangar_output_activation_epochs"

// TestOnlyTheActivationCommandWritesTheEpochRow reads every non-test Go file in
// the repository for a write against the activation table.
func TestOnlyTheActivationCommandWritesTheEpochRow(t *testing.T) {
	root := repositoryRoot()

	// A write is an UPDATE, an INSERT or a DELETE naming the table. A SELECT is
	// not: half this plane reads the epoch row, and reading an authority is
	// what an authority is for.
	writes := regexp.MustCompile(`(?is)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+` + activationTable + `\b`)

	scanned, readers, exercised := 0, 0, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		if !strings.Contains(string(body), activationTable) {
			return nil
		}
		readers++

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		pkg := filepath.ToSlash(filepath.Dir(relative))
		if !writes.MatchString(string(body)) {
			return nil
		}
		if reason, ok := activationTableWriters[pkg]; ok {
			exercised[pkg] = true
			t.Logf("allowed: %s writes %s — %s", filepath.ToSlash(relative), activationTable, reason)

			return nil
		}
		t.Errorf("%s writes %s.\n\nThat row is the output plane's single authority: every "+
			"capture records the epoch it was admitted under, and a stale label or handshake "+
			"cannot authorize emission, receipt registration, claim acquisition or "+
			"finalization. It moves only through the four guarded transitions in %s, which "+
			"run as one-shot Jobs under a PostgreSQL role distinct from the web pod's. A "+
			"second writer is a second way to turn the plane on.",
			filepath.ToSlash(relative), activationTable, "atc/hangaroutput/activation")

		return nil
	})
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	if scanned < 500 {
		t.Fatalf("scanned only %d non-test Go files; the walk failed and this rule would pass "+
			"vacuously", scanned)
	}
	if readers < 2 {
		t.Fatalf("only %d non-test files name %s at all. Half this plane reads the epoch row, "+
			"so a repository where nothing does is one where the table was renamed and this "+
			"rule is guarding a string", readers, activationTable)
	}
	for pkg := range activationTableWriters {
		if !exercised[pkg] {
			t.Errorf("%s is exempted to write %s and no file in it does; the exemption is stale",
				pkg, activationTable)
		}
	}
	for pkg, module := range nestedTestModules {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(module))); statErr != nil {
			t.Errorf("%s is exempted as a TEST HARNESS in a nested module, and %s does not "+
				"exist: %v. Without the separate module the root build graph can reach it, and "+
				"the exemption is a sentence rather than a fact.", pkg, module, statErr)
		}
	}
}

// ---------------------------------------------------------------------------
// The delete capability, guarded as a capability
// ---------------------------------------------------------------------------
//
// Three rounds of review found the same defect three times, and the lesson is
// written down here rather than left in the review: **a capability guard that
// names a route is a guard against that route, not against the capability.**
//
//	round 1  a Delete method on the object adapter every root took
//	round 2  a raw *storage.Client in a local variable in three roots, one
//	         method call from an arbitrary delete with no new import
//	round 3  three routes the guards could not see at all -- a key-only delete
//	         exported by the durable cache tier, the raw JSON API for the same
//	         service under a different import path, and the same SDK named one
//	         package away from any file under cmd/
//
// Each fix measured the shape the previous finding had; each was green while
// the next one was open. So the rules below are stated over what a package may
// NAME and what a binary may LINK, repository-wide, rather than over where a
// particular call could be written.
//
// None of this is the primary control. GCS IAM is: the daemon, inventory and
// attestor principals hold no storage.objects.delete, and the call gets a 403.
// What these rules buy is that the isolation stays true in code somebody writes
// next year, when the IAM binding is somebody else's memory of a Terraform file.

// cloudStorageSDKs are every import path that IS a Cloud Storage SDK.
//
// The second entry is the reason this is a list at all. Round 3 demonstrated
// `rawstorage "google.golang.org/api/storage/v1"` in cmd/hangar-output-daemon
// with `svc.Objects.Delete(bucket, key).Do()` -- an arbitrary, unconditional,
// key-only delete, with no go.mod change (google.golang.org/api is already
// required) and with `cloud.google.com/go/storage` appearing nowhere in the
// file. A rule comparing against one constant saw nothing.
//
// The third is round 4's route N: the AWS S3 SDK is already required at
// v1.107.1 for the durable cache tier's S3 backend, so `s3.DeleteObject` was a
// delete with no go.mod change and no Google SDK named. Cheap to close, because
// the one package that legitimately names it -- cmd/artifact-daemon/durable --
// is already an allowed importer.
//
// The rest name SDKs nothing in this repository imports today. They are
// TRIPWIRES for the first accident shape: reaching for a different cloud's
// object storage from a package that had no object-storage business. An entry
// with no importer cannot go stale the way an exemption can -- and
// TestEveryCloudStorageSDKEntryIsRecognised proves each one is MATCHED by
// isCloudStorageSDK, exactly and by sub-path. It does not, and cannot, prove
// that an entry nothing imports is load-bearing; see that test's own comment.
var cloudStorageSDKs = []string{
	"cloud.google.com/go/storage",
	"google.golang.org/api/storage/v1",
	"github.com/aws/aws-sdk-go-v2/service/s3",
	"github.com/aws/aws-sdk-go/service/s3",
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob",
	"github.com/minio/minio-go",
	"gocloud.dev/blob",
}

// cloudStorageSDKImporters are the packages allowed to name a Cloud Storage
// SDK anywhere in the repository, each with the reason.
//
// REPOSITORY-WIDE and not "under cmd/". The previous form parsed files under
// cmd/ and compared against one import path, so it stopped a root reaching for
// the SDK directly and nothing else: the same call written in a package the
// root LINKS was invisible to it, and round 3 demonstrated exactly that by
// putting `storage.NewClient` in hangar/output/publisher and calling it from
// the daemon. The build graph is the thing that decides what a binary can do,
// and the set of packages that may name a cloud client is the honest statement
// of it.
var cloudStorageSDKImporters = map[string]string{
	"hangar/internal/gcsclient": "the ONE place a *storage.Client is opened. Go's internal " +
		"rule is what makes \"nothing outside hangar/ can obtain one through this " +
		"repository's own seam\" checked by the toolchain on every build rather than by a " +
		"reviewer reading a comment",
	"hangar/gcs": "the output plane's object and bucket-policy seams. Each capability it " +
		"hands back is an interface carrying only the operations its role may issue, and none " +
		"of them carries Delete",
	"hangar/gcsstore": "the artifact daemon's strict-input store. Its DeleteTree is a delete " +
		"BY KEY, which is why it is a separate package with a separate importer rule below",
	"hangar/gcsdelete": "the object-delete capability's own package. It names the SDK because " +
		"it IS the adapter, and exactly one binary links it",
	"cmd/artifact-daemon/durable": "the durable CACHE tier's own GCS and S3 backends, which " +
		"predate the output plane and are a different store over a different bucket. It builds " +
		"its own client because it IS a backend; which binaries may LINK it is the subject of " +
		"TestTheDurableCacheTierIsLinkedOnlyByTheArtifactDaemon below, and the answer is one",
	"hangar/output/conformance": "TEST-ONLY: the tier-2 conformance suite drives the real " +
		"adapter against fake-gcs-server, because a conformance claim proved through a " +
		"hand-written fake is a claim about the fake",
	"atc/worker/jetbridge/brine/steps": "TEST HARNESS: the brine adapter's step definitions, " +
		"in their own nested module, which the root module's build graph cannot reach at all",
}

// TestNoPackageOutsideTheCapabilityPackagesNamesACloudStorageSDK is the
// repository-wide arm.
//
// It reads IMPORTS and not text: cmd/hangar-output-daemon and
// cmd/hangar-output-reclaimer both mention `cloud.google.com/go/storage` in a
// comment explaining why they do not import it, and a textual rule would either
// fail on those or be written to skip comments -- which is a parser with extra
// steps.
func TestNoPackageOutsideTheCapabilityPackagesNamesACloudStorageSDK(t *testing.T) {
	root := repositoryRoot()
	fileSet := token.NewFileSet()

	scanned, named := 0, 0
	exercised := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", "node_modules":
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		pkg := filepath.ToSlash(filepath.Dir(relative))

		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", relative, parseErr)
		}
		scanned++

		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return fmt.Errorf("%s: unquoting %s: %w", relative, spec.Path.Value, unquoteErr)
			}
			if !isCloudStorageSDK(imported) {
				continue
			}
			named++
			if reason, ok := cloudStorageSDKImporters[pkg]; ok {
				exercised[pkg] = true
				t.Logf("allowed: %s names %s — %s", relative, imported, reason)

				continue
			}
			t.Errorf("%s names the Cloud Storage SDK %s.\n\nA package that can reach the SDK "+
				"can open its own client, and an arbitrary, unconditional, key-only "+
				"objects.delete is one method call away -- with no further import, invisible "+
				"to every import-graph rule in this file, and reachable from every binary "+
				"that links this package. Take the capability from hangar/gcs, "+
				"hangar/gcsstore or hangar/gcsdelete instead: each opens and owns its own "+
				"client behind a (ctx, endpoint) constructor and returns only the operations "+
				"its role may issue. The reclaimer is not an exception -- it reaches delete "+
				"through %s.", relative, imported, outputDeleteCapability)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	if scanned < 1000 {
		t.Fatalf("parsed only %d Go files; the walk failed and this rule would pass vacuously",
			scanned)
	}
	if named < 5 {
		t.Fatalf("only %d imports of a Cloud Storage SDK were found anywhere. This repository "+
			"talks to Cloud Storage; a tree where almost nothing names the SDK is one where "+
			"the import paths moved and this rule is guarding two strings", named)
	}
	for pkg := range cloudStorageSDKImporters {
		if !exercised[pkg] {
			t.Errorf("%s is exempted to name a Cloud Storage SDK and no file in it does; the "+
				"exemption is stale, and an exemption nobody uses is a hole nobody is watching",
				pkg)
		}
	}
}

func isCloudStorageSDK(imported string) bool {
	for _, sdk := range cloudStorageSDKs {
		if imported == sdk || strings.HasPrefix(imported, sdk+"/") {
			return true
		}
	}

	return false
}

// TestEveryCloudStorageSDKEntryIsRecognised is R1-F9's fix: proof that the
// MATCHER recognises each entry in the list, exactly and by sub-path.
//
// It is not proof that each entry is load-bearing, and the sentence that once
// said so is corrected here (Phase 8 review R2-F4): an entry guarding an import
// nothing makes is a tripwire by design, and no assertion over source can tell
// a live tripwire from a dead string. What follows says which claim is which.
//
// The importer allowlist has a staleness arm -- an exemption nobody uses fails.
// The SDK list had none, and could not have the same one: two of its entries are
// MEANT to guard an import nothing makes, and `google.golang.org/api/storage/v1`
// is named in exactly one file in the repository, this one, as the list entry.
// Replacing it with a nonsense path left the whole guard green, so the entry
// that closes round 3's route I could be deleted in a refactor with nothing to
// say so.
//
// What is actually assertable is that each entry is a path the matcher matches,
// exactly and by sub-path, and that a near miss does not match. That reddens on
// a typo, on a deletion (the case count drops below the list length), and on a
// matcher that stops working.
func TestEveryCloudStorageSDKEntryIsRecognised(t *testing.T) {
	if len(cloudStorageSDKs) < 3 {
		t.Fatalf("cloudStorageSDKs has %d entries; the list collapsed and the rules over it "+
			"guard almost nothing", len(cloudStorageSDKs))
	}

	for _, sdk := range cloudStorageSDKs {
		if !isCloudStorageSDK(sdk) {
			t.Errorf("isCloudStorageSDK(%q) is false for an entry of its own list", sdk)
		}
		if !isCloudStorageSDK(sdk + "/internal/apiv2") {
			t.Errorf("isCloudStorageSDK does not match a sub-path of %q. Every one of these "+
				"SDKs has sub-packages, and a rule that matches only the root path is one "+
				"`import \"%s/types\"` away from silent.", sdk, sdk)
		}
		if isCloudStorageSDK(sdk + "-notreally") {
			t.Errorf("isCloudStorageSDK matches %q by prefix alone; the separator is what keeps "+
				"a neighbouring module out", sdk+"-notreally")
		}
	}

	// The two entries whose whole job is to close a demonstrated route, named
	// so that deleting one fails here and not in review a year later.
	for _, required := range []string{
		"cloud.google.com/go/storage",
		"google.golang.org/api/storage/v1",
		"github.com/aws/aws-sdk-go-v2/service/s3",
	} {
		if !isCloudStorageSDK(required) {
			t.Errorf("%s is no longer in cloudStorageSDKs. It is there because a delete through "+
				"it was DEMONSTRATED past the guards of its round; removing it reopens that "+
				"route.", required)
		}
	}

	for _, harmless := range []string{
		"net/http", "cloud.google.com/go/iam", "google.golang.org/api/option",
		"github.com/aws/aws-sdk-go-v2/config",
	} {
		if isCloudStorageSDK(harmless) {
			t.Errorf("isCloudStorageSDK(%q) is true; the list has grown a path that is not an "+
				"object-storage SDK, and every allowlist over it is now about the wrong thing",
				harmless)
		}
	}
}

// ---------------------------------------------------------------------------
// The two accident tripwires that are not about imports of an SDK
// ---------------------------------------------------------------------------
//
// Neither closes its route. Route M (`gcloud storage rm`) and route L (a raw
// HTTPS DELETE with application default credentials) are open to any binary that
// holds credentials, and no source rule changes that -- IAM does. What these
// catch is the shape the accident takes: somebody reaching for a cloud CLI or
// for a storage endpoint by hand, in a tree where neither has ever appeared.
//
// Both are stated over a PREDICATE with its own table test, because the corpus
// is clean: there is nothing for the scan to find today, so "it found nothing"
// is not by itself evidence it is looking. The scan floors say the walk ran; the
// predicate tables say the matcher works.

// cloudCLIProgram matches an invocation of a cloud vendor's command-line tool.
//
// Deliberately narrow. `aws`, `az` and `mc` alone are ordinary English and
// ordinary variable names, so this matches the distinctive tokens and the
// two-word forms, which is what an actual shell-out reads like.
var cloudCLIProgram = regexp.MustCompile(
	`\b(gcloud|gsutil|s3cmd|rclone|awscli|aws\s+s3|az\s+storage|azcopy)\b`)

// cloudStorageEndpointHost matches a cloud object-storage API host.
var cloudStorageEndpointHost = regexp.MustCompile(
	`\b(storage\.googleapis\.com|storage\.cloud\.google\.com|` +
		`[a-z0-9.-]*s3[a-z0-9.-]*\.amazonaws\.com|` +
		`[a-z0-9.-]*\.blob\.core\.windows\.net|` +
		`[a-z0-9.-]*\.r2\.cloudflarestorage\.com)\b`)

// cloudCLIShellOutAllowed and cloudStorageEndpointAllowed are the packages
// permitted to trip each tripwire, with the reason. Both are empty: nothing in
// this repository does either, and that is the point -- the first entry added
// is a decision somebody has to write a sentence for.
var cloudCLIShellOutAllowed = map[string]string{}

var cloudStorageEndpointAllowed = map[string]string{}

// TestNoCommandRootShellsOutToACloudCLI is route M's tell.
//
// `os/exec` in a composition root is the same shape as `unsafe` in one: a root
// wires things together, and a binary that can start a subprocess can run
// `gcloud storage rm gs://bucket/key` with the same credentials the SDK would
// have used, naming no SDK at all. Scoped to cmd/ and to the output plane's own
// trees, because exec has legitimate uses elsewhere in this repository (the
// worker runs things for a living) and a rule that had to exempt half the tree
// would be a list, not a boundary.
func TestNoCommandRootShellsOutToACloudCLI(t *testing.T) {
	trees := []string{"cmd", "hangar", "atc/hangaroutput"}
	root := repositoryRoot()
	fileSet := token.NewFileSet()
	scanned := 0

	for _, tree := range trees {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(tree)),
			func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
					strings.HasSuffix(path, "_test.go") {
					return nil
				}
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				relative = filepath.ToSlash(relative)
				pkg := filepath.ToSlash(filepath.Dir(relative))
				scanned++

				file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
				if parseErr != nil {
					return fmt.Errorf("parsing %s: %w", relative, parseErr)
				}

				importsExec := false
				for _, spec := range file.Imports {
					imported, unquoteErr := strconv.Unquote(spec.Path.Value)
					if unquoteErr != nil {
						return unquoteErr
					}
					if imported == "os/exec" {
						importsExec = true
					}
				}

				named := ""
				ast.Inspect(file, func(node ast.Node) bool {
					literal, ok := node.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						return true
					}
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						return true
					}
					if match := cloudCLIProgram.FindString(value); match != "" {
						named = match
					}

					return true
				})

				if !importsExec && named == "" {
					return nil
				}
				if reason, ok := cloudCLIShellOutAllowed[pkg]; ok {
					t.Logf("allowed: %s (exec=%v, names=%q) — %s",
						relative, importsExec, named, reason)

					return nil
				}
				switch {
				case importsExec && named != "":
					t.Errorf("%s imports os/exec and names the cloud CLI %q.\n\nThat is a "+
						"delete with the Pod's own credentials, naming no SDK and making no "+
						"import any rule in this file reads. It is not closed here -- IAM "+
						"closes it -- but nothing in this tree has ever needed it, so the "+
						"first time something does it should be a decision with a sentence "+
						"in cloudCLIShellOutAllowed.", relative, named)
				case importsExec:
					t.Errorf("%s imports os/exec.\n\nA composition root and the output plane "+
						"wire things together; they do not start subprocesses. This is route "+
						"M's tell, the way `unsafe` is route K's: a subprocess runs with the "+
						"same credentials and is invisible to every import rule here.",
						relative)
				default:
					t.Errorf("%s names the cloud CLI %q.\n\nNothing in this tree drives a "+
						"vendor CLI. A program name in a string is how a shell-out arrives "+
						"one commit before the os/exec import does.", relative, named)
				}

				return nil
			})
		if err != nil {
			t.Fatalf("scanning %s/: %v", tree, err)
		}
	}

	if scanned < 100 {
		t.Fatalf("scanned only %d non-test files across %v; the walk failed and this rule "+
			"would pass vacuously", scanned, trees)
	}
}

// TestNoPackageAddressesACloudStorageEndpointOverRawHTTP is route L's tell.
//
// Route L is the sharpest one found: `golang.org/x/oauth2/google.DefaultClient`
// plus `net/http` DELETE to
// https://storage.googleapis.com/storage/v1/b/<bucket>/o/<key>. Fifteen lines,
// no go.mod change, no SDK named, and route I one layer down -- the same JSON
// API, reached with net/http instead of the generated client. An import rule
// cannot see a URL.
//
// This is repository-wide because the URL can be built anywhere the binary
// links, and it is over string literals because that is what a host in a URL
// is. Comments are not scanned: several files legitimately explain this route,
// including the paragraph above.
func TestNoPackageAddressesACloudStorageEndpointOverRawHTTP(t *testing.T) {
	root := repositoryRoot()
	fileSet := token.NewFileSet()
	scanned := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", "node_modules", "elm-stuff":
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		pkg := filepath.ToSlash(filepath.Dir(relative))
		scanned++

		// Cheap pre-filter: parse only files whose bytes mention a host at all.
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !cloudStorageEndpointHost.Match(body) {
			return nil
		}

		file, parseErr := parser.ParseFile(fileSet, path, body, 0)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", relative, parseErr)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				return true
			}
			match := cloudStorageEndpointHost.FindString(value)
			if match == "" {
				return true
			}
			if reason, ok := cloudStorageEndpointAllowed[pkg]; ok {
				t.Logf("allowed: %s names %s — %s", relative, match, reason)

				return true
			}
			t.Errorf("%s names the cloud object-storage endpoint %s in a string literal.\n\n"+
				"An SDK takes its endpoint from a (ctx, endpoint) constructor in "+
				"hangar/internal/gcsclient or from the tier's own config; a host written into "+
				"a literal is a URL somebody is about to build by hand. `DELETE "+
				"https://storage.googleapis.com/storage/v1/b/<bucket>/o/<key>` with "+
				"application default credentials is a working object delete that names no SDK "+
				"and makes no import any rule in this file reads. IAM is what refuses it; this "+
				"is what notices it was written.", relative, match)

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	if scanned < 1000 {
		t.Fatalf("scanned only %d non-test Go files; the walk failed and this rule would pass "+
			"vacuously", scanned)
	}
}

// TestTheAccidentTripwiresRecogniseWhatTheyAreLookingFor is the positive control
// for both rules above.
//
// Neither has anything to find in this repository, so a green says only that the
// walk completed. These cases say the matchers work -- including the near misses
// that must NOT match, because a tripwire that fires on ordinary code is a
// tripwire somebody disables.
func TestTheAccidentTripwiresRecogniseWhatTheyAreLookingFor(t *testing.T) {
	for _, tripping := range []string{
		"gcloud storage rm gs://bucket/key",
		"gsutil -m rm gs://bucket/**",
		"aws s3 rm s3://bucket/key",
		"az storage blob delete",
		"rclone delete remote:bucket/key",
		"s3cmd del s3://bucket/key",
		"azcopy remove",
	} {
		if !cloudCLIProgram.MatchString(tripping) {
			t.Errorf("cloudCLIProgram does not match %q, which is a cloud CLI delete", tripping)
		}
	}
	for _, harmless := range []string{
		"gcloudy", "the aws region", "az", "mc", "team storage", "s3 bucket name",
		"downloading azcopying",
	} {
		if cloudCLIProgram.MatchString(harmless) {
			t.Errorf("cloudCLIProgram matches %q, which is ordinary text; a tripwire that "+
				"fires on ordinary code is one somebody turns off", harmless)
		}
	}

	for _, tripping := range []string{
		"https://storage.googleapis.com/storage/v1/b/jb-output/o/key",
		"storage.cloud.google.com",
		"https://jb-output.s3.amazonaws.com/key",
		"https://s3.us-east-2.amazonaws.com/jb-output",
		"https://account.blob.core.windows.net/c/k",
		"https://abc.r2.cloudflarestorage.com/jb",
	} {
		if !cloudStorageEndpointHost.MatchString(tripping) {
			t.Errorf("cloudStorageEndpointHost does not match %q, which is an object-storage "+
				"API host", tripping)
		}
	}
	for _, harmless := range []string{
		"https://www.googleapis.com/auth/devstorage.read_only",
		"http://localhost:4443",
		"https://iam.googleapis.com",
		"https://sts.amazonaws.com",
		"storage",
	} {
		if cloudStorageEndpointHost.MatchString(harmless) {
			t.Errorf("cloudStorageEndpointHost matches %q, which is not an object-storage API "+
				"host", harmless)
		}
	}
}

// TestNoCommandRootReachesForUnsafe closes route K by its tell.
//
// //go:linkname needs `import _ "unsafe"` in the file that declares the body-less
// symbol, and every output root already links cloud.google.com/go/storage
// transitively through hangar/gcs -- so the SDK's own functions ARE in the build
// graph, and a linkname declaration could call one without importing anything
// the rules above can see. Round 3 recorded the route as theoretical rather than
// closed, which is a state that lasts exactly until somebody needs it.
//
// The rule is over cmd/ rather than the repository because that is where a
// composition root is, and because unsafe has legitimate uses elsewhere in a Go
// program. A root has none: it wires things together.
func TestNoCommandRootReachesForUnsafe(t *testing.T) {
	root := filepath.Join(repositoryRoot(), "cmd")
	fileSet := token.NewFileSet()
	scanned := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, relErr := filepath.Rel(repositoryRoot(), path)
		if relErr != nil {
			return relErr
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", relative, parseErr)
		}
		scanned++

		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if imported != "unsafe" {
				continue
			}
			t.Errorf("%s imports unsafe.\n\nEvery output root already links "+
				"cloud.google.com/go/storage transitively, so its functions are in the build "+
				"graph whether or not anything imports the package -- and //go:linkname needs "+
				"exactly this import to declare a body-less symbol that calls one. A "+
				"composition root wires things together; it has no other use for unsafe.",
				filepath.ToSlash(relative))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("scanning cmd/: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d files under cmd/; the walk failed and this rule would pass "+
			"vacuously", scanned)
	}
}

// durableCacheTierImporters are the binaries allowed to LINK the durable cache
// tier, and there is one.
//
// Round 3's route H: `durable.NewGCS(ctx, durable.GCSConfig{Bucket: …})` is an
// exported constructor over a real cloud client, and `(*GCS).Delete(ctx, key)`
// is an exported, unconditional, key-only delete with the bucket taken from a
// caller-supplied field. The output daemon could construct one pointed at the
// OUTPUT bucket and remove any object in it -- one intra-repo import, no storage
// SDK named, every guard in this file green.
//
// TestTheHangarStoreIsLinkedOnlyByTheArtifactDaemon was written for the
// identical shape one package over, and its doc comment says so word for word:
// "A root that can construct one can delete a key in any bucket it can name --
// including the output bucket." The exemption that existed for this package
// answered a different question -- whether `durable` may BE a root -- and not
// who may import it.
var durableCacheTierImporters = map[string]string{
	"cmd/artifact-daemon": "the artifact daemon is the only process that talks to the durable " +
		"resource-cache bucket, and the only one that has any business holding a tier whose " +
		"Delete takes a key",
}

// TestTheDurableCacheTierIsLinkedOnlyByTheArtifactDaemon mirrors
// hangarStoreImporters onto the tier round 3 found unguarded.
func TestTheDurableCacheTierIsLinkedOnlyByTheArtifactDaemon(t *testing.T) {
	graph := loadImportGraph(t)

	if _, ok := graph.all[durableCacheTier]; !ok {
		t.Fatalf("%s does not exist; this rule would pass vacuously", durableCacheTier)
	}
	for importer := range durableCacheTierImporters {
		imports, ok := graph.all[importer]
		if !ok {
			t.Errorf("allowed importer %q does not exist; the exemption is stale", importer)

			continue
		}
		if !slices.Contains(imports, durableCacheTier) {
			t.Errorf("%s is exempted to import %s and does not import it; the exemption is "+
				"stale, and an exemption nobody uses is a hole nobody is watching",
				importer, durableCacheTier)
		}
	}

	for pkg, imports := range graph.all {
		if pkg == durableCacheTier {
			continue
		}
		for _, imported := range imports {
			if imported != durableCacheTier {
				continue
			}
			if reason, ok := durableCacheTierImporters[pkg]; ok {
				t.Logf("allowed: %s imports %s — %s", pkg, durableCacheTier, reason)

				continue
			}
			t.Errorf("%s imports %s.\n\nThat package exports NewGCS over a real cloud client "+
				"and Delete(ctx, key) over whatever bucket its config names. A package that "+
				"can construct one can remove an object from any bucket it can name -- "+
				"including the dedicated output bucket -- which is the \"key-only delete "+
				"route\" requirement 55 asks the guards to reject. It is the artifact "+
				"daemon's cache tier, and nothing else has business holding it.",
				pkg, durableCacheTier)
		}
	}

	// And the roots, so a green says it was checked at the level that decides:
	// no output root links it, however many intermediaries it went through.
	for _, root := range []string{
		"./cmd/hangar-output-daemon", "./cmd/hangar-output-inventory",
		"./cmd/hangar-output-policy-attestor", "./cmd/hangar-output-reclaimer",
		"./cmd/hangar-output-activate", "./cmd/concourse",
	} {
		if linksPackage(t, root, modulePrefix+durableCacheTier) {
			t.Errorf("%s links %s, whose Delete is a delete by key over any bucket its config "+
				"names", root, durableCacheTier)
		}
	}
}
