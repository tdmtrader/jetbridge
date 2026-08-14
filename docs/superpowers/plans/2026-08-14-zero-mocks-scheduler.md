# Mock-Free Scheduler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove all four generated scheduler fakes by exercising the production algorithm, planner, build starter, advisory locks, and persisted scheduling outcomes.

**Architecture:** `Scheduler` owns concrete `*algorithm.Algorithm` and `*buildStarter` collaborators assembled by `NewScheduler`; `buildStarter` owns concrete `builds.Planner`. `Runner` accepts a `ScheduleFunc` so only duplicate suppression and in-flight metric timing can be channel-gated; every ordinary scheduling scenario uses `realScheduler.Schedule` and verifies PostgreSQL state, metrics, or trace spans.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL scheduler fixtures, OpenTelemetry test spans, Concourse algorithm and planner.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Use `algorithm.New(schedulerVersionsDB(fixture))` and `builds.NewPlanner(atc.NewPlanFactory(0))`; do not replace them with configurable substitutes.
- Assert input mappings, pending builds, plan JSON, statuses, timestamps, metrics, and trace links rather than collaborator calls.
- Preserve genuine unplannable configuration and no-input/unresolved/max-in-flight/manual/rerun behavior.
- Drop injected ordinary database, planner, starter, and panic failures whose only source is fake configuration.
- Only duplicate suppression and observation of `JobsScheduling` while work is blocked may use a channel-gated `ScheduleFunc`.
- Run `ginkgo ./atc/scheduler`; never run this database suite concurrently via plain `go test ./...`.
- Do not modify the two untracked review documents.

---

### Task 1: Introduce a Compile-Safe Real Scheduler Constructor

**Files:**
- Modify: `atc/scheduler/scheduler.go`
- Modify: `atc/atccmd/command.go:1190-1230`
- Test: `atc/scheduler/scheduler_test.go`
- Verify only: `atc/scheduler/buildstarter.go`, `atc/scheduler/runner.go`

**Interfaces:**
- Consumes: `builds.Planner`, `*algorithm.Algorithm`, and `Scheduler.Schedule(context.Context, lager.Logger, db.SchedulerJob) (bool, error)`.
- Produces: `NewScheduler(builds.Planner, *algorithm.Algorithm) *Scheduler`. The four legacy interfaces and generated fakes remain temporarily so later consumer migrations compile independently; Task 5 removes them atomically.

- [ ] **Step 1: Write a constructor characterization using real dependencies**

In `scheduler_test.go`, replace the fixture's fake construction with:

```go
versionsDB := schedulerVersionsDB(fixture)
realScheduler := NewScheduler(
	builds.NewPlanner(atc.NewPlanFactory(0)),
	algorithm.New(versionsDB),
)
```

For a persisted job with no configured inputs, call `realScheduler.Schedule` and assert the public persisted outcome:

```go
buildInputs, resolved, err := job.GetFullNextBuildInputs()
Expect(err).NotTo(HaveOccurred())
Expect(resolved).To(BeTrue())
Expect(buildInputs).To(BeEmpty())
Expect(schedulerPendingBuilds(job)).To(BeEmpty())
```

- [ ] **Step 2: Run the focused characterization and verify it fails to compile**

Run: `ginkgo -ginkgo.focus='job with no configured inputs' ./atc/scheduler`

Expected: compile failure containing `undefined: NewScheduler` in the dot-imported external test package.

- [ ] **Step 3: Add the real constructor while preserving transitional interfaces**

In `scheduler.go`, import `atc/builds` and `atc/scheduler/algorithm` and add:

```go
func NewScheduler(planner builds.Planner, alg *algorithm.Algorithm) *Scheduler {
	return &Scheduler{
		Algorithm:    alg,
		BuildStarter: NewBuildStarter(planner, alg),
	}
}
```

Do not remove `Algorithm`, `BuildPlanner`, `BuildStarter`, or `BuildScheduler`, do not change their directives, and do not change existing struct fields or `NewBuildStarter`/`NewRunner` signatures yet. The generated files contain compile-time assertions against those names and later test files still construct them.

- [ ] **Step 4: Update production wiring through the new constructor**

In `atc/atccmd/command.go`, construct once immediately before the `components` slice and pass the object through the transitional interface:

