package snapshot

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTypeRef(t *testing.T) {
	valid := []string{"review/v1", "repository-change/v12", "foo.bar/v3"}
	for _, raw := range valid {
		t.Run(raw, func(t *testing.T) {
			ref, err := ParseTypeRef(raw)
			if err != nil {
				t.Fatalf("parse type ref: %v", err)
			}
			if err := ref.Validate(); err != nil {
				t.Fatalf("validate type ref: %v", err)
			}
			if got := ref.String(); got != raw {
				t.Fatalf("String() = %q, want %q", got, raw)
			}
		})
	}
}

func TestTypeRefRejectsInvalidGrammarAndVersion(t *testing.T) {
	invalid := []string{
		"", "Review/v1", "review/v0", "review/v01", "review/v-1", "review/1",
		"review/v1 ", "review/v1/extra", "review./v1", "review..comment/v1", "review_/v1",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseTypeRef(raw); err == nil {
				t.Fatal("expected invalid type reference to fail")
			}
		})
	}
}

func TestPort(t *testing.T) {
	ports := []Port{
		{Name: "before", Type: TypeRef("repository/v1")},
		{Name: "review", Type: TypeRef("review/v1"), Optional: true},
	}
	wantOrder := []Port{ports[0], ports[1]}
	if err := ValidatePorts(ports); err != nil {
		t.Fatalf("validate ports: %v", err)
	}
	if !reflect.DeepEqual(ports, wantOrder) {
		t.Fatalf("ValidatePorts changed source order: got %#v, want %#v", ports, wantOrder)
	}

	if err := ValidatePorts([]Port{{Name: "review", Type: TypeRef("review/v1")}, {Name: "review", Type: TypeRef("review/v1")}}); err == nil {
		t.Fatal("expected duplicate port name to fail")
	}
}

