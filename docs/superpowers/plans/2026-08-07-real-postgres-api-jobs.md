# Real PostgreSQL API Jobs Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Use one owner for `jobs_test.go`, preserve concurrent edits, and stage only
> the task's assigned files.

**Goal:** Replace every generated database fake in the Jobs API tests with
real team, pipeline, job, build, resource, input, cache, ownership, and
scheduling state from a unique PostgreSQL template clone.

**Exact baseline and target:** At `c033d05492`,
`atc/api/jobs_test.go` has 20 explicit generated database-fake constructor
sites: `FakeBuild` x9, `FakeBuildForAPI` x3, `FakeJob` x2,
`FakePipeline` x1, `FakeResource` x3, and `FakeResourceType` x2. The
`fakeDBResourceType` constructor is one static site invoked three times by the
root setup. The file also consumes suite-level `dbTeamFactory`, `dbTeam`,
`dbJobFactory`, and `dbCheckFactory` fakes. This phase targets 20 to 0,
removes the `dbfakes` import, removes every reference to those four suite DB
fakes from this file, and removes one DB-fake-importing test file from the
repository-wide census.

**Architecture:** Opt each converted Jobs API Describe into the existing
`useRealDB` harness so every spec receives one database cloned from its
Ginkgo process's migrated template in the shared machine-wide PostgreSQL
service. Persist normal behavior with production factories, `dbtest.Builder`,
and public DB methods; build each handler from copied real dependencies.
Deliver deliberate SQL failures with a closed secondary real connection when
the target operation is the first SQL call, and otherwise use a narrowly named
one-method decorator around a real persisted object. A decorator may route a
real team/pipeline/job, record arguments, or return the exact configured
error, but it must never fabricate successful state.

**Files:** Modify only `atc/api/jobs_test.go` and this plan. Do not modify
production code, generated fakes, `atc/api/real_db_test.go`,
`atc/api/api_suite_test.go`, Docker/service lifecycle, benchmarks, corpus
files, or another agent's work. Do not push.

## Global constraints

- [x] Use exactly one `useRealDB()` call per spec. The runner allows one active
  clone per Ginkgo process; create any custom server or secondary connection
  against that same clone.
- [x] Register the clone drop before connection cleanup through `useRealDB`,
  close every response body, close every custom server, and close each
  secondary doomed connection exactly once before the clone is dropped.
- [x] Every converted top-level Describe must declare its own lexically scoped
  `var server *httptest.Server` and assign only that local. Never overwrite the
  package-global suite server: it owns the generated-fake baseline server and
  would otherwise be leaked or double-closed.
- [x] Keep `fakeAccess` and other access/credential collaborators as
  non-database seams. Do not reintroduce a generated DB fake through a helper,
  composite literal, shared suite variable, or new file.
- [x] Persist every successful team, pipeline, job, build, resource, input,
  cache, ownership, and scheduling result. No decorator may return a made-up
  successful DB object, successful count, successful pagination value, or
  successful lookup.
- [x] Do not hard-code database row IDs, build IDs, build names, Unix
  timestamps, SQL result order, or pagination cursors. Derive expectations
  from the persisted objects and use keyed lookup or `ConsistOf` where SQL
  ordering is not part of the endpoint contract.
- [x] Keep every intermediate commit green under the entire `Jobs API` focus,
  not only the contexts changed by that commit. Run the normal nine-process
  focus before each implementation checkpoint is accepted.
- [x] Capture a real RED, GREEN, and restored sensitivity check for every
  implementation task. A compile error, an intentionally injected DB error,
  or a fake call-count mismatch is not a valid RED for persisted success
  behavior.
- [x] Stage exact owned paths, inspect `git diff --cached --name-only`, and
  never stage concurrent changes such as `atc/api/resources_test.go`.

## Fake-state inconsistencies the conversion must remove

- [x] The job-build list requests `limit=2` while its fake returns three
  builds. A real query must honor the requested limit.
- [x] The job-build list uses `since=5`, but `ListJobBuilds` recognizes `from`,
  `to`, `limit`, and the timestamp-mode parameter; `since` is ignored. Replace
  this with supported query parameters and assert the real range.
