package contracts

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// A consumer holding a stored record has one fact about its contract identity:
// the schema digest in the envelope. The epistemic table is keyed by revision, so
// something has to map one to the other, and the mapping is the whole reason the
// superseded case is reachable at all.
//
// This test lives inside the package for the same reason the schema-history and
// gate tests do: a type with more than one revision is required, and bumping a
// real descriptor is what this machinery exists to make safe rather than what to
// do in a test. Three revisions rather than two, deliberately — with two, the
// oldest revision's position in the newest-first history happens to equal its own
// revision number, so a two-revision fixture cannot tell a correct mapping apart
// from the inverted one.

const syntheticRevision3 = `{"contract":"synthetic-record/v1","envelope":"record/v1","revision":3}`

func syntheticThreeRevisionHistories() map[snapshot.TypeRef]recordSchemaHistory {
	return map[snapshot.TypeRef]recordSchemaHistory{
		syntheticRecordType: {
			current: recordSchemaRevision{revision: 3, descriptor: syntheticRevision3},
			superseded: []recordSchemaRevision{
				{revision: 1, descriptor: syntheticRevision1},
				{revision: 2, descriptor: syntheticRevision2},
			},
		},
	}
}

func withSyntheticEpistemicDeclarations(t *testing.T, table map[epistemicKey]map[string]EpistemicStatus) {
	t.Helper()
	previous := epistemicFieldStatuses
	t.Cleanup(func() { epistemicFieldStatuses = previous })
	epistemicFieldStatuses = table
}

// syntheticEpistemicDeclarations gives each synthetic revision a DIFFERENT status
// for the same field, so resolving the wrong revision is visible rather than
// accidentally correct.
func syntheticEpistemicDeclarations() map[epistemicKey]map[string]EpistemicStatus {
	statuses := map[int]EpistemicStatus{
		1: EpistemicAsserted,
		2: EpistemicConstrained,
		3: EpistemicDerived,
	}
	table := make(map[epistemicKey]map[string]EpistemicStatus, len(statuses))
	for revision, status := range statuses {
		table[epistemicKey{ref: syntheticRecordType, revision: revision}] = map[string]EpistemicStatus{
			"body/conclusion": status,
		}
	}
	return table
}

// TestAStoredSchemaDigestResolvesToItsOwnEpistemicRevision is the requirement:
// given the only contract-identity fact a stored record carries, a consumer must
// reach that record's own revision's declaration — including for a SUPERSEDED
// revision, which is the only reason the table is keyed by revision.
func TestAStoredSchemaDigestResolvesToItsOwnEpistemicRevision(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticThreeRevisionHistories())
	withSyntheticEpistemicDeclarations(t, syntheticEpistemicDeclarations())

	accepted, found := AcceptedSchemaDigests(syntheticRecordType)
	if !found || len(accepted) != 3 {
		t.Fatalf("AcceptedSchemaDigests(%q) = %v, %v; want three revisions", syntheticRecordType, accepted, found)
	}

	wantStatus := map[int]EpistemicStatus{
		1: EpistemicAsserted,
		2: EpistemicConstrained,
		3: EpistemicDerived,
	}
	for _, revision := range []int{1, 2, 3} {
		digest := schemaDescriptorDigest(map[int]string{
			1: syntheticRevision1, 2: syntheticRevision2, 3: syntheticRevision3,
		}[revision])

		mapped, found := SchemaRevisionFor(syntheticRecordType, digest)
		if !found {
			t.Fatalf("SchemaRevisionFor(%q, revision %d digest) not found", syntheticRecordType, revision)
		}
		if mapped != revision {
			t.Fatalf("SchemaRevisionFor(revision %d digest) = %d, want %d", revision, mapped, revision)
		}
		roundTripped, found := SchemaDigestForRevision(syntheticRecordType, revision)
		if !found || roundTripped != digest {
			t.Fatalf("SchemaDigestForRevision(%q, %d) = %q/%t, want %q", syntheticRecordType, revision, roundTripped, found, digest)
		}

		// The rule that used to be documented — the digest's POSITION in the
		// newest-first accepted history — is pinned here as wrong so nobody
		// re-derives it. With three revisions no position equals its own revision.
		position := -1
		for index, candidate := range accepted {
			if candidate == digest {
				position = index
			}
		}
		if position < 0 {
			t.Fatalf("revision %d digest %q is not in the accepted history", revision, digest)
		}
		if position == revision {
			t.Fatalf("position %d equals revision %d, so this fixture cannot tell the correct mapping "+
				"apart from the inverted one", position, revision)
		}

		declaration, found := EpistemicDeclarationForSchemaDigest(syntheticRecordType, digest)
		if !found {
			t.Fatalf("revision %d: no declaration reachable from its stored digest", revision)
		}
		if declaration.Revision != revision {
			t.Fatalf("revision %d digest resolved to declaration revision %d", revision, declaration.Revision)
		}
		status, found := declaration.StatusFor("body/conclusion")
		if !found || status != wantStatus[revision] {
			t.Fatalf("revision %d body/conclusion = %q/%t, want %q", revision, status, found, wantStatus[revision])
		}
		byDigest, found := EpistemicStatusForSchemaDigest(syntheticRecordType, digest, "body/conclusion")
		if !found || byDigest != wantStatus[revision] {
			t.Fatalf("EpistemicStatusForSchemaDigest(revision %d) = %q/%t, want %q",
				revision, byDigest, found, wantStatus[revision])
		}
	}

	// The current revision is the newest, and it is what a record authored now
	// resolves to.
	current, found := CurrentSchemaRevisionFor(syntheticRecordType)
	if !found || current != 3 {
		t.Fatalf("CurrentSchemaRevisionFor(%q) = %d/%t, want 3", syntheticRecordType, current, found)
	}
	currentDeclaration, found := CurrentEpistemicDeclarationFor(syntheticRecordType)
	if !found || currentDeclaration.Revision != 3 {
		t.Fatalf("CurrentEpistemicDeclarationFor(%q).Revision = %d/%t, want 3",
			syntheticRecordType, currentDeclaration.Revision, found)
	}
}

