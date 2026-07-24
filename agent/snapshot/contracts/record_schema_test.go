package contracts_test

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRecordSchemaDigestsArePinnedForEveryRecordContract(t *testing.T) {
	for _, raw := range []string{
		"review/v1",
		"diagnosis/v1",
		"validation/v1",
		"repository-change/v1",
		"selection/v1",
		"measurements/v1",
	} {
		ref := mustTypeRef(t, raw)
		first, found := contracts.SchemaDigestFor(ref)
		if !found {
			t.Fatalf("SchemaDigestFor(%q) not found", raw)
		}
		if err := first.Validate(); err != nil {
			t.Fatalf("SchemaDigestFor(%q) = %q: %v", raw, first, err)
		}
		second, found := contracts.SchemaDigestFor(ref)
		if !found || second != first {
			t.Fatalf("SchemaDigestFor(%q) is not stable: %q then %q", raw, first, second)
		}
	}
	if _, found := contracts.SchemaDigestFor(mustTypeRef(t, "opaque/v1")); found {
		t.Fatal("opaque/v1 unexpectedly has a record schema digest")
	}
}
