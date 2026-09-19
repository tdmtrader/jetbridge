package concourse

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A capability with no production caller is not implemented.
//
// This rule exists because the tree produced the same defect five times in two
// phases. Phase 6 shipped CloseAbandonedReadLeases with no worker. Phase 7
// shipped AdmitReclaim, RecordOutOfBandAbsence, inventory.RecoverCursor and
// inventory.Classify the same way: written, specified, covered by tests, and
// unreachable from any running process. The consequence of the first of those
// was that no published object in the deployment could ever be reclaimed, and
// every pass reported `0/ok` while saying so.
//
// A review caught each one by reading. That is the part this replaces: the rule
// is now enforced by the toolchain, so the sixth instance fails a suite instead
// of costing a reviewer.
//
// What it measures, and the two honest limits of it.
//
// It asks, for every exported entry point in the trees below, whether some
// NON-TEST file anywhere in the repository names it other than at its own
// declaration, and whether the declaring package is linked by at least one
// binary under cmd/. That is an approximation of "reachable from a main" and not
// a call graph: a cluster of exported functions that only call each other would
// pass. It is the approximation that catches every instance this tree has
// actually produced -- each of the five was named by tests and by nothing else
// -- and it costs one `go list` invocation rather than a whole-program analysis.
//
// Three shapes of this rule were wrong before this one worked, and all three
// are recorded because the next person will try them.
//
//  1. "referenced outside the declaring package" produced 56 names, almost all
//     false: a closed vocabulary's ParseX is called by its own UnmarshalJSON and
//     by nothing outside the leaf, and that is wired, not dead. Same-package
//     references count, and they have to.
//
//  2. Counting comment mentions made the rule unable to fail at all -- a
//     function's doc comment opens with its own name, so every documented
//     capability referred to itself. It reads code now: a reference is an
//     identifier in an expression. That is structural rather than careful,
//     because parser.ParseFile runs with mode 0 and the walk only inspects
//     *ast.Ident, and comment text never becomes one.
//
//  3. Resolving references by bare identifier name across the WHOLE repository
//     made the rule unfalsifiable for any name that collides with any
//     identifier anywhere. Demonstrated: an unwired exported Reload() injected
//     into hangar/output passed, because some unrelated production file names
//     Reload, while an identically unwired SetParent() failed. It was not only
//     synthetic -- controller.Runner.String had no production caller and was
//     waved through on its name alone, while the identically placed Holds had
//     to be deferred. A reference now has to be one the compiler could also
//     resolve: the referring file is in the declaring package, or imports it,
//     or the name is in interfaceSatisfied. Credit is recorded per declaring
//     PACKAGE, not per name, because four role packages each declare New.
//
// What it still cannot see, stated so nobody mistakes a green for exactness: an
// identifier that is also a struct FIELD name somewhere visible is credited to
// the method of that name. policy.Attestor.ObservedAt was found by hand, not by
// this rule, because ObservedAt is a field on half the plane's wire types. The
// fix for that is golang.org/x/tools/go/packages in NeedTypes mode, which is
// the next shape and not this one.

// reachabilityTrees are the trees whose exported surface is under this rule.
var reachabilityTrees = []string{
	"hangar/output",

	// The controllers' composition. These packages exist BECAUSE four
	// capabilities had no production caller, so they are the last place that
	// should be allowed to grow a fifth.
	// The activation surface. The enable step's ten typed preconditions
	// shipped implemented, tested and called by nothing -- the third instance
	// of this defect on this track -- because this tree was outside the rule
	// while every other composition root was inside it.
	"atc/hangaroutput/activation",

	"atc/hangaroutput/inventorypass",
	"atc/hangaroutput/reclaimpass",
	"atc/hangaroutput/attestpass",
	"atc/hangaroutput/controller",
}

// hangarSurfaceFiles are the atc/db files that make up the Hangar surface. The
// rest of atc/db is a much older API whose reachability is not this phase's
// question.
const hangarSurfacePrefix = "hangar_output_"

