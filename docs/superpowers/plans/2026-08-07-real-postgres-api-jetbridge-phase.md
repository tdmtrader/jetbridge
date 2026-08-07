# Real PostgreSQL API and Jetbridge Phase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace 35 success-state `dbfakes` constructors in three API files and eight jetbridge files with isolated real-PostgreSQL fixtures while retaining five narrowly documented post-lookup fault constructors.

**Architecture:** API success contexts opt into `useRealDB()`, serve the production router over clone-local factories, and persist the teams, workers, pipelines, jobs, and builds they assert. Jetbridge uses `postgresrunner.GinkgoRunner` for Ginkgo specs and a live-tagged `postgresrunner.StandardTestRunner` for ordinary `testing.T` tests; both create template clones on the shared PostgreSQL service, while Kubernetes clients, executors, streams, access fakes, and policy fakes remain seams.

**Tech Stack:** Go, Ginkgo v2/Gomega, `testing`, PostgreSQL, `atc/postgresrunner`, Concourse `atc/db` factories, Kubernetes client-go fake and live clients.

## Global Constraints

- Use the already-running PostgreSQL service at `127.0.0.1:15432`; give each converted Ginkgo spec and each converted `testing.T` test a unique template-cloned database.
- Support parallel package/spec execution. Never retain a connection, factory, or `db.WorkerCache` across clones.
- Never start or stop Docker, Colima, PostgreSQL, or Kubernetes. Keep the shared PostgreSQL service running after verification.
- Write and run a consumer-visible failing assertion before adding the fixture, server wiring, or helper that makes it pass. Make no production-code change without a real failing test; this phase expects test-only changes.
- A second connection opened from the current clone and then closed may represent only ordinary connection failures. Retain a fake for a post-lookup method failure that a closed `TeamFactory` or `WorkerFactory` cannot selectively reach, and comment the exact method boundary at the retained context.
- Preserve Kubernetes clientsets, SPDY/exec, streams, clocks, access/policy fakes, and all non-database seams. Exclude benchmarks and corpus code.
- Do not push. Do not stage or commit unrelated worktree changes.

---

## File map and fixed constructor budget

- Modify `atc/api/teams_test.go`: remove six of seven constructors; retain one `*dbfakes.FakeTeam` site for post-`FindTeam` `UpdateProviderAuth`, `Delete`, `Rename`, and `Builds` failures.
- Modify `atc/api/workers_test.go`: remove four of six constructors; retain one `*dbfakes.FakeTeam` for post-`FindTeam` `Team.SaveWorker` failure and one `*dbfakes.FakeWorker` for post-`GetWorker` `Worker.Delete` failure.
- Modify `atc/api/pipelines_test.go`: remove 17 of 19 constructors; retain one `*dbfakes.FakeTeam` for nested team-method failures and one `*dbfakes.FakePipeline` for pipeline-method failures and semantic conflict errors.
- Modify `atc/worker/jetbridge/jetbridge_suite_test.go` and `live_test.go`: add clone-local worker factories and named-worker helpers; add no lock factory and do not call `db.CleanupBaseResourceTypesCache`.
- Modify `atc/worker/jetbridge/live_resilience_test.go`, `live_secret_env_test.go`, `live_security_test.go`, `live_sidecar_test.go`, `live_sidecar_logstream_test.go`, `live_worker_test.go`, `podname_integration_test.go`, and `secret_env_test.go`: remove all eight `FakeWorker` constructors and all eight direct `dbfakes` imports.

The fixed baseline is exactly 40 explicit constructors: Teams 7, Workers 6, Pipelines 19, jetbridge 8. The target is exactly 35 removals and 5 retained constructors, leaving 8 whole files without `dbfakes`.

### Task 1: Convert Teams and Workers API Success State

**Files:**

- Modify: `atc/api/teams_test.go`
- Modify: `atc/api/workers_test.go`
- Reference: `atc/api/real_db_test.go` (`useRealDB() *realDB`, `(*realDB).Serve() *httptest.Server`, `realDB.Deps`, `realDB.LockFactory`)
- Reference: `atc/db/team_factory.go` (`db.NewTeamFactory(db.DbConn, lock.LockFactory) db.TeamFactory`)
- Reference: `atc/db/worker_factory.go` (`db.NewWorkerFactory(db.DbConn, *db.WorkerCache) db.WorkerFactory`)

**Interfaces:**

- Consumes: `db.TeamFactory.CreateTeam(atc.Team) (db.Team, error)`, `db.Team.SavePipeline(atc.PipelineRef, atc.Config, db.ConfigVersion, bool) (db.Pipeline, bool, error)`, `db.Team.SaveWorker(atc.Worker, time.Duration) (db.Worker, error)`, `db.WorkerFactory.SaveWorker(atc.Worker, time.Duration) (db.Worker, error)`, `db.Job.CreateBuild(string) (db.Build, error)`, `db.Build.Start(atc.Plan) (bool, error)`, and `db.Build.Finish(db.BuildStatus) error`.
- Produces: real success fixtures for Teams and Workers API routes, exact dynamic-object assertions, and only three retained constructors across the two files.

- [x] **Step 1: Add a lifecycle-correct Teams consumer assertion that is red against the fake server.**

  In the `PUT /api/v1/teams/:team_name` Describe, declare `realdb *realDB` in the Describe var block. In the authorized-update Context add a `BeforeEach` that runs before its `JustBeforeEach` request:

  ```go
  BeforeEach(func() {
      realdb = useRealDB()
      // RED: deliberately leave server pointing at the suite fake.
  })
  ```

  Add `It("persists team mutations through PostgreSQL")` after that request. Query `realdb.Deps.teamFactory.FindTeam("some-team")` and expect `found` true and `team.Auth()` equal this literal request value:

  ```go
  atc.TeamAuth{
      "owner": map[string][]string{
          "groups": {},
          "users":  {"local:username"},
      },
  }
  ```

  Because the request still reaches `dbTeamFactory`, the clone has no `some-team` row and the assertion is red.

- [x] **Step 2: Run the Teams red node before real server wiring.**

  Run: `ginkgo --focus='persists team mutations through PostgreSQL' ./atc/api`

  Expected: FAIL with `found` false after the fake handler reports success.