```go
jobScheduler := scheduler.NewScheduler(
	builds.NewPlanner(atc.NewPlanFactory(time.Now().Unix())),
	alg,
)

// component wiring
scheduler.NewRunner(
	logger.Session("scheduler"),
	dbJobFactory,
	jobScheduler,
	cmd.JobSchedulingMaxInFlight,
)
```

At this checkpoint `*Scheduler` satisfies the transitional `BuildScheduler` interface. Task 4 changes the runner to the method value after all runner consumers migrate.

- [ ] **Step 5: Format, compile, and commit the constructor seam**

Run:

```bash
gofmt -w atc/scheduler/scheduler.go atc/atccmd/command.go atc/scheduler/scheduler_test.go
go test ./atc/atccmd -run '^$'
ginkgo -ginkgo.focus='job with no configured inputs' ./atc/scheduler
```

Expected: compile succeeds and the focused real-dependency scenario passes.

```bash
git add atc/scheduler/scheduler.go atc/scheduler/buildstarter.go atc/scheduler/runner.go atc/scheduler/scheduler_test.go atc/atccmd/command.go
git commit -m "refactor(scheduler): construct real scheduling services"
```

### Task 2: Exercise Scheduler Outcomes with the Real Algorithm and Starter

**Files:**
- Rewrite relevant contexts: `atc/scheduler/scheduler_test.go`
- Modify: `atc/scheduler/job_scheduling_test.go`
- Verify: `atc/scheduler/algorithm/algorithm_test.go`
- Verify: `atc/scheduler/algorithm/regression_test.go`

**Interfaces:**
- Consumes: `NewScheduler(builds.Planner, *algorithm.Algorithm)` and the existing `schedulerVersionsDB` fixture.
- Produces: real scheduling scenarios for empty inputs, resolved resource versions, unresolved inputs, first occurrences, pending builds, `has_new_inputs`, schedule-again state, and trace links.

- [ ] **Step 1: Seed versions instead of programming algorithm returns**

For each resource-input scenario, use the scheduler database fixture to save the resource config, scope, version, build input, and first-occurrence state that the real algorithm reads. Construct the scheduler exactly as in Task 1. For no-input job tables, use the real algorithm without any version seed; it deterministically returns the empty mapping.

- [ ] **Step 2: Replace call assertions with persisted state**

After `Schedule`, reload the job/build and assert. For the existing `resource-a=v1`/`resource-b=v2` persisted-mapping case, the exact assertion is:

```go
buildInputs, resolved, err := job.GetFullNextBuildInputs()
Expect(err).NotTo(HaveOccurred())
Expect(resolved).To(BeTrue())
persisted := map[string]db.BuildInput{}
for _, input := range buildInputs {
	persisted[input.Name] = input
}
Expect(persisted).To(HaveLen(2))
Expect(persisted["a"].Version).To(Equal(atc.Version{"ref": "v1"}))
Expect(persisted["a"].ResourceID).To(Equal(scenario.Resource("resource-a").ID()))
Expect(persisted["a"].FirstOccurrence).To(BeTrue())
Expect(persisted["b"].Version).To(Equal(atc.Version{"ref": "v2"}))
Expect(persisted["b"].ResourceID).To(Equal(scenario.Resource("resource-b").ID()))
Expect(persisted["b"].FirstOccurrence).To(BeFalse())
```

For the first-occurrence trigger case, the real starter immediately consumes the pending build. Assert `schedulerPendingBuilds(job)` is empty and `schedulerJobHasNewInputs(job)` is true, then use the API-sized result and full build factory deliberately:

```go
apiBuilds, _, err := job.BuildsWithTime(db.Page{})
Expect(err).NotTo(HaveOccurred())
Expect(apiBuilds).To(HaveLen(1))
Expect(apiBuilds[0].Status()).To(Equal(db.BuildStatusStarted))
Expect(apiBuilds[0].HasPlan()).To(BeTrue())

persistedBuild, found, err := fixture.BuildFactory.Build(apiBuilds[0].ID())
Expect(err).NotTo(HaveOccurred())
Expect(found).To(BeTrue())
Expect(persistedBuild.PrivatePlan()).NotTo(Equal(atc.Plan{}))
```

