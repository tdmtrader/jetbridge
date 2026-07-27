package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/agent/workflowwait/workflowwaittest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
)

type factoryRecordingSealer struct {
	calls []snapshot.SealRequest
}

func (s *factoryRecordingSealer) Seal(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	s.calls = append(s.calls, request.Clone())
	digest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	result := make(map[string]snapshot.SealedOutput, len(request.Outputs))
	for i, output := range request.Outputs {
		result[output.ClientKey] = snapshot.SealedOutput{
			Port: output.Port,
			Snapshot: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(i + 1), Type: output.Port.Type, Digest: digest,
			},
		}
	}
	return result, nil
}

type factoryStaticWorkerFactory struct{ runtimeWorker runtime.Worker }

func (f factoryStaticWorkerFactory) NewWorker(lager.Logger, db.Worker) runtime.Worker {
	return f.runtimeWorker
}

type factoryWiringHarness struct {
	factory         CoreStepFactory
	state           exec.RunState
	ctx             context.Context
	metadata        exec.StepMetadata
	taskMetadata    db.ContainerMetadata
	agentMetadata   db.ContainerMetadata
	taskPlan        atc.Plan
	agentPlan       atc.Plan
	fakeBuild       *dbfakes.FakeBuild
	dbWorkerFactory *dbfakes.FakeWorkerFactory
}

func TestWithOutputSealerKeepsOneSharedFactoryDependency(t *testing.T) {
	sealer := &factoryRecordingSealer{}
	factory := &coreStepFactory{}
	WithOutputSealer(sealer)(factory)
	if factory.outputSealer != sealer {
		t.Fatalf("factory output sealer = %#v, want shared instance %#v", factory.outputSealer, sealer)
	}
}

func TestCoreStepFactoryLeavesOutputSealerDisabledByDefault(t *testing.T) {
	factory := &coreStepFactory{}
	if factory.outputSealer != nil {
		t.Fatalf("factory output sealer = %#v, want nil", factory.outputSealer)
	}
}

func TestWithSnapshotLoaderKeepsExactCommandScopedDependencies(t *testing.T) {
	metadata := new(snapshotfakes.FakeMetadataStore)
	content := new(snapshotfakes.FakeContentStore)
	bindings := new(dbfakes.FakeAgentWorkflowRunsFactory)
	factory := &coreStepFactory{}
	WithSnapshotLoader(metadata, content, bindings)(factory)
	if factory.snapshotMetadataStore != metadata || factory.snapshotContentStore != content || factory.snapshotInputBindings != bindings {
		t.Fatal("snapshot loader did not retain the exact command-scoped dependencies")
	}

	disabled := &coreStepFactory{}
	if disabled.snapshotMetadataStore != nil || disabled.snapshotContentStore != nil || disabled.snapshotInputBindings != nil {
		t.Fatal("snapshot loader dependencies should be nil by default")
	}
}

func TestWithSnapshotCanonicalizerKeepsDedicatedScratchConfiguration(t *testing.T) {
	canonicalizer := snapshot.Canonicalizer{
		MaxContentBytes: 1024,
		MaxEntries:      10,
		TempDir:         t.TempDir(),
	}
	factory := &coreStepFactory{}
	WithSnapshotCanonicalizer(canonicalizer)(factory)
	if factory.snapshotCanonicalizer.MaxContentBytes != canonicalizer.MaxContentBytes ||
		factory.snapshotCanonicalizer.MaxEntries != canonicalizer.MaxEntries ||
		factory.snapshotCanonicalizer.TempDir != canonicalizer.TempDir {
		t.Fatalf("snapshot canonicalizer = %#v, want %#v", factory.snapshotCanonicalizer, canonicalizer)
	}
}

func TestWithWorkflowWaitStoreKeepsExactCommandScopedDependency(t *testing.T) {
	store := workflowwaittest.NewMemoryStore(time.Now)
	factory := &coreStepFactory{}
	WithWorkflowWaitStore(store)(factory)
	if factory.workflowWaits != store {
		t.Fatal("workflow wait store did not retain the exact command-scoped dependency")
	}
}