- [x] **Step 3: Convert Teams list, detail, and builds reads.**

  In each converted Describe, declare `realdb *realDB` in its var block, assign `realdb = useRealDB()` in `BeforeEach`, and then set `server = realdb.Serve()` in that same `BeforeEach`, before `JustBeforeEach` builds the request. Reuse `realdb.Main`; do not create a second `main` team.

  For `GET /api/v1/teams`, define this literal locally so it does not refer to the PUT Describe's `teamAuth` variable:

  ```go
  listTeamAuth := atc.TeamAuth{
      "owner": map[string][]string{
          "groups": {},
          "users":  {"local:username"},
      },
  }
  ```

  Create these three rows with `listTeamAuth`:

  ```go
  avengers, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "avengers", Auth: listTeamAuth})
  Expect(err).NotTo(HaveOccurred())
  aliens, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "aliens", Auth: listTeamAuth})
  Expect(err).NotTo(HaveOccurred())
  predators, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "predators", Auth: listTeamAuth})
  Expect(err).NotTo(HaveOccurred())
  fakeAccess.IsAuthorizedCalls(func(name string) bool {
      return name == avengers.Name() || name == predators.Name()
  })
  ```

  `GetTeams` returns `aliens`, `avengers`, `main`, `predators` alphabetically; the name-based callback therefore avoids call-order coupling and rejects both `aliens` and precreated `main`. Decode the response into `[]atc.Team` and compare it to `[]atc.Team{{ID: avengers.ID(), Name: avengers.Name(), Auth: listTeamAuth}, {ID: predators.ID(), Name: predators.Name(), Auth: listTeamAuth}}`; do not assert IDs 5/22.

  For `GET /api/v1/teams/a-team`, persist `a-team` with a locally declared copy of `listTeamAuth`, decode `atc.Team`, and compare the decoded ID/name/auth to the returned `db.Team`.

  For `GET /api/v1/teams/some-team/builds`, create `some-team`, save this literal config, and resolve its job:

  ```go
  pipeline := realdb.SavePipeline(team, "some-pipeline", atc.Config{
      Jobs: atc.JobConfigs{{Name: "some-job"}},
  })
  job, found, err := pipeline.Job("some-job")
  Expect(err).NotTo(HaveOccurred())
  Expect(found).To(BeTrue())
  ```

  Create the started build with `job.CreateBuild("api-test")`, then call `started, err := build.Start(atc.Plan{})` and assert `err == nil` and `started == true`. Create the succeeded build the same way, assert `Start(atc.Plan{})` returns `(true, nil)`, then assert `build.Finish(db.BuildStatusSucceeded)` succeeds. Reload both builds, call the endpoint, decode `[]atc.Build`, and compare each decoded ID, name, job/pipeline/team name, status, `StartTime().Unix()`, and `EndTime().Unix()` to the real build; the started build has zero end time and the succeeded build has a nonzero end time. Keep page-argument observation on the one retained fake team because it tests request translation, not persistence.

  Keep exactly one constructor site, the file-level `fakeTeam = new(dbfakes.FakeTeam)`, for these selective contexts and place the stated comment at each use:

  - `Team.UpdateProviderAuth` fails after `FindTeam` succeeds.
  - `Team.Delete` fails after `FindTeam` succeeds.
  - `Team.Rename` fails after `FindTeam` succeeds.
  - `Team.Builds` fails after `FindTeam` succeeds.
  - `TeamFactory.CreateTeam` fails after `FindTeam` selectively returns not found; this uses the suite `dbTeamFactory` and adds no constructor.

  Comment form, with the method name changed per context:

  ```go
  // Retained fault seam: Team.UpdateProviderAuth must fail after FindTeam
  // succeeds; a closed TeamFactory fails the lookup before this method.
  ```

  Use a closed production factory only for `GetTeams` or `FindTeam` failure:

  ```go
  doomed := postgresRunner.OpenConn()
  Expect(doomed.Close()).To(Succeed())
  deps := realdb.Deps
  deps.teamFactory = db.NewTeamFactory(doomed, realdb.LockFactory)
  server = newAPIServer(deps)
  DeferCleanup(server.Close)
  ```

- [x] **Step 4: Convert team creation with literal request and reread.**

  In the admin/not-found Context, create the request body `atc.Team{Auth: teamAuth}` where `teamAuth` is the literal owner/users map from Step 1; send `PUT /api/v1/teams/some-team`; expect 201. Reread with `created, found, err := realdb.Deps.teamFactory.FindTeam("some-team")`; assert nil error, found true, `created.Name() == "some-team"`, and `created.Auth() == teamAuth`. The GREEN change to the RED setup is only `server = realdb.Serve()` in its `BeforeEach` before `JustBeforeEach`.

- [x] **Step 5: Convert team auth update with a preexisting row.**

  Persist `some-team` with `oldAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {"old-group"}, "users": {}}}`. Send `PUT /api/v1/teams/some-team` with `newAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}`; expect 200. Reread `some-team` and assert found, `Auth() == newAuth`, and `Auth() != oldAuth`.

- [x] **Step 6: Convert team deletion with a literal team name.**

  Persist `team` with `atc.Team{Name: "team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:admin"}}}}`; set authenticated/admin/authorized true; send `DELETE /api/v1/teams/team`; expect 204. Call `FindTeam("team")` and assert `(nil, false, nil)`. Keep the sole-admin refusal in its state-specific context and the post-lookup `Team.Delete` error on the one retained `FakeTeam`.

- [x] **Step 7: Convert team rename with literal body and both-name rereads.**

  Persist `a-team` with nonempty owner auth; send `PUT /api/v1/teams/a-team/rename` with body `{"name":"some-new-name"}`; expect 200. Assert `FindTeam("a-team")` returns not found and `FindTeam("some-new-name")` returns a row whose `Name()` is `some-new-name`. Keep invalid `_some-new-name` and empty-name validation requests unchanged.

- [x] **Step 8: Run Teams green verification.**

  Run: `ginkgo --focus='Teams API' ./atc/api`

  Expected: PASS; `teams_test.go` contains exactly one explicit `dbfakes` constructor.

- [x] **Step 9: Add a lifecycle-correct Workers consumer assertion that is red against the fake server.**

  Declare `realdb *realDB` in the POST Describe var block. In authenticated global POST, assign `realdb = useRealDB()` and `requestedAt = time.Now()` in `BeforeEach`, before `JustBeforeEach`, but leave the suite server unchanged. Add `It("persists worker registration through PostgreSQL")`; after the request, call `realdb.Deps.workerFactory.GetWorker("worker-name")`, expect found, and require `ExpiresAt()` between `requestedAt.Add(30*time.Second)` and `time.Now().Add(30*time.Second)`. The fake server returns 200 but writes no row, so this is red.

