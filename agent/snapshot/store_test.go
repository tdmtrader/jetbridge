package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testDigestText = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mustTestDigest(t *testing.T) Digest {
	t.Helper()
	digest, err := ParseDigest(testDigestText)
	if err != nil {
		t.Fatalf("parse test digest: %v", err)
	}
	return digest
}

func TestDigestAcceptsOnlyCanonicalSHA256(t *testing.T) {
	digest := mustTestDigest(t)
	if got := digest.String(); got != testDigestText {
		t.Fatalf("digest string = %q, want %q", got, testDigestText)
	}

	invalid := []string{
		"", "sha1:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("0", 63),
		"sha256:" + strings.Repeat("0", 65), "sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("g", 64), " sha256:" + strings.Repeat("0", 64),
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseDigest(raw); err == nil {
				t.Fatalf("ParseDigest accepted %q", raw)
			}
			var decoded Digest
			if err := json.Unmarshal([]byte(`"`+raw+`"`), &decoded); err == nil {
				t.Fatalf("json.Unmarshal accepted %q", raw)
			}
		})
	}
}

func TestAggregateJSONBoundariesRejectInvalidNestedValuesAndUnknownFields(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		target any
	}{
		{name: "port type", raw: `{"name":"out","type":"review/v0"}`, target: new(Port)},
		{name: "port unknown field", raw: `{"name":"out","type":"review/v1","extra":true}`, target: new(Port)},
		{name: "snapshot ref digest", raw: `{"id":"1","type":"review/v1","digest":"sha256:nope"}`, target: new(SnapshotRef)},
		{name: "retention class", raw: `{"class":"grant","reason":"authorization is not retention"}`, target: new(RetentionSpec)},
		{name: "seal commit missing outputs", raw: `{"request":{}}`, target: new(SealCommit)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.raw), tt.target); err == nil {
				t.Fatalf("json.Unmarshal accepted invalid aggregate: %s", tt.raw)
			}
		})
	}
}

func TestSealCommitUsesOnlyPrePersistenceCorrelatedDTOs(t *testing.T) {
	digest := mustTestDigest(t)
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	workflowDefinitionID := 12
	workflowRunID := WorkflowRunID(13)
	request := SealRequest{
		BuildID: 7, TeamID: 8, TeamName: "main", CreatedBy: "alice", PlanID: "plan",
		Attempt: "1", StepKind: "task", StepName: "review", InputOrder: []string{"before"},
		WorkflowDefinitionID: &workflowDefinitionID, WorkflowRunID: &workflowRunID,
		Inputs: map[string]SnapshotRef{"before": {ID: 1, Type: TypeRef("repository/v1"), Digest: digest}},
		Outputs: []CandidateOutput{{
			Port: Port{Name: "review", Type: TypeRef("review/v1")}, ArchivePath: "/private/spool/review.tar",
			Digest: digest, ByteSize: 42, FileCount: 1, Representation: "application/x-tar", IntrinsicMetadata: json.RawMessage(`{"schema":1}`),
		}},
	}
	commit := SealCommit{
		Request: request,
		Outputs: []SealCommitOutput{{
			ClientKey: "review-result", OutputPort: "review", Digest: digest, StagedUploadID: 101,
			Locations:      []Location{{Digest: digest, Driver: "jetbridge-daemon-v1", Key: "snapshots/sha256/key.tar", Node: "worker-1"}},
			Retention:      []RetentionSpec{{Class: RetentionClassBinding, ExpiresAt: ptrTime(now.Add(time.Hour)), Actor: "build/7", Reason: "build output"}},
			SourceMetadata: json.RawMessage(`{"adapter":"task"}`),
		}},
	}
	if err := commit.Validate(); err != nil {
		t.Fatalf("validate seal commit: %v", err)
	}

	typeOfCommit := reflect.TypeOf(commit)
	for _, persistedField := range []string{"Snapshots", "Productions", "Grants", "Claims", "Lineage", "StagedUploads"} {
		if _, found := typeOfCommit.FieldByName(persistedField); found {
			t.Fatalf("SealCommit exposes persistence-row field %s", persistedField)
		}
	}

	clone := commit.Clone()
	clone.Request.InputOrder[0] = "changed"
	clone.Request.Inputs["before"] = SnapshotRef{ID: 2, Type: TypeRef("repository/v1"), Digest: digest}
	*clone.Request.WorkflowDefinitionID = 99
	*clone.Request.WorkflowRunID = 99
	clone.Request.Outputs[0].IntrinsicMetadata[2] = 'X'
	clone.Outputs[0].Locations[0].Node = "changed"
	clone.Outputs[0].Retention[0].Actor = "changed"
	*clone.Outputs[0].Retention[0].ExpiresAt = now.Add(2 * time.Hour)
	clone.Outputs[0].SourceMetadata[2] = 'X'
	if commit.Request.InputOrder[0] != "before" || commit.Request.Inputs["before"].ID != 1 || *commit.Request.WorkflowDefinitionID != 12 || *commit.Request.WorkflowRunID != 13 || string(commit.Request.Outputs[0].IntrinsicMetadata) != `{"schema":1}` || commit.Outputs[0].Locations[0].Node != "worker-1" || commit.Outputs[0].Retention[0].Actor != "build/7" || !commit.Outputs[0].Retention[0].ExpiresAt.Equal(now.Add(time.Hour)) || string(commit.Outputs[0].SourceMetadata) != `{"adapter":"task"}` {
		t.Fatalf("caller mutation changed original commit: %#v", commit)
	}

	encoded, err := json.Marshal(commit)
	if err != nil {
		t.Fatalf("marshal valid commit: %v", err)
	}
	var decoded SealCommit
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal valid commit: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped commit is invalid: %v", err)
	}
}