func TestWithSnapshotPublisherKeepsExactCommandScopedDependency(t *testing.T) {
	executor := publisherExecutorStub{}
	factory := &coreStepFactory{}
	WithSnapshotPublisher(executor)(factory)
	if factory.snapshotPublisher != executor {
		t.Fatal("snapshot publisher did not retain the exact command-scoped dependency")
	}
	if (&coreStepFactory{}).snapshotPublisher != nil {
		t.Fatal("snapshot publisher should be nil by default")
	}
}

type publisherExecutorStub struct{}

func (publisherExecutorStub) Execute(context.Context, publisher.Request) (publisher.Publication, error) {
	return publisher.Publication{}, nil
}

func TestCoreStepFactoryRunsTaskAndAgentWithTheSameOutputSealer(t *testing.T) {
	sealer := &factoryRecordingSealer{}
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedStub = func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return snapshot.Snapshot{
			ID: id, Type: snapshot.TypeRef("opaque/v1"),
			Digest:         snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
			Representation: "application/x-tar",
			ContentState:   snapshot.ContentStateAvailable,
			CreatedAt:      time.Now().UTC(),
		}, true, nil
	}
	content := new(snapshotfakes.FakeContentStore)
	harness := newFactoryWiringHarness(
		t,
		WithOutputSealer(sealer),
		WithSnapshotLoader(metadata, content, nil),
	)

	task := harness.factory.TaskStep(
		harness.taskPlan, harness.metadata, harness.taskMetadata,
		DelegateFactory{build: harness.fakeBuild, plan: harness.taskPlan},
	)
	ok, err := task.Run(harness.ctx, harness.state)
	if err != nil || !ok {
		t.Fatalf("factory TaskStep.Run() = (%t, %v), want success", ok, err)
	}

	agent := harness.factory.AgentStep(
		harness.agentPlan, harness.metadata, harness.agentMetadata,
		DelegateFactory{build: harness.fakeBuild, plan: harness.agentPlan},
	)
	ok, err = agent.Run(harness.ctx, harness.state)
	if err != nil || !ok {
		t.Fatalf("factory AgentStep.Run() = (%t, %v), want success", ok, err)
	}

	if len(sealer.calls) != 2 {
		t.Fatalf("shared sealer calls = %d, want task and agent", len(sealer.calls))
	}
	if sealer.calls[0].StepKind != "task" || sealer.calls[1].StepKind != "agent" {
		t.Fatalf("shared sealer request kinds = %q, %q", sealer.calls[0].StepKind, sealer.calls[1].StepKind)
	}
	if metadata.GetAuthorizedCallCount() != 2 {
		t.Fatalf("durable snapshot metadata loads = %d, want task and agent", metadata.GetAuthorizedCallCount())
	}
}

func TestCoreStepFactoryDoesNotInjectASealerWhenDisabled(t *testing.T) {
	harness := newFactoryWiringHarness(t)

	task := harness.factory.TaskStep(
		harness.taskPlan, harness.metadata, harness.taskMetadata,
		DelegateFactory{build: harness.fakeBuild, plan: harness.taskPlan},
	)
	if _, err := task.Run(harness.ctx, harness.state); err == nil || !strings.Contains(err.Error(), "no output sealer") {
		t.Fatalf("factory TaskStep.Run() error = %v, want fail-closed no-sealer error", err)
	}

	agent := harness.factory.AgentStep(
		harness.agentPlan, harness.metadata, harness.agentMetadata,
		DelegateFactory{build: harness.fakeBuild, plan: harness.agentPlan},
	)
	if _, err := agent.Run(harness.ctx, harness.state); err == nil || !strings.Contains(err.Error(), "no output sealer") {
		t.Fatalf("factory AgentStep.Run() error = %v, want fail-closed no-sealer error", err)
	}
	if calls := harness.dbWorkerFactory.FindWorkersForContainerByOwnerCallCount(); calls != 0 {
		t.Fatalf("disabled typed steps selected workers %d times, want zero", calls)
	}
}