- [x] **Step 10: Run the Workers red node before real server wiring.**

  Run: `ginkgo --focus='persists worker registration through PostgreSQL' ./atc/api`

  Expected: FAIL with `found` false.

- [x] **Step 11: Convert worker list visibility with exactly three rows.**

  Declare `realdb *realDB` at Describe scope, assign it in `BeforeEach`, and set `server = realdb.Serve()` before `JustBeforeEach`. Create `some-team` and `other-team`, then persist exactly these three workers:

  ```go
  global, err := realdb.Deps.workerFactory.SaveWorker(atc.Worker{
      Name: "global-worker", Platform: "linux", Version: "1.2.3",
      State: string(db.WorkerStateRunning), Tags: []string{"global"},
  }, 0)
  Expect(err).NotTo(HaveOccurred())
  own, err := someTeam.SaveWorker(atc.Worker{
      Name: "some-team-worker", Platform: "linux", Version: "1.2.3",
      State: string(db.WorkerStateRunning), Tags: []string{"own"},
  }, 0)
  Expect(err).NotTo(HaveOccurred())
  other, err := otherTeam.SaveWorker(atc.Worker{
      Name: "other-team-worker", Platform: "linux", Version: "1.2.3",
      State: string(db.WorkerStateRunning), Tags: []string{"other"},
  }, 0)
  Expect(err).NotTo(HaveOccurred())
  ```

  Set `fakeAccess.TeamNamesReturns([]string{"some-team"})`. Decode the authorized response into `[]atc.Worker` and assert names `global.Name()` and `own.Name()` only. Set admin true and assert names `global.Name()`, `own.Name()`, and `other.Name()`.

- [x] **Step 12: Convert global and team worker POST separately.**

  For global POST, send the literal `atc.Worker{Name: "worker-name", ActiveContainers: 2, ActiveVolumes: 10, ActiveTasks: 42, ResourceTypes: []atc.WorkerResourceType{{Type: "some-resource", Image: "some-resource-image"}}, Platform: "haiku", Tags: []string{"not", "a", "limerick"}, Version: "1.2.3"}` with `ttl=30s`; expect 200. Reread `worker-name`, compare every listed field, assert `TeamName() == ""`, and bound expiry as in Step 9.

  For team POST, create `some-team`, set the same request's `Team` field to `some-team`, send POST with `ttl=30s`, expect 200, reread `worker-name`, and assert `TeamName() == "some-team"` and `TeamID() == someTeam.ID()` plus the same field/expiry checks. Keep `Team.SaveWorker` failure on the retained `FakeTeam` only.

- [x] **Step 13: Convert each worker DELETE authorization success separately.**

  For system DELETE, persist global `some-worker`, set authenticated/system true, send `DELETE /api/v1/workers/some-worker`, expect 200, and assert `GetWorker("some-worker")` returns `(nil, false, nil)`. Repeat from a fresh clone for admin with authenticated/admin true. For team authorization, create `some-team`, save `some-worker` through `someTeam.SaveWorker`, set authenticated/authorized true, send the same DELETE, expect 200, and assert absent through the global worker factory. Keep the post-lookup `Worker.Delete` failure on the retained `FakeWorker` only; use a missing real row for already-deleted behavior.

- [x] **Step 14: Narrow worker faults to two retained constructors and closed direct factories.**

  Keep exactly two constructor sites:

  - `foundTeam = new(dbfakes.FakeTeam)` only for `Team.SaveWorker` failing after `workerTeamFactory.FindTeam` succeeds.
  - `fakeWorker = new(dbfakes.FakeWorker)` only for `Worker.Delete` failing after `workerFactory.GetWorker` succeeds.

  Add a precise comment above each selective context. Use real absent rows for team-not-found and worker-not-found outcomes. Use these closed factories only for lookup/list/direct-save failures:

  ```go
  doomed := postgresRunner.OpenConn()
  Expect(doomed.Close()).To(Succeed())
  deps := realdb.Deps
  deps.workerFactory = db.NewWorkerFactory(
      doomed,
      db.NewStaticWorkerCache(logger, doomed, 0),
  ) // Workers, VisibleWorkers, GetWorker, or direct SaveWorker failure
  deps.workerTeamFactory = db.NewTeamFactory(
      doomed,
      realdb.LockFactory,
  ) // FindTeam failure for a team-scoped registration
  server = newAPIServer(deps)
  DeferCleanup(server.Close)
  ```

- [x] **Step 15: Verify and commit Task 1.**

  Run: `ginkgo --focus='(Teams API|Workers API)' ./atc/api`

  Expected: PASS; constructor recount is Teams 1 and Workers 2.

  ```bash
  git add atc/api/teams_test.go atc/api/workers_test.go
  git commit -m "test: persist teams and workers API fixtures"
  ```

### Task 2: Convert Pipeline Listings, Badges, Builds, and Mutations

**Files:**

- Modify: `atc/api/pipelines_test.go`
- Reference: `atc/api/real_db_test.go` (`realdb.Main`, `(*realDB).SavePipeline`, `realDB.LockFactory`)
- Reference: `atc/db/pipeline.go` (`Pipeline.CreateStartedBuild`, `Pause`, `Unpause`, `Archive`, `Expose`, `Hide`, `Destroy`)
- Reference: `atc/db/job.go` (`Job.CreateBuild`)
- Reference: `atc/db/build.go` (`Build.Start`, `Build.Finish`)

**Interfaces:**

- Consumes: `db.Pipeline.CreateStartedBuild(atc.Plan) (db.Build, error)` for POST pipeline builds; `db.Job.CreateBuild(string) (db.Build, error)` for job badge and build-list fixtures; `db.Build.Start(atc.Plan) (bool, error)`; `db.Build.Finish(db.BuildStatus) error`; and pipeline visibility/mutation methods returning `error`.
- Produces: four persisted pipeline rows per visibility spec, a five-state persisted badge matrix, real list/POST builds, real mutation assertions, and exactly two retained constructors.

- [x] **Step 1: Add a lifecycle-correct pipeline mutation assertion that is red against the fake server.**

  In the pause Describe, declare `realdb *realDB` and `persistedPipeline db.Pipeline` in its var block. In the authorized-success Context, add a `BeforeEach` that runs before `JustBeforeEach`:

  ```go
  BeforeEach(func() {
      realdb = useRealDB()
      persistedPipeline = realdb.SavePipeline(realdb.Main, "a-pipeline", atc.Config{
          Jobs: atc.JobConfigs{{Name: "job"}},
      })
      // RED: leave server on the suite fake for this first run.
  })
  ```

  Configure the suite fake lookup/pipeline to return 200, send the request from `JustBeforeEach`, and add `It("persists pipeline pause through PostgreSQL")` that reloads `persistedPipeline` and expects `Paused()` true. The fixture exists before the request, but the fake pipeline receives Pause, so the real row remains unpaused.