// deferredEntryPoint is an exported entry point that is deliberately not wired.
//
// Each one carries the reason, and the SAME reason must appear in a
// `// Deferred:` line in the declaring file, so a reader at the declaration
// learns it there rather than from a list in a test file they will never open.
// A deferral is keyed by name AND, where it matters, by the declaring package.
// Four leaf role packages each declare New; a name-only deferral would defer all
// four, and three of them are wired.
type deferredEntryPoint struct {
	name string
	pkg  string // empty means every package that declares the name
	why  string
}

var deferredEntryPoints = []deferredEntryPoint{
	// The operator surface. Every one of these answers an operator's question
	// and nothing decides anything from it; the API and the status page that
	// ask them are Phase 8's.
	// atc/db.HangarReclaimJob.Remaining, which is the reclaim JOB's own
	// remaining lease term. hangar/output.OperationLease.Remaining -- the same
	// name, a different package -- is spent by the status surface; this one is
	// not, because the status surface reports the reclaim backlog as a COUNT
	// and a per-job term would be a series per object.
	{name: "Remaining", pkg: "atc/db", why: reclaimJobDetailHasNoReader},
	{name: "LoadReclaimJob", pkg: "atc/db", why: reclaimJobDetailHasNoReader},
	{name: "ReconcilePolicyViolation", why: operatorReconciliationHasNoAPI},

	// The consumer half: verifying a grant, a receipt or a lease answer that
	// this plane issued. This phase issues them and reads none of them back.
	{name: "KeyIDs", why: consumerHalf},
	{name: "ConstantTimeKeyIDEqual", why: consumerHalf},
	{name: "ReceiptEnvelopeIsUnaltered", why: consumerHalf},
	{name: "NewReadGrantVerifier", why: consumerHalf},
	{name: "VerifyReleaseAcknowledgement", why: consumerHalf},

	{name: "DeriveCohortFindings", why: cohortIdentities},

	{name: "Rotate", pkg: "atc/hangaroutput/activation", why: rotationHasNoOperatorPath},

	// The managed read's daemon half. It is composed in specs against the real
	// control plane and the real materializer, and nothing in production builds
	// one yet: a managed read reaches a consumer pod through a lease acquired
	// by the ATC before the Pod is built, and that acquisition is the piece
	// this phase did not land.
	{name: "NewLeaseReadProfile", pkg: "hangar/output", why: managedReadHasNoConsumer},
	{name: "Renew", pkg: "hangar/output", why: managedReadHasNoConsumer},
	// The publisher's read-under-lease is the daemon end of the same half. It
	// used to be credited through the leaf's Publisher interface, which no
	// production code held; the interface is gone and the credit with it.
	{name: "OpenExactObject", pkg: "hangar/output/publisher", why: managedReadHasNoConsumer},
	{name: "ObserveExactAbsence", why: separateAbsenceStat},
	{name: "Holds", why: runnerBeliefIsNotAuthority},

	// The reclaim-admission violation gate is enforced by the schema, on the
	// INSERT itself, so this read is not part of it: a Go copy of the rule
	// beside the SQL one would be two descriptions to keep in step, and the
	// one that is not the enforcement is the one that drifts. It stays where
	// the rest of the operator surface is.
}