func newFactoryWiringHarness(t *testing.T, opts ...CoreStepFactoryOption) factoryWiringHarness {
	t.Helper()
	metadata := exec.StepMetadata{
		BuildID: 1234, TeamID: 123, TeamName: "main", SnapshotCreatedBy: "concourse",
	}
	taskPlan := atc.Plan{ID: "task-plan", Task: &atc.TaskPlan{
		Name: "typed-task",
		Config: &atc.TaskConfig{
			Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
			Outputs: []atc.TaskOutputConfig{{Name: "result"}},
		},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"result": {Type: snapshot.TypeRef("opaque/v1")},
		},
	}}
	agentPlan := atc.Plan{ID: "agent-plan", Agent: &atc.AgentPlan{
		Name: "typed-agent", Prompt: "do it", Outputs: []string{"workspace"},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"workspace": {Type: snapshot.TypeRef("opaque/v1")},
		},
	}}
	taskDir := factoryStepWorkingDirectory(taskPlan.Task.Name)
	agentDir := factoryStepWorkingDirectory(agentPlan.Agent.Name)
	taskOwner := db.NewBuildStepContainerOwner(metadata.BuildID, taskPlan.ID, metadata.TeamID)
	agentOwner := db.NewBuildStepContainerOwner(metadata.BuildID, agentPlan.ID, metadata.TeamID)
	runtimeWorker := runtimetest.NewWorker("worker").
		WithContainer(
			taskOwner,
			runtimetest.NewContainer().WithProcess(runtime.ProcessSpec{
				ID: "task", Path: "true", Dir: taskDir,
				TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}},
			}, runtimetest.ProcessStub{Attachable: true}),
			[]runtime.VolumeMount{{Volume: runtimetest.NewVolume("task-result"), MountPath: filepath.Join(taskDir, "result")}},
		).
		WithContainer(
			agentOwner,
			runtimetest.NewContainer().WithProcess(runtime.ProcessSpec{
				ID: "agent", Path: "agent-runner", Dir: agentDir,
				TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}},
			}, runtimetest.ProcessStub{Attachable: true}),
			[]runtime.VolumeMount{
				{Volume: runtimetest.NewVolume("agent-workspace"), MountPath: filepath.Join(agentDir, "workspace")},
				{Volume: runtimetest.NewVolume("agent-flight"), MountPath: filepath.Join(agentDir, "flight")},
			},
		)
	dbWorker := new(dbfakes.FakeWorker)
	dbWorker.NameReturns("worker")
	dbWorker.StateReturns(db.WorkerStateRunning)
	dbWorkerFactory := new(dbfakes.FakeWorkerFactory)
	dbWorkerFactory.FindWorkersForContainerByOwnerReturns([]db.Worker{dbWorker}, nil)
	pool := worker.NewPool(
		factoryStaticWorkerFactory{runtimeWorker: runtimeWorker},
		worker.DB{WorkerFactory: dbWorkerFactory},
	)
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.TracingAttrsReturns(tracing.Attrs{})
	allOpts := append([]CoreStepFactoryOption{WithAgentStepImage("agent-runner:test")}, opts...)
	factory := NewCoreStepFactory(
		pool, worker.Streamer{}, nil, nil, nil, nil, nil,
		atc.ContainerLimits{}, atc.ContainerLimits{}, 0, 0, 0, 0,
		allOpts...,
	)
	state := exec.NewRunState(func(atc.Plan) exec.Step { return nil }, vars.StaticVariables{})
	ctx := lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("factory-output-sealer"))
	return factoryWiringHarness{
		factory: factory, state: state, ctx: ctx, metadata: metadata,
		taskMetadata:  db.ContainerMetadata{Type: db.ContainerTypeTask, Attempt: "1"},
		agentMetadata: db.ContainerMetadata{Type: db.ContainerTypeAgent, Attempt: "1"},
		taskPlan:      taskPlan, agentPlan: agentPlan, fakeBuild: fakeBuild, dbWorkerFactory: dbWorkerFactory,
	}
}

func factoryStepWorkingDirectory(name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))
}
