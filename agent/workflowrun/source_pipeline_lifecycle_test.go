package workflowrun

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/db"
)

func TestSourcePipelineLifecycleReconcilesOneAuthoritativePhasePerPass(t *testing.T) {
	store := &sourcePipelineLifecycleStoreStub{
		pipelines: []db.AgentWorkflowResourceSourcePipelineLifecycle{
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(101, db.AgentWorkflowResourceSourcePipelineActive), Paused: true},
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(102, db.AgentWorkflowResourceSourcePipelineActive)},
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(201, db.AgentWorkflowResourceSourcePipelineDraining), InFlightBuilds: 1},
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(202, db.AgentWorkflowResourceSourcePipelineDraining)},
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(203, db.AgentWorkflowResourceSourcePipelineDraining), Paused: true},
			{AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(301, db.AgentWorkflowResourceSourcePipelineArchived), Paused: true, Archived: true},
		},
	}
	lifecycle, err := NewSourcePipelineLifecycle(7, store)
	if err != nil {
		t.Fatalf("construct lifecycle: %v", err)
	}

	if err := lifecycle.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if store.listTeamID != 7 {
		t.Fatalf("listed team = %d, want trusted team 7", store.listTeamID)
	}
	assertIntSlice(t, store.unpaused, []int{101})
	assertIntSlice(t, store.paused, []int{202})
	assertIntSlice(t, store.archived, []int{203})
}

func TestSourcePipelineLifecycleWaitsForNonterminalAdmissionsBeforeArchiving(t *testing.T) {
	store := &sourcePipelineLifecycleStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipelineLifecycle{{
		AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(203, db.AgentWorkflowResourceSourcePipelineDraining),
		Paused:                              true, NonterminalAdmissions: 1,
	}}}
	lifecycle, err := NewSourcePipelineLifecycle(7, store)
	if err != nil {
		t.Fatalf("construct lifecycle: %v", err)
	}

	if err := lifecycle.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertIntSlice(t, store.archived, nil)
}

func TestSourcePipelineLifecycleFailsClosedOnStoreFailure(t *testing.T) {
	want := errors.New("config drift")
	store := &sourcePipelineLifecycleStoreStub{
		pipelines: []db.AgentWorkflowResourceSourcePipelineLifecycle{{
			AgentWorkflowResourceSourcePipeline: sourcePipelineLifecycleRecord(101, db.AgentWorkflowResourceSourcePipelineActive),
			Paused:                              true,
		}},
		unpauseErr: want,
	}
	lifecycle, err := NewSourcePipelineLifecycle(7, store)
	if err != nil {
		t.Fatalf("construct lifecycle: %v", err)
	}

	if err := lifecycle.Reconcile(context.Background()); !errors.Is(err, want) {
		t.Fatalf("reconcile error = %v, want %v", err, want)
	}
	assertIntSlice(t, store.unpaused, []int{101})
}

func TestSourcePipelineLifecycleConvergesBindingPauseResumeAndTerminalDrain(t *testing.T) {
	store := &sourcePipelineLifecycleStoreStub{
		pipelines: []db.AgentWorkflowResourceSourcePipelineLifecycle{
			{
				AgentWorkflowResourceSourcePipeline: bindingSourcePipelineLifecycleRecord(401),
			},
			{
				AgentWorkflowResourceSourcePipeline: bindingSourcePipelineLifecycleRecord(402),
				Paused:                              true,
			},
			{
				AgentWorkflowResourceSourcePipeline: bindingSourcePipelineLifecycleRecord(403),
			},
			{
				AgentWorkflowResourceSourcePipeline: bindingSourcePipelineLifecycleRecord(404),
			},
		},
		bindingPause:   map[int]bool{401: true},
		bindingUnpause: map[int]bool{402: true},
		bindingDrain:   map[int]bool{403: true, 404: true},
	}
	lifecycle, err := NewSourcePipelineLifecycle(7, store)
	if err != nil {
		t.Fatal(err)
	}

	if err := lifecycle.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIntSlice(t, store.bindingPaused, []int{401})
	assertIntSlice(t, store.bindingUnpaused, []int{402})
	assertIntSlice(t, store.bindingDrained, []int{403, 404})
	if len(store.paused) != 0 || len(store.unpaused) != 0 {
		t.Fatalf(
			"binding pipelines reached ordinary lifecycle: pause %#v unpause %#v",
			store.paused, store.unpaused,
		)
	}
}