- [x] **Step 2: Run the Pipeline red node before replacing its fixtures.**

  Run: `ginkgo --focus='persists pipeline pause through PostgreSQL' ./atc/api`

  Expected: FAIL because the real pipeline remains unpaused.

- [x] **Step 3: Persist the exact four-row visibility graph and dynamic response values.**

  Set `server = realdb.Serve()`, reuse `mainTeam := realdb.Main`, and create only `anotherTeam`:

  ```go
  anotherTeam, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "another"})
  Expect(err).NotTo(HaveOccurred())

  mainPublicConfig := atc.Config{
      Groups: atc.GroupConfigs{{
          Name: "group2", Jobs: []string{"job3", "job4"},
          Resources: []string{"resource3", "resource4"},
      }},
      Jobs: atc.JobConfigs{{Name: "job3"}, {Name: "job4"}},
      Resources: atc.ResourceConfigs{
          {Name: "resource3", Type: "mock", Source: atc.Source{"key": "three"}},
          {Name: "resource4", Type: "mock", Source: atc.Source{"key": "four"}},
      },
      Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
  }
  mainPrivateConfig := atc.Config{
      Groups: atc.GroupConfigs{{
          Name: "group1", Jobs: []string{"job1", "job2"},
          Resources: []string{"resource1", "resource2"},
      }},
      Jobs: atc.JobConfigs{{Name: "job1"}, {Name: "job2"}},
      Resources: atc.ResourceConfigs{
          {Name: "resource1", Type: "mock", Source: atc.Source{"key": "one"}},
          {Name: "resource2", Type: "mock", Source: atc.Source{"key": "two"}},
      },
  }
  anotherPublicConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}
  anotherPrivateConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}
  ```

  Persist exactly four rows: `public-pipeline` and `private-pipeline` under `mainTeam`; `another-pipeline` and `another-private-pipeline` under `anotherTeam`. Call `Expose()` on both public rows and leave both private rows hidden. Call `Pause("api-test")` on the two rows whose response should be paused.

  Assert unauthenticated list IDs are exactly the two exposed row IDs; authorized `main` list IDs are main public, main private, and another public; admin list IDs are all four. Decode into `[]atc.Pipeline`, compare IDs/names/team/paused/public/archived/groups/display to the database objects/configs, and compare `LastUpdated` to `pipeline.LastUpdated().Unix()` after `Reload()`. Do not assert Unix 1.

  In the archived-list context, record `archiveRequestedAt := time.Now()`, call `mainPrivate.Archive()`, and reload it. Expect `paused: true`, `paused_by: "automatic-pipeline-archiver"`, dynamic `paused_at` equal to reloaded `PausedAt().Unix()` and not before `archiveRequestedAt`, and `archived: true`. Preserve `Groups == mainPrivateConfig.Groups` and `Display == mainPrivateConfig.Display`; Archive retains those pipeline-row presentation fields. Do not retain the impossible fake state `archived: true, paused: false`.

- [x] **Step 4: Replace all nine badge constructors with progressive persisted status sets.**

  Give every badge Context a fresh clone/pipeline rather than inserting every status in a common `BeforeEach`. Use these literal job sets:

  - Unknown: `atc.Config{Jobs: atc.JobConfigs{{Name: "no-build"}}}` and create no build; expect the exact `unknown` SVG.
  - Passing: jobs `succeeded`; create only a succeeded build; expect the exact `passing` SVG.
  - Aborted: jobs `succeeded`, `aborted`; create both builds; expect the exact `aborted` SVG.
  - Errored: jobs `succeeded`, `aborted`, `errored`; create all three; expect the exact `errored` SVG.
  - Failing: jobs `succeeded`, `aborted`, `errored`, `failed`; create all four; expect the exact `failing` SVG.

  Resolve each named job with `pipeline.Job(name)` and assert found. For each status-bearing job, run this exact lifecycle:

  ```go
  build, err := job.CreateBuild("api-badge-test")
  Expect(err).NotTo(HaveOccurred())
  started, err := build.Start(atc.Plan{})
  Expect(err).NotTo(HaveOccurred())
  Expect(started).To(BeTrue())
  Expect(build.Finish(status)).To(Succeed())
  ```

  Use `db.BuildStatusSucceeded`, `db.BuildStatusAborted`, `db.BuildStatusErrored`, and `db.BuildStatusFailed`. The progressive contexts prove the production precedence `failed > errored > aborted > succeeded`; each expected badge names the highest-precedence status present. This removes five `FakeJob` and four `FakeBuild` constructor sites.

- [x] **Step 5: Replace build-list and POST success constructors with real builds.**

  For GET pipeline builds, create two builds from a persisted job with `Job.CreateBuild("api-test")`. Start both with `Start(atc.Plan{})`; finish one with `Finish(db.BuildStatusSucceeded)` and leave one started. Decode `[]atc.Build` and compare IDs, names, status, team/pipeline/job name, start time, and end time to the reloaded rows. Build pagination links from the returned real IDs instead of 4/2.

  For POST pipeline builds, keep this exact request plan:

  ```go
  plan := atc.Plan{
      ID: atc.PlanID("api-manual"),
      Task: &atc.TaskPlan{Config: &atc.TaskConfig{
          Run: atc.TaskRunConfig{Path: "ls"},
      }},
  }
  ```

  Serve over a persisted `a-pipeline`. The handler calls `Pipeline.CreateStartedBuild(plan) (db.Build, error)` directly; `Job.CreateBuild` is not used for this route. After POST, call `pipeline.Builds(db.Page{Limit: 1})`, assert one build, decode the response into `atc.Build`, and compare its ID, name, team, pipeline, started status, API URL, and start time to that database build. Assert its public plan equals `plan.Public()` after reload. Do not assert fake ID 42 or synthetic end/reap times.

- [x] **Step 6: Convert Pipeline.Destroy success.**

  Save `a-pipeline-name` under `realdb.Main` with `atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}`; set authenticated/authorized true; send `DELETE /api/v1/teams/main/pipelines/a-pipeline-name`; expect 204. Call `realdb.Main.Pipeline(atc.PipelineRef{Name: "a-pipeline-name"})` and assert `(nil, false, nil)`.