- [x] Several fake builds are `started` while carrying a nonzero end time.
  Real started builds must have no completion time; completed builds must be
  started and finished through DB methods.
- [x] Manual-create, named-build, and rerun responses claim pipeline
  `a-pipeline` even though the request targets `some-pipeline`. The response
  must carry the persisted owning pipeline.
- [x] Manual-create and rerun fakes return `started` builds, while production
  `Job.CreateBuild` and `Job.RerunBuild` create pending builds. Assert the real
  pending status and production-created metadata.
- [x] The input fixture calls `resource1.IDReturns(2)` while configuring the
  second resource, leaving the second fake with its zero ID. Persist two
  distinct resources and use their actual IDs in the input mapping.
- [x] The dashboard fake can pair a failed transition build with a newer
  succeeded finished build in a combination not maintained by production's
  transition update rules. Create builds through `Start`/`Finish` and assert
  the transition that PostgreSQL actually records.
- [x] Named-build and rerun requests ask for `some-build` while their fakes
  return a build named `1`. Put the actual persisted build name in the URL and
  prove the requested row is returned.
- [x] Hard-coded build IDs, pipeline IDs, start/end times, and disconnected
  fake resource-type lists currently allow response objects that do not belong
  to one relational graph. Every response assertion must be derived from one
  owning persisted graph.
- [x] The manual-check fixture supplies a fake resource type named
  `some-input` without a persisted parent/type relationship. Configure a real
  custom resource type and resource, or use a real base type with the actual
  empty custom-type list; do not synthesize a relationship only for the
  CheckFactory call.

## Task 1: Persist job detail, badge, build-list, and named-build reads

**Payoff:** remove 10 constructor sites: the two detail `FakeBuild`s, one
badge `FakeBuild`, the list endpoint's one `FakeBuild` and three
`FakeBuildForAPI`s, and the named-build endpoint's `FakeJob` plus two
`FakeBuild`s. The exact census moves from 20 to 10.

- [x] Capture the current serial baseline for the entire `Jobs API` focus and
  record that it contains 150 specs. Recount the exact 20 sites before the
  first edit.
- [x] Add file-local fixture helpers with stable interfaces used by all three
  implementation tasks:

  ```go
  type jobsAPIFixture struct {
      Real     *realDB
      Team     db.Team
      Pipeline db.Pipeline
      Builder  dbtest.Builder
      Scenario *dbtest.Scenario
  }

  func useJobsAPIFixture(ref atc.PipelineRef, config atc.Config) *jobsAPIFixture
  func (fixture *jobsAPIFixture) Job(name string) db.Job
  func (fixture *jobsAPIFixture) Serve() *httptest.Server
  func (fixture *jobsAPIFixture) ServePipeline(pipeline db.Pipeline) *httptest.Server
  ```

  `useJobsAPIFixture` must call `useRealDB` once, create the real
  `some-team`, save the supplied pipeline ref/config with config version zero,
  initialize `dbtest.NewBuilder`, initialize `Scenario` with that exact real
  Team and Pipeline, and return without creating a second server. All
  `WithResourceVersions`, `WithNextInputMapping`, and `WithJobBuild` calls
  must run on this populated scenario so they cannot bootstrap an unrelated
  team/pipeline.
  `Serve` and `ServePipeline` must register server cleanup. The latter may use
  a static routing TeamFactory/Team decorator only to pass the supplied real
  or one-method-decorated pipeline through access middleware. Each caller must
  assign the returned server to its Describe-local shadow, never the package
  global.
- [x] Add small build helpers that call `Job.CreateBuild`, `Build.Start`,
  `Build.Finish`, and `Build.Reload`; return the real build and never update
  build rows directly. Use the persisted object's ID, name, status, owner,
  start time, and end time for JSON expectations.