const (
	statusSurface = "the operator status and diagnosis surface is Phase 8's; no running " +
		"process reads it yet"
	reclaimJobDetailHasNoReader = "the operator status surface reports the reclaim backlog as " +
		"a count, because a series per in-flight object is cardinality nobody can alert on. " +
		"Loading one job and reading its remaining term is a diagnosis of a SPECIFIC object, " +
		"and this track ships no API that names one"
	operatorReconciliationHasNoAPI = "reconciling a policy violation is a deliberate operator " +
		"act with its own record, and this track ships no API for it. The status surface is " +
		"deliberately read-only: a surface with a write in its port is one an operator can be " +
		"persuaded to \"just clear\", and a cleared violation is the record of what was wrong " +
		"while the plane refused work"
	consumerHalf = "the consumer-side verification half needs a consumer: these five verify a " +
		"receipt or a key id some process read BACK, and the process that does that is the " +
		"ATC's receipt registration. Three names this reason once covered -- ValidateLease, " +
		"RenewLease, ReleaseLease -- are now spent by hangar/output.LeaseReadProfile and are " +
		"off the list"
	cohortIdentities = "mixed-cohort detection needs a per-role observed identity the IAM read " +
		"does not return; Phase 8, with the activation verification"
	rotationHasNoOperatorPath = "rotation is the only one of the five transitions with no " +
		"operator path: the activation command has four modes and the chart's activation Job " +
		"renders those four, so neither a receipt-key rotation nor an epoch handover can be " +
		"asked for. Wiring it is a fifth mode, a second epoch flag and the Job name that " +
		"carries both, and it belongs with the first rotation rather than ahead of the first " +
		"activation: this plane ships dormant, and an epoch nobody has enabled has nothing to " +
		"rotate off"
	separateAbsenceStat = "the delete pass's own answer already reports absence; a separate " +
		"stat belongs to the ambiguous-response recovery path in Phase 8"
	runnerBeliefIsNotAuthority = "Holds is read by the liveness specs and by the Phase 8 " +
		"status surface; no running process decides anything from it, and a runner that " +
		"decided from its own belief rather than from the lease would be the stale owner " +
		"every fence in this plane exists to stop"
	managedReadHasNoConsumer = "a managed read reaches a consumer pod through a read lease " +
		"the ATC acquires before the Pod is built, and that acquisition -- the claim, the " +
		"lease transaction and the init-container route -- is the half of the Phase 8 " +
		"managed-read box this phase did not land. The profile itself is composed against " +
		"the real control plane and the real materializer in atc/hangaroutput"
)

func TestEveryExportedHangarEntryPointIsReachableOrDeclaredDeferred(t *testing.T) {
	root := repositoryRoot()

	declared := map[string][]string{} // exported name -> declaring package dirs
	for _, tree := range reachabilityTrees {
		collectExported(t, filepath.Join(root, filepath.FromSlash(tree)), root, "", declared)
	}
	collectExported(t, filepath.Join(root, "atc", "db"), root, hangarSurfacePrefix, declared)

	if len(declared) < 100 {
		t.Fatalf("found only %d exported entry points across %v and atc/db's Hangar surface, "+
			"which is far too few; the discovery failed and this rule would pass vacuously",
			len(declared), reachabilityTrees)
	}

	referenced := productionReferences(t, root, declared)
	linked := packagesLinkedByACommand(t)

	deferred := map[string]map[string]string{}
	for _, entry := range deferredEntryPoints {
		if deferred[entry.name] == nil {
			deferred[entry.name] = map[string]string{}
		}
		deferred[entry.name][entry.pkg] = entry.why
	}
	deferralUsed := map[string]bool{}

	var unreachable []string
	for name, packages := range declared {
		reachedFrom := referenced[name]
		for _, pkg := range packages {
			why, deferredHere := deferred[name][pkg]
			if !deferredHere {
				why, deferredHere = deferred[name][""]
				if deferredHere {
					deferralUsed[name] = true
				}
			} else {
				deferralUsed[name+" in "+pkg] = true
			}
			if deferredHere {
				assertDeferralIsRecordedAtTheSite(t, root, name, why, []string{pkg})
				if reachedFrom[pkg] {
					t.Errorf("%s (declared in %s) is listed in deferredEntryPoints and IS "+
						"referenced from production code. A deferral that outlived its reason "+
						"is a hole in this rule: the next capability to go unwired under that "+
						"name would be waved through. Delete the line and the `// Deferred:` "+
						"comment at the declaration.", name, pkg)
				}

				continue
			}
			if !reachedFrom[pkg] {
				unreachable = append(unreachable, name+" (declared in "+pkg+")")

				continue
			}
			if !linked[pkg] {
				unreachable = append(unreachable, name+" (declared in "+pkg+
					", which no binary under cmd/ links)")
			}
		}
	}

	// A deferral for something no longer declared is a line nobody is reading.
	for _, entry := range deferredEntryPoints {
		key := entry.name
		if entry.pkg != "" {
			key = entry.name + " in " + entry.pkg
		}
		if !deferralUsed[key] {
			t.Errorf("deferredEntryPoints lists %s and nothing under this rule declares it; "+
				"the line is stale", key)
		}
	}
	sort.Strings(unreachable)

	for _, name := range unreachable {
		t.Errorf("%s has no production caller.\n\nA capability with no production caller is not "+
			"implemented: it cannot be exercised, it cannot fail, and the suite that covers it "+
			"is measuring a description rather than a behaviour. Wire it to a main, delete it, "+
			"or add it to deferredEntryPoints with a reason and repeat that reason in a "+
			"`// Deferred:` line at the declaration.", name)
	}
}

