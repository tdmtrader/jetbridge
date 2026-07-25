package contracts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests live inside the package because the behaviour under test — a
// stored record bearing a SUPERSEDED schema digest still decoding — cannot be
// exercised from outside without bumping a real descriptor revision, and
// bumping a real descriptor is exactly the thing this task exists to make safe
// rather than to do. A synthetic two-revision history is substituted for the
// frozen table instead.

const (
	syntheticRecordType = snapshot.TypeRef("synthetic-record/v1")

	syntheticRevision1 = `{"contract":"synthetic-record/v1","envelope":"record/v1","revision":1}`
	syntheticRevision2 = `{"contract":"synthetic-record/v1","envelope":"record/v1","revision":2}`
)

func withSyntheticSchemaHistories(t *testing.T, histories map[snapshot.TypeRef]recordSchemaHistory) {
	t.Helper()
	index, err := buildRecordSchemaIndex(histories)
	if err != nil {
		t.Fatalf("buildRecordSchemaIndex(): %v", err)
	}
	previous := acceptedRecordSchemaDigests
	t.Cleanup(func() { acceptedRecordSchemaDigests = previous })
	acceptedRecordSchemaDigests = index
}

func syntheticTwoRevisionHistories() map[snapshot.TypeRef]recordSchemaHistory {
	return map[snapshot.TypeRef]recordSchemaHistory{
		syntheticRecordType: {
			current: recordSchemaRevision{revision: 2, descriptor: syntheticRevision2},
			superseded: []recordSchemaRevision{
				{revision: 1, descriptor: syntheticRevision1},
			},
		},
	}
}

