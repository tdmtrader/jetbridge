package workflowprovenance

import (
	"errors"
	"reflect"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestFromPlanCapturesAwaitSnapshotAsAnExactWorkflowOutput(t *testing.T) {
	wait := &atc.AwaitSnapshotStep{
		Name: "approval", Question: "question", Type: snapshot.TypeRef("human-answer/v1"),
		OnTimeout: atc.AwaitSnapshotOnTimeoutFail, WorkflowPort: "answer",
		WorkflowDefinitionID: 41, WorkflowRunID: "9",
	}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.TimeoutStep{
		Step: wait, Duration: "24h",
	}})
	plan := atc.Plan{ID: "timeout", Timeout: &atc.TimeoutPlan{
		Duration: "24h",
		Step: atc.Plan{ID: "await", AwaitSnapshot: &atc.AwaitSnapshotPlan{
			Name: wait.Name, Question: wait.Question, Type: wait.Type,
			OnTimeout: wait.OnTimeout, WorkflowPort: wait.WorkflowPort,
			WorkflowDefinitionID: wait.WorkflowDefinitionID, WorkflowRunID: wait.WorkflowRunID,
		}},
	}}

	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	want := []ExpectedOutput{{
		Port: "answer", Type: snapshot.TypeRef("human-answer/v1"),
		WorkflowDefinitionID: 41, WorkflowRunID: snapshot.WorkflowRunID(9),
		Producers: []ExpectedProducer{{
			PlanID: "await", StepKind: "await_snapshot", StepName: "approval", LocalOutputPort: "approval",
		}},
	}}
	if !reflect.DeepEqual(captured.Outputs, want) {
		t.Fatalf("outputs = %#v, want %#v", captured.Outputs, want)
	}

	plan.Timeout.Step.AwaitSnapshot.WorkflowRunID = "10"
	if _, err := FromPlan(input, plan); !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("wrong workflow run error = %v, want invalid provenance", err)
	}
	plan.Timeout.Step.AwaitSnapshot.WorkflowRunID = "9"
	plan.Timeout.Step.AwaitSnapshot.Type = snapshot.TypeRef("review/v1")
	if _, err := FromPlan(input, plan); !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("wrong answer type error = %v, want invalid provenance", err)
	}
}
