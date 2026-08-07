# Real PostgreSQL Lifecycle Phase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ordinary persisted-state fakes in the selected tracker, run-lifecycle, workflow-run, template, and build-log lifecycle tests with isolated PostgreSQL rows while retaining only explicit non-database and deterministic fault seams.

**Architecture:** `atc/builds` and `agent/workflowrun` use `postgresrunner.StandardTestRunner`, one migrated template per package, and one clone per `testing.T`; `atc/runlifecycle` uses an opt-in Ginkgo runner and one clone per persisted-state spec; `atc/gc` reuses its existing clone-per-spec harness. Fixtures construct real teams, pipelines, jobs, resources, workflow runs, builds, pipeline runs, artifacts, and event rows through production factories. Assertions reload rows or query persisted columns instead of observing database fake call counts.

**Tech Stack:** Go, Testify, Ginkgo v2/Gomega, PostgreSQL, `atc/postgresrunner`, Concourse `atc/db` factories.

## Global Constraints

- Use the already-running PostgreSQL service at `127.0.0.1:15432`; verify it with `pg_isready -h 127.0.0.1 -p 15432 -U postgres`. Never start, stop, recreate, or alter Docker or the service.
- Every persisted-state test owns a unique migrated clone and is safe when package commands overlap.
- Do not change production code unless a new persisted assertion first fails against the unmodified implementation. Restore every temporary sensitivity mutation before committing.
- A normal database failure uses a separately opened clone-local connection which is closed without closing the fixture connection. A selective method failure on an already-created row remains a commented narrow fake or one-method wrapper.
- Preserve `buildsfakes.Engine`/`Runnable`, in-memory check identity, durable CAS/order seams, wait cancellation, synthetic collision/race states, and exact collector method failures.
- Exclude `bench/**` and `**/corpus/**`; do not change generated fakes; do not push.
- Run focused verification before whole-package and concurrent verification. Do not use `--race`.

---

## File Structure

| File | Responsibility after the phase |
|---|---|
| `atc/builds/tracker_test.go` | `TestMain`, StandardTestRunner fixture, real job/check build discovery, non-DB engine behavior, and one exact lookup error. |
| `atc/runlifecycle/runlifecycle_suite_test.go` | Opt-in clone lifecycle plus a fully constructed `PipelineRunFactory`. |
| `atc/runlifecycle/lifecycler_test.go` | Real completion, reopen, policy archive, and retired-template outcomes; selective per-run errors remain fake. |
| `agent/workflowrun/real_db_test.go` | `TestMain`, StandardTestRunner fixture, mutex lock DB, full team/build/template/run factories, and durable linked-execution helpers. |
| `agent/workflowrun/canceler_test.go` | Real selected-build/ownership/missing-build behavior and local durable/lookup fault seams. |
| `agent/workflowrun/template_saver_test.go` | Real create/reuse/unowned-template behavior; synthetic authority, mutation, race, and reread states remain fake. |
| `atc/gc/build_log_collector_test.go` | Real retention scenario table and persisted event/cursor assertions; four exact method-fault constructors remain. |

## Exact Interface Inventory

The implementation must use these current signatures without widening production interfaces:

```go
// atc/db/pipeline_run_factory.go
CreateRun(templatePipelineID int, params map[string]any, createdBy string) (db.PipelineRun, error)
CreateRunForWorkflowRun(context.Context, snapshot.WorkflowRunID, db.WorkflowRunTemplateRef, workflow.ExecutionEnvelope, string, db.BeforeWorkflowRunCommit) (db.WorkflowRunExecution, bool, error)

// atc/db/team_factory.go and team.go
FindTeam(string) (db.Team, bool, error)
Pipeline(atc.PipelineRef) (db.Pipeline, bool, error)
SavePipeline(atc.PipelineRef, atc.Config, db.ConfigVersion, bool) (db.Pipeline, bool, error)

// atc/db/workflow_run_template_factory.go
SaveWorkflowRunTemplate(context.Context, int, atc.PipelineRef, atc.Config) (db.Pipeline, bool, error)
IsWorkflowRunTemplate(context.Context, int) (bool, error)

// atc/db/resource.go
CreateBuild(context.Context, bool, atc.Plan) (db.Build, bool, error)

// agent/workflowrun local BuildLookup
BuildForAPI(int) (db.BuildForAPI, bool, error)
```

### Task 1: Persist tracker started-build discovery

**Files:**
- Modify: `atc/builds/tracker_test.go`

**Interfaces:**
- Consumes: `postgresrunner.StandardTestRunner.Main(*testing.M) int`, `OpenConn(*testing.T) db.DbConn`, `db.Team.CreateStartedBuild`, `db.Resource.CreateBuild`, and `db.BuildFactory.GetAllStartedBuilds`.
- Produces: `useRealTrackerDB(*testing.T) trackerDB`, `createTrackerStartedBuild`, and `createTrackerCheckBuild`.

- [x] **Step 1: Add `TestMain` and the clone-local tracker fixture.**

  Add a new package entry point, not a change to the existing `TestTracker` suite entry point:

  ```go
  var trackerPostgres postgresrunner.StandardTestRunner

  func TestMain(m *testing.M) {
      os.Exit(trackerPostgres.Main(m))
  }

  type trackerDB struct {
      Conn   db.DbConn
      Teams  db.TeamFactory
      Builds db.BuildFactory
  }
  ```

  `useRealTrackerDB(t)` calls `trackerPostgres.OpenConn(t)`, clears the base-resource cache, and builds `db.NewTeamFactory` and `db.NewBuildFactory(conn, locks, 0, time.Hour)`. Define `trackerLockDB` with `sync.Mutex` and `map[string]bool`; its exact `Acquire(lock.LockID) (bool, error)` and `Release(lock.LockID) (bool, error)` behavior is the mutex-backed implementation in `agent/resourcecapture/resourcecapture_suite_test.go`. This is in-memory lock coordination inside one test clone, not a transaction or a second SQL connection.

