package contracts_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestEpistemicStatusVocabularyIsExactlyFourValues(t *testing.T) {
	want := []string{"platform", "derived", "constrained", "asserted"}
	statuses := contracts.EpistemicStatuses()
	if len(statuses) != len(want) {
		t.Fatalf("EpistemicStatuses() = %v, want exactly %v", statuses, want)
	}
	for index, status := range statuses {
		if status.String() != want[index] {
			t.Fatalf("EpistemicStatuses()[%d] = %q, want %q", index, status, want[index])
		}
		if err := status.Validate(); err != nil {
			t.Fatalf("%q.Validate() = %v, want the vocabulary value accepted", status, err)
		}
	}
}

func TestEpistemicStatusValidationRejectsAnythingOutsideTheVocabulary(t *testing.T) {
	for _, raw := range []string{"", "Platform", "PLATFORM", "platform ", "verified", "inferred", "claimed"} {
		if err := contracts.EpistemicStatus(raw).Validate(); err == nil {
			t.Fatalf("EpistemicStatus(%q).Validate() accepted a value outside the vocabulary", raw)
		}
		if _, err := contracts.ParseEpistemicStatus(raw); err == nil {
			t.Fatalf("ParseEpistemicStatus(%q) accepted a value outside the vocabulary", raw)
		}
	}
	status, err := contracts.ParseEpistemicStatus("derived")
	if err != nil {
		t.Fatalf("ParseEpistemicStatus(\"derived\") = %v", err)
	}
	if status != contracts.EpistemicDerived {
		t.Fatalf("ParseEpistemicStatus(\"derived\") = %q, want %q", status, contracts.EpistemicDerived)
	}
}

