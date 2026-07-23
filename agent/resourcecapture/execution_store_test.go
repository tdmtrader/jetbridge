package resourcecapture_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/atc/db"
)

type operationLockerStub struct {
	keys []string
	err  error
}

func (stub *operationLockerStub) WithLock(ctx context.Context, key string, action func() error) error {
	stub.keys = append(stub.keys, key)
	if stub.err != nil {
		return stub.err
	}
	return action()
}

type pipelineRunStoreStub struct {
	runs       []db.PipelineRun
	listErr    error
	createdRun db.PipelineRun
	createErr  error
	creates    int
	ref        db.WorkflowRunTemplateRef
}

func (stub *pipelineRunStoreStub) ListRuns(int, int) ([]db.PipelineRun, error) {
	return stub.runs, stub.listErr
}

func (stub *pipelineRunStoreStub) CreateRunForServerTemplate(_ context.Context, ref db.WorkflowRunTemplateRef, _ map[string]any, _ string) (db.PipelineRun, error) {
	stub.creates++
	stub.ref = ref
	return stub.createdRun, stub.createErr
}

type pipelineRunStub struct {
	db.PipelineRun
	id, templateID, instanceID int
	instanceFound              bool
	status                     db.PipelineRunStatus
}

func (run *pipelineRunStub) ID() int                 { return run.id }
func (run *pipelineRunStub) TemplatePipelineID() int { return run.templateID }
func (run *pipelineRunStub) InstancePipelineID() (int, bool) {
	return run.instanceID, run.instanceFound
}
func (run *pipelineRunStub) Status() db.PipelineRunStatus { return run.status }

func TestExecutionStoreCreatesSingletonAndReusesExistingRun(t *testing.T) {
	template := resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: "agent-resource-capture-key", ConfigVersion: 2, FullHash: "full-hash"}
	request := resourcecapture.ExecutionRequest{TeamID: 7, TeamName: "main", OperationKey: "operation-key", Template: template, CreatedBy: "Alice"}
	createdRun := &pipelineRunStub{id: 51, templateID: 41, instanceID: 61, instanceFound: true, status: db.PipelineRunRunning}
	backend := &pipelineRunStoreStub{createdRun: createdRun}
	locker := &operationLockerStub{}
	store, err := resourcecapture.NewExecutionStore(backend, locker)
	if err != nil {
		t.Fatal(err)
	}
	execution, created, err := store.StartOrGet(context.Background(), request)
	if err != nil || !created || execution.PipelineRunID != 51 || execution.InstancePipelineID != 61 {
		t.Fatalf("StartOrGet() = %#v, %v, %v", execution, created, err)
	}
	if backend.creates != 1 || backend.ref.PipelineID != 41 || backend.ref.FullHash != "full-hash" || len(locker.keys) != 1 || locker.keys[0] != "resource-capture/operation-key" {
		t.Fatalf("backend creates/ref/locks = %d, %#v, %#v", backend.creates, backend.ref, locker.keys)
	}

	backend.runs = []db.PipelineRun{createdRun}
	execution, created, err = store.StartOrGet(context.Background(), request)
	if err != nil || created || execution.PipelineRunID != 51 || backend.creates != 1 {
		t.Fatalf("reused StartOrGet() = %#v, %v, %v (creates %d)", execution, created, err, backend.creates)
	}
}

func TestExecutionStoreCreatesANewGenerationAfterTerminalFailureOrExpiredSuccess(t *testing.T) {
	template := resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: "agent-resource-capture-key", ConfigVersion: 2, FullHash: "full-hash"}
	request := resourcecapture.ExecutionRequest{TeamID: 7, TeamName: "main", OperationKey: "operation-key", Template: template, CreatedBy: "Alice"}
	failed := &pipelineRunStub{id: 51, templateID: 41, instanceID: 61, instanceFound: true, status: db.PipelineRunFailed}
	retry := &pipelineRunStub{id: 52, templateID: 41, instanceID: 62, instanceFound: true, status: db.PipelineRunRunning}
	backend := &pipelineRunStoreStub{runs: []db.PipelineRun{failed}, createdRun: retry}
	store, err := resourcecapture.NewExecutionStore(backend, &operationLockerStub{})
	if err != nil {
		t.Fatal(err)
	}

	execution, created, err := store.StartOrGet(context.Background(), request)
	if err != nil || created || execution.PipelineRunID != 51 || backend.creates != 0 {
		t.Fatalf("terminal generation without retry = %#v, %v, %v (creates %d)", execution, created, err, backend.creates)
	}

	request.RetryPipelineRunID = 51
	execution, created, err = store.StartOrGet(context.Background(), request)
	if err != nil || !created || execution.PipelineRunID != 52 || backend.creates != 1 {
		t.Fatalf("failed-generation retry = %#v, %v, %v (creates %d)", execution, created, err, backend.creates)
	}

	succeeded := &pipelineRunStub{id: 53, templateID: 41, instanceID: 63, instanceFound: true, status: db.PipelineRunSucceeded}
	secondRetry := &pipelineRunStub{id: 54, templateID: 41, instanceID: 64, instanceFound: true, status: db.PipelineRunRunning}
	backend.runs = []db.PipelineRun{succeeded, failed}
	backend.createdRun = secondRetry
	request.RetryPipelineRunID = 53
	execution, created, err = store.StartOrGet(context.Background(), request)
	if err != nil || !created || execution.PipelineRunID != 54 || backend.creates != 2 {
		t.Fatalf("expired-success retry = %#v, %v, %v (creates %d)", execution, created, err, backend.creates)
	}
}

