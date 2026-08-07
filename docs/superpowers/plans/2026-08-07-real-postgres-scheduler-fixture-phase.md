# Real PostgreSQL Scheduler Fixture Phase

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Implement one task at a time, preserve concurrent edits, and stage only the
> files owned by the task.

**Goal:** Establish an opt-in per-spec PostgreSQL clone fixture for scheduler
tests and replace normal job/build/pipeline/lock state in the metrics and
runner suites, retaining only deliberately synthetic orchestration inputs.

**Exact baseline and target:** At `32a21b1d9a`, five scheduler test files
import `dbfakes` and contain 39 constructors. This bounded phase targets 21:
`metrics_test.go` 9 to 2, `runner_test.go` 10 to 1, and removal of one dead
`FakePipeline` apiece from `buildstarter_test.go` and `scheduler_test.go`.
The retained metrics pair represents the synthetic check-build-name branch;
the retained runner `FakeJobFactory` emits the same real job twice to exercise
deduplication, a state a production query does not return.

The complete type census is 18 `FakeBuild`, 14 `FakeJob`, 5 `FakePipeline`,
and 2 `FakeJobFactory` at baseline; the target is 16 `FakeBuild`, 4 `FakeJob`,
and 1 `FakeJobFactory`. All five scheduler test files continue to import
`dbfakes`: BuildStarter retains its selective planning/start fault matrix;
job-scheduling retains its table-driven synthetic job/build pair; Scheduler
retains its late unit-protocol job faults; Metrics retains the synthetic check
job/build pair; and Runner retains only the duplicate-input factory.

**Architecture:** Add one suite `postgresrunner.GinkgoRunner` and an opt-in
`useSchedulerDB` fixture. A converted spec creates exactly one unique database
from the shared migrated template, builds all factories and advisory-lock
connections from it, closes every ordinary/secondary/singleton connection,
then drops the clone. Scheduler goroutines must be joined before cleanup.
Ordinary scheduling uses persisted teams, pipelines, jobs, pending builds,
requested times, reloads, timestamps, and real advisory-lock ownership.

**Files:** Modify
`atc/scheduler/scheduler_suite_test.go`, `metrics_test.go`, `runner_test.go`,
`buildstarter_test.go`, `scheduler_test.go`, and this plan only. Do not change
production code, generated fakes, Docker/service lifecycle, benchmarks, or
corpus files. Do not push.

## Task 1: Add and prove the opt-in scheduler fixture

- [ ] Register `postgresrunner.GinkgoRunner` once in the suite.
- [ ] Define `schedulerDB` with clone `DbConn`, production `lock.LockFactory`,
  `dbtest.Builder`, and team/pipeline/job/build factories.
- [ ] Implement `useSchedulerDB` with drop registered before close cleanup so
  Ginkgo LIFO order closes the primary and all lock connections before drop.
  No suite-wide `BeforeEach` may clone a database. Call
  `db.CleanupBaseResourceTypesCache()` for each new clone.
- [ ] Add helpers for a persisted team/pipeline/job graph, schedule requests,
  persisted reloads, secondary loaded-then-closed jobs/factories, and an
  independent lock session only where reused. Add a SQL helper that reads
  `schedule_requested` and `last_scheduled` by job ID; `db.Job` exposes no
  last-scheduled getter and its reload query does not select that column.
- [ ] Run `pg_isready -h 127.0.0.1 -p 15432 -U postgres`, compile with
  `go test ./atc/scheduler -run '^$'`, and prove one small opt-in fixture spec
  serially and with `ginkgo -p` before broader rewiring.
- [ ] Independently inspect lifecycle/order and commit the fixture as
  `test(scheduler): add isolated postgres fixture`.

## Task 2: Persist scheduler metrics and tracing state

- [ ] Call `useSchedulerDB` only in the metrics Describes that need persisted
  jobs/builds; keep metric, tracing exporter, planner, algorithm, and build
  scheduler collaborators as their existing non-database seams.
- [ ] Persist the jobs used for scheduling-gauge, scheduling-duration, and
  schedule-job tracing cases. Request scheduling through the real job API,
  feed the real `JobFactory` to `Runner`, and join each scheduling goroutine
  before clone cleanup.
- [ ] Persist an ordinary pending job build for `BuildsStarted` and the
  try-start-pending-build tracing case. Call
  `job.SaveNextInputMapping(nil, true)` before BuildStarter runs; persisted jobs
  otherwise default `inputs_determined=false` and decline to adopt/start the
  build. Reload the build through `BuildFactory` and assert its started state.
  Read scheduling timestamps with the fixture SQL helper and assert exact
  `schedule_requested`/`last_scheduled` outcomes in addition to metric/span
  observations.
