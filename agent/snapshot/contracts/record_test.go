package contracts_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRecordEnvelopeBindsAuthorityToExactInputs(t *testing.T) {
	input := snapshot.SnapshotRef{
		ID: 41, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a'),
	}
	validationContext, err := snapshot.NewValidationContext(
		map[string]snapshot.SnapshotRef{"change": input},
		nil,
	)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	record, err := contracts.NewRecord(
		mustTypeRef(t, "review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", input,
		)},
		json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	if err := record.AdmitForSeal(mustTypeRef(t, "review/v1"), validationContext); err != nil {
		t.Fatalf("AdmitForSeal(): %v", err)
	}

	tests := []struct {
		name  string
		setup func(*contracts.Record[json.RawMessage])
		want  string
	}{
		{
			name: "wrong record type",
			setup: func(value *contracts.Record[json.RawMessage]) {
				value.Type = mustTypeRef(t, "diagnosis/v1")
			},
			want: "type",
		},
		{
			name: "wrong schema digest",
			setup: func(value *contracts.Record[json.RawMessage]) {
				value.Schema = recordDigest('b')
			},
			want: "schema",
		},
		{
			name: "undeclared input",
			setup: func(value *contracts.Record[json.RawMessage]) {
				value.Subjects[0].Input = "other"
			},
			want: "not an exact declared input",
		},
		{
			name: "mismatched input type",
			setup: func(value *contracts.Record[json.RawMessage]) {
				value.Subjects[0].Type = mustTypeRef(t, "repository/v1")
			},
			want: "type",
		},
		{
			name: "mismatched input digest",
			setup: func(value *contracts.Record[json.RawMessage]) {
				value.Subjects[0].Digest = recordDigest('c')
			},
			want: "digest",
		},
		{
			name: "duplicate input",
			setup: func(value *contracts.Record[json.RawMessage]) {
				duplicate := value.Subjects[0]
				duplicate.ID = "secondary"
				value.Subjects = append(value.Subjects, duplicate)
			},
			want: "input",
		},
		{
			name: "unsorted subjects",
			setup: func(value *contracts.Record[json.RawMessage]) {
				second := value.Subjects[0]
				second.ID = "alpha"
				second.Input = "other"
				value.Subjects = append(value.Subjects, second)
			},
			want: "sorted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := record
			candidate.Subjects = append([]contracts.Subject(nil), record.Subjects...)
			tc.setup(&candidate)
			if err := candidate.AdmitForSeal(mustTypeRef(t, "review/v1"), validationContext); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AdmitForSeal() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRecordEnvelopeStrictJSONHasNoLocalSnapshotID(t *testing.T) {
	input := snapshot.SnapshotRef{
		ID: 41, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a'),
	}
	record, err := contracts.NewRecord(
		mustTypeRef(t, "review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", input,
		)},
		json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
	)
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	if strings.Contains(string(encoded), `"id":41`) || strings.Contains(string(encoded), `"snapshot_id"`) {
		t.Fatalf("record contains local snapshot identity: %s", encoded)
	}
	withLocalID := []byte(strings.Replace(
		string(encoded),
		`"digest":"`+input.Digest.String()+`"`,
		`"digest":"`+input.Digest.String()+`","snapshot_id":41`,
		1,
	))
	var decoded contracts.Record[json.RawMessage]
	if err := contracts.DecodeSealedRecord(withLocalID, mustTypeRef(t, "review/v1"), &decoded); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeSealedRecord() error = %v, want strict unknown-field error", err)
	}
}

func TestRecordValueIdentityUsesStableSubjectDigestNotLocalSnapshotID(t *testing.T) {
	body := json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`)
	makeRecord := func(id snapshot.SnapshotID, digest snapshot.Digest) []byte {
		t.Helper()
		input := snapshot.SnapshotRef{
			ID: id, Type: mustTypeRef(t, "repository-change/v1"), Digest: digest,
		}
		record, err := contracts.NewRecord(
			mustTypeRef(t, "review/v1"),
			[]contracts.Subject{contracts.SubjectFromInput(
				"primary", contracts.SubjectRolePrimary, "change", input,
			)},
			body,
		)
		if err != nil {
			t.Fatalf("NewRecord(): %v", err)
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("Marshal(): %v", err)
		}
		return encoded
	}

	first := makeRecord(41, recordDigest('a'))
	sameValueFromAnotherInstallation := makeRecord(999, recordDigest('a'))
	if !bytes.Equal(first, sameValueFromAnotherInstallation) {
		t.Fatalf("local snapshot ID changed record value:\n%s\n%s", first, sameValueFromAnotherInstallation)
	}
	differentSubject := makeRecord(41, recordDigest('b'))
	if bytes.Equal(first, differentSubject) {
		t.Fatal("different subject digest did not change record value")
	}
}

func TestRecordComponentsValidateAnchorsScoresAndContent(t *testing.T) {
	subjects := map[string]struct{}{"primary": {}}
	validAnchors := []contracts.Anchor{
		{Subject: "primary", Locator: contracts.Locator{Kind: "file-lines", Path: "src/main.go", Start: intPointer(2), End: intPointer(4)}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "log-lines", Path: "logs/test.log", Start: intPointer(1), End: intPointer(1)}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "json-pointer", Pointer: "/status/reason"}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "byte-range", Path: "artifact.bin", Start: intPointer(0), End: intPointer(8)}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "opaque", Value: "provider:event:42"}},
	}
	for _, anchor := range validAnchors {
		if err := anchor.Validate(subjects); err != nil {
			t.Errorf("Anchor(%s).Validate() error = %v", anchor.Locator.Kind, err)
		}
	}

	invalidAnchors := []contracts.Anchor{
		{Subject: "missing", Locator: contracts.Locator{Kind: "opaque", Value: "x"}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "file-lines", Path: "../main.go", Start: intPointer(1), End: intPointer(1)}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "json-pointer", Pointer: "not-a-pointer"}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "byte-range", Path: "artifact.bin", Start: intPointer(8), End: intPointer(8)}},
		{Subject: "primary", Locator: contracts.Locator{Kind: "opaque", Value: " "}},
	}
	for _, anchor := range invalidAnchors {
		if err := anchor.Validate(subjects); err == nil {
			t.Errorf("Anchor(%s).Validate() succeeded for invalid anchor %#v", anchor.Locator.Kind, anchor)
		}
	}

	for _, score := range []contracts.Score{
		{Value: 0.8, Scale: "unit-interval", Direction: "higher-is-better"},
		{Value: 7, Scale: "bounded", Direction: "higher-is-better", Minimum: floatPointer(0), Maximum: floatPointer(10)},
		{Value: 2, Scale: "unbounded", Direction: "target", Target: floatPointer(3)},
	} {
		if err := score.Validate(); err != nil {
			t.Errorf("Score.Validate() error = %v", err)
		}
	}
	for _, score := range []contracts.Score{
		{Value: 1.1, Scale: "unit-interval", Direction: "higher-is-better"},
		{Value: 7, Scale: "bounded", Direction: "higher-is-better", Minimum: floatPointer(10), Maximum: floatPointer(0)},
		{Value: 2, Scale: "unbounded", Direction: "target"},
	} {
		if err := score.Validate(); err == nil {
			t.Errorf("Score.Validate() succeeded for invalid score %#v", score)
		}
	}

	content := contracts.ContentRef{
		Path: "content/change.patch", Digest: recordDigest('d'), MediaType: "text/x-diff",
	}
	if err := content.Validate(); err != nil {
		t.Fatalf("ContentRef.Validate() error = %v", err)
	}
	content.Path = "change.patch"
	if err := content.Validate(); err == nil || !strings.Contains(err.Error(), "content/") {
		t.Fatalf("ContentRef.Validate() error = %v, want content/ confinement", err)
	}
}

func recordDigest(fill byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(fill), 64))
}

func intPointer(value int) *int           { return &value }
func floatPointer(value float64) *float64 { return &value }
