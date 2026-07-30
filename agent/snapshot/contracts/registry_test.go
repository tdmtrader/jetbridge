package contracts_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var builtinTypes = []string{
	"opaque/v1",
	"repository/v1",
	"repository-change/v1",
	"review/v1",
	"work-item/v1",
	"log-bundle/v1",
	"measurements/v1",
	"validation/v1",
	"upgrade-request/v1",
	"upgrade-report/v1",
	"database-snapshot/v1",
	"deployment-snapshot/v1",
	"audit-findings/v1",
	"diagnosis/v1",
	"question/v1",
	"human-answer/v1",
	"pull-request/v1",
	"pull-request-response/v1",
	"publish-impact/v1",
}

func TestRegistryContainsExactlyTheNamedV1Contracts(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	want := make([]snapshot.TypeRef, 0, len(builtinTypes))
	for _, raw := range builtinTypes {
		ref := mustTypeRef(t, raw)
		want = append(want, ref)
		if _, err := registry.Lookup(ref); err != nil {
			t.Errorf("Lookup(%q) error = %v", ref, err)
		}
	}
	if got := registry.Types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Types() = %q, want exact ordered types %q", got, want)
	}
	if len(registry.Types()) != 19 {
		t.Fatalf("registry type count = %d, want 19", len(registry.Types()))
	}

	for _, raw := range []string{"review/v2", "dev-mcp/v1", "Review/v1"} {
		ref := snapshot.TypeRef(raw)
		if _, err := registry.Lookup(ref); err == nil {
			t.Errorf("Lookup(%q) succeeded, want exact-match unsupported type error", raw)
		}
	}
}

// TestRetiredEngineeringDocumentTypesStayUnregistered records a decision rather
// than leaving it to be re-litigated.
//
// validation-report/v1 and gate-results/v1 were both plain versioned documents
// describing check outcomes. validation/v1 is the single sealed-record
// representation of that domain now: merge-delivery-v3 declares
// `merge-report: validation/v1` and agent/functions/repositorymerge writes the
// record envelope. Registering either retired name again would re-admit a type
// no seed declares and no function writes, so the deployment could accept a
// snapshot it can never produce.
//
// selection/v1 is the same decision reached from the other end. It was a full
// sealed-record contract with its own schema document and a candidate-port
// admission rule reaching from atc.SnapshotInputConfig through both step
// validators into the sealer, and nothing ever produced one: no seed declared a
// selecting node and no function wrote the record. Re-registering it would bring
// the whole candidacy-as-authority apparatus back for a type nothing emits.
func TestRetiredEngineeringDocumentTypesStayUnregistered(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	for _, raw := range []string{"validation-report/v1", "gate-results/v1", "selection/v1"} {
		ref := snapshot.TypeRef(raw)
		if _, err := registry.Lookup(ref); err == nil {
			t.Errorf("Lookup(%q) succeeded; if a live path now emits it, register it deliberately and update this test", raw)
		}
		if contracts.IsRecordType(ref) {
			t.Errorf("IsRecordType(%q) = true; a retired document type must not appear in the record schema tables", raw)
		}
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	ref := mustTypeRef(t, "example/v1")
	validator := registryValidatorStub{}
	_, err := contracts.NewRegistryWith(
		contracts.Registration{Type: ref, Validator: validator},
		contracts.Registration{Type: ref, Validator: validator},
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("NewRegistryWith(duplicate) error = %v, want duplicate error", err)
	}
}

func TestRegistryInstancesDoNotShareMutableState(t *testing.T) {
	first, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("first NewRegistry() error = %v", err)
	}
	second, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("second NewRegistry() error = %v", err)
	}

	firstTypes := first.Types()
	firstTypes[0] = mustTypeRef(t, "mutated/v1")
	if got := second.Types()[0]; got.String() != "opaque/v1" {
		t.Fatalf("second registry first type = %q after caller mutation, want opaque/v1", got)
	}
}

type registryValidatorStub struct{}

func (registryValidatorStub) AdmitForSeal(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return snapshot.ValidationResult{}, nil
}

func (registryValidatorStub) RevalidateSealed(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return snapshot.ValidationResult{}, nil
}

func mustTypeRef(t *testing.T, raw string) snapshot.TypeRef {
	t.Helper()
	ref, err := snapshot.ParseTypeRef(raw)
	if err != nil {
		t.Fatalf("ParseTypeRef(%q): %v", raw, err)
	}
	return ref
}