// TestTheSchemaFieldIsPlatformBecauseOfTheSealGateNotTheReadGate pins the one
// envelope field whose two gates disagree.
//
// With several accepted revisions, read-time revalidation admits every one of
// them — a set-membership check, which is this vocabulary's own definition of
// EpistemicConstrained. Seal-time admission admits exactly one. The declared
// status is EpistemicPlatform, so it must be the seal gate it describes, and this
// test fails if either the gate or the status moves: if seal-time ever admits more
// than one digest, "the producer contributes no information" stops being true, and
// if the status is ever weakened to constrained on the strength of the read gate,
// it stops describing when the value was actually certified.
func TestTheSchemaFieldIsPlatformBecauseOfTheSealGateNotTheReadGate(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticThreeRevisionHistories())

	accepted, found := AcceptedSchemaDigests(syntheticRecordType)
	if !found || len(accepted) != 3 {
		t.Fatalf("AcceptedSchemaDigests(%q) = %v, %v; want three revisions", syntheticRecordType, accepted, found)
	}
	declarations := syntheticDeclarations(t)

	admittedForSeal, admittedForRead := 0, 0
	for _, digest := range accepted {
		record := syntheticRecordPinning(t, digest)
		if err := record.AdmitForSeal(syntheticRecordType, declarations); err == nil {
			admittedForSeal++
		}
		if err := record.RevalidateSealed(syntheticRecordType); err == nil {
			admittedForRead++
		}
	}
	if admittedForSeal != 1 {
		t.Fatalf("seal-time admission accepted %d of %d accepted schema digests, want exactly 1; "+
			"%q is declared platform, which requires that a producer have no choice",
			admittedForSeal, len(accepted), "schema")
	}
	if admittedForRead != len(accepted) {
		t.Fatalf("read-time revalidation accepted %d of %d accepted schema digests, want all of them; "+
			"a validator that cannot re-validate a stored record is a defect", admittedForRead, len(accepted))
	}

	// The two gates genuinely disagree here, which is why the status has to name
	// which one it describes rather than citing whichever predicate is nearest.
	if admittedForSeal == admittedForRead {
		t.Fatal("the fixture has only one revision, so it cannot tell the two gates apart")
	}
}

// TestTheSchemaFieldIsDeclaredPlatformForEveryRecordType is the other half of the
// claim: given that the seal gate admits exactly one digest, every record type
// must say so. Separate from the gate test above because that one substitutes a
// synthetic schema index, which hides the real types.
func TestTheSchemaFieldIsDeclaredPlatformForEveryRecordType(t *testing.T) {
	for _, raw := range []string{
		"review/v1", "diagnosis/v1", "validation/v1",
		"repository-change/v1", "selection/v1", "measurements/v1",
	} {
		ref := snapshot.TypeRef(raw)
		declaration, found := CurrentEpistemicDeclarationFor(ref)
		if !found {
			t.Fatalf("CurrentEpistemicDeclarationFor(%q) not found", raw)
		}
		status, found := declaration.StatusFor("schema")
		if !found {
			t.Fatalf("%q declares no status for schema", raw)
		}
		if status != EpistemicPlatform {
			t.Fatalf("%q schema = %q, want %q: seal-time admission requires exactly the digest the "+
				"platform handed the agent, so the producer contributes nothing. Read-time set membership "+
				"is a superset the seal already certified, not a producer choice", raw, status, EpistemicPlatform)
		}
	}
}

// A digest that is in no revision, and a type that is not a record type, resolve
// to nothing rather than to revision 0 or to the current revision.
func TestSchemaRevisionForRejectsUnknownDigestsAndTypes(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticThreeRevisionHistories())

	unknown := schemaDescriptorDigest(`{"contract":"synthetic-record/v1","revision":99}`)
	if revision, found := SchemaRevisionFor(syntheticRecordType, unknown); found || revision != 0 {
		t.Fatalf("SchemaRevisionFor(unknown digest) = %d, %t; want 0, false", revision, found)
	}
	current := schemaDescriptorDigest(syntheticRevision3)
	if revision, found := SchemaRevisionFor(snapshot.TypeRef("opaque/v1"), current); found || revision != 0 {
		t.Fatalf("SchemaRevisionFor(non-record type) = %d, %t; want 0, false", revision, found)
	}
	for _, revision := range []int{-1, 0, 4} {
		if digest, found := SchemaDigestForRevision(syntheticRecordType, revision); found {
			t.Fatalf("SchemaDigestForRevision(%q, %d) = %q, true; want not found",
				syntheticRecordType, revision, digest)
		}
	}
	if _, found := EpistemicDeclarationForSchemaDigest(syntheticRecordType, unknown); found {
		t.Fatal("EpistemicDeclarationForSchemaDigest() answered for a digest in no revision")
	}
}
