package workflow

import (
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestApplyNodeInvocationMapsTaskSnapshotDeclarationsToPhysicalArtifacts(t *testing.T) {
	step := atc.Step{Config: &atc.TaskStep{
		SnapshotInputs:  map[string]atc.SnapshotInputConfig{"repository": {Type: snapshot.TypeRef("repository/v1")}},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"review": {Type: snapshot.TypeRef("review/v1")}},
	}}
	if err := applyNodeInvocation(&step, NodeReference{
		InstanceName:  "review-change",
		InputMapping:  map[string]string{"repository": "checked-out-repository"},
		OutputMapping: map[string]string{"review": "review-result"},
	}); err != nil {
		t.Fatal(err)
	}
	task := step.Config.(*atc.TaskStep)
	if _, found := task.SnapshotInputs["repository"]; found {
		t.Fatalf("logical task input declaration remained: %#v", task.SnapshotInputs)
	}
	if _, found := task.SnapshotOutputs["review"]; found {
		t.Fatalf("logical task output declaration remained: %#v", task.SnapshotOutputs)
	}
	if task.SnapshotInputs["checked-out-repository"].Type != "repository/v1" || task.SnapshotOutputs["review-result"].Type != "review/v1" {
		t.Fatalf("mapped task declarations = %#v %#v", task.SnapshotInputs, task.SnapshotOutputs)
	}
}

func TestApplyNodeInvocationOnlyRewritesBakedPublicationInput(t *testing.T) {
	step := atc.Step{Config: &atc.PublishSnapshotStep{
		Name: "publish", Publisher: "publisher/v1", Input: "change", InputType: "repository-change/v1",
		Destination: "main", Mode: publisher.ModeMerge, Parameters: map[string]string{"strategy": "squash"}, ApprovalPolicyVersion: "v1", Approval: "approval",
	}}
	if err := applyNodeInvocation(&step, NodeReference{InstanceName: "publish-change", InputMapping: map[string]string{"change": "validated-change"}}); err != nil {
		t.Fatal(err)
	}
	publish := step.Config.(*atc.PublishSnapshotStep)
	if publish.Name != "publish-change" || publish.Input != "validated-change" || publish.Destination != "main" || publish.Mode != publisher.ModeMerge || publish.Parameters["strategy"] != "squash" {
		t.Fatalf("publication authority changed: %#v", publish)
	}
	for _, ref := range []NodeReference{
		{InputMapping: map[string]string{"change": "validated-change"}, OutputMapping: map[string]string{"output": "forbidden"}},
		{InputMapping: map[string]string{"change": "validated-change"}, Parameters: map[string]string{"strategy": "override"}},
	} {
		if err := applyNodeInvocation(&atc.Step{Config: publish}, ref); err == nil {
			t.Fatal("expected publication invocation rejection")
		}
	}
}