- [x] **Step 7: Convert Pipeline.Pause success and make the RED node green.**

  In Step 1's `BeforeEach`, make the sole GREEN change `server = realdb.Serve()`. Set `fakeAccess.UserNameReturns("api-user")`; send `PUT /api/v1/teams/main/pipelines/a-pipeline/pause`; expect 200. Reload and assert `Paused() == true`, `PausedBy() == "api-user"`, and `PausedAt()` is nonzero.

- [x] **Step 8: Convert Pipeline.Archive success with presentation fields preserved.**

  Save `a-pipeline` with `archiveConfig := atc.Config{Groups: atc.GroupConfigs{{Name: "release", Jobs: []string{"ship"}}}, Jobs: atc.JobConfigs{{Name: "ship"}}, Display: &atc.DisplayConfig{BackgroundImage: "archive.jpg"}}`. Record `requestedAt := time.Now()`, send `PUT /api/v1/teams/main/pipelines/a-pipeline/archive`, expect 200, reload, and assert `Archived()`, `Paused()`, `PausedBy() == "automatic-pipeline-archiver"`, `PausedAt()` is not before `requestedAt`, `Groups() == archiveConfig.Groups`, and `Display() == archiveConfig.Display`.

- [x] **Step 9: Convert Pipeline.Unpause success.**

  Save `a-pipeline`, call `pipeline.Pause("setup-user")`, then serve and send `PUT /api/v1/teams/main/pipelines/a-pipeline/unpause`; expect 200. Reload and assert `Paused() == false`, `PausedBy() == ""`, and `PausedAt().IsZero()`.

- [x] **Step 10: Convert Pipeline.Expose success.**

  Save hidden `a-pipeline`; send `PUT /api/v1/teams/main/pipelines/a-pipeline/expose`; expect 200. Reread through `realdb.Main.Pipeline`, assert found and `Public() == true`.

- [x] **Step 11: Convert Pipeline.Hide success.**

  Save `a-pipeline`, call `Expose()` during setup, assert it succeeds, then send `PUT /api/v1/teams/main/pipelines/a-pipeline/hide`; expect 200. Reread and assert `Public() == false`.

- [x] **Step 12: Convert team-wide pipeline ordering.**

  Create `a-team`, then save the five pipelines in this deliberately different insertion order: `just-kidding`, `a-pipeline`, `one-final-pipeline`, `yet-another-pipeline`, `another-pipeline`. Before the request, call `initialPipelines, err := team.Pipelines()`, assert no error, and assert their names equal `[]string{"just-kidding", "a-pipeline", "one-final-pipeline", "yet-another-pipeline", "another-pipeline"}`.

  Send `PUT /api/v1/teams/a-team/pipelines/ordering` with JSON `["a-pipeline","another-pipeline","yet-another-pipeline","one-final-pipeline","just-kidding"]` encoded from `requestedOrder := []string{"a-pipeline", "another-pipeline", "yet-another-pipeline", "one-final-pipeline", "just-kidding"}`. Expect 200. Reread with `orderedPipelines, err := team.Pipelines()`, assert no error, map the rows to names, and assert the result equals `requestedOrder` and does not equal the initial insertion order.

- [x] **Step 13: Convert within-group instance ordering.**

  Create `a-team`; save three `a-pipeline` rows in this deliberately different insertion order: first `atc.PipelineRef{Name: "a-pipeline", InstanceVars: atc.InstanceVars{"branch": "test-2"}}`, then the uninstanced `atc.PipelineRef{Name: "a-pipeline"}` (nil `InstanceVars`, persisted as SQL `NULL`), then `atc.PipelineRef{Name: "a-pipeline", InstanceVars: atc.InstanceVars{"branch": "test"}}`. For each ref call `team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)` and assert success.

  Before the request, call `initialPipelines, err := team.Pipelines()`, filter rows whose `Name() == "a-pipeline"`, and normalize each nil `InstanceVars()` to `atc.InstanceVars{}` before comparing. Assert the normalized initial sequence equals `[]atc.InstanceVars{{"branch": "test-2"}, {}, {"branch": "test"}}`. Send `PUT /api/v1/teams/a-team/pipelines/a-pipeline/ordering` with `requestedOrder := []atc.InstanceVars{{"branch": "test"}, {}, {"branch": "test-2"}}`; keep the empty map in the request so the endpoint exercises its zero-length-map-to-SQL-`NULL` normalization, and expect 200. Reread with `orderedPipelines, err := team.Pipelines()`, filter the same group, apply the same nil-to-empty-map normalization, and assert the normalized sequence equals `requestedOrder` and differs from the normalized initial sequence.

- [x] **Step 14: Convert Pipeline rename success.**

  Create `a-team`, save `a-pipeline`, send `PUT /api/v1/teams/a-team/pipelines/a-pipeline/rename` with literal body `{"name":"some-new-name"}`, and expect 200. Assert `team.Pipeline(atc.PipelineRef{Name: "a-pipeline"})` is not found and `team.Pipeline(atc.PipelineRef{Name: "some-new-name"})` is found with `Name() == "some-new-name"`.

- [x] **Step 15: Enumerate every retained selective seam.**

  A closed `TeamFactory` represents only `FindTeam`/`GetTeams` failures:

  ```go
  doomed := postgresRunner.OpenConn()
  Expect(doomed.Close()).To(Succeed())
  deps := realdb.Deps
  deps.teamFactory = db.NewTeamFactory(doomed, realdb.LockFactory)
  deps.pipelineFactory = db.NewPipelineFactory(doomed, realdb.LockFactory)
  server = newAPIServer(deps)
  DeferCleanup(server.Close)
  ```

  Install only `deps.teamFactory` for lookup failures and only `deps.pipelineFactory` for `VisiblePipelines`/`AllPipelines` failures. Do not claim either closed factory can reach a nested method after a successful lookup.

  Retain exactly the current root `fakeTeam = new(dbfakes.FakeTeam)` and `dbPipeline = new(dbfakes.FakePipeline)` constructor sites. Add a method-specific comment at every retained context:

  - FakeTeam: `Team.Pipelines`, `Team.PublicPipelines`, `Team.Pipeline`, `Team.OrderPipelines`, `Team.OrderPipelinesWithinGroup`, and `Team.RenamePipeline` errors after `TeamFactory.FindTeam` succeeds.
  - FakePipeline: `Pipeline.Destroy`, `Archive`, `Pause`, `Unpause`, `Expose`, `Hide`, `LoadDebugVersionsDB`, `Builds`, and `CreateStartedBuild` errors after `Team.Pipeline` succeeds.
  - FakePipeline semantic returns: `db.ErrWorkflowRunTemplateImmutable` for mutation conflicts and wrapped `db.ErrWorkflowRunOwnedPipeline` for POST build conflict.

  Each comment states the specific method, for example:

  ```go
  // Retained fault seam: Pipeline.CreateStartedBuild must return the durable-run
  // conflict after Team.Pipeline succeeds; a closed TeamFactory fails earlier.
  ```