- [x] Convert job detail to a real config containing the requested job,
  resources, get/put steps, and groups. Persist `Paused`,
  `FirstLoggedBuildID`, one succeeded finished build, and one started next
  build. Decode the response and compare every field to the real pipeline,
  job, and builds, including a zero end time on the started build. Exercise no
  builds by naturally leaving the job without builds. Any `passed` input names
  must refer to explicitly configured upstream jobs so PostgreSQL actually
  creates the intended `job_inputs` graph.
- [x] Replace detail call-count-only assertions for `Inputs`, `Outputs`, and
  `FinishedAndNextBuild` with their observable persisted response fields.
  Preserve the exact error branches as follows: a closed secondary job for an
  `Inputs` error; an `outputsErrorJob` embedding the real job for an error only
  after inputs succeeded; and a `finishedAndNextErrorJob` embedding the real
  job for an error only after inputs and outputs succeeded. Route each through
  a pipeline decorator that returns the persisted/decorated job for the exact
  requested name and delegates other names.
- [x] Produce a `Pipeline.Job` SQL failure by preloading the real team and
  pipeline on a secondary connection, closing it exactly once, and routing
  that preloaded pipeline through access middleware. Use a genuinely absent
  configured job for the 404 branch.
- [x] Convert the badge matrix by creating one real build per status context,
  starting it, finishing it as succeeded/failed/aborted/errored, reloading it,
  and asserting the existing SVG status/color/title behavior. Leave the job
  buildless for the unknown badge. Persist `Expose`/`Hide` for public/private
  access outcomes.
- [x] Preserve badge `FinishedAndNextBuild` and `Pipeline.Job` failures with
  the same narrowly scoped error decorators/closed objects used by detail;
  do not add a configurable all-method job double.
- [x] Convert job-build listing pagination behaviorally. For the no-parameter
  case, create 101 real builds and prove only
  `atc.PaginationAPIDefaultLimit` are returned. For `from`, `to`, and `limit`,
  create seven builds and derive the range from their IDs. For the response
  shape, create a started ordinary build, a succeeded ordinary build, and a
  real rerun, request a supported limit large enough to return all three, and
  compare their dynamic presentation including the rerun link/number.
- [x] Create three persisted builds for previous/next pagination, request the
  middle cursor with limit one, and derive both RFC5988 links from the older
  and newer real IDs. Repeat with a pipeline saved under real instance vars;
  build the vars query with `PipelineRef.QueryParams()` instead of manually
  assuming JSON encoding or parameter order.
- [x] Produce the list's `Job.Builds` error with a closed secondary real job
  returned by the narrow job-routing pipeline. Preserve the handler's existing
  404-on-build-list-error behavior. Produce `Pipeline.Job` failure through the
  doomed pipeline path and the not-found result with a missing real job.
- [x] Convert named-build lookup by creating the real build, putting
  `build.Name()` in the request path, and comparing the decoded body to that
  exact build. For public access, expose the pipeline and persist the requested
  build rather than returning an empty fake. Exercise missing build naturally;
  use a closed secondary job for the `Job.Build` SQL error and a doomed
  pipeline for `Pipeline.Job` failure.
- [x] Before rewiring one read handler, add an assertion for the persisted
  build ID/name/status and run the focused spec to observe RED against the
  fake response. Rewire to the real server and observe GREEN. Then change one
  expected persisted build status to the wrong value, observe the focused
  spec fail, restore it, and pass.
- [x] Close every primary and additional response body. Any request issued
  inside an `It` for alternate badge titles must register its own body cleanup.
- [x] Run the read-focused contexts, then the entire `Jobs API` focus serially
  and across nine processes. Run format, compile, vet, and diff-check; recount
  exactly 20 to 10; obtain independent review with no unresolved Critical,
  Important, or Minor finding; and commit only `atc/api/jobs_test.go` as
  `test(api): persist job read state`.

  ```bash
  gofmt -w atc/api/jobs_test.go
  go test ./atc/api -run '^$'
  ginkgo --procs=1 --focus='GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name( |$)|GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/badge|GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds' ./atc/api
  ginkgo --procs=1 --focus='Jobs API' ./atc/api
  ginkgo --procs=9 --focus='Jobs API' ./atc/api
  go vet ./atc/api
  git diff --check -- atc/api/jobs_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/jobs_test.go
  ```

  The final search must report exactly 10 constructor sites.

