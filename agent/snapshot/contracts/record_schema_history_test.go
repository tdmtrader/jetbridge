package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// frozenAcceptedSchemaDigests pins the COMPLETE accepted schema digest history
// of every record type, newest first.
//
// This is deliberately a separate pin from the current-digest pin in
// record_schema_test.go: that one freezes what newly authored records carry,
// this one freezes what stored records are still allowed to carry. A digest may
// only ever be APPENDED to the end of one of these lists. Removing or
// reordering an entry retroactively invalidates every record already sealed
// under it, and the failure would surface as ErrCorruptSnapshot on read rather
// than at seal time, so this test exists to make that change loud.
var frozenAcceptedSchemaDigests = map[string][]string{
	"review/v1": {
		"sha256:01d9f0644151274e8577875373f110b11f0ec34ff29ba12b143379744416fdb5",
	},
	"diagnosis/v1": {
		"sha256:7c7060b4d663d4546836898640f71bb576749d6aee7fee1df2b5616eea21064e",
	},
	"validation/v1": {
		"sha256:b5a08c5bf14754800b4bd02eeb7fae8bf3ed1aa08e2f4905d1cfda15a96c0363",
	},
	"repository-change/v1": {
		"sha256:2dae971bc191c13eb9fc42f29992268d15c15765548a85d70bd808844af6e308",
	},
	"selection/v1": {
		"sha256:009409ee7157092f910c971b2b14be45cc7db84e8911687f75bc63a22446d7dc",
	},
	"measurements/v1": {
		"sha256:fea8ee17190c3dcf6c2d24065e2eea51acc9672c7cc091137fd3d6085e67a361",
	},
}

func TestAcceptedSchemaDigestHistoriesAreFrozenAndAppendOnly(t *testing.T) {
	for raw, want := range frozenAcceptedSchemaDigests {
		ref := mustTypeRef(t, raw)
		accepted, found := contracts.AcceptedSchemaDigests(ref)
		if !found {
			t.Fatalf("AcceptedSchemaDigests(%q) not found", raw)
		}
		if len(accepted) != len(want) {
			t.Fatalf("AcceptedSchemaDigests(%q) has %d digests %v, want the frozen %d %v; a schema digest may only be appended, never removed",
				raw, len(accepted), accepted, len(want), want)
		}
		for index, digest := range accepted {
			if err := digest.Validate(); err != nil {
				t.Fatalf("AcceptedSchemaDigests(%q)[%d] = %q: %v", raw, index, digest, err)
			}
			if digest.String() != want[index] {
				t.Fatalf("AcceptedSchemaDigests(%q)[%d] = %q, want frozen %q", raw, index, digest, want[index])
			}
		}
	}
}

func TestEveryRecordTypeHasAFrozenAcceptedDigestHistory(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	for _, ref := range registry.Types() {
		if !contracts.IsRecordType(ref) {
			continue
		}
		if _, found := frozenAcceptedSchemaDigests[ref.String()]; !found {
			t.Fatalf("record type %q has no frozen accepted digest history in frozenAcceptedSchemaDigests", ref)
		}
	}
}

func TestAcceptedSchemaDigestsLeadWithTheDigestWritesPin(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	for _, ref := range registry.Types() {
		if !contracts.IsRecordType(ref) {
			continue
		}
		accepted, found := contracts.AcceptedSchemaDigests(ref)
		if !found || len(accepted) == 0 {
			t.Fatalf("AcceptedSchemaDigests(%q) = %v, %v; want a non-empty history", ref, accepted, found)
		}
		current, found := contracts.SchemaDigestFor(ref)
		if !found {
			t.Fatalf("SchemaDigestFor(%q) not found", ref)
		}
		if accepted[0] != current {
			t.Fatalf("AcceptedSchemaDigests(%q)[0] = %q, want the current digest %q; writes pin index 0",
				ref, accepted[0], current)
		}
		seen := make(map[snapshot.Digest]struct{}, len(accepted))
		for _, digest := range accepted {
			if _, duplicate := seen[digest]; duplicate {
				t.Fatalf("AcceptedSchemaDigests(%q) repeats digest %q", ref, digest)
			}
			seen[digest] = struct{}{}
			if !contracts.IsAcceptedSchemaDigest(ref, digest) {
				t.Fatalf("IsAcceptedSchemaDigest(%q, %q) = false, want true", ref, digest)
			}
		}
	}
}

func TestNewRecordPinsTheCurrentSchemaDigest(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	input := snapshot.SnapshotRef{
		ID: 41, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a'),
	}
	record, err := contracts.NewRecord(
		ref,
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", input,
		)},
		json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	accepted, found := contracts.AcceptedSchemaDigests(ref)
	if !found {
		t.Fatalf("AcceptedSchemaDigests(%q) not found", ref)
	}
	if record.Schema != accepted[0] {
		t.Fatalf("NewRecord(%q).Schema = %q, want the newest accepted digest %q", ref, record.Schema, accepted[0])
	}
}

func TestAcceptedSchemaDigestsRejectsNonRecordTypesAndUnknownDigests(t *testing.T) {
	opaque := mustTypeRef(t, "opaque/v1")
	if accepted, found := contracts.AcceptedSchemaDigests(opaque); found || accepted != nil {
		t.Fatalf("AcceptedSchemaDigests(%q) = %v, %v; want nil, false", opaque, accepted, found)
	}
	if contracts.IsAcceptedSchemaDigest(opaque, recordDigest('a')) {
		t.Fatalf("IsAcceptedSchemaDigest(%q, ...) = true for a non-record type", opaque)
	}
	review := mustTypeRef(t, "review/v1")
	if contracts.IsAcceptedSchemaDigest(review, recordDigest('b')) {
		t.Fatalf("IsAcceptedSchemaDigest(%q, ...) = true for a digest that is in no revision of the history", review)
	}
}

func TestAcceptedSchemaDigestsCannotBeRemovedThroughTheReturnedSlice(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	first, found := contracts.AcceptedSchemaDigests(ref)
	if !found {
		t.Fatalf("AcceptedSchemaDigests(%q) not found", ref)
	}
	original := first[0]
	first[0] = recordDigest('f')
	second, found := contracts.AcceptedSchemaDigests(ref)
	if !found {
		t.Fatalf("AcceptedSchemaDigests(%q) not found on second call", ref)
	}
	if second[0] != original {
		t.Fatalf("AcceptedSchemaDigests(%q)[0] = %q after a caller mutated a previously returned slice, want %q",
			ref, second[0], original)
	}
	if !contracts.IsAcceptedSchemaDigest(ref, original) {
		t.Fatalf("IsAcceptedSchemaDigest(%q, %q) = false after a caller mutated a returned slice", ref, original)
	}
}

func TestDecodeRecordRejectsASchemaDigestThatIsInNoRevision(t *testing.T) {
	ref := mustTypeRef(t, "review/v1")
	input := snapshot.SnapshotRef{
		ID: 41, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a'),
	}
	record, err := contracts.NewRecord(
		ref,
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", input,
		)},
		json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	record.Schema = recordDigest('b')
	var decoded contracts.Record[json.RawMessage]
	err = contracts.DecodeSealedRecord(marshalRecord(t, record), ref, &decoded)
	if err == nil {
		t.Fatal("DecodeSealedRecord() accepted a record whose schema digest is in no revision of the history")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Fatalf("DecodeSealedRecord() error = %v, want it to mention the schema digest", err)
	}
}