- [x] **Step 16: Run Pipeline green verification and commit.**

  Run: `ginkgo --focus='Pipelines API' ./atc/api`

  Expected: PASS; constructor recount is Pipelines 2, with all five badge jobs, four badge builds, two list builds, detail pipeline, four visibility pipelines, and POST build fakes removed.

  ```bash
  git add atc/api/pipelines_test.go
  git commit -m "test: persist pipeline API fixtures"
  ```

### Task 3: Add Jetbridge Clone Harnesses and Convert the First Live Batch

**Files:**

- Modify: `atc/worker/jetbridge/jetbridge_suite_test.go`
- Modify: `atc/worker/jetbridge/live_test.go`
- Modify: `atc/worker/jetbridge/live_resilience_test.go`
- Modify: `atc/worker/jetbridge/live_secret_env_test.go`
- Modify: `atc/worker/jetbridge/live_security_test.go`
- Modify: `atc/worker/jetbridge/live_sidecar_test.go`
- Reference: `atc/postgresrunner/ginkgo.go`, `atc/postgresrunner/standard.go`, `atc/db/worker_factory.go`, `atc/db/worker.go`, `atc/db/container_owner.go`

**Interfaces:**

- Consumes: `postgresrunner.GinkgoRunner(*postgresrunner.Runner)`, `postgresrunner.StandardTestRunner.Main(*testing.M) int`, `StandardTestRunner.OpenConn(*testing.T) db.DbConn`, and `jetbridge.NewWorker(db.Worker, kubernetes.Interface, jetbridge.Config) *jetbridge.Worker`.
- Produces: `useJetbridgeDB() jetbridgeDB`, `useLiveJetbridgeDB(*testing.T) jetbridgeDB`, and `persistNamedWorker(jetbridgeDB, string) (db.Worker, error)` with no lock factory or global cache cleanup.

- [x] **Step 1: Add a live consumer assertion before defining the helpers.**

  Extend `setupLiveResilienceWorker` to return a fifth result, `jetbridgeDB`, add calls to the not-yet-defined `useLiveJetbridgeDB(t)` and `persistNamedWorker`, and update both callers to receive `worker, delegate, clientset, cfg, database`. For RED, persist the real worker but deliberately keep `jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)`. Immediately after `FindOrCreateContainer` in `TestLiveInvalidImageFailsFast`, require `database.WorkerFactory.GetWorker("live-k8s-worker")` to find the real worker. Then call `persisted.FindContainer(db.NewFixedHandleContainerOwner(handle))` and require `creating == nil`, `created != nil`, and `created.Handle() == handle`. Do not add a helper-only persistence spec.

- [x] **Step 2: Run the first live consumer red command before helper implementation.**

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLiveInvalidImageFailsFast$'`

  Expected: FAIL to compile because the clone helpers do not exist. This command precedes all harness implementation.

- [x] **Step 3: Implement the two runner adapters and named-worker helper.**

  In `jetbridge_suite_test.go`, add:

  ```go
  var postgresRunner postgresrunner.Runner
  var _ = postgresrunner.GinkgoRunner(&postgresRunner)

  type jetbridgeDB struct {
      WorkerFactory db.WorkerFactory
  }

  func useJetbridgeDB() jetbridgeDB {
      GinkgoHelper()
      postgresRunner.CreateTestDBFromTemplate()
      DeferCleanup(postgresRunner.DropTestDB)
      conn := postgresRunner.OpenConn()
      DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
      return jetbridgeDB{WorkerFactory: db.NewWorkerFactory(
          conn,
          db.NewStaticWorkerCache(lager.NewLogger("jetbridge-test"), conn, 0),
      )}
  }
  ```

  Do not open lock singletons, construct `lock.LockFactory`, or call `db.CleanupBaseResourceTypesCache`; `WorkerFactory` needs only the clone connection and its clone-local `WorkerCache`.

  In live-tagged `live_test.go`, add `var livePostgresRunner postgresrunner.StandardTestRunner`, then wrap all live tests:

  ```go
  func TestMain(m *testing.M) {
      os.Exit(livePostgresRunner.Main(m))
  }

  func useLiveJetbridgeDB(t *testing.T) jetbridgeDB {
      t.Helper()
      conn := livePostgresRunner.OpenConn(t)
      return jetbridgeDB{WorkerFactory: db.NewWorkerFactory(
          conn,
          db.NewStaticWorkerCache(lager.NewLogger("live-jetbridge-test"), conn, 0),
      )}
  }
  ```

  `StandardTestRunner.OpenConn` reuses one clone/connection for repeated calls with the same `*testing.T`; it creates a distinct clone for each distinct test or `t.Run` child. Under `-tags live`, `StandardTestRunner.Main` owns the ordinary-test template while `TestJetbridge` initializes the separate Ginkgo runner/template; both coexist on `127.0.0.1:15432` with different run IDs.

  Define this shared helper without a testing framework dependency:

  ```go
  func persistNamedWorker(database jetbridgeDB, name string) (db.Worker, error) {
      worker, err := database.WorkerFactory.SaveWorker(atc.Worker{
          Name: name, Platform: "linux", Version: "1.2.3",
          State: string(db.WorkerStateRunning),
      }, 0)
      if err != nil {
          return nil, fmt.Errorf("save worker %q: %w", name, err)
      }
      foundWorker, found, err := database.WorkerFactory.GetWorker(name)
      if err != nil {
          return nil, fmt.Errorf("get worker %q: %w", name, err)
      }
      if !found {
          return nil, fmt.Errorf("worker %q not found after save", name)
      }
      return foundWorker, nil
  }
  ```

- [x] **Step 4: Run the consumer RED after the helpers compile but before conversion.**

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLiveInvalidImageFailsFast$'`

  Expected: compile succeeds; `WorkerFactory.GetWorker` finds `live-k8s-worker`, but `persisted.FindContainer(fixedHandleOwner)` returns both container values nil because `FindOrCreateContainer` still ran through `FakeWorker`.

