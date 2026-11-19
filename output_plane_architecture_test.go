package concourse

import (
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
	"strconv"
	"strings"
	"testing"
)

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

// hangarGCSImporters are the packages allowed to name it, each with the reason.
//
// The list grew with the output plane, and the entries are deliberately three
// binaries-or-harnesses rather than a package layer. The role packages
// (hangar/output/publisher and its siblings) are NOT here and must not be:
// they depend on hangar/objectstore, which names no cloud SDK type, and that
// is what keeps the cloud client out of anything that links a role.
var hangarGCSImporters = map[string]string{

	"cmd/artifact-daemon/outputplane": "the artifact daemon's output plane is the publisher: it " +
		"opens the output bucket's object client through hangar/gcs, which hands back an " +
		"interface with no delete on it",
	"atc/hangaroutput/reclaim": "the web's reclaim pass and orphan sweep are the reclaimer " +
		"principal: they open the output namespace's list client and the exact-delete client, " +
		"gcs.NewDeleteClient; who may name that constructor is fixed by " +
		"hangar/architecture_test.go's TestOnlyTheReclaimerConstructsADeleteClient",
	"hangar/output/conformance": "the tier-2 conformance suite drives the real adapter against " +
		"fake-gcs-server, because a conformance claim proved through a hand-written fake is a " +
		"claim about the fake. It is a test-only import: the package has no non-test file that " +
		"names hangar/gcs, and TestTheConcourseBinaryLinksTheCloudStorageClientOnlyThroughTheReclaimer " +
		"below is what makes the consequence -- ./cmd/concourse -- checkable rather than argued",
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

// outputRolePackages are the two storage-facing roles of the Hangar output plane.
//
// Each is a separate binary with a separate Kubernetes service account, and the
// isolation only means something while no other process links one. The
// artifact daemon is the case that matters: its identity holds the cache and
// publisher roles, and a service account is Pod-wide, so an import of the
// reclaimer here would give that identity a delete no code in that process
// could give back.
var outputRolePackages = []string{
	"hangar/output/publisher",
	"hangar/output/reclaimer",
}

// outputRoleImporters are the packages allowed to link one, with the reason.
var outputRoleImporters = map[string]string{
	"hangar/diskserver": "TEST-ONLY: real TLS integration tests exercise publication through the disk adapter; production server code links no output role",
	"cmd/artifact-daemon/outputplane": "the artifact daemon's output plane is the publisher " +
		"principal: one node daemon, whose identity holds the publisher role beside the cache's, " +
		"and never inventory or reclaim",
	"atc/hangaroutput/reclaim": "the web's reclaim pass and orphan sweep: the web is the " +
		"reclaimer principal, and the node daemon -- the publisher -- holds no delete",
	"hangar/output/conformance": "the shared conformance suite drives both roles against " +
		"both substrate tiers; it is a test-only package that links into no binary",
	"atc/hangaroutput": "TEST-ONLY: the managed-read specs admit a read against the REAL " +
		"publisher's exact-generation stat over the same bucket the daemon published into, " +
		"because read admission checks the object rather than a fixture's opinion of it. " +
		"The package's production code declares ExactStat as a port and links nothing",

	"atc/db": "TEST-ONLY: the reclaim-pass specs drive the real reclaimer role against real " +
		"PostgreSQL and the tier-1 store, because the composition -- which " +
		"record precedes which effect -- is what the phase shipped unwired. The package's " +
		"production code links no role",

	"deploy/chart/tests": "TEST-ONLY: the chart's documented IAM matrix is DERIVED from " +
		"policy.RequiredPermissions rather than hand-copied beside it, because three " +
		"descriptions of one permission list had grown and the guard that was supposed to " +
		"keep them in step had three of the four principals written into it by hand. The " +
		"package renders Helm templates and links into no binary",
}

// testOnlyRoleImporters are the exemptions above whose reason says TEST-ONLY.
//
// The reason is CHECKED rather than believed: an exemption stated in a comment
// is one a later edit can quietly turn into a production import, and the whole
// point of the principal boundary is that a Pod's identity is Pod-wide. The
// check reads the non-test import graph, so a production file that named one of
// these would fail here even though the exemption is still listed.
var testOnlyRoleImporters = map[string]bool{
	"hangar/diskserver":         true,
	"hangar/output/conformance": true,
	"atc/hangaroutput":          true,
	"atc/db":                    true,
	"deploy/chart/tests":        true,
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
			t.Errorf("%s links %s.\n\nThe two output roles are two cloud principals. A Pod's identity is Pod-wide, so a process that links a role has "+
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
				t.Errorf("cmd/artifact-daemon links %s directly. Its output plane links the "+
					"publisher through cmd/artifact-daemon/outputplane and nothing else; a role "+
					"named by package main is a role the daemon's identity cannot give back.", role)
			}
		}
	}
}

