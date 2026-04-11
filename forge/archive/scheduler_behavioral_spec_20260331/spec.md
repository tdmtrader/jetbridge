# ATC Scheduler Behavioral Specification

**Track:** `scheduler_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's ATC scheduler subsystem (`atc/scheduler/` and `atc/scheduler/algorithm/`). The scheduler is responsible for the core user-visible behavior of "when does my build start?" — it resolves input versions, creates pending builds when new triggered versions appear, and starts builds when inputs are satisfied.

### Scope

- Job scheduling lifecycle (Runner polling, lock acquisition, concurrency control)
- Input resolution algorithm (individual, group, and pinned resolvers)
- Passed constraint resolution (the group resolver's recursive backtracking)
- Trigger detection and pending build creation
- Build startup (scheduler builds, manual trigger builds, rerun builds)
- Error handling and retry semantics
- Metrics and observability

### Out of Scope

- Build execution engine (`atc/engine/`)
- Build plan generation (`atc/builds/planner.go`) — covered by separate spec
- Database schema and migration details
- Resource check lifecycle (`atc/lidar/`)
- API layer for manual triggers

---

## Section 1: Job Scheduling Lifecycle (8 requirements)

### SL-01: Full job scan on each scheduling tick

When the scheduler Runner's `Run()` is invoked, the system MUST:
- Call `JobFactory.JobsToSchedule()` to retrieve ALL jobs needing scheduling
- Never use targeted job IDs from notification payloads (notifications can be dropped due to non-blocking channel sends with capacity 1)
- Ensure that any notification — even for a different job — picks up all pending work

### SL-02: Concurrent job scheduling with semaphore

When multiple jobs need scheduling, the system MUST:
- Schedule jobs concurrently in separate goroutines
- Limit concurrency via a buffered channel (`guardJobScheduling`) of size `maxJobs`
- Block new job scheduling goroutines when the semaphore is full
- Release the semaphore slot when scheduling completes (success or failure)

### SL-03: Job deduplication via sync.Map

When the same job ID appears multiple times in a scheduling tick, the system MUST:
- Use `sync.Map.LoadOrStore()` to prevent duplicate concurrent scheduling of the same job
- Skip scheduling if the job ID is already present in the running map
- Delete the job ID from the running map only after scheduling completes

### SL-04: Scheduling lock acquisition

Before scheduling a job, the system MUST:
- Call `job.AcquireSchedulingLock()` to obtain a distributed lock
- Skip scheduling silently if the lock is not acquired (another ATC has it)
- Log an error and skip scheduling if lock acquisition fails with an error
- Release the lock after scheduling completes (via defer)

### SL-05: Job reload before scheduling

After acquiring the scheduling lock, the system MUST:
- Call `job.Reload()` to get the latest job state from the database
- Skip scheduling silently if the job is not found (deleted between scan and lock)
- Return an error if reload fails

### SL-06: UpdateLastScheduled on success

When scheduling completes without error and `needsRetry` is false, the system MUST:
- Call `job.UpdateLastScheduled(requestedTime)` with the time captured before reload
- Return an error if UpdateLastScheduled fails

### SL-07: Skip UpdateLastScheduled on retry

When scheduling returns `needsRetry=true`, the system MUST:
- NOT call `job.UpdateLastScheduled()`
- This ensures the job remains eligible for scheduling on the next tick

### SL-08: Panic recovery in scheduling goroutines

When a panic occurs inside a job scheduling goroutine, the system MUST:
- Recover from the panic without crashing the Runner
- Log the panic error
- Continue scheduling other jobs
- Release the semaphore slot and clean up the running map entry

---

## Section 2: Input Resolution Algorithm (9 requirements)

### IR-01: Resolver construction by input type

When `Algorithm.Compute()` is called, the system MUST construct resolvers based on input configuration:
- Inputs with no `passed` constraints and a `PinnedVersion`: use `PinnedResolver`
- Inputs with no `passed` constraints and no pin: use `IndividualResolver`
- Inputs with `passed` constraints: group by shared passed jobs, use `GroupResolver` per group

### IR-02: Individual resolver — latest version

When an `IndividualResolver` resolves an input with `UseEveryVersion=false`, the system MUST:
- Call `VersionsDB.LatestVersionOfResource()` for the input's resource ID
- Return `LatestVersionNotFound` resolution failure if no version exists
- Return the latest version as a candidate on success

### IR-03: Individual resolver — every version

When an `IndividualResolver` resolves an input with `UseEveryVersion=true`, the system MUST:
- Call `VersionsDB.NextEveryVersion()` for the job ID and resource ID
- Return `VersionNotFound` resolution failure if no next version exists
- Set `HasNextEveryVersion` on the candidate if more versions are available
- Return the next unused version as a candidate on success

### IR-04: Pinned resolver

When a `PinnedResolver` resolves an input, the system MUST:
- Call `VersionsDB.FindVersionOfResource()` with the pinned version spec
- Return `PinnedVersionNotFound` resolution failure if the version does not exist
- Return the pinned version as a candidate on success

### IR-05: Resolution failure propagation

When any resolver returns a resolution failure (non-empty string), the system MUST:
- Set `finalResolved=false` for the overall computation
- Store the failure string in `InputResult.ResolveError` for each affected input
- Still process remaining resolvers (do not short-circuit)

### IR-06: First occurrence computation

After all resolvers produce candidates, the system MUST:
- Call `VersionsDB.IsFirstOccurrence()` for each resolved input
- Set `FirstOccurrence=true` on the `AlgorithmInput` if the version has never been used by this job+input combination
- This flag drives trigger detection (Section 4)

### IR-07: HasNextEveryVersion signaling

When computing the final result, the system MUST:
- Set `runAgain=true` if ANY resolver's candidate has `HasNextEveryVersion=true`
- This signals the scheduler to re-queue the job for another scheduling pass

### IR-08: Input mapping persistence

After algorithm computation, the system MUST:
- Call `job.SaveNextInputMapping(inputMapping, resolved)` with the computed results
- Pass `resolved=true` only if ALL resolvers succeeded without resolution failures
- Return an error if persistence fails

### IR-09: RunAgain triggers re-schedule request

When `Algorithm.Compute()` returns `runAgain=true`, the system MUST:
- Call `job.RequestSchedule()` to re-queue the job for another pass
- Return an error if the request fails

---

## Section 3: Passed Constraint Resolution (9 requirements)

### PC-01: Input grouping by shared passed jobs

When constructing group resolvers, the system MUST:
- Group inputs that share any passed job into the same resolver
- Inputs with disjoint passed job sets go into separate group resolvers
- Merge groups when a new input bridges two previously separate groups

### PC-02: Deterministic job ordering

When iterating over passed jobs for an input, the system MUST:
- Sort job IDs numerically in ascending order
- This ensures deterministic resolution across runs

### PC-03: Build output matching

When examining a passed job's build outputs, the system MUST:
- For each output, check if it matches a candidate's resource ID
- Check if the candidate's passed constraints include this job
- If the candidate already has a version, verify the output matches it (otherwise → mismatch)
- Check that the version is not disabled via `VersionIsDisabled()`

### PC-04: Version mismatch detection

When a build's output has a different version than an existing candidate, the system MUST:
- Detect the mismatch and skip to the next build
- Restore all candidates modified during this build's processing
- Continue searching older builds for a compatible version set

### PC-05: Disabled version exclusion

When a resolved version is disabled, the system MUST:
- Treat the output as unrelated (not a mismatch, not a match)
- Skip to the next output
- This prevents disabled versions from being selected for builds

### PC-06: Pinned + passed combination

When an input has both a pinned version and passed constraints, the system MUST:
- Resolve the pinned version via `FindVersionOfResource()` before recursive resolution
- During build output matching, reject outputs whose version differs from the pin
- Return `PinnedVersionNotFound` if the pinned version does not exist

### PC-07: Recursive resolution

When a candidate is found for one input, the system MUST:
- Recursively call `tryResolve()` to verify all other inputs are still satisfiable
- If recursion succeeds, accept the candidate set
- If recursion fails, restore candidates and try the next build

### PC-08: Doom detection (infinite loop prevention)

When recursive resolution fails for a candidate set, the system MUST:
- Record the failed candidate set as "doomed" via `doomCandidates()`
- Before recursing again, check `candidatesAreDoomed()` — if the current candidates exactly match the doomed set (same versions), skip recursion
- This prevents infinite loops when the same candidate set keeps failing

### PC-09: Every version with passed constraints

When resolving `UseEveryVersion` inputs with passed constraints, the system MUST:
- Find the latest build that used the latest version of the resource
- Get the build pipes (passed build IDs) from that build
- Use those as starting cursors for paginated build queries
- Skip jobs without prior usage history to avoid overriding the starting point
- Use `UnusedBuilds()` / `UnusedBuildsVersionConstrained()` for incremental resolution

---

## Section 4: Trigger Detection & Pending Build Creation (5 requirements)

### TD-01: First occurrence trigger creates pending build

When `ensurePendingBuildExists()` finds a build input where:
- `FirstOccurrence=true` AND the corresponding input config has `Trigger=true`

The system MUST:
- Call `job.EnsurePendingBuildExists()` to create a pending build
- Start a linked tracing span with job, input, and version attributes
- Break after the first matching trigger input (only one pending build per pass)

### TD-02: Non-trigger first occurrence sets hasNewInputs only

When a build input has `FirstOccurrence=true` but the input config has `Trigger=false`, the system MUST:
- NOT call `job.EnsurePendingBuildExists()`
- Still track `hasNewInputs=true` for the job state

### TD-03: Unsatisfiable inputs skip build creation

When `job.GetFullNextBuildInputs()` returns `satisfiableInputs=false`, the system MUST:
- Log "next-build-inputs-not-determined"
- Return nil without creating any pending build
- Do NOT update hasNewInputs state

### TD-04: HasNewInputs state tracking

After evaluating all inputs, the system MUST:
- Call `job.SetHasNewInputs(hasNewInputs)` ONLY when the computed value differs from `job.HasNewInputs()`
- This avoids unnecessary database writes when the state hasn't changed

### TD-05: Multiple trigger inputs — first match wins

When multiple inputs have both `FirstOccurrence=true` and `Trigger=true`, the system MUST:
- Create a pending build for the FIRST matching input only
- Break out of the input loop after the first match
- The tracing span links to the first triggering input's context

---

## Section 5: Build Startup (12 requirements)

### BS-01: Build type classification

When constructing builds for scheduling, the system MUST classify each pending build:
- `IsManuallyTriggered()=true` → `manualTriggerBuild` (recomputes algorithm)
- `RerunOf()!=0` → `rerunBuild` (uses rerun inputs)
- Otherwise → `schedulerBuild` (uses pre-computed inputs)

### BS-02: Aborted build finalization

When a pending build has `IsAborted()=true`, the system MUST:
- Call `build.Finish(db.BuildStatusAborted)` to finalize it
- Return `finished=true` and continue to the next pending build
- NOT attempt to schedule, determine inputs, or start the build

### BS-03: Max-in-flight enforcement via ScheduleBuild

When `job.ScheduleBuild(build)` returns `scheduled=false`, the system MUST:
- Stop processing remaining pending builds
- Return `needsRetry=true` so the job is re-queued

### BS-04: Manual trigger readiness check

When a `manualTriggerBuild` is being started, the system MUST:
- Call `build.ResourcesChecked()` to verify all resources have been checked since the build was created
- If resources are not checked: return `needsRetry=true` and stop scheduling
- If resources are checked: proceed to recompute algorithm inputs

### BS-05: Manual trigger recomputes algorithm

When a `manualTriggerBuild` determines inputs, the system MUST:
- Call `Algorithm.Compute()` to get fresh input versions
- Call `job.SaveNextInputMapping()` with the new results
- Call `job.RequestSchedule()` if `hasNextInputs=true`
- Call `build.AdoptInputsAndPipes()` to finalize versions

### BS-06: Scheduler build always ready

When a `schedulerBuild` checks readiness, the system MUST:
- Return `true` immediately (no resource check needed)
- Use `build.AdoptInputsAndPipes()` to adopt pre-computed inputs

### BS-07: Rerun build input adoption

When a `rerunBuild` determines inputs, the system MUST:
- Call `build.AdoptRerunInputsAndPipes()` to adopt the original build's inputs
- NOT call `AdoptInputsAndPipes()` (regular adoption)

### BS-08: Build plan creation

When inputs are determined, the system MUST:
- Fetch the job config via `job.Config()`
- Call `planner.Create()` with the step config, resources, resource types, prototypes, build inputs, and manually-triggered flag
- Use `config.StepConfig()` (which wraps PlanSequence in a DoStep)

### BS-09: Plan failure marks build errored

When `planner.Create()` returns an error, the system MUST:
- Call `build.Finish(db.BuildStatusErrored)` — NOT `ErrorBuild()` (which logs an event for started builds)
- Return `finished=true` and continue to the next pending build

### BS-10: Build start

When the plan is created successfully, the system MUST:
- Call `build.Start(plan)` to persist the plan and mark the build as started
- If `Start()` returns `started=false`: call `build.Finish(db.BuildStatusAborted)` and continue
- If `Start()` returns an error: return the error (stops scheduling)

### BS-11: Rerun build doesn't block other builds

When a `rerunBuild` fails to determine inputs (`inputsDetermined=false`), the system MUST:
- Continue to the next pending build (do NOT stop scheduling)
- This prevents stale reruns from blocking new builds

### BS-12: Regular build failure stops scheduling

When a `schedulerBuild` (non-rerun) fails to determine inputs (`inputsDetermined=false`), the system MUST:
- Stop scheduling remaining builds (break out of the loop)
- Return `needsRetry=false` (inputs are unsatisfiable, not a transient condition)

---

## Section 6: Metrics & Observability (6 requirements)

### MO-01: JobsScheduling gauge

When a job is being scheduled, the system MUST:
- Increment `metric.Metrics.JobsScheduling` at the start of `scheduleJob()`
- Decrement it when `scheduleJob()` returns (via defer)

### MO-02: JobsScheduled counter

When a job finishes scheduling (success or failure), the system MUST:
- Increment `metric.Metrics.JobsScheduled` (via defer, so it fires on every exit)

### MO-03: SchedulingJobDuration emission

After scheduling a job, the system MUST:
- Emit `metric.SchedulingJobDuration` with pipeline name, job name, job ID, and elapsed duration
- Emit even if scheduling failed (the duration is still informative)

### MO-04: BuildsStarted counter for non-check builds

When a build is successfully started and its name is NOT `db.CheckBuildName`, the system MUST:
- Increment `metric.Metrics.BuildsStarted`

### MO-05: CheckBuildsStarted counter for check builds

When a build is successfully started and its name IS `db.CheckBuildName`, the system MUST:
- Increment `metric.Metrics.CheckBuildsStarted`
- NOT increment `metric.Metrics.BuildsStarted` (to avoid breaking existing dashboards)

### MO-06: Tracing spans

The scheduler MUST create tracing spans for:
- `schedule-job` with team, pipeline, and job attributes
- `scheduler.try-start-pending-build` with team, pipeline, job, build_id, and build name
- `build.schedule`, `build.determine-inputs`, `build.create-plan`, `build.start` as child spans
- `Algorithm.Compute` with pipeline and job attributes
- Individual resolver spans (`individualResolver.Resolve`, `pinnedResolver.Resolve`, `groupResolver.Resolve`)
- `job.EnsurePendingBuildExists` as a linked span (linked to triggering input's check span)
