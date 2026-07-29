package checkpointfakes_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/checkpoint/checkpointfakes"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestFakeStoreDefensivelyCopiesCheckpointBoundaryValues(t *testing.T) {
	runID := snapshot.WorkflowRunID(41)
	identity := checkpoint.Identity{WorkflowRunID: &runID, BuildID: 42, PlanID: "plan", FunctionID: "function"}
	fence := checkpoint.FenceClaim{ExecutionAttempt: 1, Token: "11111111-1111-1111-1111-111111111111"}
	fake := new(checkpointfakes.FakeStore)
	fake.SetBeginResult(checkpoint.StagedCheckpoint{ID: 9, Identity: identity, Fence: fence}, nil)

	returned, err := fake.Begin(context.Background(), checkpoint.BeginRequest{Identity: identity, Fence: fence})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	*returned.Identity.WorkflowRunID = 99
	*identity.WorkflowRunID = 77

	if got := fake.BeginCalls()[0].Request.Identity.WorkflowRunID; got == nil || *got != 41 {
		t.Fatalf("recorded identity leaked caller mutation: %#v", got)
	}
	second, err := fake.Begin(context.Background(), checkpoint.BeginRequest{Identity: checkpoint.Identity{WorkflowRunID: &runID, BuildID: 42, PlanID: "plan", FunctionID: "function"}, Fence: fence})
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if second.Identity.WorkflowRunID == nil || *second.Identity.WorkflowRunID != 41 {
		t.Fatalf("stored result leaked returned mutation: %#v", second.Identity.WorkflowRunID)
	}
}