func TestPortRejectsInvalidFields(t *testing.T) {
	for _, port := range []Port{
		{Type: TypeRef("review/v1")},
		{Name: "review", Type: TypeRef("review/v0")},
	} {
		if err := port.Validate(); err == nil {
			t.Fatalf("expected %+v to be invalid", port)
		}
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	want := Snapshot{
		ID:                SnapshotID(9007199254740993),
		Type:              TypeRef("review/v1"),
		Digest:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ByteSize:          42,
		FileCount:         1,
		Representation:    "application/x-tar",
		IntrinsicMetadata: json.RawMessage(`{"schema_version":1}`),
		ContentState:      ContentStateAvailable,
		CreatedAt:         createdAt,
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if string(encoded) == "" || !strings.Contains(string(encoded), `"id":"9007199254740993"`) {
		t.Fatalf("snapshot id must be a quoted decimal string: %s", encoded)
	}

	var got Snapshot
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot round trip = %#v, want %#v", got, want)
	}
}

func TestSnapshotAndWorkflowRunIDsUseQuotedCanonicalPositiveDecimals(t *testing.T) {
	snapshotJSON, err := json.Marshal(SnapshotID(9007199254740993))
	if err != nil {
		t.Fatalf("marshal snapshot id: %v", err)
	}
	if got, want := string(snapshotJSON), `"9007199254740993"`; got != want {
		t.Fatalf("snapshot ID JSON = %s, want %s", got, want)
	}
	var snapshotID SnapshotID
	if err := json.Unmarshal(snapshotJSON, &snapshotID); err != nil {
		t.Fatalf("unmarshal snapshot id: %v", err)
	}
	if got, want := snapshotID.String(), "9007199254740993"; got != want {
		t.Fatalf("snapshot ID String() = %s, want %s", got, want)
	}

	workflowRunJSON, err := json.Marshal(WorkflowRunID(math.MaxInt64))
	if err != nil {
		t.Fatalf("marshal workflow run id: %v", err)
	}
	if got, want := string(workflowRunJSON), `"9223372036854775807"`; got != want {
		t.Fatalf("workflow run ID JSON = %s, want %s", got, want)
	}
	var workflowRunID WorkflowRunID
	if err := json.Unmarshal(workflowRunJSON, &workflowRunID); err != nil {
		t.Fatalf("unmarshal workflow run id: %v", err)
	}
	if got, want := workflowRunID.String(), "9223372036854775807"; got != want {
		t.Fatalf("workflow run ID String() = %s, want %s", got, want)
	}
}

func TestSnapshotAndWorkflowRunIDsUseCanonicalTextForMapKeys(t *testing.T) {
	snapshotMap := map[SnapshotID]string{SnapshotID(9007199254740993): "snapshot"}
	encoded, err := json.Marshal(snapshotMap)
	if err != nil {
		t.Fatalf("marshal snapshot ID map: %v", err)
	}
	if got, want := string(encoded), `{"9007199254740993":"snapshot"}`; got != want {
		t.Fatalf("snapshot map JSON = %s, want %s", got, want)
	}
	var decodedSnapshots map[SnapshotID]string
	if err := json.Unmarshal(encoded, &decodedSnapshots); err != nil {
		t.Fatalf("unmarshal snapshot ID map: %v", err)
	}
	if got := decodedSnapshots[SnapshotID(9007199254740993)]; got != "snapshot" {
		t.Fatalf("decoded snapshot map value = %q", got)
	}

	workflowMap := map[WorkflowRunID]string{WorkflowRunID(math.MaxInt64): "workflow"}
	encoded, err = json.Marshal(workflowMap)
	if err != nil {
		t.Fatalf("marshal workflow run ID map: %v", err)
	}
	if got, want := string(encoded), `{"9223372036854775807":"workflow"}`; got != want {
		t.Fatalf("workflow map JSON = %s, want %s", got, want)
	}
	var decodedWorkflows map[WorkflowRunID]string
	if err := json.Unmarshal(encoded, &decodedWorkflows); err != nil {
		t.Fatalf("unmarshal workflow run ID map: %v", err)
	}
	if got := decodedWorkflows[WorkflowRunID(math.MaxInt64)]; got != "workflow" {
		t.Fatalf("decoded workflow map value = %q", got)
	}
}

func TestSnapshotAndWorkflowRunIDMapKeysRejectNonCanonicalText(t *testing.T) {
	for _, raw := range []string{"0", "-1", "+1", "01", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			var snapshots map[SnapshotID]string
			if err := json.Unmarshal([]byte(`{"`+raw+`":"value"}`), &snapshots); err == nil {
				t.Fatalf("snapshot ID map accepted key %q", raw)
			}
			var workflows map[WorkflowRunID]string
			if err := json.Unmarshal([]byte(`{"`+raw+`":"value"}`), &workflows); err == nil {
				t.Fatalf("workflow run ID map accepted key %q", raw)
			}
		})
	}
}

func TestSnapshotAndWorkflowRunIDsRejectNonCanonicalJSON(t *testing.T) {
	invalid := []string{
		`0`, `null`, `1`, `-1`, `"0"`, `"-1"`, `"+1"`, `"01"`, `" 1"`, `"1 "`, `"1e3"`, `"9223372036854775808"`, `""`,
		`"\u0031"`,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			var snapshotID SnapshotID
			if err := json.Unmarshal([]byte(raw), &snapshotID); err == nil {
				t.Fatalf("SnapshotID accepted %s", raw)
			}
			var workflowRunID WorkflowRunID
			if err := json.Unmarshal([]byte(raw), &workflowRunID); err == nil {
				t.Fatalf("WorkflowRunID accepted %s", raw)
			}
		})
	}
}

func TestSnapshotAndWorkflowRunIDUnmarshalJSONRejectsTokenWhitespace(t *testing.T) {
	for _, raw := range []string{` "1"`, `"1" `} {
		var snapshotID SnapshotID
		if err := snapshotID.UnmarshalJSON([]byte(raw)); err == nil {
			t.Fatalf("SnapshotID.UnmarshalJSON accepted %q", raw)
		}
		var workflowRunID WorkflowRunID
		if err := workflowRunID.UnmarshalJSON([]byte(raw)); err == nil {
			t.Fatalf("WorkflowRunID.UnmarshalJSON accepted %q", raw)
		}
	}
}

