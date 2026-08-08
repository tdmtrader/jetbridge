package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type factoryRecordingSealer struct {
	calls          []snapshot.SealRequest
	nextSnapshotID snapshot.SnapshotID
}

func (s *factoryRecordingSealer) Seal(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	s.calls = append(s.calls, request.Clone())
	digest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	result := make(map[string]snapshot.SealedOutput, len(request.Outputs))
	for _, output := range request.Outputs {
		s.nextSnapshotID++
		result[output.ClientKey] = snapshot.SealedOutput{
			Port: output.Port,
			Snapshot: snapshot.SnapshotRef{
				ID: s.nextSnapshotID, Type: output.Port.Type, Digest: digest,
			},
		}
	}
	return result, nil
}

type factoryStaticWorkerFactory struct {
	runtimeWorker runtime.Worker

	mu      sync.Mutex
	workers []db.Worker
}

func (f *factoryStaticWorkerFactory) NewWorker(_ lager.Logger, worker db.Worker) runtime.Worker {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workers = append(f.workers, worker)
	return f.runtimeWorker
}

func (f *factoryStaticWorkerFactory) receivedWorkers() []db.Worker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db.Worker(nil), f.workers...)
}

type factoryWorkerFactoryObserver struct {
	db.WorkerFactory

	mu             sync.Mutex
	ownerLookups   int
	workerListings int
}

func (f *factoryWorkerFactoryObserver) FindWorkersForContainerByOwner(owner db.ContainerOwner) ([]db.Worker, error) {
	f.mu.Lock()
	f.ownerLookups++
	f.mu.Unlock()
	return f.WorkerFactory.FindWorkersForContainerByOwner(owner)
}

func (f *factoryWorkerFactoryObserver) Workers() ([]db.Worker, error) {
	f.mu.Lock()
	f.workerListings++
	f.mu.Unlock()
	return f.WorkerFactory.Workers()
}

func (f *factoryWorkerFactoryObserver) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownerLookups, f.workerListings
}