// collectExported records every exported top-level func and method declared in
// the non-test Go files under dir.
func collectExported(t *testing.T, dir, root, filePrefix string, into map[string][]string) {
	t.Helper()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// conformance is a test-only package: it declares the tier-2
			// suite and nothing production links it.
			if info.Name() == "conformance" || info.Name() == "testdata" {
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filePrefix != "" && !strings.HasPrefix(filepath.Base(path), filePrefix) {
			return nil
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}
		pkg := packageOf(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			if containsString(into[fn.Name.Name], pkg) {
				continue
			}
			into[fn.Name.Name] = append(into[fn.Name.Name], pkg)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// productionReferences finds, for each name, whether a non-test file that can
// SEE the declaring package names it.
//
// The first shape of this resolved references by bare identifier name across
// the whole repository, and that made the rule unfalsifiable for any name that
// collides with any identifier anywhere in the tree. Demonstrated: injecting an
// unwired exported Reload() into hangar/output passed, because some unrelated
// production file elsewhere names Reload, while an identically unwired
// SetParent() failed. It was not only synthetic -- controller.Runner.String had
// no production caller and was waved through on its name alone, while the
// identically placed Holds had to be deferred.
//
// So a reference now has to be one the Go compiler could also resolve to the
// declaration: the referring file is in the declaring package, or its package
// imports the declaring package, or the name is in interfaceSatisfied below.
// That last list is the honest cost of not running a type checker: atc/db's
// Hangar surface is reached through atc/hangaroutput's port interfaces, so the
// caller imports the port and never the implementation. Those names are written
// down with the port that carries them rather than obtained by accident from a
// repository-wide name match.
func productionReferences(t *testing.T, root string, declared map[string][]string) map[string]map[string]bool {
	t.Helper()

	referenced := map[string]map[string]bool{}
	credit := func(name, pkg string) {
		if referenced[name] == nil {
			referenced[name] = map[string]bool{}
		}
		referenced[name][pkg] = true
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", "testdata":
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

		// What this file can see: its own package, and the module-internal
		// packages it imports.
		visible := map[string]bool{packageOf(root, path): true}
		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				continue
			}
			if after, found := strings.CutPrefix(imported, modulePrefix); found {
				visible[after] = true
			}
		}

		// Every exported declaration's own name node, so a function that is
		// declared and never called is not counted as calling itself.
		declarations := map[token.Pos]bool{}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.IsExported() {
				declarations[fn.Name.Pos()] = true
			}
		}
		// A function's own doc comment opens with its own name, and counting
		// that would have made this rule unable to redden anything. It cannot
		// happen structurally rather than by care: parser.ParseFile is called
		// with mode 0, so no comment is attached at all, and the walk below
		// only inspects *ast.Ident, which a comment's text never becomes.
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || !ident.IsExported() || declarations[ident.Pos()] {
				return true
			}
			packages, known := declared[ident.Name]
			if !known {
				return true
			}
			// Credited PER DECLARING PACKAGE, not per name. Four leaf role
			// packages each declare New, and crediting the name would make
			// every one of them reachable the moment any one of them was
			// called -- the same collision that made the repository-wide rule
			// unfalsifiable, one scope smaller. A bare identifier cannot say
			// which package it meant, so every declaring package this file can
			// see is credited: conservative, and still exact enough to have
			// found policy.New unwired.
			for _, pkg := range packages {
				if visible[pkg] || interfaceSatisfied[ident.Name].credits(pkg) {
					credit(ident.Name, pkg)
				}
			}

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	return referenced
}

// packagesLinkedByACommand is the union of every package every binary under
// cmd/ transitively links, expressed as repository-relative directories.
func packagesLinkedByACommand(t *testing.T) map[string]bool {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps", "./cmd/...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps ./cmd/... failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps ./cmd/... failed: %v", err)
	}

	const module = "github.com/concourse/concourse/"
	linked := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if after, found := strings.CutPrefix(line, module); found {
			linked[after] = true
		}
	}
	if len(linked) < 50 {
		t.Fatalf("go list -deps ./cmd/... reported %d packages in this module, which is far too "+
			"few; the discovery failed and this rule would pass vacuously", len(linked))
	}

	return linked
}

