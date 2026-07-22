package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
		{name: "seal commit missing outputs", raw: `{"context":{"build_id":1,"team_id":1,"team_name":"main","created_by":"alice","plan_id":"p","attempt":"1","step_kind":"task","step_name":"s","input_order":[],"inputs":{},"expected_outputs":[{"name":"out","type":"opaque/v1"}]},"outputs":[]}`, target: new(SealCommit)},
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
		Inputs:             map[string]SnapshotRef{"before": {ID: 1, Type: TypeRef("repository/v1"), Digest: digest}},
		OutputDeclarations: []Port{{Name: "review", Type: TypeRef("review/v1")}},
		Outputs: []OutputSource{{
			ClientKey: "review-result", Port: Port{Name: "review", Type: TypeRef("review/v1")},
			OpenTar: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("tar")), nil },
		}},
	}
	context, err := request.CommitContext()
	if err != nil {
		t.Fatalf("derive commit context: %v", err)
	}
	stage := StagedUpload{ID: 101, Digest: digest, TeamID: 8, Attempt: "1", LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now}
	candidate := CandidateOutput{
		Port: Port{Name: "review", Type: TypeRef("review/v1")}, ArchivePath: "/private/spool/review.tar",
		Digest: digest, ByteSize: 42, FileCount: 1, Representation: "application/x-tar", IntrinsicMetadata: json.RawMessage(`{"schema":1}`),
	}
	output, err := candidate.CommitOutput(
		"review-result",
		"",
		stage,
		[]Location{{Digest: digest, Driver: "jetbridge-daemon-v1", Key: "snapshots/sha256/key.tar", Node: "worker-1"}},
		[]RetentionSpec{{Class: RetentionClassBinding, ExpiresAt: ptrTime(now.Add(time.Hour)), Actor: "build/7", Reason: "build output"}},
		json.RawMessage(`{"adapter":"task"}`),
	)
	if err != nil {
		t.Fatalf("derive commit output: %v", err)
	}
	commit := SealCommit{
		Context: context,
		Outputs: []SealCommitOutput{output},
	}
	if err := commit.Validate(); err != nil {
		t.Fatalf("validate seal commit: %v", err)
	}

	typeOfCommit := reflect.TypeOf(commit)
	for _, persistedField := range []string{"Request", "Snapshots", "Productions", "Grants", "Claims", "Lineage", "StagedUploads"} {
		if _, found := typeOfCommit.FieldByName(persistedField); found {
			t.Fatalf("SealCommit exposes persistence-row field %s", persistedField)
		}
	}
	if _, found := reflect.TypeOf(context).FieldByName("Outputs"); found {
		t.Fatal("SealCommitContext contains pre-upload candidates")
	}

	clone := commit.Clone()
	clone.Context.InputOrder[0] = "changed"
	clone.Context.Inputs["before"] = SnapshotRef{ID: 2, Type: TypeRef("repository/v1"), Digest: digest}
	clone.Context.ExpectedOutputs[0].Name = "changed"
	*clone.Context.WorkflowDefinitionID = 99
	*clone.Context.WorkflowRunID = 99
	clone.Outputs[0].IntrinsicMetadata[2] = 'X'
	clone.Outputs[0].Locations[0].Node = "changed"
	clone.Outputs[0].Retention[0].Actor = "changed"
	*clone.Outputs[0].Retention[0].ExpiresAt = now.Add(2 * time.Hour)
	clone.Outputs[0].SourceMetadata[2] = 'X'
	if commit.Context.InputOrder[0] != "before" || commit.Context.Inputs["before"].ID != 1 || commit.Context.ExpectedOutputs[0].Name != "review" || *commit.Context.WorkflowDefinitionID != 12 || *commit.Context.WorkflowRunID != 13 || string(commit.Outputs[0].IntrinsicMetadata) != `{"schema":1}` || commit.Outputs[0].Locations[0].Node != "worker-1" || commit.Outputs[0].Retention[0].Actor != "build/7" || !commit.Outputs[0].Retention[0].ExpiresAt.Equal(now.Add(time.Hour)) || string(commit.Outputs[0].SourceMetadata) != `{"adapter":"task"}` {
		t.Fatalf("caller mutation changed original commit: %#v", commit)
	}

	encoded, err := json.Marshal(commit)
	if err != nil {
		t.Fatalf("marshal valid commit: %v", err)
	}
	if strings.Contains(string(encoded), "ArchivePath") || strings.Contains(string(encoded), "review.tar") {
		t.Fatalf("commit JSON contains private archive path: %s", encoded)
	}
	var decoded SealCommit
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal valid commit: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped commit is invalid: %v", err)
	}
}