type factoryWiringHarness struct {
	fixture              *EngineDBFixture
	factory              CoreStepFactory
	state                exec.RunState
	ctx                  context.Context
	metadata             exec.StepMetadata
	taskMetadata         db.ContainerMetadata
	agentMetadata        db.ContainerMetadata
	taskPlan             atc.Plan
	agentPlan            atc.Plan
	build                db.Build
	dbWorker             db.Worker
	workerObserver       *factoryWorkerFactoryObserver
	runtimeWorkerFactory *factoryStaticWorkerFactory
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

var _ = Describe("CoreStepFactory typed output wiring", func() {
	It("runs task and agent through the same sealer on a persisted general worker", func() {
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
			WithOutputSealer(sealer),
			WithSnapshotLoader(metadata, content, nil),
		)

		task := harness.factory.TaskStep(
			harness.taskPlan, harness.metadata, harness.taskMetadata,
			harness.delegateFor(harness.taskPlan),
		)
		ok, err := task.Run(harness.ctx, harness.state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		agent := harness.factory.AgentStep(
			harness.agentPlan, harness.metadata, harness.agentMetadata,
			harness.delegateFor(harness.agentPlan),
		)
		ok, err = agent.Run(harness.ctx, harness.state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		Expect(sealer.calls).To(HaveLen(2))
		Expect(sealer.calls[0].StepKind).To(Equal("task"))
		Expect(sealer.calls[0].StepName).To(Equal(harness.taskPlan.Task.Name))
		Expect(sealer.calls[0].PlanID).To(Equal(string(harness.taskPlan.ID)))
		Expect(sealer.calls[0].BuildID).To(Equal(harness.build.ID()))
		Expect(sealer.calls[0].TeamID).To(Equal(harness.build.TeamID()))
		Expect(sealer.calls[0].Attempt).To(Equal(harness.taskMetadata.Attempt))
		Expect(sealer.calls[0].CreatedBy).To(Equal(harness.metadata.SnapshotCreatedBy))
		Expect(sealer.calls[1].StepKind).To(Equal("agent"))
		Expect(sealer.calls[1].StepName).To(Equal(harness.agentPlan.Agent.Name))
		Expect(sealer.calls[1].PlanID).To(Equal(string(harness.agentPlan.ID)))
		Expect(sealer.calls[1].BuildID).To(Equal(harness.build.ID()))
		Expect(sealer.calls[1].TeamID).To(Equal(harness.build.TeamID()))
		Expect(sealer.calls[1].Attempt).To(Equal(harness.agentMetadata.Attempt))
		Expect(sealer.calls[1].CreatedBy).To(Equal(harness.metadata.SnapshotCreatedBy))

		Expect(metadata.GetAuthorizedCallCount()).To(Equal(2))
		expectedSnapshotIDs := []snapshot.SnapshotID{1, 2}
		for i := 0; i < metadata.GetAuthorizedCallCount(); i++ {
			_, teamID, snapshotID := metadata.GetAuthorizedArgsForCall(i)
			Expect(teamID).To(Equal(harness.build.TeamID()))
			Expect(snapshotID).To(Equal(expectedSnapshotIDs[i]))
		}

		ownerLookups, workerListings := harness.workerObserver.counts()
		Expect(ownerLookups).To(Equal(2))
		Expect(workerListings).To(Equal(2))
		Expect(factoryBuildContainerCount(harness.fixture, harness.build)).To(BeZero())

		found, err := harness.dbWorker.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(harness.dbWorker.State()).To(Equal(db.WorkerStateRunning))
		Expect(harness.dbWorker.Platform()).To(Equal("linux"))
		Expect(harness.dbWorker.TeamID()).To(BeZero())
		Expect(harness.dbWorker.TeamName()).To(BeEmpty())

		receivedWorkers := harness.runtimeWorkerFactory.receivedWorkers()
		Expect(receivedWorkers).To(HaveLen(2))
		for _, received := range receivedWorkers {
			Expect(received.Name()).To(Equal(harness.dbWorker.Name()))
			Expect(received.State()).To(Equal(db.WorkerStateRunning))
			Expect(received.Platform()).To(Equal("linux"))
			Expect(received.TeamID()).To(BeZero())
			Expect(received.TeamName()).To(BeEmpty())
		}

		selectedWorkers := factorySelectedWorkerEvents(harness.fixture, harness.build)
		Expect(selectedWorkers).To(HaveLen(2))
		Expect(selectedWorkers[0].Origin.ID).To(Equal(event.OriginID(harness.taskPlan.ID)))
		Expect(selectedWorkers[0].WorkerName).To(Equal(harness.dbWorker.Name()))
		Expect(selectedWorkers[1].Origin.ID).To(Equal(event.OriginID(harness.agentPlan.ID)))
		Expect(selectedWorkers[1].WorkerName).To(Equal(harness.dbWorker.Name()))

		expectFactoryBuildStarted(harness.build)
	})

	It("fails closed before worker selection when no sealer is configured", func() {
		harness := newFactoryWiringHarness()

		task := harness.factory.TaskStep(
			harness.taskPlan, harness.metadata, harness.taskMetadata,
			harness.delegateFor(harness.taskPlan),
		)
		_, err := task.Run(harness.ctx, harness.state)
		Expect(err).To(MatchError(fmt.Sprintf(
			"task %q has typed snapshot declarations but no output sealer is configured",
			harness.taskPlan.Task.Name,
		)))

		agent := harness.factory.AgentStep(
			harness.agentPlan, harness.metadata, harness.agentMetadata,
			harness.delegateFor(harness.agentPlan),
		)
		_, err = agent.Run(harness.ctx, harness.state)
		Expect(err).To(MatchError(fmt.Sprintf(
			"agent %q has typed snapshot declarations but no output sealer is configured",
			harness.agentPlan.Agent.Name,
		)))

		ownerLookups, workerListings := harness.workerObserver.counts()
		Expect(ownerLookups).To(BeZero())
		Expect(workerListings).To(BeZero())
		Expect(harness.runtimeWorkerFactory.receivedWorkers()).To(BeEmpty())
		Expect(factoryBuildContainerCount(harness.fixture, harness.build)).To(BeZero())
		expectFactoryBuildStarted(harness.build)
	})
})

func newFactoryWiringHarness(opts ...CoreStepFactoryOption) factoryWiringHarness {
	GinkgoHelper()

	fixture := UseEngineDB()
	decoyTeam, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "factory-wiring-decoy-team"})
	Expect(err).NotTo(HaveOccurred())
	team, pipeline, job, build := CreateEngineJobBuild(
		fixture,
		"factory-wiring-team",
		atc.PipelineRef{Name: "factory-wiring-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		"factory-wiring-user",
	)
	Expect(decoyTeam.ID()).NotTo(Equal(team.ID()))
	Expect(team.ID()).NotTo(Equal(build.ID()))

	taskName := job.Name() + "-typed-task"
	agentName := job.Name() + "-typed-agent"
	taskPlan := atc.Plan{
		ID: atc.PlanID(fmt.Sprintf("build-%d-%s", build.ID(), taskName)),
		Task: &atc.TaskPlan{
			Name: taskName,
			Config: &atc.TaskConfig{
				Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
				Outputs: []atc.TaskOutputConfig{{Name: "result"}},
			},
			SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
				"result": {Type: snapshot.TypeRef("opaque/v1")},
			},
		},
	}
	agentPlan := atc.Plan{
		ID: atc.PlanID(fmt.Sprintf("build-%d-%s", build.ID(), agentName)),
		Agent: &atc.AgentPlan{
			Name: agentName, Prompt: "do it", Outputs: []string{"workspace"},
			SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
				"workspace": {Type: snapshot.TypeRef("opaque/v1")},
			},
		},
	}
	steps := atc.DoPlan{taskPlan, agentPlan}
	started, err := build.Start(atc.Plan{
		ID: atc.PlanID(fmt.Sprintf("pipeline-%d-build-%s", pipeline.ID(), build.Name())),
		Do: &steps,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	workerName := fmt.Sprintf("%s-build-%d-worker", team.Name(), build.ID())
	dbWorker, err := fixture.WorkerFactory.SaveWorker(atc.Worker{
		Name:      workerName,
		Platform:  "linux",
		State:     string(db.WorkerStateRunning),
		StartTime: time.Now().Unix(),
	}, 0)
	Expect(err).NotTo(HaveOccurred())

	metadata := factoryStepMetadata(build)
	taskMetadata := factoryContainerMetadata(build, db.ContainerTypeTask, taskPlan.Task.Name, taskPlan.Attempts)
	agentMetadata := factoryContainerMetadata(build, db.ContainerTypeAgent, agentPlan.Agent.Name, agentPlan.Attempts)
	taskDir := factoryStepWorkingDirectory(taskPlan.Task.Name)
	agentDir := factoryStepWorkingDirectory(agentPlan.Agent.Name)
	taskOwner := db.NewBuildStepContainerOwner(build.ID(), taskPlan.ID, team.ID())
	agentOwner := db.NewBuildStepContainerOwner(build.ID(), agentPlan.ID, team.ID())
	runtimeWorker := runtimetest.NewWorker(workerName).
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
	workerObserver := &factoryWorkerFactoryObserver{WorkerFactory: fixture.WorkerFactory}
	runtimeWorkerFactory := &factoryStaticWorkerFactory{runtimeWorker: runtimeWorker}
	pool := worker.NewPool(
		runtimeWorkerFactory,
		worker.DB{WorkerFactory: workerObserver},
	)
	allOpts := append([]CoreStepFactoryOption{WithAgentStepImage("agent-runner:test")}, opts...)
	factory := NewCoreStepFactory(
		pool,
		worker.Streamer{},
		fixture.LockFactory,
		fixture.TeamFactory,
		fixture.BuildFactory,
		fixture.ResourceCacheFactory,
		fixture.ResourceConfigFactory,
		atc.ContainerLimits{}, atc.ContainerLimits{}, 0, 0, 0, 0,
		allOpts...,
	)
	state := exec.NewRunState(func(atc.Plan) exec.Step { return nil }, vars.StaticVariables{})
	ctx := lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("factory-output-sealer"))
	return factoryWiringHarness{
		fixture: fixture, factory: factory, state: state, ctx: ctx, metadata: metadata,
		taskMetadata: taskMetadata, agentMetadata: agentMetadata,
		taskPlan: taskPlan, agentPlan: agentPlan, build: build, dbWorker: dbWorker,
		workerObserver: workerObserver, runtimeWorkerFactory: runtimeWorkerFactory,
	}
}