func sourcePipelineLifecycleRecord(pipelineID int, state db.AgentWorkflowResourceSourcePipelineState) db.AgentWorkflowResourceSourcePipeline {
	return db.AgentWorkflowResourceSourcePipeline{
		PipelineID: pipelineID, TeamID: 7, WorkflowDefinitionID: pipelineID + 1000,
		WorkflowName: "workflow", WorkflowVersion: 1, PipelineConfigVersion: 19,
		ConfigHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceDeclarations: []db.ResourceSourceDeclaration{{
			SourceName: "repository-source", ResourceName: "repository", SnapshotType: "repository/v1",
		}},
		State: state,
	}
}

func bindingSourcePipelineLifecycleRecord(
	pipelineID int,
) db.AgentWorkflowResourceSourcePipeline {
	record := sourcePipelineLifecycleRecord(
		pipelineID, db.AgentWorkflowResourceSourcePipelineActive,
	)
	bindingID := int64(pipelineID + 1000)
	record.PRBindingID = &bindingID
	return record
}

type sourcePipelineLifecycleStoreStub struct {
	pipelines                  []db.AgentWorkflowResourceSourcePipelineLifecycle
	listTeamID                 int
	unpaused, paused, archived []int
	bindingPaused              []int
	bindingUnpaused            []int
	bindingDrained             []int
	bindingPause               map[int]bool
	bindingUnpause             map[int]bool
	bindingDrain               map[int]bool
	unpauseErr                 error
}

func (store *sourcePipelineLifecycleStoreStub) ResourceSourcePipelineLifecycle(_ context.Context, teamID int) ([]db.AgentWorkflowResourceSourcePipelineLifecycle, error) {
	store.listTeamID = teamID
	return append([]db.AgentWorkflowResourceSourcePipelineLifecycle(nil), store.pipelines...), nil
}
func (store *sourcePipelineLifecycleStoreStub) UnpauseActiveResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	store.unpaused = append(store.unpaused, pipeline.PipelineID)
	return store.unpauseErr == nil, store.unpauseErr
}
func (store *sourcePipelineLifecycleStoreStub) PauseDrainedResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	store.paused = append(store.paused, pipeline.PipelineID)
	return true, nil
}
func (store *sourcePipelineLifecycleStoreStub) ArchiveDrainedResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	store.archived = append(store.archived, pipeline.PipelineID)
	return true, nil
}
func (store *sourcePipelineLifecycleStoreStub) PauseActiveBindingResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	if !store.bindingPause[pipeline.PipelineID] {
		return false, nil
	}
	store.bindingPaused = append(store.bindingPaused, pipeline.PipelineID)
	return true, nil
}
func (store *sourcePipelineLifecycleStoreStub) UnpauseActiveBindingResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	if !store.bindingUnpause[pipeline.PipelineID] {
		return false, nil
	}
	store.bindingUnpaused = append(store.bindingUnpaused, pipeline.PipelineID)
	return true, nil
}
func (store *sourcePipelineLifecycleStoreStub) DrainTerminalBindingResourceSourcePipeline(_ context.Context, _ int, pipeline db.AgentWorkflowResourceSourcePipeline) (bool, error) {
	if !store.bindingDrain[pipeline.PipelineID] {
		return false, nil
	}
	store.bindingDrained = append(store.bindingDrained, pipeline.PipelineID)
	return true, nil
}

func assertIntSlice(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