func TestEpistemicStatusSerializedFormIsTheLowercaseToken(t *testing.T) {
	encoded, err := json.Marshal(contracts.EpistemicConstrained)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `"constrained"` {
		t.Fatalf("marshal(EpistemicConstrained) = %s, want \"constrained\"", encoded)
	}

	var status contracts.EpistemicStatus
	if err := json.Unmarshal([]byte(`"asserted"`), &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if status != contracts.EpistemicAsserted {
		t.Fatalf("unmarshal(\"asserted\") = %q, want %q", status, contracts.EpistemicAsserted)
	}

	if err := json.Unmarshal([]byte(`"verified"`), &status); err == nil {
		t.Fatal("unmarshal accepted a token outside the vocabulary; the serialized form must validate")
	}
	if _, err := json.Marshal(contracts.EpistemicStatus("verified")); err == nil {
		t.Fatal("marshal accepted a value outside the vocabulary")
	}
}

// A consumer reads epistemic status for a type by asking for the type, not by
// knowing anything about it. These are the whole public surface.
func TestConsumersReadEpistemicStatusWithoutTypeKnowledge(t *testing.T) {
	declarations := contracts.EpistemicDeclarations()
	if len(declarations) == 0 {
		t.Fatal("EpistemicDeclarations() is empty")
	}
	for _, declaration := range declarations {
		if err := declaration.Validate(); err != nil {
			t.Fatalf("declaration %q revision %d: %v", declaration.Type, declaration.Revision, err)
		}
		byType, found := contracts.EpistemicDeclarationFor(declaration.Type, declaration.Revision)
		if !found {
			t.Fatalf("EpistemicDeclarationFor(%q, %d) not found", declaration.Type, declaration.Revision)
		}
		if len(byType.Fields) != len(declaration.Fields) {
			t.Fatalf("EpistemicDeclarationFor(%q, %d) has %d fields, want %d",
				declaration.Type, declaration.Revision, len(byType.Fields), len(declaration.Fields))
		}
		for path, status := range declaration.Fields {
			got, ok := contracts.EpistemicStatusFor(declaration.Type, declaration.Revision, path)
			if !ok || got != status {
				t.Fatalf("EpistemicStatusFor(%q, %d, %q) = %q/%t, want %q",
					declaration.Type, declaration.Revision, path, got, ok, status)
			}
		}
	}
	if !sort.SliceIsSorted(declarations, func(i, j int) bool {
		if declarations[i].Type == declarations[j].Type {
			return declarations[i].Revision < declarations[j].Revision
		}
		return declarations[i].Type < declarations[j].Type
	}) {
		t.Fatal("EpistemicDeclarations() is not sorted by (type, revision)")
	}
}

// The table is package-level state. Handing out the live map would let any
// consumer relabel a field for the whole process.
func TestEpistemicDeclarationsCannotBeEditedThroughTheReturnedMap(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	declaration, found := contracts.EpistemicDeclarationFor(ref, 1)
	if !found {
		t.Fatal("EpistemicDeclarationFor(review/v1, 1) not found")
	}
	declaration.Fields["body/summary"] = contracts.EpistemicPlatform
	delete(declaration.Fields, "body/conclusion")

	again, _ := contracts.EpistemicDeclarationFor(ref, 1)
	if again.Fields["body/summary"] != contracts.EpistemicAsserted {
		t.Fatalf("body/summary is now %q; the declaration table was edited through a returned map", again.Fields["body/summary"])
	}
	if _, found := again.Fields["body/conclusion"]; !found {
		t.Fatal("body/conclusion was deleted from the declaration table through a returned map")
	}
}

func TestCurrentEpistemicDeclarationTracksTheCurrentSchemaRevision(t *testing.T) {
	for _, raw := range []string{
		"review/v1", "diagnosis/v1", "validation/v1",
		"repository-change/v1", "selection/v1", "measurements/v1",
	} {
		ref := mustTypeRef(t, raw)
		declaration, found := contracts.CurrentEpistemicDeclarationFor(ref)
		if !found {
			t.Fatalf("CurrentEpistemicDeclarationFor(%q) not found", raw)
		}
		if declaration.Type != ref {
			t.Fatalf("CurrentEpistemicDeclarationFor(%q).Type = %q", raw, declaration.Type)
		}
		accepted, ok := contracts.AcceptedSchemaDigests(ref)
		if !ok {
			t.Fatalf("AcceptedSchemaDigests(%q) not found", raw)
		}
		if declaration.Revision != len(accepted) {
			t.Fatalf("CurrentEpistemicDeclarationFor(%q).Revision = %d, want the current schema revision %d",
				raw, declaration.Revision, len(accepted))
		}
	}
	if _, found := contracts.CurrentEpistemicDeclarationFor(snapshot.TypeRef("repository/v1")); found {
		t.Fatal("CurrentEpistemicDeclarationFor() answered for a type that is not a record type")
	}
}

// The only contract-identity fact a stored record carries is its schema digest.
// Every record type's whole accepted history must resolve from that digest alone,
// through the public surface, with no revision arithmetic at the call site.
func TestEveryAcceptedSchemaDigestResolvesToItsDeclaration(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	for _, ref := range registry.Types() {
		if !contracts.IsRecordType(ref) {
			continue
		}
		accepted, found := contracts.AcceptedSchemaDigests(ref)
		if !found {
			t.Fatalf("AcceptedSchemaDigests(%q) not found", ref)
		}
		for position, digest := range accepted {
			revision, found := contracts.SchemaRevisionFor(ref, digest)
			if !found {
				t.Fatalf("SchemaRevisionFor(%q, %q) not found", ref, digest)
			}
			if want := len(accepted) - position; revision != want {
				t.Fatalf("SchemaRevisionFor(%q, position %d) = %d, want %d: the history is newest-first",
					ref, position, revision, want)
			}
			declaration, found := contracts.EpistemicDeclarationForSchemaDigest(ref, digest)
			if !found {
				t.Fatalf("EpistemicDeclarationForSchemaDigest(%q, revision %d) not found", ref, revision)
			}
			if declaration.Revision != revision || declaration.Type != ref {
				t.Fatalf("EpistemicDeclarationForSchemaDigest(%q, revision %d) = %q revision %d",
					ref, revision, declaration.Type, declaration.Revision)
			}
		}
		current, found := contracts.SchemaDigestFor(ref)
		if !found {
			t.Fatalf("SchemaDigestFor(%q) not found", ref)
		}
		currentRevision, found := contracts.CurrentSchemaRevisionFor(ref)
		if !found || currentRevision != len(accepted) {
			t.Fatalf("CurrentSchemaRevisionFor(%q) = %d/%t, want %d", ref, currentRevision, found, len(accepted))
		}
		if digest, found := contracts.SchemaDigestForRevision(ref, currentRevision); !found || digest != current {
			t.Fatalf("SchemaDigestForRevision(%q, %d) = %q/%t, want the current digest %q",
				ref, currentRevision, digest, found, current)
		}
	}
}

// A stored address names one element by its stable id; the declaration names
// every element with '*'. A consumer holding an address must be able to resolve
// it without re-implementing the grammar.
func TestEpistemicStatusResolvesStoredAddresses(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	status, found := contracts.EpistemicStatusFor(ref, 1, "body/findings/f-001/description")
	if !found {
		t.Fatal("EpistemicStatusFor() could not resolve a stored address against a wildcard declaration")
	}
	if status != contracts.EpistemicAsserted {
		t.Fatalf("body/findings/f-001/description = %q, want %q: an agent-written description is a producer claim",
			status, contracts.EpistemicAsserted)
	}
	if _, found := contracts.EpistemicStatusFor(ref, 1, "body/findings/f-001/invented"); found {
		t.Fatal("EpistemicStatusFor() answered for a field that does not exist")
	}
	if _, found := contracts.EpistemicStatusFor(ref, 99, "body/summary"); found {
		t.Fatal("EpistemicStatusFor() answered for a revision that does not exist")
	}
}

// The grammar admits dot inside a segment, so a dotted path is syntactically
// valid — it just names nothing. This is the guard against a second spelling
// arriving by accident: it can be written, and it resolves to no field.
func TestADottedPathIsOneSegmentAndResolvesToNothing(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	if err := contracts.ValidateFieldDeclarationPath("body.summary"); err != nil {
		t.Fatalf("ValidateFieldDeclarationPath(\"body.summary\") = %v, want it accepted as one segment", err)
	}
	if _, found := contracts.EpistemicStatusFor(ref, 1, "body.summary"); found {
		t.Fatal("a dotted path resolved to a field; the frozen grammar has exactly one spelling")
	}
	if _, found := contracts.EpistemicStatusFor(ref, 1, "body/summary"); !found {
		t.Fatal("the frozen spelling body/summary does not resolve")
	}
}

// The four honest assignments the task guidance names, one each, read through
// the public surface.
func TestEpistemicStatusOfRepresentativeFieldsIsHonest(t *testing.T) {
	tests := []struct {
		rawType string
		path    string
		want    contracts.EpistemicStatus
		why     string
	}{
		{
			rawType: "review/v1", path: "schema", want: contracts.EpistemicPlatform,
			why: "the platform hands the producer the exact schema digest to transcribe " +
				"(agent/runner/runner.go:307-313) and the SEAL-TIME gate then admits that digest and no other " +
				"(record.go currentSchemaDigestOnly). Read-time set membership over the accepted history is a " +
				"superset the seal already certified, not a producer choice",
		},
		{
			rawType: "validation/v1", path: "body/conclusion", want: contracts.EpistemicDerived,
			why: "the validator recomputes it from the checks and rejects any other value (validation.go:54-57)",
		},
		{
			rawType: "review/v1", path: "body/findings/*/severity", want: contracts.EpistemicConstrained,
			why: "a closed vocabulary the validator enforces (review.go:67-79)",
		},
		{
			rawType: "diagnosis/v1", path: "body/hypotheses/*/statement", want: contracts.EpistemicAsserted,
			why: "agent prose nothing verifies (diagnosis.go:102-104)",
		},
		{
			rawType: "repository-change/v1", path: "body/result_tree", want: contracts.EpistemicDerived,
			why: "the validator recomputes the tree from the payload (repository_change.go:244-246)",
		},
	}
	for _, test := range tests {
		t.Run(test.rawType+" "+test.path, func(t *testing.T) {
			declaration, found := contracts.CurrentEpistemicDeclarationFor(mustTypeRef(t, test.rawType))
			if !found {
				t.Fatalf("CurrentEpistemicDeclarationFor(%q) not found", test.rawType)
			}
			status, found := declaration.StatusFor(test.path)
			if !found {
				t.Fatalf("StatusFor(%q) not declared", test.path)
			}
			if status != test.want {
				t.Fatalf("%s %s = %q, want %q because %s", test.rawType, test.path, status, test.want, test.why)
			}
		})
	}
}

// Placement is the entire point: nothing about epistemic status may be reachable
// from a sealed byte, so no record type may carry an epistemic field and no
// frozen schema descriptor may mention one. If either became false, correcting a
// mislabelled status would force a descriptor bump.
func TestEpistemicStatusLivesOutsideEverySealedByte(t *testing.T) {
	for _, declaration := range contracts.EpistemicDeclarations() {
		for path := range declaration.Fields {
			for _, segment := range strings.Split(path, "/") {
				if strings.Contains(strings.ToLower(segment), "epistemic") {
					t.Fatalf("%q declares a field %q: epistemic status must never be a record field",
						declaration.Type, path)
				}
			}
		}
	}
	encoded, err := json.Marshal(contracts.EpistemicDeclarations())
	if err != nil {
		t.Fatalf("marshal declarations: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatal("the epistemic declaration table has no serialized form")
	}
}
