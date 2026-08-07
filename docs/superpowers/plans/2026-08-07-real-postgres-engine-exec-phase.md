# Real PostgreSQL Engine and Exec Conversion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Replace the selected engine and exec tests’ successful persisted-state dbfakes with isolated PostgreSQL fixtures while keeping narrowly scoped algorithm, runtime, authorization, validator, and deterministic fault seams.

**Architecture:** Engine and exec each own one postgresrunner.Runner and an opt-in useEngineDB or useExecDB helper. A migrated clone is created only by a converted persisted-state Describe, not by a suite-wide BeforeEach, so the unrelated 259 engine specs and 730 exec specs do not acquire databases. Normal state is built through real teams, instance-var pipelines, jobs, builds, resource versions, caches, workers, volumes, and artifacts; interface-embedding wrappers override one method only for failures PostgreSQL cannot deterministically produce.

**Tech Stack:** Go, Ginkgo v2/Gomega, atc/postgresrunner, PostgreSQL, atc/db, atc/db/dbtest, atc/db/lock.

## Global Constraints

- Use the already-running PostgreSQL service at 127.0.0.1:15432. Verify it with pg_isready -h 127.0.0.1 -p 15432 -U postgres; never start, stop, recreate, or alter Docker or the service.
- Every opted-in persisted-state spec creates one unique clone with CreateTestDBFromTemplate, closes all ordinary and singleton connections, and then calls DropTestDB. No repository, worker cache, or factory survives its clone.
- Do not install a suite-wide BeforeEach. Each migrated persisted-state Describe calls useEngineDB or useExecDB exactly once from its own BeforeEach.
- Preserve normal Ginkgo parallel safety. Lock connections come from postgresRunner.OpenSingleton and are registered for cleanup before the clone is dropped.
- Do not change production code without first observing a focused test fail against unmodified production behavior. This phase is expected to change tests and audit bookkeeping only.
- A separately opened and immediately closed clone-local connection is valid only for a factory or repository whose constructor accepts db.DbConn. It cannot make an already-created db.Build, db.Pipeline, db.WorkerArtifact, or db.CreatedVolume fail.
- Failures on already-created values use one-method embedding wrappers around healthy real state. Each wrapper has a comment naming why a healthy clone cannot deterministically produce that failure.
- Preserve pool, runtime, stepper, lock, policy, authorization, validator, stream, retry-order, clock, and callback-order seams.
- Exclude benchmarks and corpus tests. Do not push.

---

## File Structure

- Modify atc/engine/engine_suite_test.go: opt-in engine fixture, job-build helper, closed connection helper, and build-event consumer.
- Modify atc/engine/engine_test.go, set_pipeline_delegate_test.go, put_delegate_test.go, builder_test.go: first persisted-success batch; isolate the retained engine runtime matrix.
- Modify atc/engine/get_delegate_test.go, build_step_delegate_test.go, check_delegate_test.go: real pipeline/resource/config/scope/version/cache state plus local one-method fault wrappers.
- Modify atc/exec/exec_suite_test.go: opt-in exec fixture and safe one-time-per-process policy-agent registration.
- Modify atc/exec/put_step_test.go, artifact_input_step_test.go, artifact_output_step_test.go: typed-nil cleanup and real build/artifact/volume state.
- Modify atc/exec/get_step_test.go, check_step_test.go, set_pipeline_step_test.go: real cache/config/version and valid team/build/pipeline state.
- Modify .superpowers/sdd/2026-08-06-next-real-postgres-conversions/audit-remaining-core.md: observed engine/exec import and constructor recount only.

## Exact Fixture and Wrapper Contracts

### Engine opt-in fixture

Add this package-local fixture shape to atc/engine/engine_suite_test.go. There is no suite-wide BeforeEach.

~~~go
type engineDBFixture struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	WorkerFactory         db.WorkerFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

var enginePostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&enginePostgresRunner)

func useEngineDB() *engineDBFixture {
	GinkgoHelper()

	enginePostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(enginePostgresRunner.DropTestDB)

	conn := enginePostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := enginePostgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
	logger := lagertest.NewTestLogger("engine-postgres-fixture")

	return &engineDBFixture{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		WorkerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		ResourceCacheFactory:  db.NewResourceCacheFactory(conn, lockFactory),
	}
}

func closedEngineCloneConn() db.DbConn {
	GinkgoHelper()
	conn := enginePostgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}
~~~

Create pipeline-dependent builds with a real job. Do not use CreateOneOffBuild for delegates that read Pipeline, PipelineID, PipelineName, JobID, JobName, or pipeline resources.

~~~go
func createEngineJobBuild(
	fixture *engineDBFixture,
	teamName string,
	ref atc.PipelineRef,
	config atc.Config,
	createdBy string,
) (db.Team, db.Pipeline, db.Job, db.Build) {
	GinkgoHelper()
	_, configured := config.Jobs.Lookup("some-job")
	Expect(configured).To(BeTrue())

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	Expect(err).NotTo(HaveOccurred())
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	job := scenario.Job("some-job")
	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline, job, build
}
~~~

Instance-variable fixtures must call SavePipeline with the instance vars in the reference:

~~~go
ref := atc.PipelineRef{
	Name:         "some-pipeline",
	InstanceVars: atc.InstanceVars{"branch": "master"},
}
team, pipeline, job, realBuild := createEngineJobBuild(
	fixture,
	"some-team",
	ref,
	atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
	"some-user",
)
~~~

Consume and close each real build event source. Parsing event.Message returns the concrete atc.Event needed by the existing equality assertions.

~~~go
func consumeEngineBuildEvent(build db.Build, from uint) atc.Event {
	GinkgoHelper()
	source, err := build.Events(from)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(source.Close()).To(Succeed()) }()

	envelope, err := source.Next()
	Expect(err).NotTo(HaveOccurred())
	encoded, err := json.Marshal(envelope)
	Expect(err).NotTo(HaveOccurred())
	var message event.Message
	Expect(json.Unmarshal(encoded, &message)).To(Succeed())
	return message.Event
}
~~~

### Exec opt-in fixture and policy registration

Add this complete package-local fixture and job-build helper to atc/exec/exec_suite_test.go. There is no suite-wide BeforeEach.

~~~go
type execDBFixture struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	WorkerFactory         db.WorkerFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

var execPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&execPostgresRunner)

func useExecDB() *execDBFixture {
	GinkgoHelper()

	execPostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(execPostgresRunner.DropTestDB)

	conn := execPostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := execPostgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
	logger := lagertest.NewTestLogger("exec-postgres-fixture")

	return &execDBFixture{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		WorkerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		ResourceCacheFactory:  db.NewResourceCacheFactory(conn, lockFactory),
	}
}

func closedExecCloneConn() db.DbConn {
	GinkgoHelper()
	conn := execPostgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}

func createExecJobBuild(
	fixture *execDBFixture,
	teamName string,
	ref atc.PipelineRef,
	config atc.Config,
	createdBy string,
) (db.Team, db.Pipeline, db.Job, db.Build) {
	GinkgoHelper()
	_, configured := config.Jobs.Lookup("some-job")
	Expect(configured).To(BeTrue())

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	Expect(err).NotTo(HaveOccurred())
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	job := scenario.Job("some-job")
	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline, job, build
}
~~~

postgresrunner.GinkgoRunner installs SynchronizedBeforeSuite, so the existing exec BeforeSuite is illegal once the runner is present. Move policy setup into the existing package init function and delete the BeforeSuite node:

~~~go
func init() {
	util.PanicSink = GinkgoWriter

	fakePolicyAgentFactory = new(policyfakes.FakeAgentFactory)
	fakePolicyAgentFactory.IsConfiguredReturns(true)
	fakePolicyAgentFactory.DescriptionReturns("fakeAgent")
	policy.RegisterAgent(fakePolicyAgentFactory)
}
~~~

Go invokes init exactly once in each Ginkgo process. The policy registry is process-local, so this registers exactly one fake agent per process and never once per spec or once per clone.

### Required one-method wrappers

Define wrappers beside the test that uses them, not in a product package. The embedded value is healthy real state; only the named method is synthetic.

~~~go
// A healthy clone cannot make Pipeline fail on demand while preserving the
// build row needed by the rest of this delegate test.
type pipelineErrorBuild struct {
	db.Build
	err error
}

func (build pipelineErrorBuild) Pipeline() (db.Pipeline, bool, error) {
	return nil, false, build.err
}

// A healthy clone cannot make Artifact fail on demand while preserving the
// build and artifact rows used by the surrounding success setup.
type artifactErrorBuild struct {
	db.Build
	err error
}

func (build artifactErrorBuild) Artifact(int) (db.WorkerArtifact, error) {
	return nil, build.err
}

// A healthy created volume cannot fail only InitializeArtifact. Embedding the
// real volume keeps every other db.CreatedVolume method tied to persisted state.
type initErrorVolume struct {
	db.CreatedVolume
	err error
}

func (volume initErrorVolume) InitializeArtifact(string, int) (db.WorkerArtifact, error) {
	return nil, volume.err
}
~~~

Define every additional wrapper explicitly beside the test that consumes it:

~~~go
// A healthy pipeline cannot fail only Resource while its other persisted
// fields remain readable.
type resourceErrorPipeline struct {
	db.Pipeline
	err error
}

func (pipeline resourceErrorPipeline) Resource(string) (db.Resource, bool, error) {
	return nil, false, pipeline.err
}

// pipelineResultBuild injects a wrapped persisted pipeline into GetDelegate;
// every other Build method remains backed by the healthy real build.
type pipelineResultBuild struct {
	db.Build
	pipeline db.Pipeline
	found    bool
}

func (build pipelineResultBuild) Pipeline() (db.Pipeline, bool, error) {
	return build.pipeline, build.found, nil
}

// A healthy resource config cannot fail only FindOrCreateScope while retaining
// its persisted identity and origin fields.
type scopeErrorResourceConfig struct {
	db.ResourceConfig
	err error
}

func (config scopeErrorResourceConfig) FindOrCreateScope(*int) (db.ResourceConfigScope, error) {
	return nil, config.err
}

// A healthy scope cannot fail only LatestVersion after its versions have been
// persisted successfully.
type latestVersionErrorScope struct {
	db.ResourceConfigScope
	err error
}

func (scope latestVersionErrorScope) LatestVersion() (db.ResourceConfigVersion, bool, error) {
	return nil, false, scope.err
}