func TestSealRequestRequiresInputOrderToBeAnExactPermutation(t *testing.T) {
	digest := mustTestDigest(t)
	base := SealRequest{
		BuildID: 1, TeamID: 1, TeamName: "main", CreatedBy: "alice", PlanID: "p", Attempt: "1", StepKind: "task", StepName: "s",
		Inputs: map[string]SnapshotRef{
			"first":  {ID: 1, Type: TypeRef("opaque/v1"), Digest: digest},
			"second": {ID: 2, Type: TypeRef("opaque/v1"), Digest: digest},
		},
		Outputs: []CandidateOutput{{Port: Port{Name: "out", Type: TypeRef("opaque/v1")}, ArchivePath: "/tmp/out.tar", Digest: digest, ByteSize: 1, Representation: "application/x-tar"}},
	}
	for name, order := range map[string][]string{
		"missing": {"first"}, "duplicate": {"first", "first"}, "unknown": {"first", "third"},
	} {
		t.Run(name, func(t *testing.T) {
			request := base.Clone()
			request.InputOrder = order
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate accepted input order %#v", order)
			}
		})
	}
	request := base.Clone()
	request.InputOrder = []string{"second", "first"}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid input order: %v", err)
	}
}

func TestStageUploadRequestHasNoCallerSuppliedPersistenceFields(t *testing.T) {
	digest := mustTestDigest(t)
	request := StageUploadRequest{Digest: digest, TeamID: 1, Attempt: "1", LeaseExpiresAt: time.Now().Add(time.Minute)}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate stage request: %v", err)
	}
	typ := reflect.TypeOf(request)
	for _, forbidden := range []string{"ID", "CreatedAt"} {
		if _, found := typ.FieldByName(forbidden); found {
			t.Fatalf("StageUploadRequest lets caller supply %s", forbidden)
		}
	}
}