## Task 2: Persist manual creation, job inputs, and reruns

**Payoff:** remove seven constructor sites: manual creation's `FakeBuild`,
`FakeResource`, and `FakeResourceType`; input listing's two `FakeResource`s;
and rerun's two `FakeBuild`s. The exact census moves from 10 to 3.

- [x] Convert manual build creation to a real ordinary pipeline/job. Assert
  the handler inserts exactly one persisted build, returns its dynamic ID and
  name, reports the real `some-pipeline` owner, records the authenticated
  creator, and returns the production pending status with zero start/end
  times. Replace `CreateBuildCallCount` with a fresh `Job.Build`/`Job.Builds`
  query proving the inserted row.
- [x] Persist a base template through `atc.Config{Template: true}` and exercise
  its natural 409. Persist `DisableManualTrigger: true` and prove 409 plus no
  inserted build through a fresh DB query.
- [x] Exercise the workflow-run-owned 409 with real authoritative ownership:
  insert the minimal valid `agent_workflow_definitions`, `pipeline_runs`, and
  `agent_workflow_runs` rows using the schema and constraints proven by
  `atc/db/workflow_run_owned_pipeline_test.go`. Tie all IDs to the current
  fixture team/pipeline, use unique hashes/keys, and assert neither a job build
  nor a check is created. Keep this direct SQL helper file-local and limited
  to ownership authority that has no public setup method.
- [x] Produce a generic `Job.CreateBuild` SQL failure with a preloaded job from
  a secondary connection that is then closed exactly once. Route that real
  job through the persisted pipeline. Do not return a hand-built error from a
  broad job stub when the production method can fail on the closed connection.
- [x] Persist the manual input resource and its actual resource-type graph.
  Use `dbtest.Builder.WithResourceVersions`, locate the persisted version,
  call `Resource.PinVersion`, reload the resource, and verify
  `CurrentPinnedVersion` before issuing the request.
- [x] Add a `recordingJobsCheckFactory` that embeds the real `db.CheckFactory`,
  records `TryCreateCheck` arguments under a mutex, and always delegates its
  normal result. It must have no successful-result override. Record the real
  build count before the request (because `WithResourceVersions` already
  creates and finishes a check), assert the endpoint produces an exact `+1`
  persisted check-build delta or identify the endpoint-created row by its
  returned build, and assert that `from` equals the real pin and the exact
  `manuallyTriggered`, `skipIntervalRecursively`, and `toDB` arguments are all
  true. The fixture's check-build
  channel is not part of this assertion: `toDB=true` returns before the
  in-memory enqueue path.
- [x] Deliver the post-create `Pipeline.Resources` and
  `Pipeline.ResourceTypes` errors through separate one-method pipeline
  decorators embedding the persisted pipeline. Deliver the later `Job.Inputs`
  error through a one-method job decorator embedding the real job. In each
  case, assert the manual build inserted before the downstream error remains
  in PostgreSQL; the decorator may only choose the exact error boundary.
- [x] Convert job input listing with a real config containing two distinct
  resources and two get steps, including params, tags, and passed constraints.
  Explicitly configure every upstream job named by those `passed` constraints
  (`a`, `b`, or dynamically selected persisted equivalents); an unmatched
  passed name causes PostgreSQL to omit the input row rather than preserve the
  intended relationship.
  Save versions with `dbtest.Builder.WithResourceVersions` and save the exact
  `dbtest.JobInputs` through `WithNextInputMapping`. Compare returned resource
  type/source/version/params/tags to the persisted graph and use each real
  resource ID rather than the current duplicate-ID fake setup.
- [x] Exercise no available input versions by leaving `inputs_determined`
  false naturally. Produce `Pipeline.Resources` error through a one-method
  pipeline decorator, `Job.GetFullNextBuildInputs` error with a closed
  secondary job, and the later `Job.Config` error through a one-method
  `configErrorJob` after the real mapping succeeds. Use a missing configured
  job for 404.