// A healthy build cannot fail only SavePipeline while retaining the team,
// pipeline, job, and build rows needed by SetPipelineStep.
type savePipelineErrorBuild struct {
	db.Build
	err error
}

func (build savePipelineErrorBuild) SavePipeline(
	ref atc.PipelineRef,
	teamID int,
	config atc.Config,
	from db.ConfigVersion,
	initiallyPaused bool,
) (db.Pipeline, bool, error) {
	return nil, false, build.err
}

// A healthy team cannot fail only Pipeline after TeamFactory has found it.
type pipelineLookupErrorTeam struct {
	db.Team
	err error
}

func (team pipelineLookupErrorTeam) Pipeline(atc.PipelineRef) (db.Pipeline, bool, error) {
	return nil, false, team.err
}

// A healthy pipeline cannot fail only Config after Team.Pipeline has returned
// the persisted row.
type configErrorPipeline struct {
	db.Pipeline
	err error
}

func (pipeline configErrorPipeline) Config() (atc.Config, error) {
	return atc.Config{}, pipeline.err
}
~~~

Artifact-input volume failures require both the wrapped artifact and a build
that returns it. Define both adapters explicitly:

~~~go
// A healthy artifact cannot deterministically return a Volume error/not-found
// result after its persisted volume association has been created.
type volumeResultArtifact struct {
	db.WorkerArtifact
	volume db.CreatedVolume
	found  bool
	err    error
}

func (artifact volumeResultArtifact) Volume(int) (db.CreatedVolume, bool, error) {
	return artifact.volume, artifact.found, artifact.err
}

// artifactResultBuild returns the wrapped artifact at the Build.Artifact seam;
// every other Build method remains backed by the healthy real build.
type artifactResultBuild struct {
	db.Build
	artifact db.WorkerArtifact
}

func (build artifactResultBuild) Artifact(int) (db.WorkerArtifact, error) {
	return build.artifact, nil
}
~~~

Artifact output needs a local runtime.Volume adapter because runtimetest.NewVolume always returns a FakeCreatedVolume from DBVolume:

~~~go
type runtimeVolumeWithDB struct {
	runtime.Volume
	databaseVolume db.CreatedVolume
}

func (volume runtimeVolumeWithDB) DBVolume() db.CreatedVolume {
	return volume.databaseVolume
}
~~~

## Task 1: Add opt-in engine and exec harnesses

**Files:**

- Modify atc/engine/engine_suite_test.go
- Modify atc/exec/exec_suite_test.go

**Interfaces:**

- Produces useEngineDB() *engineDBFixture, closedEngineCloneConn() db.DbConn, createEngineJobBuild, consumeEngineBuildEvent.
- Produces useExecDB() *execDBFixture, closedExecCloneConn() db.DbConn, createExecJobBuild.
- Preserves fakePolicyAgentFactory as a process-local suite dependency without a second Ginkgo suite setup node.

- [x] **Step 1: Verify the fixed service and current non-DB leaves.**

Run: pg_isready -h 127.0.0.1 -p 15432 -U postgres

Expected: 127.0.0.1:15432 - accepting connections.

Run: ginkgo ./atc/engine/ --focus='Engine NewBuild returns a build'

Expected: PASS.

Run: ginkgo ./atc/exec/ --focus='PutStep'

Expected: PASS.

- [x] **Step 2: Add the exact opt-in helpers and move exec policy registration.**

Implement the fixture, job-build, event-consumer, and init code above. Do not add a package-level BeforeEach and do not add a permanent fixture-only spec.

- [x] **Step 3: Compile and prove no suite-node collision.**

Run: go test ./atc/engine ./atc/exec -run '^$'

Expected: both packages compile.

Run: ginkgo ./atc/exec/ --focus='PutStep'

Expected: PASS without “BeforeSuite can only be called once” or duplicate policy-agent behavior. Because PutStep does not call useExecDB, this focused run creates no per-spec clone.

- [x] **Step 4: Commit the harnesses.**

~~~bash
git add atc/engine/engine_suite_test.go atc/exec/exec_suite_test.go
git commit -m "test: add opt-in engine and exec postgres fixtures"
~~~

## Task 2: Convert the first engine success-state batch

**Files:**

- Modify atc/engine/engine_test.go
- Modify atc/engine/set_pipeline_delegate_test.go
- Modify atc/engine/put_delegate_test.go
- Modify atc/engine/builder_test.go

**Interfaces:**

- Consumes Task 1’s opt-in engine fixture, instance-var job-build helper, and event consumer.
- Produces persisted delegate events, output rows, and dynamic StepMetadata assertions.
- Retains one generated FakeBuild construction in engine_test.go only inside a Describe named retained runtime and fault matrix.

- [x] **Step 1: Add the persisted assertion before switching the delegate build.**

In set_pipeline_delegate_test.go add Describe("persisted PostgreSQL state"). Its BeforeEach calls useEngineDB once, creates config with job some-job, saves PipelineRef{Name: "some-pipeline", InstanceVars: {"branch": "master"}}, and creates the job build. Initially leave NewSetPipelineStepDelegate wired to the old fakeBuild. Add leaf “saves changed event” that calls SetPipelineChanged, finishes the real build so an empty real stream cannot block, consumes event zero, and expects event.SetPipelineChanged with Changed true.

