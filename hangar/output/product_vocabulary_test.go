package output

import (
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
var forbiddenVocabulary = map[string][]string{
	outputPackageDir: productVocabulary,
	basePackageDir:   append(append([]string{}, productVocabulary...), baseOnlyVocabulary...),
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
func checkNoProductVocabulary(fields []declaredField) []string {
	var problems []string

	if len(fields) == 0 {
		return []string{"the field inventory is empty; this rule would pass vacuously"}
	}

	usedExemptions := map[string]bool{}
	perPackage := map[string]int{}
	tagged := 0

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
	fields := contractFields(t, []string{outputPackageDir, basePackageDir})

	for _, problem := range checkNoProductVocabulary(fields) {
		t.Errorf("product vocabulary: %s", problem)
	}

	tagged := 0
	for _, field := range fields {
		if field.JSONName != "" {
			tagged++
		}
	}
	t.Logf("inventoried %d exported fields (%d with a wire name) across %v",
		len(fields), tagged, []string{outputPackageDir, basePackageDir})
}

// TestTheProductVocabularyGuardIsNotVacuous drives the rule with the two shapes
// the Phase 0 review demonstrated it used to miss (M-B and M-C), and with the
// benign shapes it must not object to.
func TestTheProductVocabularyGuardIsNotVacuous(t *testing.T) {
	t.Run("an empty inventory is fatal", func(t *testing.T) {
		if problems := checkNoProductVocabulary(nil); len(problems) == 0 {
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
		want   string
	}{
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
			problems := checkNoProductVocabulary(violation.fields)
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
		)
		if problems := checkNoProductVocabulary(benign); len(problems) != 0 {
			t.Errorf("the rule objected to Hangar's own vocabulary: %v", problems)
		}
	})
}