func TestCandidateOutputRequiresPrivateArchivePath(t *testing.T) {
	digest := mustTestDigest(t)
	candidate := CandidateOutput{
		Port: Port{Name: "out", Type: TypeRef("opaque/v1")}, Digest: digest,
		ByteSize: 1, Representation: "application/x-tar",
	}
	if err := candidate.Validate(); err == nil {
		t.Fatal("CandidateOutput.Validate accepted an empty ArchivePath")
	}
}

func TestSealRequestValidatesDeclaredRequiredAndOptionalOutputs(t *testing.T) {
	requiredA := Port{Name: "first", Type: TypeRef("opaque/v1")}
	requiredB := Port{Name: "second", Type: TypeRef("review/v1")}
	optional := Port{Name: "notes", Type: TypeRef("opaque/v1"), Optional: true}
	source := func(port Port) OutputSource {
		return OutputSource{
			ClientKey: port.Name,
			Port:      port,
			OpenTar: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("tar")), nil
			},
		}
	}
	base := SealRequest{
		BuildID: 1, TeamID: 1, TeamName: "main", CreatedBy: "alice", PlanID: "p",
		Attempt: "1", StepKind: "task", StepName: "s", Inputs: map[string]SnapshotRef{}, InputOrder: []string{},
		OutputDeclarations: []Port{requiredA, optional, requiredB},
		Outputs:            []OutputSource{source(requiredA), source(requiredB)},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("required outputs with absent optional output: %v", err)
	}
	context, err := base.CommitContext()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := context.ExpectedOutputs, []Port{requiredA, requiredB}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected outputs = %#v, want actual candidates %#v", got, want)
	}

	withOptional := base.Clone()
	withOptional.Outputs = append(withOptional.Outputs, source(optional))
	if err := withOptional.Validate(); err != nil {
		t.Fatalf("present optional output: %v", err)
	}
	context, err = withOptional.CommitContext()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := context.ExpectedOutputs, []Port{requiredA, requiredB, optional}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected outputs with optional = %#v, want %#v", got, want)
	}
	onlyOptional := base.Clone()
	onlyOptional.OutputDeclarations = []Port{optional}
	onlyOptional.Outputs = nil
	if err := onlyOptional.Validate(); err != nil {
		t.Fatalf("all-optional absent output set: %v", err)
	}
	context, err = onlyOptional.CommitContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(context.ExpectedOutputs) != 0 {
		t.Fatalf("absent optional output unexpectedly committed: %#v", context.ExpectedOutputs)
	}
	if err := (SealCommit{Context: context}).Validate(); err != nil {
		t.Fatalf("empty actual commit for absent optional output: %v", err)
	}

	for name, mutate := range map[string]func(*SealRequest){
		"missing required": func(request *SealRequest) { request.Outputs = request.Outputs[:1] },
		"undeclared": func(request *SealRequest) {
			request.Outputs[1].Port = Port{Name: "extra", Type: TypeRef("review/v1")}
		},
		"type mismatch": func(request *SealRequest) { request.Outputs[1].Port.Type = TypeRef("opaque/v1") },
		"optionality mismatch": func(request *SealRequest) {
			request.Outputs = append(request.Outputs, source(Port{Name: optional.Name, Type: optional.Type}))
		},
		"duplicate candidate": func(request *SealRequest) { request.Outputs = append(request.Outputs, request.Outputs[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			request := base.Clone()
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("SealRequest.Validate accepted %s", name)
			}
		})
	}
}