- [ ] Retain exactly one shared `FakeJob` and one shared `FakeBuild` for the
  check-build-name metric case, whose synthetic job pending-build result and
  special build name are the behavior under observation. Comment this exact
  boundary.
- [ ] Add a representative persisted assertion before rewiring and capture
  RED, then GREEN. Sensitivity-check a wrong job/build ID or timestamp and
  restore it.
- [ ] Run the metrics focus serially and in parallel, format, vet, diff-check,
  recount 9 to 2, obtain independent review, and commit as
  `test(scheduler): persist metrics scheduling state`.

## Task 3: Persist runner job, reload, timestamp, and lock behavior

- [ ] Replace ordinary `FakeJob`, `FakePipeline`, and `FakeJobFactory` setup
  with persisted pipelines/jobs returned by the real job factory. Preserve
  exact scheduler resources, but do not assume result order:
  `JobsToSchedule` has no `ORDER BY` and scheduling is asynchronous. Key
  scheduler stub behavior by real job ID/name and make observations
  order-independent.
- [ ] Assert successful runs by querying each job's persisted
  `schedule_requested`/`last_scheduled` timestamps with the fixture SQL helper,
  not generated call counts or `Job.Reload` alone.
- [ ] Exercise lock refusal with a second production lock session holding the
  same job scheduling lock. Release it and join every runner goroutine before
  clone cleanup.
- [ ] Exercise direct SQL failures with objects loaded from secondary
  connections that are closed only after loading and close each deliberately
  doomed `DbConn` exactly once. Exercise reload-not-found with a narrow
  real-`JobFactory` decorator that delegates `JobsToSchedule`, then
  synchronously destroys the pipeline after scanning and before returning the
  already-loaded real job. Propagate ordinary callback errors; never make
  Gomega assertions in Runner callbacks. Merely deactivating a job is
  insufficient because `Job.Reload` still finds inactive rows by ID.
- [ ] Define a test-owned completion mechanism around each Runner invocation.
  Register failure-safe unblock/wait cleanup before `Run`; observe completion
  only after the scheduling lock is released or the last possible database
  operation. `Runner.Run` returns after launching goroutines and exposes no
  wait method, so no spec may rely on its return as completion. Never assert in
  scheduler callbacks because Runner recovers callback panics into logs.
- [ ] Retain exactly one shared `FakeJobFactory` for the deliberately duplicated
  same-real-job input. Comment that a production `JobsToSchedule` query cannot
  return this duplicate. Do not retain generated jobs or pipelines.
- [ ] Keep `schedulerfakes.FakeBuildScheduler` and lock fakes only where they
  model the orchestration/runtime subject rather than database state.
- [ ] Capture RED/GREEN on a persisted timestamp assertion and sensitivity on
  the wrong job ID/time; then run the Runner focus serially and in parallel.
- [ ] Format, vet, diff-check, recount 10 to 1, inspect all goroutine joins and
  connection cleanup, obtain independent review, and commit as
  `test(scheduler): persist runner scheduling state`.

## Task 4: Delete dead pipeline setup and close the phase

- [ ] Remove the unused `FakePipeline` declaration/setup from
  `buildstarter_test.go` and `scheduler_test.go` without changing test
  behavior. Run their focused specs before and after and commit as
  `test(scheduler): remove dead pipeline doubles`.
- [ ] Run all focused commands, `gofmt` on modified Go files,
  `go test ./atc/scheduler -run '^$'`, full `ginkgo ./atc/scheduler`, uncached
  `go test ./atc/scheduler -count=1`, and full `ginkgo -p ./atc/scheduler`.
- [ ] Run `go vet ./atc/scheduler`, live-tag compile/vet if the package has
  live-tagged files, `git diff --check`, `git status --short`, and exact
  constructor/import counts.
- [ ] Confirm the scheduler census is exactly 39 to 21 and every retained
  database fake is a documented synthetic/fault boundary.
- [ ] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding; record observed evidence and commit plan closure as
  `docs: record scheduler postgres fixture phase`.

## Phase acceptance

- [ ] The scheduler suite has one opt-in per-spec clone fixture and unrelated
  specs do not create databases.
- [ ] Metrics reaches 9 to 2, Runner reaches 10 to 1, and both dead pipeline
  constructors are removed, for an exact scheduler total of 21.
- [ ] Normal job/build/reload/timestamp/lock state is persisted and reread;
  retained generated database fakes model only the explicit synthetic inputs.
- [ ] Serial and parallel full suites pass against isolated clones in one
  machine-wide PostgreSQL service with no goroutine using a dropped clone.
- [ ] No production behavior or unrelated file changes, and nothing is pushed.