// cloudStorageModule is the dependency the rule above exists to keep out of the
// ATC binary.
const cloudStorageModule = "cloud.google.com/go/storage"

// TestTheConcourseBinaryLinksTheCloudStorageClientOnlyThroughTheReclaimer
// measures the consequence.
//
// The import allowlist above is a rule about who may name a package. This one
// asks the toolchain what ./cmd/concourse actually links. The web is the
// reclaimer principal -- its reclaim pass and orphan sweep list and delete in
// the output namespace -- so it links the GCS client, and that costs the binary
// the client's packages (about 170 of them and 20 MB). What this keeps true is
// that it arrives through exactly one first-party door: hangar/gcs, named only
// by atc/hangaroutput/reclaim. A second door is the ATC growing cloud access for
// some other reason, and it fails here by name.
func TestTheConcourseBinaryLinksTheCloudStorageClientOnlyThroughTheReclaimer(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./cmd/concourse").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 500 {
		t.Fatalf("go list -deps reported %d packages for ./cmd/concourse, which is far too few "+
			"to be the ATC; the listing failed and this check would pass vacuously", len(lines))
	}

	const gcsAdapter = modulePrefix + hangarGCSPackage
	const onlyDoor = modulePrefix + "atc/hangaroutput/reclaim"
	reached := false
	for _, line := range lines {
		fields := strings.Fields(line)
		pkg, imports := fields[0], fields[1:]
		if strings.HasPrefix(pkg, "cloud.google.com/") || strings.HasPrefix(pkg, "google.golang.org/") {
			continue
		}
		for _, imported := range imports {
			sdk := imported == cloudStorageModule || strings.HasPrefix(imported, cloudStorageModule+"/")
			if sdk && pkg != gcsAdapter && !strings.HasPrefix(pkg, modulePrefix+"hangar/internal/") {
				t.Errorf("./cmd/concourse links %s through %s. The web reaches cloud storage only "+
					"through hangar/gcs, opened by its reclaim pass; depend on the hangar/objectstore "+
					"interface instead.", imported, pkg)
			}
			if imported != gcsAdapter {
				continue
			}
			if pkg != onlyDoor {
				t.Errorf("%s names %s inside ./cmd/concourse. The web's one door to the cloud "+
					"client is %s, the reclaimer principal.", pkg, gcsAdapter, onlyDoor)
			}
			reached = true
		}
	}
	if !reached {
		t.Error("./cmd/concourse does not link hangar/gcs at all, so the web has no GCS reclaim " +
			"pass and this rule is guarding nothing")
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
		// free; an allowlist entry nothing exercises stops describing the tree
		// it guards.
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
				"hangar/objectstore interface instead, or add %s above with the reason it must "+
				"link a cloud client.", pkg, hangarGCSPackage, pkg)
		}
	}
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

// outputDeleteCapability names the constructors that can build an object
// delete over a real backend. They used to be a package of their own, so
// "which root links it" answered "who can delete". The one-storage-interface
// track folded them into hangar/gcs and hangar/disk beside the read/create
// clients, which every storage-facing binary links, so the question is now
// answered at the CALL: hangar/architecture_test.go's
// TestOnlyTheReclaimerConstructsADeleteClient fixes who may name
// gcs.NewDeleteClient or disk.NewDeleteClient -- the reclaimer over the output
// namespace only. This file keeps the role half: only the reclaimer links the reclaimer.
const outputDeleteCapability = "gcs.NewDeleteClient and disk.NewDeleteClient"

// outputDeleteRoot is the one binary allowed to link the reclaimer role: the
// web, whose reclaim pass and orphan sweep are the only deletes in the output
// namespace.
const outputDeleteRoot = "./cmd/concourse"