- [x] Convert rerun setup with `dbtest.Builder.WithJobBuild` so the original
  persisted build has adopted inputs and `InputsReady()` is true. Put its real
  name in the URL. Assert the handler creates a persisted pending rerun whose
  `RerunOf`, `RerunOfName`, `RerunNumber`, job, pipeline, team, and creator all
  point to the original relational graph.
- [x] Exercise the not-ready 500 with an ordinary `Job.CreateBuild` that has
  not adopted input mappings. Exercise missing build naturally and
  `Job.Build` failure with a closed secondary job. Deliver a generic
  `Job.RerunBuild` error with a one-method `rerunErrorJob` only after its
  embedded real `Build` lookup succeeds. Reuse the real workflow ownership
  helper for the rerun 409 and assert no rerun row was inserted.
- [x] Add a persisted manual-build or rerun response assertion before changing
  its server and capture RED, then GREEN. Change the expected persisted pin,
  original build ID, or rerun number to a wrong value for the sensitivity
  failure; restore it and pass.
- [x] Run the manual-create, input, and rerun contexts followed by the complete
  `Jobs API` focus serially and across nine processes. Run format, compile,
  vet, and diff-check; recount exactly 10 to 3; independently review the real
  check delegation, ownership rows, and one-method fault boundaries; and
  commit only `atc/api/jobs_test.go` as
  `test(api): persist job build state`.

  ```bash
  gofmt -w atc/api/jobs_test.go
  go test ./atc/api -run '^$'
  ginkgo --procs=1 --focus='POST /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds|GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/inputs' ./atc/api
  ginkgo --procs=1 --focus='Jobs API' ./atc/api
  ginkgo --procs=9 --focus='Jobs API' ./atc/api
  go vet ./atc/api
  git diff --check -- atc/api/jobs_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/jobs_test.go
  ```

  The final search must report exactly 3 constructor sites.

## Task 3: Persist global listing, dashboard, and job mutations

**Payoff:** remove the final three constructor sites: the root `FakeJob`, root
`FakePipeline`, and `fakeDBResourceType` helper. The exact census moves from 3
to 0, the `dbfakes` import disappears, and the file stops using its four
suite-level DB fakes.

- [x] Convert `GET /api/v1/jobs` with at least three persisted pipelines: an
  exposed public pipeline in another team, a private pipeline in an authorized
  team, and a private pipeline in an unauthorized team. Configure real job
  inputs/groups/builds. Assert unauthenticated access returns only public jobs,
  team membership adds that team's private jobs, and admin access returns all
  active jobs. Replace `VisibleJobs`/`AllActiveJobs` call-count assertions with
  response membership keyed by persisted job IDs. For each returned job also
  preserve the existing response-shape coverage: compare paused state, next
  and finished build presentation, passed/trigger input summaries, and groups
  to fresh persisted jobs/builds. Configure every job named by a `passed`
  constraint (`job-a` through `job-d`, or explicit persisted equivalents) so
  the production save creates the relationship instead of silently dropping
  it.
- [x] Exercise the empty global list naturally with no visible configured
  jobs. Produce the JobFactory 500 by constructing
  `db.NewJobFactory(doomedConn, fixture.Real.LockFactory)`, closing that
  secondary connection exactly once, copying `fixture.Real.Deps`, and replacing
  only `deps.jobFactory` before building the custom server.
- [x] Convert the pipeline dashboard using a persisted instanced pipeline with
  three jobs, real group membership, inputs, and pause state. Create ordinary
  builds through `CreateBuild`/`Start`/`Finish` so PostgreSQL owns
  `next_build_id`, `latest_completed_build_id`, and `transition_build_id`.
  Decode the response, key jobs by their real IDs/names, and compare build
  summaries to the reloaded real builds. Do not preserve the impossible fake
  transition/latest combination or fixed IDs/times.
