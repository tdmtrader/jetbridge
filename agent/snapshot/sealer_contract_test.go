package snapshot

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestSealRequestValidatesProcessLocalOutputSources(t *testing.T) {
	workflowDefinitionID := 7
	workflowRunID := WorkflowRunID(9)
	openTar := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("tar")), nil
	}
	request := SealRequest{
		BuildID: 1, TeamID: 2, TeamName: "main", CreatedBy: "concourse",
		PlanID: "plan", Attempt: "1", StepKind: "task", StepName: "produce",
		WorkflowDefinitionID: &workflowDefinitionID,
		WorkflowRunID:        &workflowRunID,
		OutputDeclarations: []Port{
			{Name: "change", Type: TypeRef("repository-change/v1")},
			{Name: "review", Type: TypeRef("review/v1"), Optional: true},
		},
		Outputs: []OutputSource{{
			ClientKey:    "mapped-change",
			Port:         Port{Name: "change", Type: TypeRef("repository-change/v1")},
			OpenTar:      openTar,
			Retention:    RetentionClassWorkflow,
			WorkflowPort: "result",
		}},
	}

	if err := request.Validate(); err != nil {
		t.Fatalf("SealRequest.Validate() error = %v", err)
	}

	clone := request.Clone()
	clone.Outputs[0].SourceMetadata = []byte(`{"changed":true}`)
	clone.Outputs[0].Port.Name = "changed"
	if request.Outputs[0].Port.Name != "change" || len(request.Outputs[0].SourceMetadata) != 0 {
		t.Fatalf("SealRequest.Clone() aliased output source: %#v", request.Outputs[0])
	}
	if clone.Outputs[0].OpenTar == nil {
		t.Fatal("SealRequest.Clone() dropped process-local tar opener")
	}
}

func TestSealRequestValidatesDeclaredCandidateInputs(t *testing.T) {
	workflowDefinitionID := 7
	workflowRunID := WorkflowRunID(9)
	openTar := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("tar")), nil
	}
	digest := mustTestDigest(t)
	base := SealRequest{
		BuildID: 1, TeamID: 2, TeamName: "main", CreatedBy: "concourse",
		PlanID: "plan", Attempt: "1", StepKind: "agent", StepName: "judge",
		WorkflowDefinitionID: &workflowDefinitionID,
		WorkflowRunID:        &workflowRunID,
		InputOrder:           []string{"left", "right", "base"},
		Inputs: map[string]SnapshotRef{
			"left":  {ID: 1, Type: TypeRef("repository-change/v1"), Digest: digest},
			"right": {ID: 2, Type: TypeRef("repository-change/v1"), Digest: digest},
			"base":  {ID: 3, Type: TypeRef("repository/v1"), Digest: digest},
		},
		CandidateInputs:    []string{"left", "right"},
		OutputDeclarations: []Port{{Name: "selection", Type: TypeRef("selection/v1")}},
		Outputs: []OutputSource{{
			ClientKey: "selection", Port: Port{Name: "selection", Type: TypeRef("selection/v1")},
			OpenTar: openTar,
		}},
	}

	if err := base.Validate(); err != nil {
		t.Fatalf("SealRequest.Validate() error = %v", err)
	}
	clone := base.Clone()
	clone.CandidateInputs[0] = "mutated"
	if base.CandidateInputs[0] != "left" {
		t.Fatalf("SealRequest.Clone() aliased candidate inputs: %q", base.CandidateInputs)
	}

	unexposed := base.Clone()
	unexposed.CandidateInputs = []string{"left", "ghost"}
	if err := unexposed.Validate(); err == nil || !strings.Contains(err.Error(), "candidate input") {
		t.Fatalf("SealRequest.Validate() error = %v, want unexposed candidate-input rejection", err)
	}

	duplicate := base.Clone()
	duplicate.CandidateInputs = []string{"left", "left"}
	if err := duplicate.Validate(); err == nil || !strings.Contains(err.Error(), "candidate input") {
		t.Fatalf("SealRequest.Validate() error = %v, want duplicate candidate-input rejection", err)
	}
}