- [x] **Step 5: Convert the four first-batch constructors and satisfy the consumer assertion.**

  Replace `FakeWorker`, `NameReturns`, and `setupFakeDBContainer` in `setupLiveResilienceWorker`, `TestLiveSecretEnvRef`, `setupLiveWorkerWithConfig`, and `TestLiveSidecarViaWorkerAPI` with:

  ```go
  database := useLiveJetbridgeDB(t)
  dbWorker, err := persistNamedWorker(database, "live-k8s-worker")
  if err != nil {
      t.Fatalf("persisting worker: %v", err)
  }
  worker := jetbridge.NewWorker(dbWorker, clientset, *cfg)
  ```

  Use `"live-sidecar-worker"` in the sidecar API test. Preserve `db.NewFixedHandleContainerOwner(handle)`: the real worker creates the fixed-handle container, transitions it to `db.CreatedContainer` inside `FindOrCreateContainer`, and keeps K8s pod lookup names unchanged. Assert persistence through `database.WorkerFactory.GetWorker`, then through `db.Worker.FindContainer`; never query `db.DbConn` directly.

  Preserve these first-batch observations: invalid-image ErrImagePull/ImagePullBackOff diagnostics, pod startup timeout, live SecretKeyRef value, configured service account, and sidecar presence. `TestLiveResourceLimitsQoS` and `TestLiveSecureDefaults` call `setupLiveWorker` from `live_worker_test.go`; they are deliberately deferred to Task 4.

- [x] **Step 6: Run truthful Task 3 verification and commit.**

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLive(InvalidImageFailsFast|PodStartupTimeout|SecretEnvRef|ServiceAccount|SidecarViaWorkerAPI)$'`

  Expected: PASS with a configured live cluster. This command does not claim ResourceLimitsQoS or SecureDefaults are converted.

  ```bash
  git add atc/worker/jetbridge/jetbridge_suite_test.go atc/worker/jetbridge/live_test.go atc/worker/jetbridge/live_resilience_test.go atc/worker/jetbridge/live_secret_env_test.go atc/worker/jetbridge/live_security_test.go atc/worker/jetbridge/live_sidecar_test.go
  git commit -m "test: add isolated jetbridge worker fixtures"
  ```

### Task 4: Reuse the Jetbridge Fixture in Four More Files

**Files:**

- Modify: `atc/worker/jetbridge/live_sidecar_logstream_test.go`
- Modify: `atc/worker/jetbridge/live_worker_test.go`
- Modify: `atc/worker/jetbridge/podname_integration_test.go`
- Modify: `atc/worker/jetbridge/secret_env_test.go`

**Interfaces:**

- Consumes: `useJetbridgeDB() jetbridgeDB`, `useLiveJetbridgeDB(*testing.T) jetbridgeDB`, `persistNamedWorker(jetbridgeDB, string) (db.Worker, error)`, `db.WorkerFactory.GetWorker(string) (db.Worker, bool, error)`, and `db.Worker.FindContainer(db.ContainerOwner) (db.CreatingContainer, db.CreatedContainer, error)`.
- Produces: the remaining four worker constructors removed, distinct logstream child-test clones, and persisted worker/container assertions in fake-K8s and live-K8s consumers.

- [x] **Step 1: Add consumer-visible assertions while all four consumers still use FakeWorker.**

  In both Ginkgo Describe var blocks, declare `database jetbridgeDB`. In each `BeforeEach`, assign `database = useJetbridgeDB()` before constructing the fake worker; do not short-declare it. After `FindOrCreateContainer`, call `database.WorkerFactory.GetWorker("k8s-worker-1")` and expect `found == true`; this is red because the consumer still uses `FakeWorker` and no real worker row exists.

  In `setupLiveWorkerWithLocator`, call `database := useLiveJetbridgeDB(t)` but leave `fakeDBWorker` in `jetbridge.NewWorker`; immediately require `database.WorkerFactory.GetWorker("live-k8s-worker")` to find a row. In logstream `runOnce`, make the same red assertion for `live-sc11-worker` while it still uses `FakeWorker`.

- [x] **Step 2: Run both red commands before converting the consumers.**

  Run: `ginkgo --focus='uses GeneratePodName when metadata has pipeline/job/build|emits ValueFrom.SecretKeyRef for env vars in SecretEnv' ./atc/worker/jetbridge/`

  Expected: FAIL because both clone-local worker factories return `found == false`.

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLive(SidecarLogStreamTimeout|WorkerTaskExecution)$'`

  Expected: FAIL at the new worker lookup before pod execution.

- [x] **Step 3: Convert Ginkgo worker/container consumers.**

  In each Ginkgo `BeforeEach`, assign the Describe-scoped field with `database = useJetbridgeDB()`, persist `k8s-worker-1`, and build the jetbridge worker from the returned `db.Worker`. Remove `setupFakeDBContainer` calls in all specs in both files. After each `FindOrCreateContainer`, verify through `database.WorkerFactory.GetWorker`, then:

  ```go
  creating, created, err := persisted.FindContainer(
      db.NewFixedHandleContainerOwner(handle),
  )
  Expect(err).NotTo(HaveOccurred())
  Expect(creating).To(BeNil())
  Expect(created).NotTo(BeNil())
  Expect(created.Handle()).To(Equal(handle))
  ```

  Keep `fake.NewSimpleClientset`, `fakeExecExecutor`, pod-name/label/attach/volume-binding assertions, literal env assertions, and SecretKeyRef assertions unchanged.

- [x] **Step 4: Convert live worker and logstream consumers with accurate clone ownership.**

  In `setupLiveWorkerWithLocator`, persist `live-k8s-worker` once per calling `*testing.T`, then preserve SPDY, artifact daemon configuration, and `ArtifactLocator` sharing.

  In `TestLiveSidecarLogStreamTimeout`, keep `runOnce(t *testing.T, handle string, withSidecarWriter bool) time.Duration`, but call it from distinct child tests so `StandardTestRunner` allocates distinct clones:

  ```go
  stamp := time.Now().Format("20060102-150405.000000000")

  var controlDuration time.Duration
  t.Run("control", func(t *testing.T) {
      controlDuration = runOnce(t, "live-sc11-control-"+stamp, false)
  })

  var writerDuration time.Duration
  t.Run("with-sidecar-writer", func(t *testing.T) {
      writerDuration = runOnce(t, "live-sc11-writer-"+stamp, true)
  })
  ```

  The shared nanosecond-resolution stamp keeps the pair attributable to one measurement while the `control`/`writer` prefixes prevent collisions between the two pods and with leaked pods from earlier runs. Inside `runOnce`, call `useLiveJetbridgeDB(t)` once, persist `live-sc11-worker`, and build the worker from it. Do not claim two helper calls with the parent `t` would create two clones: repeated `OpenConn(parentT)` calls reuse the parent clone. Preserve the elapsed assertion comparing `writerDuration-controlDuration` to the five-second bound.