func TestSealCommitRequiresEveryActualOutputExactlyOnce(t *testing.T) {
	digest := mustTestDigest(t)
	commit := validSealCommit(t, digest)
	second := commit.Outputs[0].Clone()
	second.ClientKey = "second"
	second.Port = Port{Name: "second", Type: TypeRef("review/v1")}
	second.StagedUploadID = 100
	commit.Context.ExpectedOutputs = []Port{commit.Outputs[0].Port, second.Port}

	if err := commit.Validate(); err == nil {
		t.Fatal("two-required-output context accepted a one-output commit")
	}
	commit.Outputs = append(commit.Outputs, second)
	commit.Outputs[0], commit.Outputs[1] = commit.Outputs[1], commit.Outputs[0]
	if err := commit.Validate(); err != nil {
		t.Fatalf("exact output permutation: %v", err)
	}

	missing := commit.Clone()
	missing.Outputs = missing.Outputs[:1]
	if err := missing.Validate(); err == nil {
		t.Fatal("commit accepted a missing expected output")
	}
	extra := commit.Clone()
	extraOutput := extra.Outputs[0].Clone()
	extraOutput.ClientKey = "extra"
	extraOutput.Port.Name = "extra"
	extra.Outputs = append(extra.Outputs, extraOutput)
	if err := extra.Validate(); err == nil {
		t.Fatal("commit accepted an extra output")
	}
}

func TestSealCommitRejectsDuplicateWorkflowPorts(t *testing.T) {
	digest := mustTestDigest(t)
	commit := validSealCommit(t, digest)
	first := &commit.Outputs[0]
	first.WorkflowPort = "review"
	first.Retention = []RetentionSpec{{Class: RetentionClassWorkflow, Actor: "workflow:first", Reason: "durable workflow-run output"}}

	second := first.Clone()
	second.ClientKey = "second"
	second.Port.Name = "second"
	second.StagedUploadID = 100
	commit.Outputs = append(commit.Outputs, second)
	commit.Context.ExpectedOutputs = append(commit.Context.ExpectedOutputs, second.Port)

	if err := commit.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate workflow port") {
		t.Fatalf("SealCommit.Validate() error = %v, want duplicate workflow-port rejection", err)
	}
}

func TestCandidateAndSealRequestAreInternalOnlyJSONValues(t *testing.T) {
	digest := mustTestDigest(t)
	candidate := CandidateOutput{
		Port: Port{Name: "out", Type: TypeRef("opaque/v1")}, ArchivePath: "/private/out.tar",
		Digest: digest, ByteSize: 1, Representation: "application/x-tar",
	}
	request := SealRequest{
		BuildID: 1, TeamID: 1, TeamName: "main", CreatedBy: "alice", PlanID: "p",
		Attempt: "1", StepKind: "task", StepName: "s", InputOrder: []string{}, Inputs: map[string]SnapshotRef{},
		OutputDeclarations: []Port{candidate.Port}, Outputs: []OutputSource{{
			ClientKey: "out", Port: candidate.Port,
			OpenTar: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("tar")), nil },
		}},
	}
	for name, value := range map[string]any{"candidate": candidate, "seal request": request} {
		t.Run(name+" marshal", func(t *testing.T) {
			if _, err := json.Marshal(value); !errors.Is(err, ErrInternalOnly) {
				t.Fatalf("json.Marshal error = %v, want ErrInternalOnly", err)
			}
		})
	}
	var decodedCandidate CandidateOutput
	if err := json.Unmarshal([]byte(`{}`), &decodedCandidate); !errors.Is(err, ErrInternalOnly) {
		t.Fatalf("candidate json.Unmarshal error = %v, want ErrInternalOnly", err)
	}
	var decodedRequest SealRequest
	if err := json.Unmarshal([]byte(`{}`), &decodedRequest); !errors.Is(err, ErrInternalOnly) {
		t.Fatalf("request json.Unmarshal error = %v, want ErrInternalOnly", err)
	}
}