- [x] **Step 2: Add one assertion against the old fake dependency and observe RED.**

  In `TestTrackRunsStartedBuilds`, create a real team and real started rows, but initially leave `s.tracker` constructed with `s.fakeBuildFactory`. Use a bounded receive so the failing test cannot hang:

  ```go
  select {
  case got := <-running:
      s.Equal(started.ID(), got.ID())
  case <-time.After(time.Second):
      s.Fail("real started build was not discovered")
  }
  ```

  Run: `go test ./atc/builds -run 'TestTracker/TestTrackRunsStartedBuilds' -count=1`

  Expected: FAIL with `real started build was not discovered`; the old `FakeBuildFactory` has no persisted rows.

- [x] **Step 3: Rewire real discovery and create genuine job and check builds.**

  Reconstruct the tracker in each converted test with `fixture.Builds`, the existing `buildsfakes.FakeEngine`, and the existing channel. Ordinary started rows may use `team.CreateStartedBuild(atc.Plan{ID: "tracker-one-off"})`; the released-job case must save `atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}`, call `job.CreateBuild("tracker")`, and call `build.Start(atc.Plan{ID: "tracker-job"})`.

  A real check is not a one-off build renamed in memory. Save a resource pipeline and use the exact production signature:

  ```go
  pipeline, _, err := team.SavePipeline(
      atc.PipelineRef{Name: "tracker-checks"},
      atc.Config{Resources: atc.ResourceConfigs{{
          Name: "some-resource", Type: "some-base-type", Source: atc.Source{"key": "value"},
      }}},
      db.ConfigVersion(0), false,
  )
  s.Require().NoError(err)
  resource, found, err := pipeline.Resource("some-resource")
  s.Require().NoError(err)
  s.True(found)
  check, created, err := resource.CreateBuild(context.Background(), true, atc.Plan{
      ID: "tracker-check",
      Check: &atc.CheckPlan{
          Name: resource.Name(), Type: resource.Type(), Source: resource.Source(), Resource: resource.Name(),
      },
  })
  s.Require().NoError(err)
  s.True(created)
  s.Equal(db.CheckBuildName, check.Name())
  ```

  Use this helper in orphaned-check finalization and the check gauge test. Keep only the three lexical `FakeBuild` constructors used by the in-memory tests (`ID()==0` plus `ResourceID()`) and one `FakeBuildFactory` in `TestTrackerReturnsStartedBuildLookupError`, commented: `// Retained: ordinary rows cannot make GetAllStartedBuilds return an error.`

- [x] **Step 4: Assert asynchronous persisted outcomes with reloads.**

  Replace sleeps with `s.Eventually` and reload through `fixture.Builds.Build(id)` on every poll:

  ```go
  s.Eventually(func() bool {
      reloaded, found, err := fixture.Builds.Build(check.ID())
      return err == nil && found && reloaded.Status() == db.BuildStatusErrored
  }, time.Second, 10*time.Millisecond)
  ```

  For panic isolation assert only the panicking build becomes errored and the other two IDs reach the engine. For duplicate tracking use the blocked runnable and assert no second channel send. For a returned job build assert the reloaded row remains started. For normal completion finish the row from the runnable and assert the tracker does not change its terminal status. Keep engine, drain, metrics, and panic delivery as non-database seams.

  Run: `go test ./atc/builds -count=1`

  Expected: PASS.

- [x] **Step 5: Run a post-GREEN sensitivity mutation and commit.**

  Temporarily change `GetAllStartedBuilds` to select pending rather than started rows, run `go test ./atc/builds -run 'TestTracker/TestTrackRunsStartedBuilds' -count=1`, and confirm the bounded discovery assertion fails. Restore `atc/db/build_factory.go`, rerun the focused test to PASS, then commit only the test:

  ```bash
  git add atc/builds/tracker_test.go
  git commit -m "test: persist tracker lifecycle fixtures"
  ```

### Task 2: Persist pipeline-run lifecycle outcomes with an opt-in Ginkgo fixture

**Files:**
- Modify: `atc/runlifecycle/runlifecycle_suite_test.go`
- Modify: `atc/runlifecycle/lifecycler_test.go`

**Interfaces:**
- Consumes: `db.NewCheckFactory(db.DbConn, lock.LockFactory, creds.Secrets, creds.VarSourcePool, chan<- db.Build, util.SequenceGenerator) db.CheckFactory` and `db.NewPipelineRunFactory(lager.Logger, db.DbConn, lock.LockFactory, db.CheckFactory) db.PipelineRunFactory`.
- Produces: `useLifecycleDB() *lifecycleDB`, `quiesceRun`, and `completeRun`.