- [x] Exercise an empty dashboard with an actual pipeline whose config has no
  jobs. Deliver the dashboard 500 through a `dashboardErrorPipeline` that
  embeds the real pipeline and overrides only `Dashboard`. Persist
  `Expose`/`Hide` for public/private authorization outcomes.
- [x] Convert pause success by requesting a real `job-name`, reloading it, and
  asserting `Paused`, `PausedBy`, and `PausedAt`. Convert unpause by pausing the
  job before the request, then reloading and proving its persisted pause state
  was cleared. Use a missing configured job for 404 and a doomed pipeline for
  lookup failure.
- [x] Produce generic pause/unpause SQL errors with a preloaded secondary real
  job whose connection is then closed exactly once. Exercise immutable 409s
  with real source-selection ownership: insert a valid workflow definition,
  then call `db.NewWorkflowResourceSourcePipelinesFactory(conn).Activate`
  using the real pipeline/team/config version, a valid 64-character config
  hash, and a source declaration matching a configured resource. For unpause,
  pause before activating ownership because the ownership guard must prevent
  the endpoint mutation itself.
- [x] Convert task-cache deletion with a concrete URL step name rather than
  the literal `:step_name`. Seed matching and decoy rows through
  `db.NewTaskCacheFactory(fixture.Real.Conn).FindOrCreate`: matching step/path,
  matching step/different path, and another job. Assert no-path deletion,
  path-specific deletion, zero-row cases, and decoy survival through fresh
  factory queries. Produce the 500 with a closed secondary real job rather
  than a fabricated negative deletion count.
- [x] Convert schedule success by capturing the persisted
  `ScheduleRequestedTime`, issuing the request, reloading the job, and proving
  the timestamp advanced. Use a missing job for 404, a doomed pipeline for
  lookup failure, and a closed secondary real job for `RequestSchedule` SQL
  failure.
- [x] Remove the root `fakeJob`, `fakePipeline`, `versionedResourceTypes`, and
  `fakeDBResourceType` setup. Remove `dbfakes` and every reference to
  `dbTeamFactory`, `dbTeam`, `dbJobFactory`, and `dbCheckFactory`. Retain only
  `fakeAccess` and explicitly justified one-method decorators around real DB
  objects.
- [x] Capture RED by asserting a real persisted pause, cache deletion, or
  schedule timestamp before the endpoint uses real dependencies; capture
  GREEN after rewiring. Change one expected persisted cache survivor or
  scheduling timestamp relationship to a wrong value, observe failure,
  restore it, and pass.
- [x] Run global-list, dashboard, pause, unpause, cache, and schedule contexts,
  then the complete `Jobs API` focus serially and across nine processes. Run
  format, compile, vet, and diff-check; prove exactly 3 to 0 and no suite-fake
  references; obtain independent review with no unresolved Critical,
  Important, or Minor finding; and commit only `atc/api/jobs_test.go` as
  `test(api): persist job mutation state`.

  ```bash
  gofmt -w atc/api/jobs_test.go
  go test ./atc/api -run '^$'
  ginkgo --procs=1 --focus='GET /api/v1/jobs( |$)|GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs( |$)|PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/(pause|unpause|schedule)|DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/tasks/:step_name/cache' ./atc/api
  ginkgo --procs=1 --focus='Jobs API' ./atc/api
  ginkgo --procs=9 --focus='Jobs API' ./atc/api
  go vet ./atc/api
  git diff --check -- atc/api/jobs_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/jobs_test.go
  rg -n '\b(dbTeamFactory|dbTeam|dbJobFactory|dbCheckFactory)\b' atc/api/jobs_test.go
  ```

  Both searches must return no matches.

## Task 4: Full verification and closure

- [x] Run `gofmt -w atc/api/jobs_test.go` and `git diff --check`.
- [x] Run `go test ./atc/api -run '^$'`, `go vet ./atc/api`, and the equivalent
  `-tags=live` compile/vet commands. Jobs API has no live-only runtime
  dependency, so these are compile/static checks rather than Docker work.