func factoryStepMetadata(build db.Build) exec.StepMetadata {
	GinkgoHelper()

	snapshotCreatedBy := "concourse"
	if build.CreatedBy() != nil && strings.TrimSpace(*build.CreatedBy()) != "" {
		snapshotCreatedBy = *build.CreatedBy()
	}
	return exec.StepMetadata{
		BuildID:              build.ID(),
		BuildName:            build.Name(),
		TeamID:               build.TeamID(),
		TeamName:             build.TeamName(),
		JobID:                build.JobID(),
		JobName:              build.JobName(),
		PipelineID:           build.PipelineID(),
		PipelineName:         build.PipelineName(),
		PipelineInstanceVars: build.PipelineInstanceVars(),
		InstanceVarsQuery:    build.PipelineRef().QueryParams(),
		SnapshotCreatedBy:    snapshotCreatedBy,
	}
}

func factoryContainerMetadata(build db.Build, containerType db.ContainerType, stepName string, attempts []int) db.ContainerMetadata {
	GinkgoHelper()

	attemptParts := make([]string, len(attempts))
	for i, attempt := range attempts {
		attemptParts[i] = fmt.Sprintf("%d", attempt)
	}
	attempt := strings.Join(attemptParts, ".")
	if attempt == "" {
		attempt = "0"
	}

	var pipelineInstanceVars string
	if build.PipelineInstanceVars() != nil {
		encoded, err := json.Marshal(build.PipelineInstanceVars())
		Expect(err).NotTo(HaveOccurred())
		pipelineInstanceVars = string(encoded)
	}
	return db.ContainerMetadata{
		Type: containerType,

		StepName: stepName,
		Attempt:  attempt,

		PipelineID: build.PipelineID(),
		JobID:      build.JobID(),
		BuildID:    build.ID(),

		PipelineName:         build.PipelineName(),
		PipelineInstanceVars: pipelineInstanceVars,
		JobName:              build.JobName(),
		BuildName:            build.Name(),
	}
}