- [x] **Step 1: Add the opt-in clone API and all six CheckFactory collaborators.**

  Keep `TestRunLifecycle` unchanged and add:

  ```go
  type lifecycleDB struct {
      Conn         db.DbConn
      Team         db.Team
      Runs         db.PipelineRunFactory
      Templates    db.WorkflowRunTemplateFactory
      WorkflowRuns db.AgentWorkflowRunsFactory
  }

  var lifecyclePostgres postgresrunner.Runner
  var _ = postgresrunner.GinkgoRunner(&lifecyclePostgres)
  ```

  `useLifecycleDB()` calls `CreateTestDBFromTemplate`, registers `DropTestDB` first, opens and registers cleanup for the ordinary connection, clears base-resource cache, opens all `lock.FactoryCount` singleton connections and registers each close, then creates the lock factory with the exact production constructor. Ginkgo runs cleanups last-in-first-out, so every ordinary and singleton connection closes before the clone is dropped:

  ```go
  var lockConns [lock.FactoryCount]*sql.DB
  for index := range lockConns {
      singleton := lifecyclePostgres.OpenSingleton()
      lockConns[index] = singleton
      DeferCleanup(func() { Expect(singleton.Close()).To(Succeed()) })
  }
  noOpLockLog := func(lager.Logger, lock.LockID) {}
  lockFactory := lock.NewLockFactory(lockConns, noOpLockLog, noOpLockLog)
  ```

  Construct the check factory exactly as follows; the credential objects are retained non-database seams and the channel is buffered because no lidar consumer runs in this suite:

  ```go
  checkFactory := db.NewCheckFactory(
      conn,
      lockFactory,
      new(credsfakes.FakeSecrets),
      new(credsfakes.FakeVarSourcePool),
      make(chan db.Build, 16),
      util.NewSequenceGenerator(1),
  )
  runs := db.NewPipelineRunFactory(
      lagertest.NewTestLogger("runlifecycle-postgres"), conn, lockFactory, checkFactory,
  )
  ```

  Create a unique real team and return the same-connection factories explicitly:

  ```go
  return &lifecycleDB{
      Conn: conn, Team: team, Runs: runs,
      Templates: db.NewWorkflowRunTemplateFactory(conn, lockFactory),
      WorkflowRuns: db.NewAgentWorkflowRunsFactory(conn),
  }
  ```

  Only the persisted-state `Describe` calls this helper from its `BeforeEach`; the selective fault `Describe` creates no clone.

- [x] **Step 2: Define exact quiescent and completed run helpers.**

  Save a template config with `Template: true` and entry/second jobs. `quiesceRun(run, terminalBuildStatus)` must load the instance pipeline, enumerate `instance.Jobs()`, finish every build returned by each job's `GetPendingBuilds()`, and then execute:

  ```sql
  UPDATE jobs
  SET last_scheduled = schedule_requested
  WHERE pipeline_id = $1
  ```

  Assert `run.CheckComplete()` returns `(expectedPipelineRunStatus, true, nil)` but leave the run status `running`; this is the fixture used to prove `Lifecycler.Run` performs `Finish`. `completeRun` calls `quiesceRun` and then `run.Finish(expectedPipelineRunStatus)`. It never marks a run complete while an entry build remains pending.

- [x] **Step 3: Add persisted assertions against the old fake factory and observe RED.**

  Create a real quiescent failed run and initially leave `lifecycler` constructed with `FakePipelineRunFactory`. Run it, reload with `fixture.Runs.GetRun(template.ID(), run.Number())`, and assert `Status()==db.PipelineRunFailed`.

  Run: `ginkgo --focus='Lifecycler persisted PostgreSQL state finishes complete runs' ./atc/runlifecycle`

  Expected: FAIL because the old fake factory never discovers the real running row.

- [x] **Step 4: Rewire and cover completion, reopen, archive, and retirement.**

  Construct `runlifecycle.NewLifecycler(fixture.Runs, 720*time.Hour)`. Cover:

  - one quiescent failed run and one pending incomplete run; reload both and assert only the quiescent run finishes;
  - a `completeRun` plus a newly created pending build on its instance entry job; reload and assert `running` with no `CompletedAt` after reopen;
  - a template with `RunRetention: &atc.RunRetentionConfig{KeepLast: 1}` and two `completeRun` rows; assert only the older run and its instance pipeline archive;
  - the combined generic finish/reopen/archive passes in one spec using three distinct templates;
  - an owned workflow-run template, terminal durable citation, strictly newer live workflow definition, terminal pipeline run backdated 31 days, and no template `run_retention`; assert retirement archives the run and its instance;
  - the same eligible retirement row with `NewLifecycler(fixture.Runs, 0)`; assert it remains unarchived.

  Build the retirement fixture through a package-local `createRetiredTemplateRun` helper, not an unowned `Team.SavePipeline` shortcut. Render a v1 `workflow.FunctionTarget`, insert its definition row as non-live, create the admitting durable row through `fixture.WorkflowRuns.CreateWithInputs`, save `rendered.Config` through `fixture.Templates.SaveWorkflowRunTemplate`, and call `fixture.Runs.CreateRunForWorkflowRun` with the complete `db.WorkflowRunTemplateRef` and `rendered.ExecutionEnvelope`. Require every returned `created` flag.

  Preserve this exact durable/build ordering:

  1. transition the durable row `admitting -> running` and reload it;
  2. while it is still active, quiesce the instance by finishing the selected entry build with `db.BuildStatusErrored`, mark every instance job scheduled, require `CheckComplete()==(db.PipelineRunErrored, true, nil)`, and call `run.Finish(db.PipelineRunErrored)`;
  3. reload through `fixture.WorkflowRuns.Get`, require `ExecutionStatus` is non-nil and equals `db.AgentWorkflowRunExecutionStatusErrored`, and retain that exact pointer value;
  4. call `fixture.WorkflowRuns.Finalize` with `WorkflowRunID`, `ExpectedStatus: db.AgentWorkflowRunStatusRunning`, `ExpectedExecutionStatus` equal to the captured value, `ExpectedActualPlanHash: nil`, `TerminalStatus: db.AgentWorkflowRunStatusErrored`, and a bounded error message; require no error and `applied==true`;
  5. only then insert the same workflow name at version 2 with `live=true`, backdate `pipeline_runs.completed_at` by 31 days, and reload both rows.

  `Build.Finish` captures the selected workflow execution result, so it must run before durable finalization; terminalizing first with nil execution evidence would make the build finish roll back as an immutable-history conflict. Require the final durable status, captured execution status, exact `TemplatePipelineID`, terminal pipeline-run status, and age before running the lifecycler. This makes each retirement predicate—server ownership, terminal citation, strictly newer live definition, absent run retention, terminal run, and age—visible in the fixture.

  Keep one `FakePipelineRunFactory` plus four `FakePipelineRun` constructors for only two selective-error specs: `CheckComplete` error followed by a good run, and `Archive` error followed by a good retired-template run. Comment that a healthy clone cannot make only one selected row's method fail while the next succeeds.

  Run: `ginkgo ./atc/runlifecycle`

  Expected: PASS.