`BuildsWithTime` returns `db.BuildForAPI`, which intentionally does not expose `PrivatePlan`; do not call that method on the API value. For the non-triggering first occurrence, assert no build and `HasNewInputs=true`. For a mapping with no first occurrences, assert no build and `HasNewInputs=false` after first seeding the job with `SetHasNewInputs(true)`. The existing `schedulerInputResult` and resource fixtures provide the IDs and versions used in those assertions.

- [ ] **Step 3: Keep the user-visible state matrix and prune injected internals**

Retain: empty-input resolution, persisted mapping, unresolved mapping, first-occurrence trigger, non-triggering new input, clearing `has_new_inputs`, pending build creation, and linked tracing context. Delete cases created only by fake `Inputs`, `Compute`, `RequestSchedule`, `SaveNextInputMapping`, `GetFullNextBuildInputs`, `EnsurePendingBuildExists`, `SetHasNewInputs`, or `BuildStarter` errors.

- [ ] **Step 4: Convert `job_scheduling_test.go` table to real components**

Replace its fake algorithm/planner/starter construction with:

```go
realScheduler := scheduler.NewScheduler(
	builds.NewPlanner(atc.NewPlanFactory(0)),
	algorithm.New(schedulerVersionsDB(fixture)),
)
```

Keep every row whose input is a real persisted state (aborted, max-in-flight, unchecked resources, undetermined inputs, unplannable config, failed start, rerun ordering). Remove rows only when their state can be reached solely by a fake method error.

Replace the current `PlanFails` row's unresolved resource input: the real scheduler stops at input resolution before it reaches the planner. Use a no-input job whose only plan step is `&atc.RunStep{Message: "hello", Type: "missing-prototype"}` with no matching `atc.Prototype`. This reaches `builds.UnknownPrototypeError` through the production planner after scheduling.

- [ ] **Step 5: Verify scheduler and algorithm behavior**

Run:

```bash
gofmt -w atc/scheduler/scheduler_test.go atc/scheduler/job_scheduling_test.go
ginkgo ./atc/scheduler/algorithm
ginkgo -ginkgo.focus='Scheduler|Job Scheduling' ./atc/scheduler
```

Expected: PASS with no fake algorithm, planner, or starter construction in either file.

- [ ] **Step 6: Commit persisted scheduler outcomes**

```bash
git add atc/scheduler/scheduler_test.go atc/scheduler/job_scheduling_test.go
git commit -m "test(scheduler): compute inputs from persisted versions"
```

### Task 3: Exercise Build Starting with the Real Planner

**Files:**
- Rewrite relevant contexts: `atc/scheduler/buildstarter_test.go`
- Verify: `atc/builds/planner_test.go`

**Interfaces:**
- Consumes: `NewBuildStarter(builds.Planner, *algorithm.Algorithm) *buildStarter`.
- Produces: persisted plan/status coverage for automatic, manual, aborted, rerun, max-in-flight, undetermined-input, and unplannable builds.

- [ ] **Step 1: Replace fake planner and algorithm setup**

Construct:

```go
starter := scheduler.NewBuildStarter(
	builds.NewPlanner(atc.NewPlanFactory(0)),
	algorithm.New(schedulerVersionsDB(fixture)),
)
```

Seed real versions for manual builds and leave real inputs absent for undetermined-input behavior.

- [ ] **Step 2: Assert the manual flag through the persisted plan**

After a manual task build starts, reload it and decode/read `PrivatePlan`; assert:

```go
Expect(build.Status()).To(Equal(db.BuildStatusStarted))
Expect(build.PrivatePlan().Do).NotTo(BeNil())
Expect(*build.PrivatePlan().Do).To(HaveLen(1))
Expect((*build.PrivatePlan().Do)[0].Task).NotTo(BeNil())
Expect((*build.PrivatePlan().Do)[0].Task.CheckSkipInterval).To(BeTrue())
```

`JobConfig.StepConfig()` always wraps `PlanSequence` in a top-level `Do`; for an automatically scheduled task, inspect the same nested task and assert `CheckSkipInterval` is false. Do not inspect planner arguments.

- [ ] **Step 3: Preserve a real planning failure**

