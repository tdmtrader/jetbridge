package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The two halves of the coordinated descriptor bump, asserted against the REAL
// six record types rather than against a synthetic history.
//
// gates_internal_test.go already pins the two gates' behaviour, but it does so on
// a synthetic two-revision type substituted for the frozen table, which is what
// let it exist before any real descriptor had ever been bumped. That fixture
// cannot answer the question these tests ask, because the substitution hides the
// real histories entirely: it proves the mechanism works, not that the six types
// went through it.
//
// So these are deliberately per-type and deliberately external:
//
//   - Every one of the six must still re-validate a record sealed under its
//     revision-1 digest. That is the whole reason the accepted-digest history was
//     built first — a validator that cannot re-validate a stored record is a
//     defect, and a bump that broke this would report as ErrCorruptSnapshot on
//     some later read rather than here.
//   - Every one of the six must refuse a candidate pinning revision 1 at seal
//     time. A producer that could still author the superseded identity would be
//     choosing which contract its own output advertises.
//
// Before the bump the second half fails for all six types, because revision 1 IS
// each type's current revision and seal-time admission is therefore obliged to
// accept it. That failure is the point: it is what proves these tests are reading
// the histories and not restating a tautology.

// bumpedRecordTypes is every type the coordinated bump covers. It is written out
// rather than derived from the registry so that a type silently dropping out of
// the histories is a failure here too; TestEveryRecordTypeIsCoveredByTheBumpTests
// closes the other direction.
var bumpedRecordTypes = []string{
	"review/v1",
	"diagnosis/v1",
	"consultation/v1",
	"validation/v1",
	"repository-change/v1",
	"measurements/v1",
	"pull-request/v1",
	"pull-request-response/v1",
	"publish-impact/v1",
}

// revisionOneRecordFor is a stored record of ref as it would have been sealed
// under revision 1: the envelope the platform stamped then, carrying the digest
// that was current then.
//
// The body is opaque on purpose. Both gates under test are ENVELOPE gates, and
// giving each of the six a plausible body would make a failure ambiguous between
// the envelope rule these tests are about and a per-type semantic rule they are
// not.
func revisionOneRecordFor(t *testing.T, raw string) (contracts.Record[json.RawMessage], snapshot.Digest) {
	t.Helper()
	ref := mustTypeRef(t, raw)
	revisionOne, found := contracts.SchemaDigestForRevision(ref, 1)
	if !found {
		t.Fatalf("SchemaDigestForRevision(%q, 1) not found; every record type has a revision 1", raw)
	}
	subjectType := mustTypeRef(t, "repository/v1")
	if raw == "pull-request-response/v1" {
		subjectType = mustTypeRef(t, "pull-request/v1")
	}
	record, err := contracts.NewRecord(
		ref,
		[]contracts.Subject{{
			ID:     "primary",
			Role:   contracts.SubjectRolePrimary,
			Input:  "subject-input",
			Type:   subjectType,
			Digest: recordDigest('a'),
		}},
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("NewRecord(%q): %v", raw, err)
	}
	// Overwriting the digest is what makes this a revision-1 record: NewRecord
	// pins whatever is current, which after the bump is revision 2.
	record.Schema = revisionOne
	return record, revisionOne
}

func exposedInputFor(t *testing.T, record contracts.Record[json.RawMessage]) snapshot.ValidationContext {
	t.Helper()
	subject := record.Subjects[0]
	return validationContextFor(t, map[string]snapshot.SnapshotRef{
		subject.Input: {ID: 7, Type: subject.Type, Digest: subject.Digest},
	})
}

// TestRevalidateSealedStillAcceptsARevisionOneRecordForEveryType is the corpus
// half. It must hold both before and after the bump, and it is the one that would
// have made a bump that dropped a revision visible immediately.
func TestRevalidateSealedStillAcceptsARevisionOneRecordForEveryType(t *testing.T) {
	for _, raw := range bumpedRecordTypes {
		t.Run(raw, func(t *testing.T) {
			ref := mustTypeRef(t, raw)
			record, revisionOne := revisionOneRecordFor(t, raw)

			if !contracts.IsAcceptedSchemaDigest(ref, revisionOne) {
				t.Fatalf("%q revision 1 digest %q is no longer accepted; a digest may only ever be appended",
					raw, revisionOne)
			}
			if err := record.RevalidateSealed(ref); err != nil {
				t.Fatalf("RevalidateSealed(%q) rejected a record sealed under revision 1 (%q): %v; "+
					"a validator that cannot re-validate a stored record is a defect", raw, revisionOne, err)
			}
			var decoded contracts.Record[json.RawMessage]
			if err := contracts.DecodeSealedRecord(marshalRecord(t, record), ref, &decoded); err != nil {
				t.Fatalf("DecodeSealedRecord(%q) rejected a stored revision-1 record: %v", raw, err)
			}
			if decoded.Schema != revisionOne {
				t.Fatalf("decoded %q schema = %q, want %q preserved", raw, decoded.Schema, revisionOne)
			}
		})
	}
}

