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
// Same-package references count, and they have to. A closed vocabulary's
// Parse<Member> is called by its own UnmarshalJSON and by nothing outside the
// leaf, and that is a wired capability rather than a dead one: the wire type it
// decodes is what a controller reads.
//
// It reads CODE and not comments. A comment scan was the first shape of this
// rule and it was wrong: a function's own doc comment opens with its own name,
// and half the prose in this tree names the method it is about, so every
// documented capability referred to itself and nothing could ever redden. A
// reference here is an identifier in an expression.

// reachabilityTrees are the trees whose exported surface is under this rule.
var reachabilityTrees = []string{
	"hangar/output",
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
type deferredEntryPoint struct {
	name string
	why  string
}

var deferredEntryPoints = []deferredEntryPoint{
	// The operator surface. Every one of these answers an operator's question
	// and nothing decides anything from it; the API and the status page that
	// ask them are Phase 8's.
	{"AdmitsNewWork", statusSurface},
	{"AtCycleStart", statusSurface},
	{"Remaining", statusSurface},
	{"ValidatePolicyEvidenceAge", statusSurface},
	{"HangarDatabaseNow", statusSurface},
	{"LoadReclaimJob", statusSurface},
	{"ReadOperationLease", statusSurface},
	{"ReadInventoryDebt", statusSurface},
	{"ReconcilePolicyViolation", statusSurface},

	// The consumer half: verifying a grant, a receipt or a lease answer that
	// this plane issued. This phase issues them and reads none of them back.
	{"KeyIDs", consumerHalf},
	{"ConstantTimeKeyIDEqual", consumerHalf},
	{"ReceiptEnvelopeIsUnaltered", consumerHalf},
	{"NewReadGrantVerifier", consumerHalf},
	{"VerifyReleaseAcknowledgement", consumerHalf},
	{"ValidateLease", consumerHalf},
	{"RenewLease", consumerHalf},
	{"ReleaseLease", consumerHalf},

	{"DeriveCohortFindings", cohortIdentities},
	{"ObserveExactAbsence", separateAbsenceStat},
	{"ValidateSealDeadline", sealDeadlineHasNoFlag},

	// Two that are wired later in this revision. They are here so the first
	// commit is green and the rule is on from it; the commit that wires each
	// deletes its line, and the "listed as deferred but referenced" arm below
	// is what stops a line outliving its reason.
	{"HangarOutputNotify", notifyArrivesLater},
	{"OpenPolicyViolations", violationGateArrivesLater},
}

const (
	statusSurface = "the operator status and diagnosis surface is Phase 8's; no running " +
		"process reads it yet"
	consumerHalf = "the consumer-side verification half is Phase 8's; nothing in this phase " +
		"reads a grant, a receipt or a lease answer back"
	cohortIdentities = "mixed-cohort detection needs a per-role observed identity the IAM read " +
		"does not return; Phase 8, with the activation verification"
	separateAbsenceStat = "the delete pass's own answer already reports absence; a separate " +
		"stat belongs to the ambiguous-response recovery path in Phase 8"
	sealDeadlineHasNoFlag = "Req 17's bound has no operator-facing flag to refuse; the " +
		"coordinator's seal deadline is set by the ATC's own composition"
	notifyArrivesLater        = "NOTIFY acceleration is wired later in this revision, under R1-F9"
	violationGateArrivesLater = "the reclaim-admission violation gate is wired later in this " +
		"revision, under R1-F15"
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

	deferred := map[string]string{}
	for _, entry := range deferredEntryPoints {
		deferred[entry.name] = entry.why
	}

	var unreachable []string
	for name, packages := range declared {
		if why, ok := deferred[name]; ok {
			assertDeferralIsRecordedAtTheSite(t, root, name, why, packages)
			if referenced[name] {
				t.Errorf("%s is listed in deferredEntryPoints and IS referenced from production "+
					"code. A deferral that outlived its reason is a hole in this rule: the next "+
					"capability to go unwired under that name would be waved through. Delete "+
					"the line and the `// Deferred:` comment at the declaration.", name)
			}

			continue
		}
		if !referenced[name] {
			unreachable = append(unreachable, name+" (declared in "+strings.Join(packages, ", ")+")")

			continue
		}
		for _, pkg := range packages {
			if !linked[pkg] {
				unreachable = append(unreachable, name+" (declared in "+pkg+
					", which no binary under cmd/ links)")
			}
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

// productionReferences finds, for each name, whether a non-test file outside
// the declaring package names it.
func productionReferences(t *testing.T, root string, declared map[string][]string) map[string]bool {
	t.Helper()

	referenced := map[string]bool{}
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
		// Every exported declaration's own name node, so a function that is
		// declared and never called is not counted as calling itself.
		declarations := map[token.Pos]bool{}
		// A function's own doc comment opens with its own name. Counting that
		// as a reference would mean every documented function in the tree
		// referred to itself and this rule could never redden anything.
		ownDoc := map[*ast.CommentGroup]string{}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.IsExported() {
				declarations[fn.Name.Pos()] = true
				if fn.Doc != nil {
					ownDoc[fn.Doc] = fn.Name.Name
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || !ident.IsExported() {
				return true
			}
			if _, known := declared[ident.Name]; known && !declarations[ident.Pos()] {
				referenced[ident.Name] = true
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