func syntheticSubjects() []Subject {
	return []Subject{{
		ID:     "primary",
		Role:   SubjectRolePrimary,
		Input:  "change",
		Type:   snapshot.TypeRef("repository-change/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
}

func TestSchemaDigestHistoryOrdersNewestFirstRegardlessOfLiteralOrder(t *testing.T) {
	histories := map[snapshot.TypeRef]recordSchemaHistory{
		syntheticRecordType: {
			current: recordSchemaRevision{revision: 3, descriptor: `{"revision":3}`},
			// Deliberately in the "wrong" literal order: ordering is derived
			// from the declared revision number, never from list position, so
			// an author cannot append to the wrong end.
			superseded: []recordSchemaRevision{
				{revision: 1, descriptor: `{"revision":1}`},
				{revision: 2, descriptor: `{"revision":2}`},
			},
		},
	}
	index, err := buildRecordSchemaIndex(histories)
	if err != nil {
		t.Fatalf("buildRecordSchemaIndex(): %v", err)
	}
	want := []snapshot.Digest{
		schemaDescriptorDigest(`{"revision":3}`),
		schemaDescriptorDigest(`{"revision":2}`),
		schemaDescriptorDigest(`{"revision":1}`),
	}
	accepted := index[syntheticRecordType]
	if len(accepted) != len(want) {
		t.Fatalf("accepted digests = %v, want %v", accepted, want)
	}
	for position, digest := range want {
		if accepted[position] != digest {
			t.Fatalf("accepted[%d] = %q, want %q (newest first)", position, accepted[position], digest)
		}
	}
}

func TestCurrentAndSupersededSchemaDigestsBothValidate(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())

	current := schemaDescriptorDigest(syntheticRevision2)
	superseded := schemaDescriptorDigest(syntheticRevision1)

	record, err := NewRecord(syntheticRecordType, syntheticSubjects(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	if record.Schema != current {
		t.Fatalf("NewRecord().Schema = %q, want the newest digest %q", record.Schema, current)
	}

	tests := []struct {
		name    string
		schema  snapshot.Digest
		wantErr bool
	}{
		{name: "current digest", schema: current},
		{name: "superseded digest", schema: superseded},
		{
			name:    "digest from no revision",
			schema:  snapshot.Digest("sha256:" + strings.Repeat("b", 64)),
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := record
			stored.Schema = test.schema
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatalf("marshal record: %v", err)
			}
			var decoded Record[json.RawMessage]
			err = DecodeSealedRecord(encoded, syntheticRecordType, &decoded)
			if test.wantErr {
				if err == nil {
					t.Fatal("DecodeSealedRecord() accepted a schema digest that is in no revision")
				}
				if !strings.Contains(err.Error(), "schema") {
					t.Fatalf("DecodeSealedRecord() error = %v, want it to mention the schema digest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeSealedRecord() = %v, want the digest accepted", err)
			}
			if decoded.Schema != test.schema {
				t.Fatalf("decoded schema = %q, want %q preserved", decoded.Schema, test.schema)
			}
		})
	}
}

func TestBuildRecordSchemaIndexRejectsHistoriesThatLoseARevision(t *testing.T) {
	tests := []struct {
		name      string
		histories map[snapshot.TypeRef]recordSchemaHistory
		wantError string
	}{
		{
			name: "removed revision leaves a gap",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 3, descriptor: `{"revision":3}`},
					superseded: []recordSchemaRevision{
						{revision: 1, descriptor: `{"revision":1}`},
					},
				},
			},
			wantError: "contiguous",
		},
		{
			name: "duplicate revision number",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 2, descriptor: `{"revision":2}`},
					superseded: []recordSchemaRevision{
						{revision: 2, descriptor: `{"revision":2-bis}`},
					},
				},
			},
			wantError: "contiguous",
		},
		{
			name: "current is not the newest revision",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 1, descriptor: `{"revision":1}`},
					superseded: []recordSchemaRevision{
						{revision: 2, descriptor: `{"revision":2}`},
					},
				},
			},
			wantError: "newest",
		},
		{
			name: "revisions do not start at 1",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 2, descriptor: `{"revision":2}`},
				},
			},
			wantError: "contiguous",
		},
		{
			name: "empty descriptor",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 1, descriptor: ""},
				},
			},
			wantError: "descriptor",
		},
		{
			name: "appended revision forgot to change the descriptor bytes",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				syntheticRecordType: {
					current: recordSchemaRevision{revision: 2, descriptor: syntheticRevision1},
					superseded: []recordSchemaRevision{
						{revision: 1, descriptor: syntheticRevision1},
					},
				},
			},
			wantError: "distinct",
		},
		{
			name: "invalid type reference",
			histories: map[snapshot.TypeRef]recordSchemaHistory{
				snapshot.TypeRef("Not A Type"): {
					current: recordSchemaRevision{revision: 1, descriptor: `{"revision":1}`},
				},
			},
			wantError: "invalid type reference",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildRecordSchemaIndex(test.histories)
			if err == nil {
				t.Fatal("buildRecordSchemaIndex() accepted a history that is not append-only")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("buildRecordSchemaIndex() error = %v, want it to mention %q", err, test.wantError)
			}
		})
	}
}

func TestMustBuildRecordSchemaIndexPanicsOnAnInvalidHistory(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("mustBuildRecordSchemaIndex() did not panic on an invalid history")
		}
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "record schema history") {
			t.Fatalf("recovered %v, want a message naming the record schema history", recovered)
		}
	}()
	mustBuildRecordSchemaIndex(map[snapshot.TypeRef]recordSchemaHistory{
		syntheticRecordType: {
			current: recordSchemaRevision{revision: 2, descriptor: `{"revision":2}`},
		},
	})
}

func TestFrozenRecordSchemaHistoriesSatisfyTheAppendOnlyInvariants(t *testing.T) {
	if _, err := buildRecordSchemaIndex(recordSchemaHistories); err != nil {
		t.Fatalf("buildRecordSchemaIndex(recordSchemaHistories): %v", err)
	}
	for ref, history := range recordSchemaHistories {
		if history.current.revision != len(history.superseded)+1 {
			t.Fatalf("%q current revision %d, want %d for %d superseded revisions",
				ref, history.current.revision, len(history.superseded)+1, len(history.superseded))
		}
	}
}