func TestNoCommandRootReachesAnObjectDeleteThroughTheGoComposition(t *testing.T) {
	roots := commandRoots(t)
	if len(roots) < 4 {
		t.Fatalf("found %d command roots, which is far too few to be this repository's cmd/ "+
			"directory; the discovery failed and this rule would pass vacuously", len(roots))
	}

	// The control FIRST: the reclaimer really does link its role. Without it
	// every assertion below would also pass for a tree in which the role had
	// been deleted entirely.
	if !linksPackage(t, outputDeleteRoot, outputDeleteRole) {
		t.Fatalf("%s does not link %s, so this rule is guarding nothing",
			outputDeleteRoot, outputDeleteRole)
	}

	for _, root := range roots {
		if root == outputDeleteRoot {
			continue
		}
		if linksPackage(t, root, outputDeleteRole) {
			t.Errorf("%s reaches an object delete through the Go composition: it links %s.\n\n"+
				"Only the web -- its reclaim pass and orphan sweep -- may import or invoke the "+
				"output delete client. A Kubernetes service account is Pod-wide, so a second "+
				"binary that linked this would be a second Pod whose identity IAM would then "+
				"have to be trusted to keep delete away from. Who may construct a delete client at all "+
				"(%s) is fixed by hangar/architecture_test.go.", root, outputDeleteRole,
				outputDeleteCapability)
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
		if !ok || spec.Name.Name != "Client" {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		found = true
		for _, method := range iface.Methods.List {
			for _, name := range method.Names {
				if name.Name == "Delete" || name.Name == "DeleteExact" {
					t.Errorf("objectstore.Client declares Delete.\n\nEvery root that takes an "+
						"object adapter then holds the delete capability, whatever role it "+
						"links -- which is exactly the state a live objects.delete was "+
						"demonstrated from the output daemon in. Deletion belongs on "+
						"objectstore.DeleteClient, whose only implementation over a real cloud "+
						"client is %s.", outputDeleteCapability)
				}
			}
		}

		return false
	})
	if !found {
		t.Fatal("objectstore.Client was not found; this rule would pass vacuously")
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

	out, err := goList("-deps", root)
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

// TestEachOutputControllerLinksOnlyItsOwnRole is the same measurement for
// both principals.
//
// The isolation only means anything while each binary links ONE role. A
// controller that linked two would be one Kubernetes service account holding
// two sets of cloud permissions, and no care inside the process takes that back.
func TestEachOutputControllerLinksOnlyItsOwnRole(t *testing.T) {
	const prefix = "github.com/concourse/concourse/hangar/output/"

	expected := map[string]string{
		"./cmd/artifact-daemon": prefix + "publisher",
		"./cmd/concourse":       prefix + "reclaimer",
	}
	all := []string{
		prefix + "publisher", prefix + "reclaimer",
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

// inServiceWriters are the packages allowed to write the in-service row,
// hangar_enabled, and there is one.
//
// The row is what admission takes FOR SHARE: while it says false, no capture,
// claim-backed result or Run input that needs the output plane is admitted. So
// the set of things that can move it is the set of things that can put the
// plane in service or take it out, and that is the web's startup write
// (db.SetHangarEnabled, from hangarOutput.webEnabled). The rule is stated over
// SOURCE because what it is about is a STATEMENT: any package that can open a
// database handle can write any table, and no import rule can see that.
var inServiceWriters = map[string]string{
	"atc/db": "db.SetHangarEnabled, the web's startup write of its configured " +
		"--kubernetes-hangar-output-capture-enabled",
}

// nestedTestModules are the test harnesses that may write the in-service row,
// with the nested go.mod that makes the claim checkable.
//
// A comment saying "this is only a harness" is a comment a later edit can
// quietly make untrue. A separate module is not: the root module's build graph
// cannot reach it at all, so nothing this repository ships can link it.
var nestedTestModules = map[string]string{
	"atc/worker/jetbridge/brine/steps": "atc/worker/jetbridge/brine/go.mod",
}

const inServiceTable = "hangar_enabled"

// TestOnlyTheWebStartupWritesTheInServiceRow reads every non-test Go file in
// the repository for a write against the in-service row.
func TestOnlyTheWebStartupWritesTheInServiceRow(t *testing.T) {
	root := repositoryRoot()

	// A write is an UPDATE, an INSERT or a DELETE naming the table. A SELECT is
	// not: admission reads the row, and reading an authority is what an
	// authority is for.
	writes := regexp.MustCompile(`(?is)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+` + inServiceTable + `\b`)

	// And no production code changes who may write anything: a GRANT, a SET
	// ROLE or a DROP TRIGGER in SQL would be web granting itself an authority,
	// borrowing an identity, or removing a guard. Case-sensitive and
	// SQL-shaped, so prose about a step pod grant does not match.
	authorityStatements := regexp.MustCompile(`\bGRANT\s+[A-Z][A-Z_, ()]*\bON\b|\bSET\s+(LOCAL\s+)?ROLE\b|\bDROP\s+TRIGGER\b`)

	scanned, readers, exercised := 0, 0, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == ".git" || entry.Name() == ".claude" || entry.Name() == "node_modules" {
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
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		pkg := filepath.ToSlash(filepath.Dir(relative))
		if found := authorityStatements.FindString(string(body)); found != "" {
			if testHelperPackage(t, root, pkg) {
				t.Logf("allowed: %s issues %q in a test helper package", filepath.ToSlash(relative), found)
			} else if _, nested := nestedTestModules[pkg]; !nested {
				t.Errorf("%s issues %q. No production code may grant privileges, borrow a role, or "+
					"drop a trigger: roles and guards are the migrations' to define.",
					filepath.ToSlash(relative), found)
			}
		}
		if !strings.Contains(string(body), inServiceTable) {
			return nil
		}
		readers++
		if !writes.MatchString(string(body)) {
			return nil
		}
		if reason, ok := inServiceWriters[pkg]; ok {
			exercised[pkg] = true
			t.Logf("allowed: %s writes %s — %s", filepath.ToSlash(relative), inServiceTable, reason)

			return nil
		}
		if _, nested := nestedTestModules[pkg]; nested {
			t.Logf("allowed: %s writes %s — a TEST HARNESS in a nested module", filepath.ToSlash(relative), inServiceTable)

			return nil
		}
		t.Errorf("%s writes %s.\n\nThat row is what admission takes FOR SHARE: it puts the "+
			"output plane in service or takes it out. It moves only through the web's startup "+
			"write, db.SetHangarEnabled. A second writer is a second way to turn the plane on.",
			filepath.ToSlash(relative), inServiceTable)

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
		t.Fatalf("only %d non-test files name %s at all. Admission reads the row, so a "+
			"repository where nothing does is one where the table was renamed and this rule "+
			"is guarding a string", readers, inServiceTable)
	}
	for pkg := range inServiceWriters {
		if !exercised[pkg] {
			t.Errorf("%s is exempted to write %s and no file in it does; the exemption is stale",
				pkg, inServiceTable)
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
//	         in a shared wrapper, the raw JSON API for the same
//	         service under a different import path, and the same SDK named one
//	         package away from any file under cmd/
//
// Each fix measured the shape the previous finding had; each was green while
// the next one was open. So the rules below are stated over what a package may
// NAME and what a binary may LINK, repository-wide, rather than over where a
// particular call could be written.
//
// None of this is the primary control. GCS IAM is: the daemon's principal holds
// no storage.objects.delete, and the call gets a 403.
// What these rules buy is that the isolation stays true in code somebody writes
// next year, when the IAM binding is somebody else's memory of a Terraform file.

// cloudStorageSDKs are every import path that IS a Cloud Storage SDK.
//
// The second entry is the reason this is a list at all. Round 3 demonstrated
// `rawstorage "google.golang.org/api/storage/v1"` in the output daemon
// with `svc.Objects.Delete(bucket, key).Do()` -- an arbitrary, unconditional,
// key-only delete, with no go.mod change (google.golang.org/api is already
// required) and with `cloud.google.com/go/storage` appearing nowhere in the
// file. A rule comparing against one constant saw nothing.
//
// The third catches `s3.DeleteObject`: a delete with no Google SDK named.
// No production code needs that SDK; the entry stays as a tripwire.
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
	"hangar/gcs": "the one GCS object adapter every plane shares. Each capability it hands " +
		"back is an interface carrying only the operations its role may issue; the delete " +
		"client is a separate constructor whose callers hangar/architecture_test.go fixes",
	"hangar/output/conformance": "TEST-ONLY: the tier-2 conformance suite drives the real " +
		"adapter against fake-gcs-server, because a conformance claim proved through a " +
		"hand-written fake is a claim about the fake",
	"atc/worker/jetbridge/brine/steps": "TEST HARNESS: the brine adapter's step definitions, " +
		"in their own nested module, which the root module's build graph cannot reach at all",
}

// TestNoPackageOutsideTheCapabilityPackagesNamesACloudStorageSDK is the
// repository-wide arm.
//
// It reads IMPORTS and not text: the output plane mentions
// `cloud.google.com/go/storage` in a comment explaining why it does not import it, and a textual rule would either
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
			// Nested git checkouts are independent repositories, not this build's source.
			if path != root {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					return filepath.SkipDir
				}
			}

			switch entry.Name() {
			case "vendor", ".git", ".claude", "node_modules":
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
				"that links this package. Take the capability from hangar/gcs instead: each "+
				"constructor there opens and owns its own client behind a (ctx, endpoint) "+
				"signature and returns only the operations its role may issue. The reclaimer "+
				"is not an exception -- it reaches delete through %s.", relative, imported,
				outputDeleteCapability)
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
			case "vendor", ".git", ".claude", "node_modules", "elm-stuff":
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

// testHelperPackage reports whether pkg is a test-support package: one whose
// non-test files import Ginkgo, which nothing this repository ships links.
// It is a checked property rather than a comment that says so.
func testHelperPackage(t *testing.T, root, pkg string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(pkg)))
	if err != nil {
		t.Fatalf("reading %s: %v", pkg, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pkg), entry.Name()))
		if err != nil {
			t.Fatalf("reading %s/%s: %v", pkg, entry.Name(), err)
		}
		if strings.Contains(string(body), `"github.com/onsi/ginkgo/v2"`) {
			return true
		}
	}
	return false
}
