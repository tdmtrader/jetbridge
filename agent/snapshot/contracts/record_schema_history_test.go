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
//
// The procedure at a bump is therefore PREPEND, never replace: the new revision's
// digest goes at the front, because the lists are newest-first, and every entry
// below it stays byte for byte where it was. The revision-1 entries below are the
// pre-dialect one-line stamps and are what a record sealed before the dialect
// landed still carries.
var frozenAcceptedSchemaDigests = map[string][]string{
	"review/v1": {
		"sha256:8b460c4d9ea3a6ca6c7d1b8fb1e8dce448df8a2745f3d81a52992cec8e760220",
		"sha256:01d9f0644151274e8577875373f110b11f0ec34ff29ba12b143379744416fdb5",
	},
	"diagnosis/v1": {
		"sha256:47301b1acc54725bd94804e6a20ca284b85568e4336d5592c64a89bcd2f58c47",
		"sha256:7c7060b4d663d4546836898640f71bb576749d6aee7fee1df2b5616eea21064e",
	},
	"validation/v1": {
		"sha256:4d03fc7af7796a7add4d72d08a286c64ecfa7e9486fb06f136600aacc3f1b0e7",
		"sha256:68811d591b6f1f9cac7f2c27f36d96282717298c2420e3e16f521e5cd7351821",
		"sha256:b5a08c5bf14754800b4bd02eeb7fae8bf3ed1aa08e2f4905d1cfda15a96c0363",
	},
	"repository-change/v1": {
		"sha256:afdb59e4eb682a09f86fb92165c57d3df215487be5a55e316944eba8bdc1f013",
		"sha256:2dae971bc191c13eb9fc42f29992268d15c15765548a85d70bd808844af6e308",
	},
	"measurements/v1": {
		"sha256:d2e0b89126ce534c957a8e93166391f517e126aec5aa961f66a4c6c178bc57a0",
		"sha256:fea8ee17190c3dcf6c2d24065e2eea51acc9672c7cc091137fd3d6085e67a361",
	},
	"pull-request/v1": {
		"sha256:3cbba0207c612715c8050c69357ac727fc6b9b2f68b3083736d846b7d6c668f4",
		"sha256:0d2ecf6dc021678fb2fffa8f82d9cd9355165521f0a05f2faeed6024d661b737",
		"sha256:476e292848c15ad9de04e15e9872f8fac88f85f40aea9e8daf980ac0438c6722",
	},
	"pull-request-response/v1": {
		"sha256:d60fcaa7aa054b5eaac43e96ef5d6386a69221da9d5346de3b675313229c8538",
		"sha256:9ecb1a7bb3269ae9d98a38d54c94011e1cb1e4975b4f23476a53b239fe66bc0e",
		"sha256:3d1c43b8a8d9397b7cfa26f145921b3bd6d7e4076c129a927a043658ad3d3286",
	},
	"publish-impact/v1": {
		"sha256:58b98f51d3372bb41e197ccac545d6a0d47ee1d2d551fdd79d3389049d1809b2",
		"sha256:21952c646a7c81107f3eaad385a9467a9548e3d03492a87942989bedff00f045",
		"sha256:a60683c64aa8206f97fe2789b21d7f29d2a3440920f7be91aa287c670f4f05a2",
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