- [x] **Step 5: Prove status sensitivity and commit.**

  Temporarily pass `db.PipelineRunSucceeded` rather than the aggregate returned by `CheckComplete` to `run.Finish`, run the focused failed-completion spec, and confirm its reload reports the wrong status. Restore `atc/runlifecycle/lifecycler.go`, rerun to PASS, then:

  ```bash
  git add atc/runlifecycle/runlifecycle_suite_test.go atc/runlifecycle/lifecycler_test.go
  git commit -m "test: persist run lifecycle outcomes"
  ```

### Task 3: Build a complete workflow-run fixture and persist cancellation ownership

**Files:**
- Create: `agent/workflowrun/real_db_test.go`
- Modify: `agent/workflowrun/canceler_test.go`

**Interfaces:**
- Consumes: all exact interfaces in the inventory, `db.AgentWorkflowRunsFactory.Transition`, `Get`, and `ValidateCancellationTarget`.
- Produces: `useRealWorkflowRunDB(*testing.T) workflowRunDB`, `createDurableRun`, and `createLinkedExecution(*testing.T, workflowRunDB) (db.AgentWorkflowRun, db.BuildForAPI)`.

- [x] **Step 1: Add `TestMain`, the mutex lock DB, and the complete fixture.**

  `agent/workflowrun` has no package `TestMain`; add it in `real_db_test.go`:

  ```go
  var workflowRunPostgres postgresrunner.StandardTestRunner

  func TestMain(m *testing.M) {
      os.Exit(workflowRunPostgres.Main(m))
  }

  type workflowRunDB struct {
      Conn         db.DbConn
      Team         db.Team
      Teams        db.TeamFactory
      Builds       db.BuildFactory
      Runs         db.AgentWorkflowRunsFactory
      Templates    db.WorkflowRunTemplateFactory
      PipelineRuns db.PipelineRunFactory
  }
  ```

  Define `workflowRunLockDB` with `sync.Mutex`, `held map[string]bool`, and exact `Acquire(lock.LockID) (bool, error)` / `Release(lock.LockID) (bool, error)` methods. `useRealWorkflowRunDB(t)` opens the StandardTestRunner connection, constructs `lock.NewTestLockFactory`, team/build/run/template factories, and a full pipeline-run factory. Its check factory uses the same six concrete collaborators as Task 2:

  ```go
  checks := db.NewCheckFactory(
      conn, locks,
      new(credsfakes.FakeSecrets), new(credsfakes.FakeVarSourcePool),
      make(chan db.Build, 16), util.NewSequenceGenerator(1),
  )
  pipelineRuns := db.NewPipelineRunFactory(
      lagertest.NewTestLogger("workflowrun-postgres"), conn, locks, checks,
  )
  ```

  Create `Team` through the same `Teams` factory returned in the struct. No factory or row comes from another connection or clone.