func TestDirectJSONRejectsInvalidScalarAndManifestValues(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		target any
	}{
		{name: "type ref", raw: `"review/v0"`, target: new(TypeRef)},
		{name: "content state", raw: `"missing"`, target: new(ContentState)},
		{name: "snapshot manifest", raw: `{"id":"1","type":"review/v0","digest":"sha256:nope","byte_size":1,"file_count":1,"representation":"application/x-tar","content_state":"available","created_at":"2026-07-21T12:00:00Z"}`, target: new(Snapshot)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.raw), tt.target); err == nil {
				t.Fatalf("json.Unmarshal accepted invalid %s", tt.raw)
			}
		})
	}
}

func TestRetentionClaimBoundaries(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	for _, claim := range []RetentionClaim{
		{ID: 1, SnapshotID: 1, Class: RetentionClass("grant")},
		{ID: 2, SnapshotID: 1, Class: RetentionClass("future-value")},
	} {
		if claim.Active(now) {
			t.Fatalf("unknown retention class %q retained bytes", claim.Class)
		}
	}

	equalToNow := now
	if (RetentionClaim{ID: 3, SnapshotID: 1, Class: RetentionClassPin, ExpiresAt: &equalToNow}).Active(now) {
		t.Fatal("claim expiring exactly at now is active")
	}
	expired := now.Add(-time.Nanosecond)
	active := now.Add(time.Nanosecond)
	claims := []RetentionClaim{
		{ID: 4, SnapshotID: 1, Class: RetentionClassBinding, ExpiresAt: &expired},
		{ID: 5, SnapshotID: 1, Class: RetentionClassPin, ExpiresAt: &active},
	}
	if got, ok := EffectiveRetentionClaim(claims, now); !ok || got.ID != 5 {
		t.Fatalf("effective claim = %#v, %t, want unexpired claim 5", got, ok)
	}
}

func TestRetentionClaimsSortDeterministicallyWithoutTreatingGrantsAsRetention(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	claims := []RetentionClaim{
		{ID: 2, SnapshotID: 1, Class: RetentionClassPin, Actor: "zoe", CreatedAt: later},
		{ID: 3, SnapshotID: 1, Class: RetentionClassBinding, Actor: "build", CreatedAt: later},
		{ID: 1, SnapshotID: 1, Class: RetentionClassPin, Actor: "amy", CreatedAt: now},
	}
	SortRetentionClaims(claims)
	if got, want := []int64{claims[0].ID, claims[1].ID, claims[2].ID}, []int64{3, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim order = %v, want %v", got, want)
	}
	if got, ok := EffectiveRetentionClaim(claims, now); !ok || got.ID != 3 {
		t.Fatalf("effective retention claim = %#v, %t, want binding claim", got, ok)
	}
	if got, ok := EffectiveRetentionClaim(nil, now); ok {
		t.Fatalf("effective retention claim = %#v, true, want false", got)
	}
}

func TestEffectiveRetentionClaimReturnsAnIndependentValue(t *testing.T) {
	expires := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	claims := []RetentionClaim{{
		ID: 1, SnapshotID: 1, Class: RetentionClassPin, ExpiresAt: &expires,
	}}

	got, ok := EffectiveRetentionClaim(claims, expires.Add(-time.Hour))
	if !ok {
		t.Fatal("expected an effective retention claim")
	}
	got.ID = 99
	*got.ExpiresAt = expires.Add(time.Hour)
	if claims[0].ID != 1 || !claims[0].ExpiresAt.Equal(expires) {
		t.Fatalf("caller mutation changed original claim: %#v", claims[0])
	}
}
