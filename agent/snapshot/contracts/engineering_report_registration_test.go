package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const validationReportType = "validation-report/v1"

func validValidationReport() contracts.ValidationReportDocument {
	return contracts.ValidationReportDocument{
		SchemaVersion: "1.0.0",
		Subject:       "snapshot:41@sha256:" + strings.Repeat("a", 64),
		Status:        "failed",
		Summary:       "merge conflicts with the delivery target",
		Checks: []contracts.LegacyValidationCheck{
			{Name: "repository-merge", Status: "ok"},
			{Name: "docs/README.md", Status: "failed", Detail: "unmerged"},
		},
	}
}

// TestValidationReportContractIsRegisteredAndRevalidatable pins the contract the
// merge-delivery workflow depends on end to end.
//
// The seed declares `merge-report: validation-report/v1`
// (agent/workflow/seeds/merge-delivery-v3/workflow.yml) and the function writes
// validation-report.json (agent/functions/repositorymerge WriteReport). The seal
// path resolves that port type through the built-in registry by exact match, so
// an unregistered type kills the whole merge chain with
// `snapshot contracts: unsupported type "validation-report/v1"` and no operator
// can repair it.
//
// Both gates are asserted, not just the seal gate: a stored report that cannot be
// re-validated is unreadable, which is the same defect one read later.
func TestValidationReportContractIsRegisteredAndRevalidatable(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	ref := mustTypeRef(t, validationReportType)
	if _, err := registry.Lookup(ref); err != nil {
		t.Fatalf("Lookup(%q) error = %v; the merge-delivery seal path cannot resolve a validator for its own declared output type", ref, err)
	}

	tree := map[string][]byte{"validation-report.json": marshalDocument(t, validValidationReport())}
	if _, err := validateFiles(t, validationReportType, tree, emptyValidationContext(t)); err != nil {
		t.Fatalf("AdmitForSeal(valid validation-report.json) error = %v", err)
	}
	if _, err := revalidateSealedFiles(t, validationReportType, tree, emptyValidationContext(t)); err != nil {
		t.Fatalf("RevalidateSealed(valid validation-report.json) error = %v; a sealed report must stay readable", err)
	}
}

// The registration must carry the real document rules, not merely accept
// anything: a validator that admits a malformed report is no gate at all.
func TestValidationReportRegistrationEnforcesTheDocumentRules(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{
			name: "wrong file name",
			files: map[string][]byte{
				"report.json": marshalDocument(t, validValidationReport()),
			},
			want: "validation-report.json",
		},
		{
			name: "status does not match the worst check",
			files: func() map[string][]byte {
				document := validValidationReport()
				document.Status = "ok"
				return map[string][]byte{"validation-report.json": marshalDocument(t, document)}
			}(),
			want: "worst check status",
		},
		{
			name: "wrong schema version",
			files: func() map[string][]byte {
				document := validValidationReport()
				document.SchemaVersion = "1.0.1"
				return map[string][]byte{"validation-report.json": marshalDocument(t, document)}
			}(),
			want: "1.0.0",
		},
		{
			name: "unknown field",
			files: map[string][]byte{
				"validation-report.json": []byte(`{"schema_version":"1.0.0","subject":"s","status":"ok","summary":"s","checks":[{"name":"n","status":"ok"}],"unknown":true}`),
			},
			want: "unknown",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateFiles(t, validationReportType, test.files, emptyValidationContext(t))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AdmitForSeal() error = %v, want an error mentioning %q", err, test.want)
			}
		})
	}
}

// TestValidationReportIsADocumentTypeNotARecordType fixes which side of the
// record/document line this type sits on.
//
// It is a plain versioned JSON document: it carries no record envelope, so it has
// no schema-digest history, no embedded schema document, and no epistemic
// declaration. Registering it must not drag it into any of those tables — a
// record-type table entry would mean stored reports are expected to pin a schema
// digest they were never sealed with, and every read of an already-stored report
// would be reported as corruption.
func TestValidationReportIsADocumentTypeNotARecordType(t *testing.T) {
	ref := mustTypeRef(t, validationReportType)

	if contracts.IsRecordType(ref) {
		t.Fatalf("IsRecordType(%q) = true, want false: it is a versioned document, not a record envelope", ref)
	}
	if digest, found := contracts.SchemaDigestFor(ref); found {
		t.Fatalf("SchemaDigestFor(%q) = %q, want not found: a document type has no schema-digest history", ref, digest)
	}
	if accepted, found := contracts.AcceptedSchemaDigests(ref); found {
		t.Fatalf("AcceptedSchemaDigests(%q) = %v, want not found", ref, accepted)
	}
	if revision, found := contracts.CurrentSchemaRevisionFor(ref); found {
		t.Fatalf("CurrentSchemaRevisionFor(%q) = %d, want not found", ref, revision)
	}
	if _, found := contracts.SchemaDocumentFor(ref); found {
		t.Fatalf("SchemaDocumentFor(%q) found, want not found: a document type has no embedded schema document", ref)
	}
	if declaration, found := contracts.CurrentEpistemicDeclarationFor(ref); found {
		t.Fatalf("CurrentEpistemicDeclarationFor(%q) = %+v, want not found", ref, declaration)
	}
	for _, declaration := range contracts.EpistemicDeclarations() {
		if declaration.Type == ref {
			t.Fatalf("EpistemicDeclarations() includes %q; document types must stay out of the epistemic table", ref)
		}
	}
}

// gate-results/v1 was removed alongside validation-report/v1, but unlike it no
// live path emits gate-results/v1 any more: no seed declares the port and no
// function writes the document. Restoring it would be re-registering a type the
// deployment does not produce, so this records the asymmetry as a decision rather
// than leaving it to be re-litigated.
func TestGateResultsStaysUnregisteredBecauseNothingEmitsIt(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	if _, err := registry.Lookup(snapshot.TypeRef("gate-results/v1")); err == nil {
		t.Fatal("Lookup(\"gate-results/v1\") succeeded; if a live path now emits it, register it deliberately and update this test")
	}
}