// TestAdmitForSealRejectsTheRevisionOneDigestForEveryType is the authority half,
// and the half that is RED until the type is bumped.
func TestAdmitForSealRejectsTheRevisionOneDigestForEveryType(t *testing.T) {
	for _, raw := range bumpedRecordTypes {
		t.Run(raw, func(t *testing.T) {
			ref := mustTypeRef(t, raw)
			record, revisionOne := revisionOneRecordFor(t, raw)

			current, found := contracts.SchemaDigestFor(ref)
			if !found {
				t.Fatalf("SchemaDigestFor(%q) not found", raw)
			}
			if current == revisionOne {
				t.Fatalf("%q is still at revision 1, so seal-time admission cannot reject it; "+
					"the coordinated descriptor bump has not landed for this type", raw)
			}

			err := record.AdmitForSeal(ref, exposedInputFor(t, record))
			if err == nil {
				t.Fatalf("AdmitForSeal(%q) admitted a candidate pinning the superseded revision-1 digest %q; "+
					"a producer must not choose which contract identity its own output advertises",
					raw, revisionOne)
			}
			if !strings.Contains(err.Error(), "current") {
				t.Fatalf("AdmitForSeal(%q) error = %v, want it to name the current schema digest", raw, err)
			}
			if err := contracts.DecodeRecordForSeal(marshalRecord(t, record), ref, &contracts.Record[json.RawMessage]{}); err == nil {
				t.Fatalf("DecodeRecordForSeal(%q) admitted bytes pinning the revision-1 digest", raw)
			}

			// The same record with the current digest is admitted, so the rejection
			// above is about the digest and not about the envelope or the subject.
			admissible := record
			admissible.Schema = current
			if err := admissible.AdmitForSeal(ref, exposedInputFor(t, record)); err != nil {
				t.Fatalf("AdmitForSeal(%q) rejected a candidate pinning the CURRENT digest %q: %v",
					raw, current, err)
			}
		})
	}
}

// TestAStoredDigestResolvesToItsOwnRevisionForEveryRealType asserts against the
// real histories that a stored digest maps to the revision it identifies.
//
// It could not be asserted before the bump: with one revision per type, every
// digest resolves to revision 1 whether the mapping is right or inverted. Two
// revisions is enough to tell the two apart in one direction, which is the
// direction that now carries stored data.
func TestAStoredDigestResolvesToItsOwnRevisionForEveryRealType(t *testing.T) {
	for _, raw := range bumpedRecordTypes {
		t.Run(raw, func(t *testing.T) {
			ref := mustTypeRef(t, raw)
			accepted, found := contracts.AcceptedSchemaDigests(ref)
			wantRevisions := 2
			if raw == "validation/v1" || raw == "pull-request/v1" || raw == "pull-request-response/v1" || raw == "publish-impact/v1" {
				wantRevisions = 3
			}
			if raw == "pull-request-response/v1" {
				wantRevisions = 4
			}
			if !found || len(accepted) != wantRevisions {
				t.Fatalf("AcceptedSchemaDigests(%q) = %v, want exactly %d revisions", raw, accepted, wantRevisions)
			}
			for revision := 1; revision <= wantRevisions; revision++ {
				digest, found := contracts.SchemaDigestForRevision(ref, revision)
				if !found {
					t.Fatalf("SchemaDigestForRevision(%q, %d) not found", raw, revision)
				}
				mapped, found := contracts.SchemaRevisionFor(ref, digest)
				if !found || mapped != revision {
					t.Fatalf("SchemaRevisionFor(%q, revision %d digest) = %d/%t, want %d",
						raw, revision, mapped, found, revision)
				}
			}
			// Newest-first, so revision 2 is at position 0 and revision 1 at 1 —
			// which is why position must never be read as revision.
			if current, _ := contracts.SchemaDigestFor(ref); current != accepted[0] {
				t.Fatalf("SchemaDigestFor(%q) = %q, want the newest accepted digest %q", raw, current, accepted[0])
			}
		})
	}
}

// TestEveryRecordTypeIsCoveredByTheBumpTests stops a new record type from being
// added without the two assertions above applying to it.
func TestEveryRecordTypeIsCoveredByTheBumpTests(t *testing.T) {
	covered := make(map[string]struct{}, len(bumpedRecordTypes))
	for _, raw := range bumpedRecordTypes {
		covered[raw] = struct{}{}
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	for _, ref := range registry.Types() {
		if !contracts.IsRecordType(ref) {
			continue
		}
		if _, found := covered[ref.String()]; !found {
			t.Fatalf("record type %q is not covered by the descriptor-bump tests", ref)
		}
	}
}