func TestSealCommitEnforcesPhysicalAndStageDigestConsistency(t *testing.T) {
	digest := mustTestDigest(t)
	other, err := ParseDigest("sha256:1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	base := validSealCommit(t, digest)
	second := base.Outputs[0].Clone()
	second.ClientKey = "second"
	second.Port.Name = "second"
	base.Outputs = append(base.Outputs, second)
	base.Context.ExpectedOutputs = append(base.Context.ExpectedOutputs, second.Port)

	tests := map[string]func(*SealCommit){
		"byte size":      func(commit *SealCommit) { commit.Outputs[1].ByteSize++ },
		"file count":     func(commit *SealCommit) { commit.Outputs[1].FileCount++ },
		"representation": func(commit *SealCommit) { commit.Outputs[1].Representation = "application/zip" },
		"stage digest": func(commit *SealCommit) {
			commit.Outputs[1].Digest = other
			for i := range commit.Outputs[1].Locations {
				commit.Outputs[1].Locations[i].Digest = other
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			commit := base.Clone()
			mutate(&commit)
			if err := commit.Validate(); err == nil {
				t.Fatalf("SealCommit.Validate accepted inconsistent %s", name)
			}
		})
	}
}

func TestSealCommitRejectsConflictingBatchIntrinsicMetadata(t *testing.T) {
	digest := mustTestDigest(t)
	base := validSealCommit(t, digest)
	second := base.Outputs[0].Clone()
	second.ClientKey = "second"
	second.Port.Name = "second"
	second.StagedUploadID = 100
	base.Outputs[0].IntrinsicMetadata = json.RawMessage(`{"schema":1,"format":"tar"}`)
	second.IntrinsicMetadata = cloneRaw(base.Outputs[0].IntrinsicMetadata)
	base.Outputs = append(base.Outputs, second)
	base.Context.ExpectedOutputs = append(base.Context.ExpectedOutputs, second.Port)

	conflicting := base.Clone()
	conflicting.Outputs[1].IntrinsicMetadata = json.RawMessage(`{"schema":2,"format":"tar"}`)
	if err := conflicting.Validate(); err == nil {
		t.Fatal("SealCommit.Validate accepted conflicting intrinsic metadata for one type and digest")
	}

	equivalent := base.Clone()
	equivalent.Outputs[1].IntrinsicMetadata = json.RawMessage("{\n  \"format\" : \"tar\",\n  \"schema\" : 1\n}")
	if err := equivalent.Validate(); err != nil {
		t.Fatalf("semantically equivalent intrinsic metadata: %v", err)
	}

	differentType := base.Clone()
	differentType.Outputs[1].Port.Type = TypeRef("review/v1")
	differentType.Context.ExpectedOutputs[1].Type = TypeRef("review/v1")
	differentType.Outputs[1].IntrinsicMetadata = json.RawMessage(`{"schema":2}`)
	if err := differentType.Validate(); err != nil {
		t.Fatalf("different types sharing one digest may have different intrinsic metadata: %v", err)
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
		OutputDeclarations: []Port{{Name: "out", Type: TypeRef("opaque/v1")}},
		Outputs: []OutputSource{{
			ClientKey: "out", Port: Port{Name: "out", Type: TypeRef("opaque/v1")},
			OpenTar: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("tar")), nil },
		}},
	}
	for name, order := range map[string][]string{
		"missing": {"first"}, "duplicate": {"first", "first"}, "unknown": {"first", "third"}, "whitespace order": {"first", " "},
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
	request = base.Clone()
	request.Inputs[" "] = request.Inputs["second"]
	delete(request.Inputs, "second")
	request.InputOrder = []string{"first", " "}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate accepted a whitespace Inputs map key")
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

func TestWithDigestLeaseClosesPartialLeaseReturnedWithAcquireError(t *testing.T) {
	digest := mustTestDigest(t)
	acquireErr := errors.New("second digest lock failed")
	closeErr := errors.New("partial lease close failed")
	events := []string{}
	lease := &recordingLease{covered: map[Digest]bool{digest: true}, events: &events, closeErr: closeErr}
	manager := &recordingLockManager{lease: lease, acquireErr: acquireErr}
	called := false
	err := WithDigestLease(context.Background(), manager, []Digest{digest}, func(DigestLease) error {
		called = true
		return nil
	})
	if called || !errors.Is(err, acquireErr) || !errors.Is(err, closeErr) {
		t.Fatalf("callback=%t error=%v, want joined acquire/close errors", called, err)
	}
	if want := []string{"close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDigestStateAggregatesSemanticSnapshotsSharingPhysicalBytes(t *testing.T) {
	digest := mustTestDigest(t)
	available := DigestState{
		Digest: digest,
		Snapshots: []Snapshot{
			{ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: 1, FileCount: 2, Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: time.Now()},
			{ID: 2, Type: TypeRef("review/v1"), Digest: digest, ByteSize: 1, FileCount: 2, Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: time.Now()},
		},
		Locations:          []Location{{Digest: digest, Driver: "driver", Key: "key"}},
		HasActiveRetention: true,
	}
	if err := available.Validate(); err != nil {
		t.Fatalf("validate aggregate digest state: %v", err)
	}
	if !available.HasManifest() || !available.Available() || !available.Reusable() {
		t.Fatalf("available state predicates were false: %#v", available)
	}

	withoutLocation := available.Clone()
	withoutLocation.Locations = nil
	if !withoutLocation.Available() || withoutLocation.Reusable() {
		t.Fatalf("location-free state available=%t reusable=%t", withoutLocation.Available(), withoutLocation.Reusable())
	}
	expired := available.Clone()
	for i := range expired.Snapshots {
		expired.Snapshots[i].ContentState = ContentStateExpired
	}
	if !expired.HasManifest() || expired.Available() || expired.Reusable() {
		t.Fatalf("expired state predicates are unsafe: %#v", expired)
	}

	for name, mutate := range map[string]func(*DigestState){
		"byte size":      func(state *DigestState) { state.Snapshots[1].ByteSize++ },
		"file count":     func(state *DigestState) { state.Snapshots[1].FileCount++ },
		"representation": func(state *DigestState) { state.Snapshots[1].Representation = "application/zip" },
		"content state":  func(state *DigestState) { state.Snapshots[1].ContentState = ContentStateExpired },
	} {
		t.Run(name, func(t *testing.T) {
			state := available.Clone()
			mutate(&state)
			if err := state.Validate(); err == nil {
				t.Fatalf("DigestState.Validate accepted contradictory %s", name)
			}
			if state.Available() || state.Reusable() {
				t.Fatalf("contradictory state reported available/reusable: %#v", state)
			}
		})
	}

	now := time.Now()
	expirable := available.Clone()
	expirable.HasActiveRetention = false
	expirable.Locations = nil
	expirable.Stages = []StagedUpload{{
		ID: 1, Digest: digest, TeamID: 1, Attempt: "1", CreatedAt: now.Add(-time.Hour), LeaseExpiresAt: now.Add(time.Hour),
	}}
	if expirable.CanExpire(now) {
		t.Fatal("digest with an unexpired sibling stage can expire")
	}
	expirable.Stages[0].LeaseExpiresAt = now
	if !expirable.CanExpire(now) {
		t.Fatal("unretained digest with no locations and no unexpired stage cannot expire")
	}
}

func TestDigestStateValidatesCommitAgainstPersistedSemanticManifests(t *testing.T) {
	digest := mustTestDigest(t)
	output := validSealCommit(t, digest).Outputs[0]
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	expired := DigestState{
		Digest: digest,
		Snapshots: []Snapshot{{
			ID: 1, Type: output.Port.Type, Digest: digest,
			ByteSize: output.ByteSize, FileCount: output.FileCount, Representation: output.Representation,
			IntrinsicMetadata: cloneRaw(output.IntrinsicMetadata), ContentState: ContentStateExpired, CreatedAt: createdAt,
		}},
	}
	if err := expired.ValidateCommit(output); err != nil || !expired.Accepts(output) {
		t.Fatalf("coherent expired reseal rejected: %v", err)
	}

	for name, mutate := range map[string]func(*SealCommitOutput){
		"byte size":      func(candidate *SealCommitOutput) { candidate.ByteSize++ },
		"file count":     func(candidate *SealCommitOutput) { candidate.FileCount++ },
		"representation": func(candidate *SealCommitOutput) { candidate.Representation = "application/zip" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := output.Clone()
			mutate(&candidate)
			if err := expired.ValidateCommit(candidate); err == nil || expired.Accepts(candidate) {
				t.Fatalf("persisted digest accepted conflicting %s", name)
			}
		})
	}

	sameTypeConflict := output.Clone()
	sameTypeConflict.IntrinsicMetadata = json.RawMessage(`{"schema":2}`)
	if err := expired.ValidateCommit(sameTypeConflict); err == nil || expired.Accepts(sameTypeConflict) {
		t.Fatal("identical type/digest accepted conflicting intrinsic metadata")
	}

	differentType := expired.Clone()
	differentType.Snapshots[0].Type = TypeRef("review/v1")
	differentType.Snapshots[0].IntrinsicMetadata = json.RawMessage(`{"review_schema":99}`)
	if err := differentType.ValidateCommit(output); err != nil || !differentType.Accepts(output) {
		t.Fatalf("different semantic type intrinsic metadata was not independent: %v", err)
	}
}

func TestLifecyclePageRequestIsBoundedAndHasStableTermination(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxLifecyclePageSize + 1} {
		if err := (LifecyclePageRequest{Limit: limit}).Validate(); err == nil {
			t.Fatalf("LifecyclePageRequest accepted limit %d", limit)
		}
	}
	if err := (LifecyclePageRequest{Limit: MaxLifecyclePageSize}).Validate(); err != nil {
		t.Fatalf("max lifecycle page: %v", err)
	}
	if err := (LifecyclePageRequest{After: "not-a-cursor", Limit: 1}).Validate(); err == nil {
		t.Fatal("LifecyclePageRequest accepted an invalid cursor")
	}
	page := LifecycleCandidatePage{}
	if !page.Terminal() {
		t.Fatal("empty Next cursor must terminate discovery")
	}

	digest := mustTestDigest(t)
	page = LifecycleCandidatePage{
		Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}},
		Next:       LifecycleCursor(testDigestText + "|expiry"),
	}
	if err := page.Validate(LifecyclePageRequest{Limit: 1}); err != nil {
		t.Fatalf("valid ordered lifecycle page: %v", err)
	}
	page = LifecycleCandidatePage{
		Candidates: []LifecycleCandidate{
			{Digest: digest, Kind: LifecycleCandidateRepair},
			{Digest: digest, Kind: LifecycleCandidateExpiry},
		},
	}
	if err := page.Validate(LifecyclePageRequest{Limit: 2}); err == nil {
		t.Fatal("lifecycle page accepted candidates outside deterministic cursor order")
	}
}

