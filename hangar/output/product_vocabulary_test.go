package output

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Hangar is the product-neutral durable result plane. Spec AC 20 requires the
// guards to reject Run, workflow, ticket, agent, Anvil or playbook domain
// *fields* as well as imports, and plan.md adds the base envelope's own extras
// on top of that.
//
// A field has two names. `Origin string \`json:"run_id"\`` is a benign Go
// identifier and a product word on the wire, and the wire is the half other
// implementations read. So the rule below runs over both spellings, and over
// both contract packages: `ClaimAcquisition.RunID` in hangar/output is the same
// defect on the other side of the extension boundary.
//
// The exemptions are the honest part. `WriterTicketID` is a Hangar
// source-ledger ticket, not a JetBridge one, and there is no way to tell those
// apart by tokenizing. Each exemption is named, reasoned, and must match
// something, so it cannot outlive the field it excuses.
//
// Reqs 1, 57; AC 20.

// productVocabulary is spec AC 20's list, and it applies to both packages. The
// capture extension is no more allowed to learn what a Run is than the base
// protocol is.
var productVocabulary = []string{
	"run", "workflow", "ticket", "agent", "anvil", "playbook",
}

// baseOnlyVocabulary are the meanings the *base* exact-execution protocol
// additionally must not carry. They are what lets the sibling track wire an
// ordinary job, a one-off and a check onto the same protocol without Hangar
// learning which is which — and they are exactly hangar/output's own subject
// matter, so they stop at the extension boundary.
var baseOnlyVocabulary = []string{
	"build", "job", "check", "cancel", "cancellation",
	"output", "capture", "hold", "receipt", "claim", "bucket",
}

// forbiddenVocabulary states the rule per package, so that a package this file
// does not describe cannot inherit an empty one by default.
// coordinatorPackageDir is the control plane's capture coordinator. It is a
// THIRD package under this rule, and it has to be: it is where a Run, a build
// or a job would first be tempted in, because it is the half that talks to a
// deployment. It may say `capture`, `output`, `hold` and `receipt` -- those are
// its subject -- and it may not say what a capture is FOR.
const coordinatorPackageDir = "../../atc/hangaroutput"

// coordinatorVocabulary is the product list plus the execution words the
// coordinator must not learn either. It is generic over an opaque handoff; a
// field named for a build or a job would be the boundary quietly moving.
var coordinatorVocabulary = append(append([]string{}, productVocabulary...),
	"build", "job", "check")

var forbiddenVocabulary = map[string][]string{
	outputPackageDir:      productVocabulary,
	basePackageDir:        append(append([]string{}, productVocabulary...), baseOnlyVocabulary...),
	coordinatorPackageDir: coordinatorVocabulary,
}

// hangarTreeRoot is the hangar module root, relative to this package. Every Go
// package under it is subject to at least productVocabulary: the three
// packages forbiddenVocabulary states keep their stricter lists, and any other
// package found by the walk -- a store, a ledger, a publisher, a reclaimer --
// gets the product list. A new package under hangar/ is therefore covered the
// day it is created, not the day someone remembers to name it here.
const hangarTreeRoot = ".."