- [x] **Step 2: Implement a fully linked durable execution helper.**

  `createDurableRun(t, fixture)` returns `(db.AgentWorkflowRun, workflow.RenderedFunction)`. Insert the definition first, then render with that exact database ID and create the durable row:

  ```go
  definitionName := fmt.Sprintf("canceler-workflow-%d", time.Now().UnixNano())
  definitionSource := fmt.Sprintf(`schema_version: 3
  name: %s
  signature_version: 1
  inputs: []
  outputs: []
  plan:
    - agent: run
      function_id: run
      prompt: test
  `, definitionName)
  definitionHash := workflow.Manifest{
      workflow.LegacyWorkflowFileName: definitionSource,
  }.Hash()
  var definitionID int
  require.NoError(t, fixture.Conn.QueryRow(`
      INSERT INTO agent_workflow_definitions
          (definition_kind, name, version, content_hash, definition, created_by,
           schema_version, signature_version, live)
      VALUES ('workflow', $1, 1, $2, $3, 'alice', 3, 1, true)
      RETURNING id
  `, definitionName, definitionHash, definitionSource).Scan(&definitionID))

  rendered, err := workflow.RenderFunction(workflow.FunctionTarget{
      Kind: workflow.TargetWorkflow,
      WorkflowDefinitionID: definitionID,
      WorkflowName: definitionName,
      WorkflowVersion: 1,
      SignatureVersion: 1,
      Function: workflow.FunctionConfig{
          SignatureVersion: 1,
          Plan: []atc.Step{{Config: &atc.TaskStep{
              Name: "run", FunctionID: "run",
              Config: &atc.TaskConfig{
                  Platform: "linux",
                  ImageResource: &atc.ImageResource{
                      Type: "registry-image",
                      Source: atc.Source{"repository": "example/task"},
                      Version: atc.Version{"digest": "sha256:immutable"},
                  },
                  Run: atc.TaskRunConfig{Path: "/bin/true"},
              },
          }}},
      },
  })
  require.NoError(t, err)
  canonical, err := rendered.Config.CanonicalJSON()
  require.NoError(t, err)
  targetHash, err := workflow.TargetConfigHash(rendered.Config)
  require.NoError(t, err)
  durable, created, err := fixture.Runs.CreateWithInputs(context.Background(), db.AgentWorkflowRunCreateRequest{
      DefinitionKind: workflow.DefinitionKindWorkflow,
      TeamID: fixture.Team.ID(), TeamName: fixture.Team.Name(),
      WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 1,
      SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definitionHash,
      IdempotencyKey: fmt.Sprintf("cancel-%d", time.Now().UnixNano()),
      ParameterizedConfig: json.RawMessage(canonical), ParameterizedConfigHash: targetHash,
      OriginKind: "manual", CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
      Inputs: map[string]snapshot.SnapshotRef{},
  })
  require.NoError(t, err)
  require.True(t, created)
  ```

  `createLinkedExecution` saves the rendered config through `Templates.SaveWorkflowRunTemplate`, constructs the full comparable reference, builds the envelope with the allocated durable ID, and invokes the exact transactional API:

  ```go
  template, created, err := fixture.Templates.SaveWorkflowRunTemplate(
      context.Background(), fixture.Team.ID(),
      atc.PipelineRef{Name: fmt.Sprintf("cancel-template-%d", durable.ID)},
      rendered.Config,
  )
  require.NoError(t, err)
  require.True(t, created)
  targetHash, err := workflow.TargetConfigHash(rendered.Config)
  require.NoError(t, err)
  templateRef := db.WorkflowRunTemplateRef{
      PipelineID: template.ID(), TeamID: fixture.Team.ID(), Name: template.Name(),
      ConfigVersion: int(template.ConfigVersion()), FullHash: targetHash,
  }
  envelope, err := rendered.ExecutionEnvelope(map[string]any{
      "workflow_run_id": durable.ID.String(),
  })
  require.NoError(t, err)
  execution, created, err := fixture.PipelineRuns.CreateRunForWorkflowRun(
      context.Background(), durable.ID, templateRef, envelope, "alice",
      func(context.Context, int) error { return nil },
  )
  require.NoError(t, err)
  require.True(t, created)
  ```

  Reload with `fixture.Runs.Get`, and assert all committed linkage before any status transition:

  ```go
  stored, found, err := fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
  require.NoError(t, err)
  require.True(t, found)
  require.NotNil(t, stored.PipelineRunID)
  require.Equal(t, execution.PipelineRun.ID(), *stored.PipelineRunID)
  require.NotNil(t, stored.TemplatePipelineID)
  require.Equal(t, templateRef.PipelineID, *stored.TemplatePipelineID)
  instanceID, hasInstance := execution.PipelineRun.InstancePipelineID()
  require.True(t, hasInstance)
  require.NotNil(t, stored.InstancePipelineID)
  require.Equal(t, instanceID, *stored.InstancePipelineID)
  require.Len(t, execution.EntryBuildIDs, 1)
  require.NotNil(t, stored.PlannedBuildID)
  require.Equal(t, int64(execution.EntryBuildIDs[0]), *stored.PlannedBuildID)
  require.JSONEq(t, string(execution.InstanceCanonicalJSON), string(stored.ConcreteConfig))
  require.NotNil(t, stored.ConcreteConfigHash)
  require.Equal(t, execution.InstanceConfigHash, *stored.ConcreteConfigHash)
  ```

  Transition `admitting -> running` with `fixture.Runs.Transition`, require `changed==true`, reload again, require `StatusRunning`, load the selected row with `fixture.Builds.BuildForAPI`, and return the reloaded run plus real selected build. `CreateRunForWorkflowRun` commits the link and entry build in one transaction; the later status transition is a separate durable operation and must not be described as part of that transaction.

- [x] **Step 3: Add selected-build assertions against the old dependencies and observe RED.**

  In `TestCancelerAbortsOnlyExactSelectedBuild`, create the linked real execution but initially invoke the current stub store/fake lookup canceler. Parameterize that old arrangement from the real fixture rather than retaining its literal `teamID=9`, `runID=71`, and `buildID=313`: copy the reloaded linked run into `running`, copy it again with `StatusCanceling`, make every store callback validate `fixture.Team.ID()`, `running.ID`, and `*running.PlannedBuildID`, and configure the fake lookup/build to return those same real build and team IDs. Call `Cancel(ctx, fixture.Team.ID(), running.ID)`. Assert the real selected row's persisted `aborted` column is true. The old dependencies mark only their fake value, so this assertion fails for the claimed reason rather than at scope validation or against an unrelated synthetic run.

  Run: `go test ./agent/workflowrun -run TestCancelerAbortsOnlyExactSelectedBuild -count=1`

  Expected: FAIL because the persisted selected build remains unaborted.

- [x] **Step 4: Rewire the linked and unlinked ownership tests.**

  For the linked case pass `fixture.Runs` and `fixture.Builds` to `NewCanceler`. The helper returns a `running` durable row; `Canceler.Cancel` itself must perform `running -> canceling`, validate the full ownership join, mark the selected build aborted, reload the durable run, and return `canceling`. Assert the persisted `builds.aborted` column and full returned run ID/team/status.

  For the unlinked case:

  1. create an admitting durable row with `createDurableRun` but do not call `CreateRunForWorkflowRun`;
  2. call `fixture.Team.CreateStartedBuild(atc.Plan{ID: "unlinked-cancellation-target"})`, not `CreateOneOffBuild`, so `MarkAsAborted` cannot fail on a jobless pending scheduling path;
  3. update only `agent_workflow_runs.planned_build_id` to the started build ID;
  4. reload through `fixture.Runs.Get`, require the pointer contains that ID, transition `admitting -> canceling`, and reload again;
  5. call `fixture.Runs.ValidateCancellationTarget(ctx, teamID, runID, buildID)` directly and require `(false, nil)` before calling the canceler;
  6. call `NewCanceler(fixture.Runs, fixture.Builds).Cancel`, require `ErrCancelFailure`, and query `builds.aborted` to require false.

  This proves rejection comes from the missing pipeline-run/instance/job ownership chain, not from a pending-build abort rollback.

