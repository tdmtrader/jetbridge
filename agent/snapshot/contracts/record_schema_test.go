package contracts_test

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// This pin freezes what NEWLY AUTHORED records carry, which is the one digest
// seal-time admission accepts. It is REPLACED at a bump, never appended to — the
// complete accepted history is the separate pin in record_schema_history_test.go,
// and conflating the two is how a superseded digest ends up being handed to a
// producer.
//
// These are the revision-2 digests: the canonical serialization of each embedded
// schema document, pinned independently in
// TestSchemaDocumentCanonicalSerializationIsStable before the bump so that this
// change installs reviewed bytes rather than whatever the tree happens to compute
// today.
func TestRecordSchemaDigestsArePinnedForEveryRecordContract(t *testing.T) {
	expected := map[string]string{
		"review/v1":            "sha256:8b460c4d9ea3a6ca6c7d1b8fb1e8dce448df8a2745f3d81a52992cec8e760220",
		"diagnosis/v1":         "sha256:47301b1acc54725bd94804e6a20ca284b85568e4336d5592c64a89bcd2f58c47",
		"validation/v1":        "sha256:4d03fc7af7796a7add4d72d08a286c64ecfa7e9486fb06f136600aacc3f1b0e7",
		"repository-change/v1": "sha256:afdb59e4eb682a09f86fb92165c57d3df215487be5a55e316944eba8bdc1f013",
		"measurements/v1":      "sha256:d2e0b89126ce534c957a8e93166391f517e126aec5aa961f66a4c6c178bc57a0",
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