func TestExecutionStoreRetryRequestReusesANewerGenerationWonByAnotherCaller(t *testing.T) {
	template := resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: "agent-resource-capture-key", ConfigVersion: 2, FullHash: "full-hash"}
	newest := &pipelineRunStub{id: 52, templateID: 41, instanceID: 62, instanceFound: true, status: db.PipelineRunRunning}
	previous := &pipelineRunStub{id: 51, templateID: 41, instanceID: 61, instanceFound: true, status: db.PipelineRunSucceeded}
	backend := &pipelineRunStoreStub{runs: []db.PipelineRun{newest, previous}}
	store, err := resourcecapture.NewExecutionStore(backend, &operationLockerStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := resourcecapture.ExecutionRequest{
		TeamID: 7, TeamName: "main", OperationKey: "operation-key", Template: template,
		CreatedBy: "Alice", RetryPipelineRunID: 51,
	}
	execution, created, err := store.StartOrGet(context.Background(), request)
	if err != nil || created || execution.PipelineRunID != 52 || backend.creates != 0 {
		t.Fatalf("concurrent retry reuse = %#v, %v, %v (creates %d)", execution, created, err, backend.creates)
	}
}

func TestExecutionStorePropagatesCancellationAndRejectsConcurrentRunningGenerations(t *testing.T) {
	backend := &pipelineRunStoreStub{runs: []db.PipelineRun{
		&pipelineRunStub{id: 1, templateID: 41, instanceID: 2, instanceFound: true, status: db.PipelineRunRunning},
		&pipelineRunStub{id: 3, templateID: 41, instanceID: 4, instanceFound: true, status: db.PipelineRunRunning},
	}}
	store, err := resourcecapture.NewExecutionStore(backend, &operationLockerStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := resourcecapture.ExecutionRequest{TeamID: 7, TeamName: "main", OperationKey: "operation-key", Template: resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: "capture", ConfigVersion: 2, FullHash: "hash"}, CreatedBy: "Alice"}
	if _, _, err := store.StartOrGet(context.Background(), request); err == nil {
		t.Fatal("expected concurrent-running-generation corruption")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend.runs = nil
	if _, _, err := store.StartOrGet(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestExecutionStoreRejectsRetryWithoutDurableHistoryAndOlderRunningGeneration(t *testing.T) {
	template := resourcecapture.TemplateRef{ID: 41, TeamID: 7, Name: "capture", ConfigVersion: 2, FullHash: "hash"}
	request := resourcecapture.ExecutionRequest{
		TeamID: 7, TeamName: "main", OperationKey: "operation-key", Template: template,
		CreatedBy: "Alice", RetryPipelineRunID: 51,
	}
	backend := &pipelineRunStoreStub{}
	store, err := resourcecapture.NewExecutionStore(backend, &operationLockerStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StartOrGet(context.Background(), request); err == nil || backend.creates != 0 {
		t.Fatalf("missing retry history error/creates = %v/%d", err, backend.creates)
	}

	backend.runs = []db.PipelineRun{
		&pipelineRunStub{id: 52, templateID: 41, instanceID: 62, instanceFound: true, status: db.PipelineRunFailed},
		&pipelineRunStub{id: 51, templateID: 41, instanceID: 61, instanceFound: true, status: db.PipelineRunRunning},
	}
	if _, _, err := store.StartOrGet(context.Background(), request); err == nil || backend.creates != 0 {
		t.Fatalf("older running generation error/creates = %v/%d", err, backend.creates)
	}
}