- [x] **Step 5: Replace generated lookup-only fakes with exact local adapters.**

  Define:

  ```go
  type buildLookupStub struct {
      lookup func(int) (db.BuildForAPI, bool, error)
  }
  func (stub buildLookupStub) BuildForAPI(id int) (db.BuildForAPI, bool, error) {
      return stub.lookup(id)
  }

  type wrongIdentityBuild struct {
      db.BuildForAPI
      id int
  }
  func (build wrongIdentityBuild) ID() int { return build.id }
  ```

  Use a healthy real build embedded in `wrongIdentityBuild` for the identity mismatch and `buildLookupStub` only for the deterministic lookup error. A missing selected build is ordinary persisted state: create an admitting durable row, SQL-set a positive nonexistent `planned_build_id` (the live schema intentionally dropped that FK), transition it to canceling, and use the real `fixture.Builds`; require Cancel returns the unchanged canceling run. Keep `cancellationStoreStub` for CAS ordering, terminal conflicts, durable dependency errors, and waits. No generated `dbfakes` constructor is needed in `canceler_test.go`.

- [x] **Step 6: Run GREEN, linkage sensitivity, and commit.**

  Run: `go test ./agent/workflowrun -run TestCanceler -count=1`

  Expected: PASS.

  Temporarily bypass `ValidateCancellationTarget` in `Canceler.abortSelectedBuild`, run `go test ./agent/workflowrun -run TestCancelerRejectsSelectedBuildWithoutDurableInstanceAndJobLinkage -count=1`, and confirm the persisted `aborted=false` assertion fails. Restore `canceler.go`, rerun to PASS, then:

  ```bash
  git add agent/workflowrun/real_db_test.go agent/workflowrun/canceler_test.go
  git commit -m "test: persist workflow run cancellation selection"
  ```

### Task 4: Persist template saver create, reuse, and ownership

**Files:**
- Modify: `agent/workflowrun/template_saver_test.go`

**Interfaces:**
- Consumes: `workflowRunDB.Teams`, `workflowRunDB.Team`, `workflowRunDB.Templates`, and the exact team/template interfaces in the inventory.
- Produces: complete `WorkflowRunTemplateRef` comparisons against real pipeline identity/version/hash.

- [ ] **Step 1: Add a persisted creation assertion against the old saver and observe RED.**

  In `TestTemplateSaverCreatesWithCreateOnlyVersion`, allocate `fixture := useRealWorkflowRunDB(t)` but initially leave the saver on the current fake team/store. After `SaveOrReuse`, query `fixture.Team.Pipeline(atc.PipelineRef{Name: spec.Name})` and require `found==true`.

  Run: `go test ./agent/workflowrun -run TestTemplateSaverCreatesWithCreateOnlyVersion -count=1`

  Expected: FAIL because the old stub reports a synthetic pipeline but creates no row in the clone.

- [ ] **Step 2: Rewire create, exact reuse, and exact-unowned rejection.**

  Construct `NewTemplateSaver(fixture.Teams, fixture.Templates)`. For creation, load the resulting pipeline and compare the complete value:

  ```go
  want := WorkflowRunTemplateRef{
      PipelineID: pipeline.ID(), TeamID: fixture.Team.ID(), Name: spec.Name,
      ConfigVersion: int(pipeline.ConfigVersion()), FullHash: spec.FullHash,
  }
  require.Equal(t, want, ref)
  owned, err := fixture.Templates.IsWorkflowRunTemplate(ctx, pipeline.ID())
  require.NoError(t, err)
  require.True(t, owned)
  ```

  For exact reuse, pre-create through `SaveWorkflowRunTemplate`, call `SaveOrReuse` twice, and require both full refs equal the pre-created pipeline's ID/team/name/config-version/hash. For ownership rejection, create the same template config through `fixture.Team.SavePipeline` rather than the ownership factory, require `IsWorkflowRunTemplate==false`, and require `ErrImmutableTemplateCollision`.

  Remove only the three `FakeTeam` constructor sites for these tests. Retain eight explicit constructors: seven `FakeTeam` sites for the two authority collisions, mutable-shape matrix, concurrent creator, two drift cases, and authoritative team identity; plus the one `FakePipeline` in `exactTemplatePipeline`. Comment every retained `FakeTeam`/`FakePipeline` site with its synthetic authority, mutation, race, or reread role.

  Run: `go test ./agent/workflowrun -run TestTemplateSaver -count=1`

  Expected: PASS.

- [ ] **Step 3: Prove the unowned assertion is sensitive and commit.**

  Temporarily force `owned = true` after `IsWorkflowRunTemplate` inside `validateImmutableTemplate`, run `go test ./agent/workflowrun -run TestTemplateSaverRejectsAnExactButUnownedPipeline -count=1`, and confirm the expected collision fails because the saver now returns a ref. Restore `template_saver.go`, rerun to PASS, then:

  ```bash
  git add agent/workflowrun/template_saver_test.go
  git commit -m "test: persist workflow template reuse"
  ```