func TestSealRequestRejectsPartialWorkflowAssociation(t *testing.T) {
	workflowDefinitionID := 7
	request := SealRequest{
		BuildID: 1, TeamID: 2, TeamName: "main", CreatedBy: "concourse",
		PlanID: "plan", Attempt: "1", StepKind: "task", StepName: "produce",
		WorkflowDefinitionID: &workflowDefinitionID,
		OutputDeclarations:   []Port{{Name: "change", Type: TypeRef("repository-change/v1")}},
		Outputs: []OutputSource{{
			ClientKey: "change", Port: Port{Name: "change", Type: TypeRef("repository-change/v1")},
			OpenTar: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("tar")), nil },
		}},
	}

	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "workflow definition and run") {
		t.Fatalf("SealRequest.Validate() error = %v, want partial-association rejection", err)
	}
}

func TestSealRequestRejectsDuplicateWorkflowPorts(t *testing.T) {
	workflowDefinitionID := 7
	workflowRunID := WorkflowRunID(9)
	openTar := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("tar")), nil
	}
	request := SealRequest{
		BuildID: 1, TeamID: 2, TeamName: "main", CreatedBy: "concourse",
		PlanID: "plan", Attempt: "1", StepKind: "task", StepName: "produce",
		WorkflowDefinitionID: &workflowDefinitionID,
		WorkflowRunID:        &workflowRunID,
		OutputDeclarations: []Port{
			{Name: "first", Type: TypeRef("review/v1")},
			{Name: "second", Type: TypeRef("review/v1")},
		},
		Outputs: []OutputSource{
			{ClientKey: "first", Port: Port{Name: "first", Type: TypeRef("review/v1")}, OpenTar: openTar, Retention: RetentionClassWorkflow, WorkflowPort: "review"},
			{ClientKey: "second", Port: Port{Name: "second", Type: TypeRef("review/v1")}, OpenTar: openTar, Retention: RetentionClassWorkflow, WorkflowPort: "review"},
		},
	}

	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate workflow port") {
		t.Fatalf("SealRequest.Validate() error = %v, want duplicate workflow-port rejection", err)
	}
}

func TestSealCommitOutputRequiresWorkflowRetentionExactlyWhenBound(t *testing.T) {
	digest := mustTestDigest(t)
	base := SealCommitOutput{
		ClientKey: "review", Port: Port{Name: "review", Type: TypeRef("review/v1")},
		Digest: digest, ByteSize: 1, Representation: "application/x-tar", StagedUploadID: 1,
		Locations: []Location{{Digest: digest, Driver: "driver", Key: "key"}},
		Retention: []RetentionSpec{{Class: RetentionClassBinding, Actor: "build", Reason: "build output"}},
	}

	boundWithoutWorkflowRetention := base.Clone()
	boundWithoutWorkflowRetention.WorkflowPort = "result"
	if err := boundWithoutWorkflowRetention.Validate(); err == nil {
		t.Fatal("SealCommitOutput.Validate() accepted a workflow binding without workflow retention")
	}

	workflowRetentionWithoutBinding := base.Clone()
	workflowRetentionWithoutBinding.Retention = append(workflowRetentionWithoutBinding.Retention, RetentionSpec{
		Class: RetentionClassWorkflow, Actor: "workflow", Reason: "durable workflow-run output",
	})
	if err := workflowRetentionWithoutBinding.Validate(); err == nil {
		t.Fatal("SealCommitOutput.Validate() accepted workflow retention without a workflow port")
	}

	valid := workflowRetentionWithoutBinding.Clone()
	valid.WorkflowPort = "result"
	if err := valid.Validate(); err != nil {
		t.Fatalf("SealCommitOutput.Validate() error = %v", err)
	}
}

func TestSealCommitRequiresRunRetentionToMatchProducingRun(t *testing.T) {
	digest := mustTestDigest(t)
	commit := validSealCommit(t, digest)
	definitionID := 7
	producingRunID := WorkflowRunID(8)
	otherRunID := WorkflowRunID(9)
	commit.Context.Build.WorkflowDefinitionID = &definitionID
	commit.Context.Build.WorkflowRunID = &producingRunID
	commit.Outputs[0].Retention = append(commit.Outputs[0].Retention, RetentionSpec{
		Class: RetentionClassRun, WorkflowRunID: &otherRunID,
		Actor: "forged-run", Reason: "active workflow-run internal output",
	})

	if err := commit.Validate(); err == nil || !strings.Contains(err.Error(), "exact producing workflow run") {
		t.Fatalf("SealCommit.Validate() error = %v, want mismatched run-retention rejection", err)
	}

	commit.Outputs[0].Retention[1].WorkflowRunID = &producingRunID
	if err := commit.Validate(); err != nil {
		t.Fatalf("SealCommit.Validate() rejected exact run retention: %v", err)
	}
}
