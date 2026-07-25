package contracts_test

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRecordSchemaDigestsArePinnedForEveryRecordContract(t *testing.T) {
	expected := map[string]string{
		"review/v1":            "sha256:01d9f0644151274e8577875373f110b11f0ec34ff29ba12b143379744416fdb5",
		"diagnosis/v1":         "sha256:7c7060b4d663d4546836898640f71bb576749d6aee7fee1df2b5616eea21064e",
		"validation/v1":        "sha256:b5a08c5bf14754800b4bd02eeb7fae8bf3ed1aa08e2f4905d1cfda15a96c0363",
		"repository-change/v1": "sha256:2dae971bc191c13eb9fc42f29992268d15c15765548a85d70bd808844af6e308",
		"selection/v1":         "sha256:009409ee7157092f910c971b2b14be45cc7db84e8911687f75bc63a22446d7dc",
		"measurements/v1":      "sha256:fea8ee17190c3dcf6c2d24065e2eea51acc9672c7cc091137fd3d6085e67a361",
	}
	for raw, want := range expected {
		ref := mustTypeRef(t, raw)
		first, found := contracts.SchemaDigestFor(ref)
		if !found {
			t.Fatalf("SchemaDigestFor(%q) not found", raw)
		}
		if err := first.Validate(); err != nil {
			t.Fatalf("SchemaDigestFor(%q) = %q: %v", raw, first, err)
		}
		if first.String() != want {
			t.Fatalf("SchemaDigestFor(%q) = %q, want frozen %q", raw, first, want)
		}
	}
	if _, found := contracts.SchemaDigestFor(mustTypeRef(t, "opaque/v1")); found {
		t.Fatal("opaque/v1 unexpectedly has a record schema digest")
	}
}