### Task 5: Persist BuildLogCollector retention outcomes

**Files:**
- Modify: `atc/gc/build_log_collector_test.go`

**Interfaces:**
- Consumes: existing GC `dbConn`, `lockFactory`, `db.NewPipelineFactory`, `db.NewPipelineLifecycle`, `Job.CreateBuild`, `Build.Start`, `Build.Finish`, `Build.SetDrained`, `Pipeline.Pause`, and `Job.Pause`.
- Produces: `runRetentionScenario(retentionScenario)` plus persisted build-event and `first_logged_build_id` assertions.

- [ ] **Step 1: Define the real fixture records and event assertions.**

  Define `retentionBuild` with `name`, `status db.BuildStatus`, `completed bool`, `drained bool`, `endAgo time.Duration`, and `reapAgo time.Duration`; define `retentionScenario` with job retention, calculator, drainer flag, paused pipeline/job flags, builds oldest-to-newest, expected deleted names, and expected first-logged name.

  The helper saves a fresh one-job pipeline, creates builds in listed order, starts every row with `atc.Plan{ID: atc.PlanID("log-"+name)}`, finishes rows whose `completed` field is true, calls `SetDrained`, backdates `end_time`, sets `reap_time` only when requested, explicitly sets the job's `first_logged_build_id` to the oldest build ID, and reloads the job through `pipeline.Job`. It runs:

  ```go
  NewBuildLogCollector(
      db.NewPipelineFactory(dbConn, lockFactory),
      db.NewPipelineLifecycle(dbConn, lockFactory),
      5, scenario.calculator, scenario.drainerConfigured,
  ).Run(ctx)
  ```

  Query `pipeline_build_events_<pipeline.ID()>` with a parameterized `build_id`, and compare the set of build names whose event count became zero to `expectedDeleted`. Reload the job after the collector and compare `FirstLoggedBuildID()` to the ID mapped from `expectedFirstLogged`. For `expectedFirstLogged==""`, require zero. An already-reaped row must retain its seeded event while an eligible non-reaped row is deleted; that distinguishes filtering from deletion selection.

- [ ] **Step 2: Implement this complete scenario table.**

  `end` and `reap` are ages relative to fixture time; `-` means the column remains zero. Status values use the matching `db.BuildStatus`. The order column is oldest to newest.

  | Scenario | Order | Status | Completed | Drained | End | Reap | Retention / calculator | Drainer | Paused | Deleted | First logged |
  |---|---|---|---|---|---|---|---|---:|---|---|---|
  | drain filters | b1,b2,b3,b4,b5,b6,b7 | failed×7 | true×7 | T,F,T,F,F,T,T | 2h×7 | -×7 | builds=2 | yes | no | b1,b3 | b2 |
  | drain disabled | b1,b2,b3,b4,b5,b6 | failed×6 | true×6 | F,T,F,T,F,T | 2h×6 | -×6 | builds=2 | no | no | b1,b2,b3,b4 | b5 |
  | running rows survive | b1,b2,b3,b4,b5,b6 | failed,failed,failed,started,started,succeeded | T,T,T,F,F,T | T×6 | 2h,2h,2h,-,-,2h | -×6 | builds=3 | no | no | b1 | b2 |
  | no eligible reap | b1 | started | false | true | - | - | builds=2 | no | no | - | b1 |
  | no builds | - | - | - | - | - | - | builds=2 | no | no | - | - |
  | count only | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | builds=1 | no | no | b1 | b2 |
  | days satisfied | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | days=1 | no | no | b1 | b2 |
  | days protect both | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | days=3 | no | no | - | b1 |
  | combined count protects | b1 | failed | true | true | 49h | - | builds=1,days=2 | no | no | - | b1 |
  | combined days protect | b1,b2 | failed,failed | T,T | T,T | 24h,23h | -,- | builds=1,days=2 | no | no | - | b1 |
  | combined deletes | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | builds=1,days=2 | no | no | b1 | b2 |
  | minimum success | b1,b2,b3,b4,b5 | failed,succeeded,failed,succeeded,failed | T×5 | T×5 | 2h×5 | -×5 | builds=3,min-success=2 | no | no | b1,b3 | b2 |
  | already reaped excluded | b1,b2,b3 | failed,failed,failed | T,T,T | T,T,T | 49h,49h,23h | 1h,-,- | builds=1 | no | no | b2 | b3 |
  | all eligible | b1,b2 | failed,failed | T,T | T,T | 30h,30h | -,- | days=1 | no | no | b1,b2 | b1 |
  | retain zero skips | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | builds=0,days=0 | no | no | - | b1 |
  | calculator cap | b1,b2,b3,b4 | failed×4 | true×4 | true×4 | 2h×4 | -×4 | legacy builds=10; `NewBuildLogRetentionCalculator(3,3,0,0)` | no | no | b1 | b2 |
  | paused pipeline | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | builds=1 | no | pipeline | - | b1 |
  | paused job | b1,b2 | failed,failed | T,T | T,T | 49h,23h | -,- | builds=1 | no | job | - | b1 |

  Use `pipeline.Pause("collector-test")` and `job.Pause("collector-test")`, then reload the affected value before running the collector. The `all eligible` expected cursor is deliberately `b1`: when every event is deleted the collector does not reset the persisted cursor to zero.

- [ ] **Step 3: Add the first real assertion against the old collector and observe RED.**

  Instantiate the `count only` real scenario but initially run the current collector built with fake pipeline factory/lifecycle. Its b1 event count remains nonzero, so compare deleted names to `{b1}`.

  Run: `ginkgo --focus='BuildLogCollector persisted PostgreSQL retention count only' ./atc/gc`

  Expected: FAIL because the old fake collaborators never enumerate or mutate the real pipeline.