Create a no-input job with a `run` step whose `Type` is `missing-prototype` and no matching prototype, start its pending build, and assert the build reaches `db.BuildStatusErrored`, is completed, and has no private plan. `buildStarter` logs `failed-to-create-build-plan` and calls `Finish(BuildStatusErrored)`; it does not persist an error event or reason, so assert that logger entry only if the reason itself matters. This replaces configured planner errors without inventing a persisted contract.

- [ ] **Step 4: Prune injected failure chains**

Retain aborted-build continuation, manual resources-not-checked retry, multiple pending builds, reruns, max-in-flight, undetermined inputs, persisted plan, and real unplannable config. Delete cases whose only mechanism is injected failures from fetching pending builds, finishing, scheduling, readiness, algorithm compute, request/save/adopt, starting, or “planner fails and Finish fails.”

- [ ] **Step 5: Run and commit**

Run:

```bash
gofmt -w atc/scheduler/buildstarter_test.go
ginkgo ./atc/builds
ginkgo -ginkgo.focus='BuildStarter' ./atc/scheduler
```

Expected: PASS with no fake planner/algorithm references.

```bash
git add atc/scheduler/buildstarter_test.go
git commit -m "test(scheduler): plan and start real pending builds"
```

### Task 4: Use Real Runner Outcomes and Gate Only Concurrency

**Files:**
- Rewrite relevant contexts: `atc/scheduler/runner_test.go`
- Modify: `atc/scheduler/metrics_test.go`

**Interfaces:**
- Consumes: a real scheduler method value and the transitional `BuildScheduler` API.
- Produces: `type ScheduleFunc func(context.Context, lager.Logger, db.SchedulerJob) (bool, error)`, `NewRunner(lager.Logger, db.JobFactory, ScheduleFunc, uint64) *Runner`, real scan/lock/timestamp/retry/deletion/tracing coverage, and two channel-gated concurrency scenarios. The unused `BuildScheduler` declaration remains only until its generated file is deleted in Task 5.

- [ ] **Step 1: Use a real scheduler for ordinary runner scenarios**

Construct `realScheduler` with the real planner and algorithm, then:

```go
runner := NewRunner(logger, fixture.JobFactory, realScheduler.Schedule, maxJobs)
```

Change each ordinary runner fixture to a job with a triggering `get` and seed a real resource version/first occurrence before requesting scheduling. Assert the full persisted scan by observing one started build with a private plan for each job plus `last_scheduled`; pending builds should be empty because the real starter consumes them. Assert a held production advisory lock prevents both the build and timestamp mutations. For a pipeline deleted before the runner reloads it, assert the job is absent and no new build exists; the cascading delete removes the job row, so there is no timestamp left to inspect. The existing non-triggering/no-version jobs cannot demonstrate a real runner build and must not be reused unchanged.

- [ ] **Step 2: Introduce the narrow runner seam at the same compile boundary**

In `runner.go`, add:

```go
type ScheduleFunc func(context.Context, lager.Logger, db.SchedulerJob) (bool, error)
```

Change `Runner.scheduler BuildScheduler` to `Runner.schedule ScheduleFunc`, change the third `NewRunner` parameter to `schedule ScheduleFunc`, and call `s.schedule(spanCtx, logger, job)` inside `scheduleJob`. Update production wiring from `jobScheduler` to `jobScheduler.Schedule`. Keep the `BuildScheduler` interface and directive temporarily so the generated fake still compiles until Task 5; no migrated test may construct it.

- [ ] **Step 3: Keep only real retry state**

Create the real max-in-flight or resources-not-ready condition that makes `Schedule` return `needsRetry=true`, run the runner, and assert `last_scheduled` is not advanced. Delete the injected scheduler error and panic rows, closed-secondary-connection timing cases, and direct scan-error decorator when they exist only as unlikely database failure injection.

- [ ] **Step 4: Express duplicate suppression with one channel-gated function**

Use:

```go
started := make(chan struct{}, 1)
release := make(chan struct{})
schedule := ScheduleFunc(func(context.Context, lager.Logger, db.SchedulerJob) (bool, error) {
	started <- struct{}{}
	<-release
	return false, nil
})
```

Submit the same real job twice while the first invocation is blocked. Assert only one `started` event is received with `Consistently(started).ShouldNot(Receive())` after the first receive, then close `release` and assert the runner's metrics return to idle. Do not add a counter or captured job slice.

- [ ] **Step 5: Reuse the gated function only for the in-flight gauge**