// hangarPackageDirs walks hangar/... and returns every directory holding a
// non-test Go file, spelled the way contractFields keys the inventory (relative
// to this package, so hangar/output is "." and hangar/executioncontrol is
// "../executioncontrol"). testdata directories are not packages and are
// skipped.
func hangarPackageDirs(t *testing.T) []string {
	t.Helper()

	var dirs []string
	err := filepath.WalkDir(hangarTreeRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == "testdata" {
			return fs.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, file := range entries {
			name := file.Name()
			if file.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}
			// Spell the directory relative to THIS package as the walk sees
			// it (hangar/output), so that hangar/output comes back as "."
			// and hangar/executioncontrol as "../executioncontrol" -- the
			// keys forbiddenVocabulary and the inventory already use.
			rel, err := filepath.Rel(filepath.Join(hangarTreeRoot, "output"), path)
			if err != nil {
				return err
			}
			dirs = append(dirs, rel)

			break
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", hangarTreeRoot, err)
	}
	sort.Strings(dirs)

	return dirs
}

// vocabularyExemptions are the exported fields whose name or wire spelling
// contains a forbidden token for a reason that is Hangar's own.
//
// There is exactly one such word today. A *writer ticket* is the source
// ledger's admission record over one source incarnation — issued, drained and
// closed entirely inside Hangar — and it has nothing to do with the product's
// tickets. No amount of tokenizing can tell those apart, so the exemption is
// named rather than inferred, and each entry must match an inventoried field or
// this rule reports it.
var vocabularyExemptions = map[string]string{
	outputPackageDir + ":WriterAdmission.WriterTicketID": "the source ledger's own admission " +
		"record over one source incarnation, not a product ticket",
	outputPackageDir + ":CaptureAcknowledgement.WriterTicketID": "the ticket a writer_ticket_* " +
		"statement is about",
	outputPackageDir + ":DrainedWriter.WriterTicketID": "the captured-drain-set entry this " +
		"close evidence accounts for",
}

// checkNoProductVocabulary is the rule, as a pure function over an inventory,
// so that TestTheProductVocabularyGuardIsNotVacuous can drive it with fields
// that violate it.
//
// treePackages are the packages the hangar/... walk found. A package
// forbiddenVocabulary states keeps its stricter list whether or not the walk
// found it; any other walked package is held to productVocabulary; a package
// in neither is undescribed and refused, so the rule still cannot be satisfied
// by a package it has never heard of.
func checkNoProductVocabulary(fields []declaredField, treePackages []string) []string {
	var problems []string

	if len(fields) == 0 {
		return []string{"the field inventory is empty; this rule would pass vacuously"}
	}

	walked := map[string]bool{}
	for _, pkg := range treePackages {
		walked[pkg] = true
	}

	usedExemptions := map[string]bool{}
	perPackage := map[string]int{}
	tagged := 0
	treeOnlyFields := 0

	for _, field := range fields {
		// An embedded field carries no name of its own; the type it embeds is
		// inventoried where it is declared.
		if field.Name == "" {
			continue
		}
		perPackage[field.Package]++
		if field.JSONName != "" {
			tagged++
		}

		forbidden, described := forbiddenVocabulary[field.Package]
		if !described && walked[field.Package] {
			forbidden, described = productVocabulary, true
			treeOnlyFields++
		}
		if !described {
			problems = append(problems, field.Package+" declares exported fields and "+
				"forbiddenVocabulary does not describe it. Every scanned package states its own "+
				"forbidden vocabulary, so a renamed or added package cannot inherit an empty rule.")

			continue
		}

		key := field.Package + ":" + field.Owner + "." + field.Name
		for _, term := range forbidden {
			for _, spelling := range []struct{ what, value string }{
				{"its Go field name", field.Name},
				{"its wire name", field.JSONName},
			} {
				if spelling.value == "" || !namesTerm(spelling.value, term) {
					continue
				}
				if _, exempt := vocabularyExemptions[key]; exempt {
					usedExemptions[key] = true

					continue
				}
				problems = append(problems, field.Package+": "+field.Owner+"."+field.Name+
					" carries "+term+" meaning in "+spelling.what+" ("+spelling.value+
					"). Hangar owns durable capture, sealing, publication and claims; it must not "+
					"learn why the caller opted in. If this is genuinely Hangar's own word, name "+
					"it in vocabularyExemptions with the reason.")
			}
		}
	}

	if tagged == 0 {
		problems = append(problems, "no inventoried field carries a json tag. The wire name is "+
			"half of what this rule checks, and an inventory that stopped extracting tags would "+
			"report success over every product word on the wire.")
	}
	for pkg := range forbiddenVocabulary {
		if perPackage[pkg] == 0 {
			problems = append(problems, "forbiddenVocabulary describes "+pkg+", and the inventory "+
				"found no exported field there. The rule would pass vacuously for that package.")
		}
	}
	if len(treePackages) > 0 && treeOnlyFields == 0 {
		problems = append(problems, "the hangar/... walk named packages beyond the ones "+
			"forbiddenVocabulary states, and the inventory found no exported field in any of "+
			"them. The widened rule would pass vacuously over the rest of the tree.")
	}
	for key, reason := range vocabularyExemptions {
		if !usedExemptions[key] {
			problems = append(problems, "vocabularyExemptions exempts "+key+" ("+reason+"), and "+
				"no inventoried field by that name carries a forbidden term any more. Remove the "+
				"entry so the exemption list keeps describing what is actually true.")
		}
	}

	return problems
}

func TestNeitherContractPackageCarriesProductMeaning(t *testing.T) {
	tree := hangarPackageDirs(t)

	// The walk must find the two stated packages that live under hangar/ by
	// the same spelling forbiddenVocabulary uses, or the stricter lists would
	// silently apply to nothing while the walk reported the tree covered.
	found := map[string]bool{}
	for _, dir := range tree {
		found[dir] = true
	}
	for _, stated := range []string{outputPackageDir, basePackageDir} {
		if !found[stated] {
			t.Fatalf("the hangar/... walk did not find %s; it found %v", stated, tree)
		}
	}
	if len(tree) <= len(forbiddenVocabulary) {
		t.Fatalf("the hangar/... walk found only %v; the widened rule is meant to reach "+
			"packages forbiddenVocabulary does not state", tree)
	}

	dirs := append(append([]string{}, tree...), coordinatorPackageDir)
	fields := contractFields(t, dirs)

	for _, problem := range checkNoProductVocabulary(fields, tree) {
		t.Errorf("product vocabulary: %s", problem)
	}

	tagged := 0
	for _, field := range fields {
		if field.JSONName != "" {
			tagged++
		}
	}
	t.Logf("inventoried %d exported fields (%d with a wire name) across %v",
		len(fields), tagged, dirs)
}

// TestTheProductVocabularyGuardIsNotVacuous drives the rule with the two shapes
// the Phase 0 review demonstrated it used to miss (M-B and M-C), and with the
// benign shapes it must not object to.
func TestTheProductVocabularyGuardIsNotVacuous(t *testing.T) {
	t.Run("an empty inventory is fatal", func(t *testing.T) {
		if problems := checkNoProductVocabulary(nil, nil); len(problems) == 0 {
			t.Fatal("the rule passed over an empty inventory")
		}
	})

	// Every violating inventory below carries the real exempted fields too, so
	// that the exemption-must-match clause does not drown the assertion.
	exempted := []declaredField{
		{basePackageDir, "Envelope", "ProtocolVersion", "protocol_version", "string"},
		{outputPackageDir, "WriterAdmission", "WriterTicketID", "", "WriterTicketID"},
		{outputPackageDir, "CaptureAcknowledgement", "WriterTicketID", "writer_ticket_id", "WriterTicketID"},
		{outputPackageDir, "DrainedWriter", "WriterTicketID", "", "WriterTicketID"},
	}
	with := func(extra ...declaredField) []declaredField {
		return append(append([]declaredField{}, exempted...), extra...)
	}

	violations := map[string]struct {
		fields []declaredField
		tree   []string
		want   string
	}{
		// The widened rule: a package the walk found, and nothing states, is
		// held to the product list.
		"a product word in a walked package nobody stated": {
			fields: with(declaredField{"../gcsstore", "Store", "AgentID", "agent_id", "string"}),
			tree:   []string{outputPackageDir, basePackageDir, "../gcsstore"},
			want:   "../gcsstore: Store.AgentID carries agent meaning in its Go field name (AgentID)",
		},
		"a walked package on the wire": {
			fields: with(declaredField{"ledger", "Entry", "Origin", "workflow_id", "string"}),
			tree:   []string{outputPackageDir, basePackageDir, "ledger"},
			want:   "ledger: Entry.Origin carries workflow meaning in its wire name (workflow_id)",
		},
		// The stricter list wins over the walk for a package both name.
		"a base-only extra still bites when the walk also found the base package": {
			fields: with(declaredField{basePackageDir, "Envelope", "OutputName", "output", "string"}),
			tree:   []string{outputPackageDir, basePackageDir, "../gcsstore"},
			want:   "Envelope.OutputName carries output meaning",
		},
		"a walk that reaches no field outside the stated packages is refused": {
			fields: with(declaredField{outputPackageDir, "ReadLease", "LeaseFence", "lease_fence", "string"}),
			tree:   []string{outputPackageDir, basePackageDir, "../gcsstore"},
			want:   "found no exported field in any of them",
		},
		// M-B: the Go name is innocent and the wire says run_id.
		"a product word on the base envelope's wire": {
			fields: with(declaredField{basePackageDir, "Envelope", "Origin", "run_id", "string"}),
			want:   "Envelope.Origin carries run meaning in its wire name (run_id)",
		},
		// M-C: the same defect one package over.
		"a product field in hangar/output": {
			fields: with(declaredField{outputPackageDir, "ClaimAcquisition", "RunID", "run_id", "string"}),
			want:   "ClaimAcquisition.RunID carries run meaning in its Go field name (RunID)",
		},
		"a base-only extra still bites on the base package": {
			fields: with(declaredField{basePackageDir, "Envelope", "OutputName", "output", "string"}),
			want:   "Envelope.OutputName carries output meaning",
		},
		"an undescribed package cannot inherit an empty rule": {
			fields: with(declaredField{"../elsewhere", "Thing", "Field", "field", "string"}),
			want:   "forbiddenVocabulary does not describe it",
		},
		"an inventory with no wire names at all is refused": {
			fields: []declaredField{
				{basePackageDir, "Envelope", "NodeUID", "", "NodeUID"},
				{outputPackageDir, "ReadLease", "LeaseFence", "", "LeaseFence"},
			},
			want: "no inventoried field carries a json tag",
		},
		"a package with no fields is refused": {
			fields: []declaredField{{basePackageDir, "Envelope", "NodeUID", "node_uid", "NodeUID"}},
			want:   "found no exported field there",
		},
	}

	for name, violation := range violations {
		t.Run(name, func(t *testing.T) {
			problems := checkNoProductVocabulary(violation.fields, violation.tree)
			if len(problems) == 0 {
				t.Fatalf("the rule found nothing wrong with an inventory built to violate it: %v",
					violation.fields)
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, violation.want) {
				t.Errorf("the rule did not report %q. It reported:\n%s", violation.want, joined)
			}
		})
	}

	t.Run("the rule tolerates Hangar's own vocabulary", func(t *testing.T) {
		benign := with(
			// hangar/output is the capture extension: output, capture, hold,
			// receipt, claim and bucket are its own words there.
			declaredField{outputPackageDir, "CaptureAdmission", "Output", "output", "OutputName"},
			declaredField{outputPackageDir, "Receipt", "Claims", "claims", "ReceiptClaims"},
			declaredField{outputPackageDir, "PolicySnapshot", "BucketFingerprint", "bucket_fingerprint", "string"},
			// and words that merely contain a forbidden one are not it.
			declaredField{basePackageDir, "Acknowledgement", "ProcessIdentity", "process_identity", "ProcessIdentity"},
			declaredField{outputPackageDir, "InventoryDebt", "Attempts", "attempts", "int"},
			// The coordinator's own words are capture words too: it is the
			// control plane's half of the same extension, and it says nothing
			// about what a capture is for.
			declaredField{coordinatorPackageDir, "Coordinator", "ReceiptKeyID", "", "string"},
			// A walked package may use Hangar's own capture words freely; only
			// the product list reaches it.
			declaredField{"../gcsstore", "Store", "BucketName", "bucket", "string"},
			declaredField{"reclaimer", "Pass", "HeldReceipts", "held_receipts", "int"},
		)
		tree := []string{outputPackageDir, basePackageDir, "../gcsstore", "reclaimer"}
		if problems := checkNoProductVocabulary(benign, tree); len(problems) != 0 {
			t.Errorf("the rule objected to Hangar's own vocabulary: %v", problems)
		}
	})
}