- [x] **Step 5: Run green verification and commit.**

  Run: `ginkgo --focus='(Pod Name Integration|SecretEnv in Pod Spec)' ./atc/worker/jetbridge/`

  Expected: PASS with real worker/container rows and fake Kubernetes behavior.

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLive(SidecarLogStreamTimeout|WorkerTaskExecution|WorkerNonZeroExit|WorkerExecMode|WorkerPodSurvivesCompletion|WorkerHijackExistingPod|ResourceLimitsQoS|SecureDefaults)$'`

  Expected: PASS with a configured live cluster. ResourceLimitsQoS and SecureDefaults are included here because `setupLiveWorker` is now backed by the converted `setupLiveWorkerWithLocator`.

  ```bash
  git add atc/worker/jetbridge/live_sidecar_logstream_test.go atc/worker/jetbridge/live_worker_test.go atc/worker/jetbridge/podname_integration_test.go atc/worker/jetbridge/secret_env_test.go
  git commit -m "test: reuse persisted workers in jetbridge tests"
  ```

### Task 5: Combined Verification, Exact Recount, Review, and Bookkeeping

**Files:**

- Review: the three API files, both jetbridge harness files, and all eight converted jetbridge consumer files listed above.
- Modify: only a test file whose focused command exposes a concrete defect, plus this plan’s checkbox state.

**Interfaces:**

- Consumes: all Task 1–4 fixtures and commands.
- Produces: exact count evidence, command evidence, an independent review verdict, and plan bookkeeping without a push.

- [ ] **Step 1: Check the shared service without managing it.**

  Run: `pg_isready -h 127.0.0.1 -p 15432 -U postgres`

  Expected: `accepting connections`. If unavailable, record the exact output and do not run a Docker, Colima, PostgreSQL, or Kubernetes lifecycle command.

- [ ] **Step 2: Run all non-live converted suites.**

  Run: `ginkgo ./atc/api ./atc/worker/jetbridge/`

  Expected: PASS. Ginkgo specs clone per spec; API and jetbridge packages may run concurrently against the shared service.

- [ ] **Step 3: Run the complete converted live set when cluster credentials exist.**

  Run: `go test -tags live ./atc/worker/jetbridge -run '^TestLive(InvalidImageFailsFast|PodStartupTimeout|SecretEnvRef|ServiceAccount|SidecarViaWorkerAPI|SidecarLogStreamTimeout|WorkerTaskExecution|WorkerNonZeroExit|WorkerExecMode|WorkerPodSurvivesCompletion|WorkerHijackExistingPod|ResourceLimitsQoS|SecureDefaults)$'`

  Expected: PASS with `KUBECONFIG` or in-cluster credentials. A Kubernetes prerequisite failure is recorded separately from PostgreSQL results and does not authorize a local cluster or Docker action.

- [ ] **Step 4: Recount imports and constructors against the exact matrix.**

  Run:

  ```bash
  rg -n 'atc/db/dbfakes|new\(dbfakes\.Fake' \
    atc/api/teams_test.go \
    atc/api/workers_test.go \
    atc/api/pipelines_test.go \
    atc/worker/jetbridge/live_resilience_test.go \
    atc/worker/jetbridge/live_secret_env_test.go \
    atc/worker/jetbridge/live_security_test.go \
    atc/worker/jetbridge/live_sidecar_test.go \
    atc/worker/jetbridge/live_sidecar_logstream_test.go \
    atc/worker/jetbridge/live_worker_test.go \
    atc/worker/jetbridge/podname_integration_test.go \
    atc/worker/jetbridge/secret_env_test.go
  ```

  Expected explicit constructors: Teams 1 (`FakeTeam`), Workers 2 (`FakeTeam`, `FakeWorker`), Pipelines 2 (`FakeTeam`, `FakePipeline`), and all eight jetbridge files 0. Total remaining is exactly 5; total removed is exactly 35 from the baseline 40. Each jetbridge file has no `dbfakes` import. `rg -n 'setupFakeDBContainer'` does not list these eight files; the suite helper remains because out-of-scope tests still call it.

- [ ] **Step 5: Run placeholder, type, command, formatting, and scope review.**

  Check each retained fake use has a method-specific comment and each ordinary closed-connection context installs only the dependency field named in Tasks 1–2. Confirm `Pipeline.CreateStartedBuild(atc.Plan) (db.Build, error)` is used only for POST pipeline builds, `Job.CreateBuild(string)` is used for badges/listings, worker persistence assertions go through `WorkerFactory.GetWorker`, no jetbridge lock/cache-global setup exists, and logstream child tests own separate clones.

  Run:

  ```bash
  rg -n -i 'T[O]DO|T[B]D|implement[[:space:]]+later|fill[[:space:]]+in|appropriate[[:space:]]+error|add[[:space:]]+validation|handle[[:space:]]+edge[[:space:]]+cases|similar[[:space:]]+to[[:space:]]+Task|\x3c[^\x3e]+\x3e' \
    docs/superpowers/plans/2026-08-07-real-postgres-api-jetbridge-phase.md
  git diff --check
  git diff --name-only
  ```

  Expected: placeholder scan prints nothing; `git diff --check` prints nothing; changed product/test paths are only the files declared by Tasks 1–4, and no benchmark/corpus file appears. Request an independent code review and require a PASS verdict before bookkeeping.

- [ ] **Step 6: Commit bookkeeping without pushing.**

  ```bash
  git add docs/superpowers/plans/2026-08-07-real-postgres-api-jetbridge-phase.md
  git commit -m "docs: record real postgres API and jetbridge phase"
  ```

## Exact payoff and concerns

- Removed constructors: Teams 6 + Workers 4 + Pipelines 17 + jetbridge 8 = 35.
- Retained constructors: Teams `FakeTeam` 1 + Workers `FakeTeam` 1 and `FakeWorker` 1 + Pipelines `FakeTeam` 1 and `FakePipeline` 1 = 5.
- Whole-file payoff: all eight named jetbridge consumer files drop `atc/db/dbfakes`; the three API files retain their documented narrow sites.
- Live Kubernetes access remains an external prerequisite. The Ginkgo runner and live `StandardTestRunner` own separate templates when live-tagged tests run, and fixed-handle owners keep container/pod handles stable. Repeated `StandardTestRunner.OpenConn` calls for the same `*testing.T` reuse one clone; only distinct tests or `t.Run` children receive distinct clones.