In `metrics_test.go`, sample `metric.Metrics.JobsScheduling` after receiving `started` and before closing `release`; assert it returns to zero after release. Use the real scheduler for scheduling duration and runner tracing. Use the real `NewBuildStarter(builds.NewPlanner(...), algorithm.New(...))` directly for `BuildsStarted`, `CheckBuildsStarted`, and the try-start span. Retain the narrow `checkNamedBuild` name adapter only for `CheckBuildsStarted`: a job-scoped pending-build query cannot produce a check-named build, so that metric boundary cannot be reached through `Scheduler.Schedule`. The adapter changes only the domain build name and records nothing.

- [ ] **Step 6: Run and commit runner behavior**

Run:

```bash
gofmt -w atc/scheduler/runner_test.go atc/scheduler/metrics_test.go
ginkgo -ginkgo.focus='Runner|Scheduler Metrics' ./atc/scheduler
```

Expected: PASS; `ScheduleFunc` appears only in the duplicate-suppression and in-flight-gauge contexts.

```bash
git add atc/scheduler/runner.go atc/scheduler/runner_test.go atc/scheduler/metrics_test.go atc/atccmd/command.go
git commit -m "test(scheduler): observe runner through real state"
```

### Task 5: Delete Scheduler Fakes and Verify the Package

**Files:**
- Delete: `atc/scheduler/schedulerfakes/fake_algorithm.go`
- Delete: `atc/scheduler/schedulerfakes/fake_build_planner.go`
- Delete: `atc/scheduler/schedulerfakes/fake_build_starter.go`
- Delete: `atc/scheduler/schedulerfakes/fake_build_scheduler.go`
- Modify: `atc/scheduler/scheduler.go`
- Modify: `atc/scheduler/buildstarter.go`
- Modify: `atc/scheduler/runner.go`
- Modify if imports remain: all five scheduler test files named above.

**Interfaces:**
- Consumes: the concrete constructors and green persisted behavior from Tasks 1-4.
- Produces: a scheduler package with no generated doubles, fake-only interfaces, or interaction assertions.

- [ ] **Step 1: Make production dependencies concrete and delete all generated outputs atomically**

In `scheduler.go`, remove `Algorithm` and use private concrete fields:

```go
type Scheduler struct {
	algorithm    *algorithm.Algorithm
	buildStarter *buildStarter
}
```

Update `NewScheduler` and `Schedule` for those fields. In `buildstarter.go`, remove `BuildStarter` and `BuildPlanner`; change `NewBuildStarter(planner builds.Planner, alg *algorithm.Algorithm) *buildStarter` and the struct fields to those concrete types. Retain the internal `Build` interface: its manual, rerun, and scheduler implementations are production strategies, not substitute collaborators. In `runner.go`, remove the now-unused `BuildScheduler` interface and its directive; keep `ScheduleFunc` as the narrow concurrency seam.

In the same edit, delete the four generated files and remove the now-empty directory. Remove the package-level Counterfeiter `go:generate` line when no scheduler directive remains. Do not compile between interface deletion and generated-file deletion because the generated assertions depend on those names.

- [ ] **Step 2: Verify no scheduler interaction mock remains**

Run:

```bash
if git grep -n -E 'schedulerfakes|FakeAlgorithm|FakeBuildPlanner|FakeBuildStarter|FakeBuildScheduler|type (Algorithm|BuildPlanner|BuildStarter|BuildScheduler) interface|CallCount|ArgsForCall|ReturnsOnCall|Invocations' -- 'atc/scheduler/*.go' 'atc/scheduler/schedulerfakes/*.go'; then false; else test $? -eq 1; fi
```

Expected: no matches. The production `Build` interface is intentionally not in the banned list.

- [ ] **Step 3: Run the full scheduler package and measure it**

Run:

```bash
ginkgo ./atc/scheduler
/usr/bin/time -p ginkgo ./atc/scheduler
go test ./atc/atccmd -run '^$'
```

Expected: PASS with no unexplained material runtime increase over baseline.

- [ ] **Step 4: Commit the completed scheduler migration**

```bash
git add atc/scheduler atc/atccmd/command.go
git add -u atc/scheduler/schedulerfakes
git commit -m "test(scheduler): replace mocks with persisted outcomes"
```
