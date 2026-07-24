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
	if len(registry.Types()) != 16 {
		t.Fatalf("registry type count = %d, want 16 before selection/v1 lands", len(registry.Types()))
	}

	for _, raw := range []string{"review/v2", "dev-mcp/v1", "Review/v1"} {
		ref := snapshot.TypeRef(raw)
		if _, err := registry.Lookup(ref); err == nil {
			t.Errorf("Lookup(%q) succeeded, want exact-match unsupported type error", raw)
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

func (registryValidatorStub) Validate(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
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