// assertDeferralIsRecordedAtTheSite requires the reason to be written where the
// declaration is, not only in this file.
func assertDeferralIsRecordedAtTheSite(t *testing.T, root, name, why string, packages []string) {
	t.Helper()

	for _, pkg := range packages {
		found := false
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(pkg)))
		if err != nil {
			t.Fatalf("reading %s: %v", pkg, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pkg), entry.Name()))
			if err != nil {
				t.Fatalf("reading %s/%s: %v", pkg, entry.Name(), err)
			}
			if strings.Contains(unwrapComments(string(content)), "// Deferred: "+why) {
				found = true

				break
			}
		}
		if !found {
			t.Errorf("%s is listed as deferred with the reason %q, but no file in %s carries a "+
				"`// Deferred: %s` line. A deferral a reader only finds in a test file is a "+
				"deferral the next person to read the declaration will not find.",
				name, why, pkg, why)
		}
	}
}

// unwrapComments joins a wrapped `//` comment block into one line, so a reason
// too long for 80 columns at the declaration is still the same sentence as the
// one in the list above.
func unwrapComments(content string) string {
	lines := strings.Split(content, "\n")
	var joined []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") && len(joined) > 0 &&
			strings.HasPrefix(strings.TrimSpace(joined[len(joined)-1]), "//") {
			joined[len(joined)-1] += " " + strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))

			continue
		}
		joined = append(joined, line)
	}

	return strings.Join(joined, "\n")
}

func packageOf(root, path string) string {
	relative, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return ""
	}

	return filepath.ToSlash(relative)
}

func containsString(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}

	return false
}

func repositoryRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)

	return filepath.Dir(thisFile)
}

// interfaceSatisfied are names reached through an interface rather than through
// an import of the declaring package.
//
// They are the price of resolving references by visibility instead of by a type
// checker. atc/db's Hangar surface is the implementation behind the port
// interfaces atc/hangaroutput declares, and a pass that calls
// Repository.AdmitReclaim imports the port, never atc/db -- so no rule based on
// what a file can see will find the declaration. The alternative is
// golang.org/x/tools/go/packages in NeedTypes mode, which would make this exact,
// and the reason it is not used yet is cost: this file runs in the same package
// as the architecture guards and loads no type information at all.
//
// Written down, with the port that carries each name, rather than obtained by
// accident: a repository-wide name match gave every one of these for free and
// gave controller.Runner.String for free with them.
//
// What an entry claims and what it does NOT. It says the declaration satisfies
// the named interface, so a caller that reaches it does so without importing
// the implementation. It does not say the port is called: that is the limit of
// a rule with no type information, and it is the reason this list is short,
// named port by port, and reviewed when it grows.
//
// Each entry carries its DECLARING PACKAGE, exactly as deferredEntryPoint does
// and for the identical reason. Without it the credit clause read
//
//	if visible[pkg] || interfaceSatisfied[ident.Name] != "" {
//
// -- the right-hand disjunct has no package in it, so a name on this list was
// credited to EVERY package that declares it, and the repository-wide bare-name
// rule this file was rewritten to remove was still alive for exactly these
// names. It was demonstrated: OpenExactObject and AcquireReadLease injected
// unwired into atc/hangaroutput/controller -- which satisfies neither port --
// were both waved through while a control in the same file was reported.
type satisfiedEntry struct {
	// pkg is the package whose declaration satisfies the port. Empty means
	// every package, and there is exactly one such entry, whose reason is that
	// the caller is the reflection inside encoding/json.
	pkg string

	// port names the interface, so a reviewer can check the claim.
	port string
}