func TestWithDigestLeaseSortsDeduplicatesAndClosesAfterAllWork(t *testing.T) {
	digest := mustTestDigest(t)
	other, err := ParseDigest("sha256:1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	lease := &recordingLease{covered: map[Digest]bool{digest: true, other: true}, events: &events}
	manager := &recordingLockManager{lease: lease}
	err = WithDigestLease(context.Background(), manager, []Digest{other, digest, other}, func(held DigestLease) error {
		if err := RequireDigestLease(held, digest); err != nil {
			return err
		}
		events = append(events, "stage", "content-io", "commit")
		return nil
	})
	if err != nil {
		t.Fatalf("with digest lease: %v", err)
	}
	if got, want := manager.acquired, []Digest{digest, other}; !reflect.DeepEqual(got, want) {
		t.Fatalf("acquired digests = %v, want %v", got, want)
	}
	if want := []string{"stage", "content-io", "commit", "close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestWithDigestLeaseClosesAfterLifecycleCallbacksAndErrors(t *testing.T) {
	digest := mustTestDigest(t)
	workErr := errors.New("remove stages failed")
	closeErr := errors.New("close failed")
	events := []string{}
	lease := &recordingLease{covered: map[Digest]bool{digest: true}, events: &events, closeErr: closeErr}
	manager := &recordingLockManager{lease: lease}
	err := WithDigestLease(context.Background(), manager, []Digest{digest}, func(DigestLease) error {
		events = append(events, "final-recheck", "delete", "remove-stages")
		return workErr
	})
	if !errors.Is(err, workErr) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want joined work and close errors", err)
	}
	if want := []string{"final-recheck", "delete", "remove-stages", "close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestWithDigestLeaseRejectsLeaseThatDoesNotCoverRequestedDigest(t *testing.T) {
	digest := mustTestDigest(t)
	events := []string{}
	lease := &recordingLease{covered: map[Digest]bool{}, events: &events}
	manager := &recordingLockManager{lease: lease}
	called := false
	err := WithDigestLease(context.Background(), manager, []Digest{digest}, func(DigestLease) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("WithDigestLease error = %v, callback called = %t", err, called)
	}
	if want := []string{"close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDigestStateAggregatesSemanticSnapshotsSharingPhysicalBytes(t *testing.T) {
	digest := mustTestDigest(t)
	state := DigestState{
		Digest: digest,
		Snapshots: []Snapshot{
			{ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: 1, Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: time.Now()},
			{ID: 2, Type: TypeRef("review/v1"), Digest: digest, ByteSize: 1, Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: time.Now()},
		},
		Locations:          []Location{{Digest: digest, Driver: "driver", Key: "key"}},
		HasActiveRetention: true,
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("validate aggregate digest state: %v", err)
	}
	if !state.Committed() {
		t.Fatal("state with semantic snapshots is not committed")
	}
}

func TestValidatedIDsProvideSafeTemplateValues(t *testing.T) {
	snapshotID, err := ParseSnapshotID("9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := snapshotID.TemplateValue(); err != nil || got != "9007199254740993" {
		t.Fatalf("snapshot template value = %q, %v", got, err)
	}
	workflowID, err := ParseWorkflowRunID("9223372036854775807")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := workflowID.TemplateValue(); err != nil || got != "9223372036854775807" {
		t.Fatalf("workflow template value = %q, %v", got, err)
	}
	if got := SnapshotID(0).String(); got != "" {
		t.Fatalf("invalid SnapshotID.String() = %q, want empty", got)
	}
	if _, err := WorkflowRunID(-1).TemplateValue(); err == nil {
		t.Fatal("invalid workflow run ID produced a template value")
	}
}

func TestCloneHelpersCopyManifestAndFilterPointers(t *testing.T) {
	digest := mustTestDigest(t)
	snapshot := Snapshot{
		ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, Representation: "application/x-tar",
		IntrinsicMetadata: json.RawMessage(`{"head":"abc"}`), ContentState: ContentStateAvailable, CreatedAt: time.Now(),
	}
	clonedSnapshot := snapshot.Clone()
	clonedSnapshot.IntrinsicMetadata[2] = 'X'
	if string(snapshot.IntrinsicMetadata) != `{"head":"abc"}` {
		t.Fatalf("snapshot clone aliased intrinsic metadata: %s", snapshot.IntrinsicMetadata)
	}

	after := time.Now()
	filter := SnapshotListFilter{CreatedAfter: &after}
	clonedFilter := filter.Clone()
	*clonedFilter.CreatedAfter = after.Add(time.Hour)
	if !filter.CreatedAfter.Equal(after) {
		t.Fatalf("filter clone aliased CreatedAfter: %s", filter.CreatedAfter)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

type recordingLease struct {
	covered  map[Digest]bool
	events   *[]string
	closeErr error
}

func (l *recordingLease) Covers(digest Digest) bool { return l.covered[digest] }
func (l *recordingLease) Close() error {
	*l.events = append(*l.events, "close")
	return l.closeErr
}

type recordingLockManager struct {
	lease    DigestLease
	acquired []Digest
}

func (m *recordingLockManager) AcquireMany(_ context.Context, digests []Digest) (DigestLease, error) {
	m.acquired = append([]Digest(nil), digests...)
	return m.lease, nil
}