- [ ] **Step 4: Rewire the table and isolate four exact method-fault constructors.**

  Run all table entries through real `PipelineFactory`/`PipelineLifecycle`. Consolidate error-only contexts under `Describe("retained selective database method faults")` with exactly one `FakePipelineFactory`, one `FakePipelineLifecycle`, one `FakePipeline`, and one `FakeJob` lexical constructor. Reuse them to cover `RemoveBuildEventsForDeletedPipelines`, `AllPipelines`, `Pipeline.Jobs`, `DeleteBuildEventsByBuildIDs`, `Job.ChronoBuilds`, and `UpdateFirstLoggedBuildID` errors. Comment the pipeline/job fakes: a healthy persisted row cannot fail one selected interface method while preserving the surrounding row graph. Delete `sb`, `sbTime`, `sbDrained`, `runningBuild`, `reapedBuild`, and `successBuild`.

  Run: `ginkgo --focus='BuildLogCollector' ./atc/gc`

  Expected: PASS.

- [ ] **Step 5: Prove event deletion sensitivity and commit.**

  Temporarily skip `pipeline.DeleteBuildEventsByBuildIDs(buildIDsToDelete)`, run the focused `count only` persisted spec, and confirm the expected-deleted set is empty rather than `{b1}`. Restore `build_log_collector.go`, rerun to PASS, then:

  ```bash
  git add atc/gc/build_log_collector_test.go
  git commit -m "test: persist build log retention outcomes"
  ```

### Task 6: Verify exact payoff, concurrent clone safety, and bookkeeping

**Files:**
- Test: all files named above; no additional production or documentation file.

**Interfaces:**
- Consumes: the five converted suites and the audit's explicit-constructor counting convention.
- Produces: an exact 65-to-21 constructor reconciliation and clean committed diff, without a push.

- [ ] **Step 1: Record baseline and after counts including new fixture files.**

  Baseline is exactly `13/12/13/11/16 = 65` for tracker/lifecycler/canceler/template-saver/build-log-collector. After implementation run:

  ```bash
  for file in \
    atc/builds/tracker_test.go \
    atc/runlifecycle/runlifecycle_suite_test.go atc/runlifecycle/lifecycler_test.go \
    agent/workflowrun/real_db_test.go agent/workflowrun/canceler_test.go \
    agent/workflowrun/template_saver_test.go \
    atc/gc/build_log_collector_test.go
  do
    count=$(rg -o 'new\(dbfakes\.Fake[^)]*\)' "$file" | wc -l | tr -d ' ')
    printf '%s %s\n' "$file" "$count"
  done
  ```

  Expected survivors are tracker `4`, runlifecycle suite plus test `0+5`, workflowrun fixture plus canceler `0+0`, template saver `8`, and build-log collector `4`: exactly `21`. The wrong-identity case must embed a healthy real build in `wrongIdentityBuild` and prove the real selected row remains `aborted=false`; no generated `FakeBuildForAPI` fallback is permitted. No unclassified survivor is accepted.

- [ ] **Step 2: Run focused and full package verification.**

  ```bash
  pg_isready -h 127.0.0.1 -p 15432 -U postgres
  go test ./atc/builds -count=1
  ginkgo ./atc/runlifecycle
  go test ./agent/workflowrun -count=1
  ginkgo --focus='BuildLogCollector' ./atc/gc
  ```

  Expected: PostgreSQL reports `accepting connections`; all four test commands PASS.

- [ ] **Step 3: Run direct concurrent lifecycle commands.**

  Do not use the obsolete Docker/Colima-backed concurrency target. Run the real packages against the named shared service concurrently:

  ```bash
  pg_isready -h 127.0.0.1 -p 15432 -U postgres
  pids=()
  go test ./atc/builds -count=1 & pids+=("$!")
  ginkgo ./atc/runlifecycle & pids+=("$!")
  go test ./agent/workflowrun -count=1 & pids+=("$!")
  ginkgo --focus='BuildLogCollector' ./atc/gc & pids+=("$!")
  result=0
  for pid in "${pids[@]}"; do
    wait "$pid" || result=1
  done
  test "$result" -eq 0
  ```

  Expected: all four commands overlap and PASS; each runner uses independent clone names and none starts or stops the service.

- [ ] **Step 4: Review coverage, types, lifecycle, and scope.**

  Run `gofmt` on changed Go test files, `git diff --check`, and `git diff HEAD -- atc/builds atc/runlifecycle agent/workflowrun atc/gc`. Confirm:

  - both StandardTestRunner packages have exactly one `TestMain` and reuse one connection per `testing.T`;
  - Ginkgo cleanup closes ordinary/singleton connections before dropping each clone;
  - check factories receive all six exact arguments;
  - tracker check builds come from `Resource.CreateBuild`, and asynchronous row changes use reload-based `Eventually`;
  - completed pipeline-run helpers finish all pending entry builds and mark jobs scheduled before `Finish`;
  - linked cancellation asserts every durable execution pointer before transitioning to running;
  - unlinked cancellation uses a started build, proves `ValidateCancellationTarget==false`, and remains unaborted;
  - missing-build lookup uses ordinary persisted absence, while only lookup-error/wrong-identity paths use local adapters;
  - template refs are compared in full and every surviving fake team/pipeline is commented;
  - the GC table distinguishes already-reaped rows from rows selected for deletion and asserts paused row state;
  - no product mutation from sensitivity checks, benchmark/corpus file, Docker/service lifecycle file, Pipeline API work, engine plan, phase ledger, or generated fake changed.

  Do not push.