// credits reports whether this entry vouches for a declaration in pkg.
func (entry satisfiedEntry) credits(pkg string) bool {
	return entry.port != "" && (entry.pkg == "" || entry.pkg == pkg)
}

var interfaceSatisfied = map[string]satisfiedEntry{
	// atc/hangaroutput.Repository -- the ATC-side port the passes hold. Its
	// implementation is db.HangarOutputRepository and no pass imports atc/db.
	"AcknowledgeCaptureRelease":    {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"AcquireCaptureLease":          {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"IssueStatChallenge":           {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"RecordFirstObjectCreate":      {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"RecordSealDeadline":           {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"RecordTerminalCaptureFailure": {pkg: "atc/db", port: "atc/hangaroutput.Repository"},
	"SealDeadlinePassed":           {pkg: "atc/db", port: "atc/hangaroutput.Repository"},

	// The same implementation, reached through the leaf's own repository ports
	// as well, so the capture half can be composed without the ATC.
	"AcknowledgeNoCaptureRelease":            {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"AcknowledgePreReservationCancelRelease": {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"CommitCaptureReservation":               {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"RecordNoCaptureIntent":                  {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"RegisterReceipt":                        {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"ResolveLogicalReservation":              {pkg: "atc/db", port: "atc/hangaroutput.Repository and hangar/output.CaptureRepository"},
	"CancelOrSettle":                         {pkg: "atc/db", port: "atc/hangaroutput.Repository"},

	// The read-lease ports, split so a holder of one cannot use the other.
	"AcquireReadLease":  {pkg: "atc/db", port: "atc/hangaroutput.ReadLeaseStore"},
	"ReleaseReadLease":  {pkg: "atc/db", port: "atc/hangaroutput.LeaseControlStore"},
	"RenewReadLease":    {pkg: "atc/db", port: "atc/hangaroutput.LeaseControlStore"},
	"ValidateReadLease": {pkg: "atc/db", port: "atc/hangaroutput.LeaseControlStore"},

	"CloseAbandonedReadLeases": {pkg: "atc/db", port: "atc/hangaroutput.AbandonedReadLeases"},
	"IncompleteHandoffs":       {pkg: "atc/db", port: "atc/hangaroutput.IncompleteReader"},

	// The controller's lease port, which is how the three command roots reach
	// the same implementation without linking atc/db's package name.
	"ClaimOperationLease": {pkg: "atc/db", port: "atc/hangaroutput/controller.Leases"},
	"RenewOperationLease": {pkg: "atc/db", port: "atc/hangaroutput/controller.Leases"},

	// The operator status surface's read port. Its implementation is
	// db.HangarOutputRepository, and atc/hangaroutput/status.go holds the port
	// rather than the package -- which is the same shape every other entry here
	// has, and the reason the reads below are reached without an atc/db import.
	"CountOutputPlaneState":       {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"LatestPolicySnapshot":        {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"ReadInventoryCursorProgress": {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"HangarDatabaseNow":           {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"ReadOperationLease":          {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"ReadInventoryDebt":           {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},
	"OpenPolicyViolations":        {pkg: "atc/db", port: "atc/hangaroutput.StatusStore"},

	// A standard-library interface, and the one case where the port is not in
	// this repository at all: encoding/json reaches these by reflection, so no
	// file names them anywhere.
	"UnmarshalJSON": {pkg: "", port: "encoding/json.Unmarshaler"},
}