- [x] Run the complete Jobs API focus (109 specs after consolidation) with one
  process and the normal nine processes:

  ```bash
  ginkgo --procs=1 --focus='Jobs API' ./atc/api
  ginkgo --procs=9 --focus='Jobs API' ./atc/api
  ```

- [ ] Run the full API package serially and uncached, then across nine Ginkgo
  processes if package-wide runtime remains practical:

  ```bash
  ginkgo --procs=1 ./atc/api
  go test ./atc/api -count=1
  ginkgo --procs=9 ./atc/api
  ```

- [x] Prove the final fake census and suite-fake removal with exact searches:

  ```bash
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/jobs_test.go
  rg -n 'github.com/concourse/concourse/atc/db/dbfakes' atc/api/jobs_test.go
  rg -n '\b(dbTeamFactory|dbTeam|dbJobFactory|dbCheckFactory)\b' atc/api/jobs_test.go
  ```

  All three searches must return no matches.
- [x] Inspect every fixture for one clone per spec, dynamic identifiers and
  ordering, body/server cleanup, exact-once doomed connection closes, and no
  decorator that fabricates a successful object or result. Inspect every
  ownership/cache/check row through a fresh production factory or SQL query.
- [x] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding. Record exact command/spec counts and sensitivity evidence
  in this plan, mark completed checkboxes, and commit the plan closure as
  `docs: record api jobs postgres conversion` without staging unrelated files.

## Phase acceptance

- [x] `jobs_test.go` reaches exactly 20 to 0 generated database-fake
  constructor sites, drops `dbfakes`, and no longer consumes
  `dbTeamFactory`, `dbTeam`, `dbJobFactory`, or `dbCheckFactory`.
- [x] All successful job listing, detail, badge, build, input, manual-create,
  rerun, pause, unpause, cache, and schedule state is persisted and asserted
  through real rows from the spec's unique clone.
- [x] Natural missing rows, template/manual guards, workflow-run ownership,
  resource-source immutability, pagination, and cache deletion are exercised
  through production DB behavior.
- [x] Retained fault/protocol seams are limited to access collaborators,
  closed secondary real connections, a delegating CheckFactory recorder, and
  one-method error decorators around real persisted objects. No retained seam
  manufactures successful DB state.
- [x] Every listed fake-state inconsistency is replaced by a production-valid
  graph and dynamic assertion.
- [ ] Focused serial/nine-process and full API verification pass against
  isolated clones in the single shared PostgreSQL service.
- [x] No production behavior, shared fixture, unrelated file, Docker service,
  benchmark, or corpus change is included, and nothing is pushed.

## Observed completion evidence (2026-08-07)

- The conversion landed in three path-scoped commits:
  `a2716e87bd` (`test(api): persist job read state`, 20 to 10),
  `d06ed1467a` (`test(api): persist job build state`, 10 to 3), and
  `7f213d9833` (`test(api): persist job mutation state`, 3 to 0). Each commit
  changes only `atc/api/jobs_test.go`.
- Exact final searches report no generated database-fake constructor, no
  `dbfakes` import, and no `dbTeamFactory`, `dbTeam`, `dbJobFactory`, or
  `dbCheckFactory` reference in `jobs_test.go`: the phase census is 20 to 0.
- The final checkpoint's persisted RED showed the fake-backed pause endpoint
  leaving the real job unpaused (`expected true`, observed `false`). GREEN
  passed the converted endpoint focus. Its sensitivity mutation inverted a
  persisted cache-survivor expectation and failed (`expected true to be
  false`); restoring the expectation passed the focused spec.
- A follow-up review of the original 48-to-34 `It`-node consolidation found
  one Important omission: the distinct no-path zero-row cache-deletion case.
  The final commit restores that case with fresh survivor queries. The
  reviewer confirmed the finding resolved; the final converted focus is
  35/35 and the complete Jobs API focus is 109/109 both serially and across
  nine processes.
- Formatting and diff checks pass. Standard and `live`-tag API compilation
  (`go test ... -run '^$'`) and vet checks pass. The unfiltered full API
  package commands in the remaining unchecked Task 4 item were not run, so
  no full-package runtime result is claimed here.