~~~go
Expect(realBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
Expect(consumeEngineBuildEvent(realBuild, 0)).To(Equal(event.SetPipelineChanged{
	Origin:  event.Origin{ID: event.OriginID("some-plan-id")},
	Changed: true,
}))
~~~

- [x] **Step 2: Run the assertion-first RED command.**

Run: ginkgo ./atc/engine/ --focus='SetPipelineStepDelegate persisted PostgreSQL state saves changed event'

Expected: FAIL because event zero on realBuild is the status event; the delegate still saved SetPipelineChanged only on fakeBuild.

- [x] **Step 3: Implement the persisted-success Describes.**

Wire the persisted SetPipelineStepDelegate Describe to realBuild. After every write call realBuild.Reload, assert found and no error, then consume and close the event source. Assert both warning log events by offsets zero and one and assert their concrete event.Log fields.

In put_delegate_test.go create config containing job some-job and resource some-resource, create the job build, save the resource version, and create the exact cache with db.ForBuild(realBuild.ID()). Wire Finished and SaveOutput success to realBuild. Reload before reading; assert FinishPut through consumeEngineBuildEvent and assert realBuild.Resources returns one BuildOutput{Name: "some-name", Version: info.Version}.

In builder_test.go save the instance-var reference through team.SavePipeline, create job some-job’s build with CreateBuild("some-user"), and inject fixture.WorkerFactory. Build expected metadata from real IDs and names:

~~~go
expectedMetadata := exec.StepMetadata{
	BuildID:              realBuild.ID(),
	BuildName:            realBuild.Name(),
	TeamID:               team.ID(),
	TeamName:             team.Name(),
	JobID:                job.ID(),
	JobName:              job.Name(),
	PipelineID:           pipeline.ID(),
	PipelineName:         pipeline.Name(),
	PipelineInstanceVars: pipeline.InstanceVars(),
	ExternalURL:          "http://example.com",
	CreatedBy:            "some-user",
	SnapshotCreatedBy:    "some-user",
}
~~~

Move wrong-schema, workflow-association failure, resource-capture failure, and mutually-exclusive-association cases into retained fault seams. Use small embedding wrappers around realBuild when one override is sufficient; keep their algorithm assertions unchanged.

In engine_test.go create a persisted job build for NewBuild and the ordinary successful construction assertion. Move the channel, abort, release, lock, panic, retry, and callback-order matrix under Describe("retained runtime and fault matrix") and keep its one FakeBuild. Add a comment that this fake controls non-persistable lock/listener/cancellation ordering.

- [x] **Step 4: Run GREEN and sensitivity checks.**

Run: ginkgo ./atc/engine/ --focus='(Engine|SetPipelineStepDelegate|PutDelegate|Builder)'

Expected: PASS.

Temporarily set Changed false only in the persisted expectation.

Run: ginkgo ./atc/engine/ --focus='SetPipelineStepDelegate persisted PostgreSQL state saves changed event'

Expected: FAIL on the concrete event value. Restore Changed true and rerun the command to PASS.

- [x] **Step 5: Verify payoff and commit.**

Run: rg -n 'new\(dbfakes\.(FakeBuild|FakePipeline|FakeResourceCache|FakeWorkerFactory)\)' atc/engine/engine_test.go atc/engine/set_pipeline_delegate_test.go atc/engine/put_delegate_test.go atc/engine/builder_test.go

Expected: one FakeBuild construction in engine_test.go’s retained runtime/fault Describe; the other six audited constructions are gone. The dbfakes import disappears from set_pipeline_delegate_test.go, put_delegate_test.go, and builder_test.go.

~~~bash
git add atc/engine/engine_test.go atc/engine/set_pipeline_delegate_test.go atc/engine/put_delegate_test.go atc/engine/builder_test.go
git commit -m "test(engine): persist first delegate success state"
~~~

## Task 3: Convert engine resource, cache, and scope success state

**Files:**

- Modify atc/engine/get_delegate_test.go
- Modify atc/engine/build_step_delegate_test.go
- Modify atc/engine/check_delegate_test.go

**Interfaces:**

- Consumes real job builds, resources, ResourceConfig.FindOrCreateScope(*int), ResourceConfigScope.SaveVersions, ResourceCacheFactory.FindOrCreateResourceCache, and the required wrappers.
- Produces persisted GetDelegate metadata, BuildStepDelegate cache/config chains, and CheckDelegate scope attachment.
- Retains one FakeBuild in each of build_step_delegate_test.go and check_delegate_test.go for runtime/timing matrices.

- [ ] **Step 1: Add the GetDelegate persisted assertion before rewiring it.**

Add Describe("persisted PostgreSQL state") to get_delegate_test.go. Call useEngineDB once, save a config with some-job and some-resource, create the job build, and use fixture.Builder.WithResourceVersions for info.Version. Initially leave the delegate on fakeBuild. After UpdateResourceVersion, reload the real ResourceConfigVersion and assert its metadata.

~~~go
version := scenario.ResourceVersion("some-resource", info.Version)
found, err := version.Reload()
Expect(err).NotTo(HaveOccurred())
Expect(found).To(BeTrue())
Expect(version.Metadata()).To(Equal(db.NewResourceConfigMetadataFields(info.Metadata)))
~~~

- [ ] **Step 2: Run the assertion-first RED command.**

Run: ginkgo ./atc/engine/ --focus='GetDelegate persisted PostgreSQL state updates resource version metadata'

Expected: FAIL because the real version metadata is empty while UpdateResourceVersion still targets the fake pipeline/resource.

- [ ] **Step 3: Implement GetDelegate success and exact failures.**

Wire Finished and successful UpdateResourceVersion to the real job build. Reload the build before consuming FinishGet. Use a real one-off build only for the genuine pipeline-not-found state because PipelineID is zero. Use pipelineErrorBuild{Build: realBuild, err: errors.New("nope")} for the pipeline error.

For the resource error, wrap the real pipeline as resourceErrorPipeline{Pipeline: pipeline, err: errors.New("nope")} and return it from pipelineResultBuild{Build: realBuild, pipeline: wrappedPipeline, found: true}. Use an absent resource name for real not-found. The wrapper comments state that healthy PostgreSQL cannot fail only that lookup while keeping the build/pipeline row.

- [ ] **Step 4: Convert BuildStepDelegate and CheckDelegate persisted Describes.**

In build_step_delegate_test.go create a job build and real config/scope/version/cache chain for every success path. Use db.ForBuild(realBuild.ID()) when creating caches. Replace the fake metadata cache and custom-type caches with rows from fixture.ResourceCacheFactory. Keep resolver, stepper, run-state, policy, and runtime volume fakes. Move the single FakeBuild plus callback/retry matrix into Describe("retained runtime and fault matrix"); replace generated cache/config fakes in normal paths with real values or one-method wrappers.

In check_delegate_test.go create config containing some-job, some-resource, a resource type, and a prototype. For a resource check, create the build through resource.CreateBuild rather than CreateOneOffBuild; for other pipeline-dependent paths use the real job build. Use real global and resource scopes. After PointToCheckedConfig call resource.Reload or pipeline.Reload, then assert the resource/resource-type/prototype resolves the same scope ID. After UpdateLastCheckStartTime or UpdateLastCheckEndTime, reload the scope and assert the timestamps/status changed. Keep clock, rate limiter, lock, and one FakeBuild under Describe("retained timing and fault matrix"). Use scopeErrorResourceConfig and latestVersionErrorScope around healthy real values for injected errors.

- [ ] **Step 5: Run GREEN and sensitivity checks.**

Run: ginkgo ./atc/engine/ --focus='(GetDelegate|BuildStepDelegate|CheckDelegate)'

Expected: PASS.

Temporarily use FindOrCreateScope(nil) in the persisted resource-scope setup while leaving the attached resource-scope assertion unchanged.

Run: ginkgo ./atc/engine/ --focus='CheckDelegate persisted PostgreSQL state'

Expected: FAIL because the global scope ID is not attached as the named resource scope. Restore FindOrCreateScope(&resourceID) and rerun to PASS.

- [ ] **Step 6: Verify payoff and commit.**

Run: rg -n 'new\(dbfakes\.(FakeBuild|FakePipeline|FakeResource|FakeResourceCache|FakeResourceConfig|FakeResourceConfigFactory|FakeResourceConfigScope|FakeResourceConfigVersion)\)' atc/engine/get_delegate_test.go atc/engine/build_step_delegate_test.go atc/engine/check_delegate_test.go

Expected: one retained FakeBuild in build_step_delegate_test.go and one retained FakeBuild in check_delegate_test.go. Nineteen of the 21 audited constructions are removed, and get_delegate_test.go drops its dbfakes import.

~~~bash
git add atc/engine/get_delegate_test.go atc/engine/build_step_delegate_test.go atc/engine/check_delegate_test.go
git commit -m "test(engine): persist resource cache and scope state"
~~~

## Task 4: Add real exec artifact state and remove the typed nil

**Files:**

- Modify atc/exec/put_step_test.go
- Modify atc/exec/artifact_input_step_test.go
- Modify atc/exec/artifact_output_step_test.go

**Interfaces:**

- Consumes useExecDB, real one-off builds, VolumeRepository.CreateVolumeWithHandle, CreatedVolume.InitializeArtifact(string, int), artifactErrorBuild, volumeResultArtifact, artifactResultBuild, initErrorVolume, and runtimeVolumeWithDB.
- Produces real artifact-to-build and volume-to-artifact associations.

- [ ] **Step 1: Add the output artifact assertion before installing the adapter.**

Add Describe("persisted PostgreSQL state") to artifact_output_step_test.go. Call useExecDB once, create team some-team, create a one-off build, save worker worker, and create/finalize an artifact volume with handle some-volume. Initially keep the step on fakeBuild and runtimetest.NewVolume. After Run, assert realBuild.Artifacts has one artifact named some-artifact-name.

- [ ] **Step 2: Run the assertion-first RED command.**

Run: ginkgo ./atc/exec/ --focus='ArtifactOutputStep persisted PostgreSQL state initializes output artifact'

Expected: FAIL with an empty realBuild.Artifacts result because the old runtimetest DBVolume is a FakeCreatedVolume and the step still uses fakeBuild.

- [ ] **Step 3: Implement real artifact input/output state and precise failures.**

Delete the typed dbfakes ResourceCache from put_step_test.go by declaring var imageResourceCache db.ResourceCache. Preserve the nil delegate-forwarding assertion.

For artifact input, create and finalize the real volume, then initialize it with both required arguments:

~~~go
artifact, err := created.InitializeArtifact("some-input-artifact-name", realBuild.ID())
Expect(err).NotTo(HaveOccurred())
plan.ArtifactInput.ArtifactID = artifact.ID()
~~~

Pass realBuild to NewArtifactInputStep. Locate the persisted created.TeamID and created.Handle through FakePool’s runtime seam, then assert repository registration. Use artifactErrorBuild around realBuild for Artifact failure. For deterministic volume error/not-found results, wrap the real artifact in volumeResultArtifact and inject it through artifactResultBuild:

~~~go
wrappedArtifact := volumeResultArtifact{
	WorkerArtifact: artifact,
	volume:         nil,
	found:          false,
	err:            errors.New("nope"),
}
wrappedBuild := artifactResultBuild{
	Build:    realBuild,
	artifact: wrappedArtifact,
}
~~~

For the not-found case set err to nil and found to false. Pass wrappedBuild to NewArtifactInputStep; do not pass realBuild in these two wrapper contexts.

For artifact output, wrap runtimetest.NewVolume in runtimeVolumeWithDB{Volume: runtimeVolume, databaseVolume: created} and pass realBuild. The step itself calls created.InitializeArtifact(artifactName, realBuild.ID()). Reload realBuild, read Artifacts, and assert Name, BuildID, and Volume(realBuild.TeamID()) returns the same persisted handle.

For initialization failure, replace only databaseVolume with initErrorVolume{CreatedVolume: created, err: errors.New("nope")}. Do not close a connection to fake Build.Artifact or CreatedVolume.InitializeArtifact errors.

- [ ] **Step 4: Run GREEN and sensitivity checks.**

Run: ginkgo ./atc/exec/ --focus='Artifact(Input|Output)Step'

Expected: PASS.

Temporarily increment the persisted artifact ID before assigning ArtifactInputPlan.ArtifactID.

Run: ginkgo ./atc/exec/ --focus='ArtifactInputStep persisted PostgreSQL state registers the artifact'

Expected: FAIL because realBuild.Artifact cannot find that ID. Restore artifact.ID() and rerun to PASS.

- [ ] **Step 5: Verify payoff and commit.**

Run: rg -n 'dbfakes|FakeCreatedVolume|FakeWorkerArtifact|FakeBuild' atc/exec/put_step_test.go atc/exec/artifact_input_step_test.go atc/exec/artifact_output_step_test.go

Expected: no dbfakes import, typed nil, or generated DB fake construction in these three files. All six audited artifact constructions are removed.

~~~bash
git add atc/exec/put_step_test.go atc/exec/artifact_input_step_test.go atc/exec/artifact_output_step_test.go
git commit -m "test(exec): persist artifact step state"
~~~

## Task 5: Convert exec cache, version, and valid set-pipeline state

**Files:**

- Modify atc/exec/get_step_test.go
- Modify atc/exec/check_step_test.go
- Modify atc/exec/set_pipeline_step_test.go

**Interfaces:**

- Consumes useExecDB, createExecJobBuild, real resource config/version/cache factories, and real team/build/pipeline values.
- Produces cache rows owned by db.ForBuild(realBuild.ID()), metadata read from those exact IDs, and persisted valid set-pipeline results.
- Retains policy, authorization, validator, streamer, delegate, pool, runtime, lock, tracing, and resolver seams.

- [ ] **Step 1: Add the GetStep row assertion before replacing the factory.**

Add Describe("persisted PostgreSQL state") to get_step_test.go. Call useExecDB once, save config with some-job and some-resource, create the real job build, and set:

~~~go
fakeDelegate.ResourceCacheUserReturns(db.ForBuild(realBuild.ID()))
~~~

Initially leave NewGetStep wired to fakeResourceCacheFactory. Add leaf “creates the resource cache row” that queries the clone:

~~~go
var count int
err := fixture.Conn.QueryRow(
		"SELECT count(*) FROM resource_cache_uses WHERE build_id = $1",
		realBuild.ID(),
	).Scan(&count)
Expect(err).NotTo(HaveOccurred())
Expect(count).To(Equal(1))
~~~

- [ ] **Step 2: Run the assertion-first RED command.**

Run: ginkgo ./atc/exec/ --focus='GetStep persisted PostgreSQL state creates the resource cache row'

Expected: FAIL with count zero because the generated fake factory did not write PostgreSQL.

- [ ] **Step 3: Implement real GetStep and CheckStep state.**

Pass fixture.ResourceCacheFactory to NewGetStep. Keep fakeDelegate.ResourceCacheUserReturns(db.ForBuild(realBuild.ID())). After the step, select the cache ID from the ownership table, then load it through the factory:

~~~go
var resourceCacheID int
err := fixture.Conn.QueryRow(
	"SELECT resource_cache_id FROM resource_cache_uses WHERE build_id = $1",
	realBuild.ID(),
).Scan(&resourceCacheID)
Expect(err).NotTo(HaveOccurred())
cache, found, err := fixture.ResourceCacheFactory.FindResourceCacheByID(resourceCacheID)
Expect(err).NotTo(HaveOccurred())
Expect(found).To(BeTrue())
~~~

Assert cache.Version, ResourceConfig source/type, and metadata. Cache-hit contexts call UpdateResourceCacheMetadata on that same loaded cache ID; do not create a second cache just to supply metadata. Custom-type contexts create the image cache as a real row and assert the resulting resource config references that same image cache ID. Factory error contexts may use db.NewResourceCacheFactory(closedExecCloneConn(), fixture.LockFactory), with the required closed-connection comment.

In check_step_test.go pass fixture.ResourceConfigFactory to NewCheckStep. Persist the base resource config, scope, and versions; reload the exact version before asserting run-state results. Persist custom-type image caches and compare IDs. Keep delegate/pool/runtime/lock/tracing/resolver seams. Use one-method wrappers around healthy config/scope only for deterministic method failures.

- [ ] **Step 4: Implement valid SetPipelineStep state and retain external seams.**

Create the current build with createExecJobBuild using config containing some-job and the exact current reference:

~~~go
currentRef := atc.PipelineRef{
	Name:         "parent-pipeline",
	InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
}
currentTeam, currentPipeline, currentJob, realBuild := createExecJobBuild(
	fixture,
	"some-team",
	currentRef,
	atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
	"some-user",
)
~~~

Use real IDs/names from currentTeam, currentPipeline, currentJob, and realBuild in stepMetadata. Pass fixture.TeamFactory and fixture.BuildFactory to NewSetPipelineStep. Separate Describe("persisted PostgreSQL success") from Describe("retained authorization validator and save faults"). Because currentPipeline is parent-pipeline, the ordinary some-pipeline target is genuinely absent until a context explicitly saves it. Existing target pipelines are saved with:

~~~go
targetRef := atc.PipelineRef{
	Name:         "some-pipeline",
	InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
}
target, _, err := currentTeam.SavePipeline(targetRef, pipelineObject, 0, false)
Expect(err).NotTo(HaveOccurred())
~~~

After the step, load currentTeam.Pipeline(targetRef), call Reload, assert found, Name, InstanceVars, Config, ParentJobID equals currentJob.ID, and ParentBuildID equals realBuild.ID. Construct a fresh unchangedConfig and changedConfig per BeforeEach; do not mutate the package-level pipelineObject across specs. Assert fakeDelegate.SetPipelineChanged receives false for unchangedConfig and true for changedConfig.

Keep set-pipeline self in its own context. Set spPlan.Name to "self", leave stepMetadata derived from currentPipeline, and after the step reread currentTeam.Pipeline(currentRef), not targetRef. Assert the self context updates parent-pipeline with currentRef.InstanceVars and ignores spPlan.Team exactly as production does.

Keep policy agent, delegate, streamer, parser/validator, and authorization seams. Use real current/target/admin teams for valid authorization states. Use pipelineLookupErrorTeam, configErrorPipeline, and savePipelineErrorBuild around healthy real state for the three DB method errors, each with its narrow-fault comment.

- [ ] **Step 5: Run GREEN, the exact positive focus, and sensitivities.**

Run: ginkgo ./atc/exec/ --focus='(GetStep|CheckStep|SetPipelineStep)'

Expected: PASS.

Run: ginkgo ./atc/exec/ --focus='SetPipelineStep when file is configured when pipeline file is good when specified pipeline not found should save the pipeline'

Expected: PASS. This is the exact positive hierarchy in set_pipeline_step_test.go.

Temporarily return db.ForBuild(realBuild.ID()+1) from ResourceCacheUser.

Run: ginkgo ./atc/exec/ --focus='GetStep persisted PostgreSQL state creates the resource cache row'

Expected: FAIL because the asserted build owns no cache row. Restore realBuild.ID() and rerun to PASS.

Temporarily query currentTeam.Pipeline with PipelineRef{Name: "some-pipeline"} without InstanceVars.

Run: ginkgo ./atc/exec/ --focus='SetPipelineStep persisted PostgreSQL success'

Expected: FAIL on found or instance vars. Restore targetRef and rerun to PASS.

- [ ] **Step 6: Verify payoff and commit.**

Run: rg -n 'new\(dbfakes\.(FakeResourceCache|FakeResourceCacheFactory|FakeResourceConfig|FakeResourceConfigFactory|FakeResourceConfigScope|FakeResourceConfigVersion|FakeTeamFactory|FakeBuildFactory|FakeBuild|FakeTeam|FakePipeline)\)' atc/exec/get_step_test.go atc/exec/check_step_test.go atc/exec/set_pipeline_step_test.go

Expected: no matches. All 15 audited constructions are removed from these files.

~~~bash
git add atc/exec/get_step_test.go atc/exec/check_step_test.go atc/exec/set_pipeline_step_test.go
git commit -m "test(exec): persist resource and pipeline state"
~~~

## Task 6: Verify, recount, review, and update the audit

**Files:**

- Modify .superpowers/sdd/2026-08-06-next-real-postgres-conversions/audit-remaining-core.md
- Test atc/engine and atc/exec

**Interfaces:**

- Consumes Tasks 1–5 and the audit’s direct-import/explicit-constructor counting rule.
- Produces observed counts and a retained-seam inventory; it does not claim an unverified global zero.

- [ ] **Step 1: Format and type-check exact signatures.**

Run: gofmt -w atc/engine/engine_suite_test.go atc/engine/engine_test.go atc/engine/set_pipeline_delegate_test.go atc/engine/put_delegate_test.go atc/engine/builder_test.go atc/engine/get_delegate_test.go atc/engine/build_step_delegate_test.go atc/engine/check_delegate_test.go atc/exec/exec_suite_test.go atc/exec/put_step_test.go atc/exec/artifact_input_step_test.go atc/exec/artifact_output_step_test.go atc/exec/get_step_test.go atc/exec/check_step_test.go atc/exec/set_pipeline_step_test.go

Run: go test ./atc/engine ./atc/exec -run '^$'

Expected: both packages compile, proving wrapper methods, InitializeArtifact arguments, runtime.Volume adapter, and factory signatures are correct.

- [ ] **Step 2: Run focused and full package verification.**

Run: pg_isready -h 127.0.0.1 -p 15432 -U postgres

Expected: accepting connections.

Run: ginkgo ./atc/engine/ --focus='(Engine|SetPipelineStepDelegate|PutDelegate|Builder|GetDelegate|BuildStepDelegate|CheckDelegate)'

Expected: PASS.

Run: ginkgo ./atc/exec/ --focus='(PutStep|ArtifactInputStep|ArtifactOutputStep|GetStep|CheckStep|SetPipelineStep)'

Expected: PASS.

Run: ginkgo ./atc/engine/ ./atc/exec/

Expected: both complete packages PASS.

Run: ginkgo -p ./atc/engine/ ./atc/exec/

Expected: PASS in parallel. Unrelated specs do not clone because only persisted-state Describes call the opt-in helpers.

- [ ] **Step 3: Recount imports and explicit constructors.**

Run:

~~~bash
rg -l 'github.com/concourse/concourse/atc/db/dbfakes' atc/engine atc/exec --glob '*_test.go'
rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z]+\{' atc/engine atc/exec --glob '*_test.go'
~~~

Expected target-file payoff: 25 of 28 audited engine constructors removed, leaving one documented FakeBuild in each of engine_test.go, build_step_delegate_test.go, and check_delegate_test.go. All 21 audited exec constructors are removed, and the separate typed-nil dbfakes reference in put_step_test.go is also removed. Four target engine files and all six target exec files drop their dbfakes imports. Record observed counts if a narrower retained seam changes those exact targets.

- [ ] **Step 4: Review lifecycle, assertions, and retained seams.**

Confirm from source and test output:

- postgresrunner.GinkgoRunner is the only suite setup node in exec; policy registration occurs once in init per process.
- No suite-wide BeforeEach creates clones. Each persisted-state Describe calls its opt-in helper once.
- Every clone connection closes before DropTestDB.
- Pipeline-dependent fixtures contain some-job and use Job.CreateBuild("some-user"); instance vars enter through Team.SavePipeline with the full PipelineRef.
- Every persisted event is consumed from Events, decoded, and its source closed.
- Every mutated build, pipeline, resource, version, or scope is reloaded before assertion.
- GetStep ResourceCacheUser is db.ForBuild(realBuild.ID()), and metadata is read/written through the cache ID created by the step.
- Artifact output uses runtimeVolumeWithDB; created.InitializeArtifact always receives name and build ID.
- pipelineErrorBuild, artifactErrorBuild, initErrorVolume, resourceErrorPipeline, pipelineResultBuild, scopeErrorResourceConfig, latestVersionErrorScope, savePipelineErrorBuild, pipelineLookupErrorTeam, configErrorPipeline, volumeResultArtifact, and artifactResultBuild embed healthy real state and carry narrow-fault comments.
- Persisted-success Describes are separate from retained runtime/fault/authorization/validator Describes.
- No product, benchmark, corpus, Docker, service lifecycle, phase6, or lifecycle-plan file changed.

- [ ] **Step 5: Update audit bookkeeping and commit.**

Update only the engine/exec rows, totals, and batch recommendations in audit-remaining-core.md from the observed commands.

~~~bash
git add .superpowers/sdd/2026-08-06-next-real-postgres-conversions/audit-remaining-core.md
git commit -m "docs: recount engine and exec database fakes"
~~~

## Self-Review

- Spec coverage: Task 1 resolves opt-in clone lifecycle and exec suite registration; Tasks 2–3 cover all named engine files; Tasks 4–5 cover all named exec files; Task 6 covers combined verification, recount, review, and audit bookkeeping.
- RED/GREEN coverage: Tasks 2–5 each add a persisted assertion, run a focused expected failure before rewiring the dependency, implement the conversion, then run GREEN. Separate post-GREEN sensitivity mutations prove the assertions remain state-sensitive.
- Type consistency: CreatedVolume.InitializeArtifact takes string and int; runtimeVolumeWithDB returns db.CreatedVolume; ResourceCacheUser uses db.ForBuild; wrapper overrides exactly match embedded interface signatures.
- Expected payoff: 25/28 engine constructors and 21/21 exec constructors in this phase are removed; the additional exec typed-nil reference is removed separately, and three generated engine FakeBuild constructors remain as explicit runtime/timing seams.
- Placeholder scan: every code step names concrete APIs, commands, expected outcomes, and commit messages.