func (h factoryWiringHarness) delegateFor(plan atc.Plan) DelegateFactory {
	return DelegateFactory{
		build:                 h.build,
		plan:                  plan,
		dbWorkerFactory:       h.workerObserver,
		lockFactory:           h.fixture.LockFactory,
		resourceConfigFactory: h.fixture.ResourceConfigFactory,
		resourceCacheFactory:  h.fixture.ResourceCacheFactory,
	}
}

func factoryBuildContainerCount(fixture *EngineDBFixture, build db.Build) int {
	GinkgoHelper()

	var count int
	Expect(fixture.Conn.QueryRow(`
		SELECT COUNT(*)
		FROM containers
		WHERE build_id = $1
	`, build.ID()).Scan(&count)).To(Succeed())
	return count
}

func factorySelectedWorkerEvents(fixture *EngineDBFixture, build db.Build) []event.SelectedWorker {
	GinkgoHelper()

	rows, err := fixture.Conn.Query(`
		SELECT type, version, payload
		FROM build_events
		WHERE (build_id = $1 OR build_id_old = $1) AND type = $2
		ORDER BY event_id
	`, build.ID(), event.SelectedWorker{}.EventType())
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()

	var selectedWorkers []event.SelectedWorker
	for rows.Next() {
		var eventType, version, payload string
		Expect(rows.Scan(&eventType, &version, &payload)).To(Succeed())
		parsed, err := event.ParseEvent(atc.EventVersion(version), atc.EventType(eventType), []byte(payload))
		Expect(err).NotTo(HaveOccurred())
		selected, ok := parsed.(event.SelectedWorker)
		Expect(ok).To(BeTrue())
		selectedWorkers = append(selectedWorkers, selected)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return selectedWorkers
}

func expectFactoryBuildStarted(build db.Build) {
	GinkgoHelper()

	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(build.Status()).To(Equal(db.BuildStatusStarted))
	Expect(build.IsRunning()).To(BeTrue())
	Expect(build.IsCompleted()).To(BeFalse())
}

func factoryStepWorkingDirectory(name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))
}