func TestContentStoreOpenKeepsFrozenSignature(t *testing.T) {
	method, found := reflect.TypeOf((*ContentStore)(nil)).Elem().MethodByName("Open")
	if !found {
		t.Fatal("ContentStore.Open is missing")
	}
	if got := method.Type.NumIn(); got != 2 {
		t.Fatalf("ContentStore.Open has %d inputs, want context.Context and Snapshot", got)
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

func validSealCommit(t *testing.T, digest Digest) SealCommit {
	t.Helper()
	return SealCommit{
		Context: SealCommitContext{
			BuildID: 1, TeamID: 1, TeamName: "main", CreatedBy: "alice", PlanID: "p",
			Attempt: "1", StepKind: "task", StepName: "s", InputOrder: []string{}, Inputs: map[string]SnapshotRef{},
			ExpectedOutputs: []Port{{Name: "first", Type: TypeRef("opaque/v1")}},
		},
		Outputs: []SealCommitOutput{{
			ClientKey: "first", Port: Port{Name: "first", Type: TypeRef("opaque/v1")}, Digest: digest,
			ByteSize: 10, FileCount: 1, Representation: "application/x-tar", IntrinsicMetadata: json.RawMessage(`{"schema":1}`),
			StagedUploadID: 99,
			Locations:      []Location{{Digest: digest, Driver: "driver", Key: "key"}},
			Retention:      []RetentionSpec{{Class: RetentionClassBinding, Reason: "output"}},
			SourceMetadata: json.RawMessage(`{"source":"test"}`),
		}},
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
	lease      DigestLease
	acquired   []Digest
	acquireErr error
}

func (m *recordingLockManager) AcquireMany(_ context.Context, digests []Digest) (DigestLease, error) {
	m.acquired = append([]Digest(nil), digests...)
	return m.lease, m.acquireErr
}
