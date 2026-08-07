package workflowprovenance

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestFromPlanCapturesPublishSnapshotAsAVisibleNonProducer(t *testing.T) {
	config := &atc.PublishSnapshotStep{
		Name: "publish-change", Publisher: publisher.GitPublisher, Input: "change",
		InputType: snapshot.TypeRef("repository-change/v1"), Destination: "github.example/team/repo",
		Mode: publisher.ModeBranch, Parameters: map[string]string{
			"source_branch": "agent/change", "target_branch": "main",
		},
		ApprovalPolicyVersion: "engineering/v2",
	}
	input := captureInput(t, 41, 9, atc.Step{Config: config})
	plan := atc.Plan{ID: "publish", PublishSnapshot: &atc.PublishSnapshotPlan{
		Name: config.Name, Publisher: config.Publisher, Input: config.Input, InputType: config.InputType,
		Destination: config.Destination, Mode: config.Mode, Parameters: config.Parameters,
		ApprovalPolicyVersion: config.ApprovalPolicyVersion,
	}}

	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	if len(captured.Outputs) != 0 {
		t.Fatalf("publish outputs = %#v, want none", captured.Outputs)
	}
	if !strings.Contains(string(captured.CanonicalPlan), `"publish_snapshot"`) {
		t.Fatalf("canonical plan does not retain the visible publisher: %s", captured.CanonicalPlan)
	}

	plan.PublishSnapshot.InputType = snapshot.TypeRef("repository/v1")
	if _, err := FromPlan(input, plan); err == nil || !strings.Contains(err.Error(), "requires repository-change/v1") {
		t.Fatalf("invalid publisher input error = %v", err)
	}
}

func TestFromPlanBindsMergePublisherToFrozenWorkflowRun(t *testing.T) {
	config := &atc.PublishSnapshotStep{
		Name: "merge-change", Publisher: publisher.GitPublisher, Input: "change",
		InputType: snapshot.TypeRef("repository-change/v1"), Destination: "github.example/team/repo",
		Mode: publisher.ModeMerge, Parameters: map[string]string{"target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2", Approval: "approval",
		WorkflowRunID: "((workflow_run_id))",
	}
	input := captureInput(t, 41, 9, atc.Step{Config: config})
	plan := atc.Plan{ID: "merge", PublishSnapshot: &atc.PublishSnapshotPlan{
		Name: config.Name, Publisher: config.Publisher, Input: config.Input, InputType: config.InputType,
		Destination: config.Destination, Mode: config.Mode, Parameters: config.Parameters,
		ApprovalPolicyVersion: config.ApprovalPolicyVersion, Approval: config.Approval, WorkflowRunID: "9",
	}}
	if _, err := FromPlan(input, plan); err != nil {
		t.Fatalf("FromPlan valid merge: %v", err)
	}
	plan.PublishSnapshot.WorkflowRunID = "10"
	if _, err := FromPlan(input, plan); err == nil || !strings.Contains(err.Error(), "workflow run identity") {
		t.Fatalf("mismatched run error = %v", err)
	}
	plan.PublishSnapshot.WorkflowRunID = ""
	if _, err := FromPlan(input, plan); err == nil || !strings.Contains(err.Error(), "workflow run") {
		t.Fatalf("missing run error = %v", err)
	}
}
