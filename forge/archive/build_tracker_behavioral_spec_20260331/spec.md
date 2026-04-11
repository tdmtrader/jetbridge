# Build Tracker Behavioral Specification

**Track:** `build_tracker_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's build tracker, plan generation, and engine subsystems. Together these components answer "what happens to my build after it starts?" — the tracker manages in-flight builds, the planner converts job step configurations into execution plans via the visitor pattern, and the engine executes plans by dispatching to step implementations.

### Scope

- Build tracking lifecycle (started build discovery, in-memory build channel, deduplication, drain)
- Plan generation via visitor pattern (14 step types, control flow combinators, error types)
- Engine execution (lock acquisition, state management, abort handling, finish status, retries)
- Step construction (stepper factory dispatching, container/step metadata, decorator wrapping)
- Delegate event emission (Initialize/Start/Finish events for Get/Put/Task/Check/SetPipeline)
- Image fetching (metadata-only path, plan-based fallback, policy checking)

### Out of Scope

- Individual step execution logic (`atc/exec/` step implementations) — covered by exec_step spec
- Check delegation and locking — covered by check_runner spec
- Resource check lifecycle — covered by check_runner spec
- Worker selection and container runtime
- Database schema and migrations

---

## Section 1: Build Tracking Lifecycle (6 requirements)

### BT-01: Started build discovery on initialization

When the tracker's `Run()` is invoked, the system MUST load all started builds from the database via `BuildFactory.GetAllStartedBuilds()` and begin tracking each one.

### BT-02: In-memory build channel consumption

The tracker MUST spawn a goroutine that continuously receives builds from `checkBuildsChan` and tracks each one. This allows dynamically created check builds to be executed.

### BT-03: Build deduplication via sync.Map

When tracking a build, the system MUST use `sync.Map.LoadOrStore()` with a unique key (`"build-{ID}"` for DB builds, `"resource-{ResourceID}"` for in-memory checks) to prevent duplicate concurrent execution of the same build.

### BT-04: Per-build goroutine with panic recovery

Each tracked build MUST run in its own goroutine. If a panic occurs, the system MUST:
- Recover from the panic
- Mark the build as Errored via `build.Finish()`
- Continue tracking other builds

### BT-05: Build running metrics

When a build begins tracking, the system MUST increment `Metrics.BuildsRunning`. When tracking ends, it MUST decrement the gauge.

### BT-06: Drain delegates to engine

When `Drain()` is called on the tracker, it MUST delegate to `engine.Drain()` to signal all in-flight builds to release.

---

## Section 2: Plan Generation — Visitor Pattern (15 requirements)

### PG-01: Get step plan generation

When visiting a GetStep, the planner MUST:
- Look up the resource by name (error with `UnknownResourceError` if not found)
- Find the version from build inputs (error with `VersionNotProvidedError` if not found)
- Generate a GetPlan with resource name, type, source, params, version, and tags
- Include TypeImage with check/get plans for custom resource types

### PG-02: Put step plan generation

When visiting a PutStep, the planner MUST:
- Generate a PutPlan with resource name, type, source, params, inputs, and tags
- Unless `NoGet=true`, wrap the PutPlan in an OnSuccess with a dependent GetPlan that uses `VersionFrom` pointing to the put's plan ID

### PG-03: Task step plan generation

When visiting a TaskStep, the planner MUST generate a TaskPlan with config file path, inline config, params, vars, tags, and privilege flag. If the build was manually triggered, it MUST set `SkipInterval=true` in any image TypeImage.

### PG-04: Run step plan generation

When visiting a RunStep, the planner MUST:
- Look up the prototype by name (error with `UnknownPrototypeError` if not found)
- Merge params with prototype defaults (step params override defaults)
- Generate a RunPlan with type, params, and prototype source

### PG-05: SetPipeline step plan generation

When visiting a SetPipelineStep, the planner MUST generate a SetPipelinePlan with the pipeline name, file path, vars, var_files, and instance_vars.

### PG-06: LoadVar step plan generation

When visiting a LoadVarStep, the planner MUST generate a LoadVarPlan with name, file path, format, and reveal flag.

### PG-07: Do step — sequential composition

When visiting a DoStep, the planner MUST recursively visit each sub-step and collect the resulting plans into a DoPlan (ordered slice).

### PG-08: InParallel step — parallel composition

When visiting an InParallelStep, the planner MUST recursively visit each sub-step and create an InParallelPlan with the collected plans, limit, and fail_fast flag.

### PG-09: Across step — matrix expansion

When visiting an AcrossStep, the planner MUST visit the inner step, marshal the resulting plan to JSON as a SubStepTemplate, and create an AcrossPlan with variable definitions and fail_fast.

### PG-10: Try step wrapping

When visiting a TryStep, the planner MUST visit the inner step and wrap the result in a TryPlan.

### PG-11: Timeout step wrapping

When visiting a TimeoutStep, the planner MUST visit the inner step and wrap the result in a TimeoutPlan with the specified duration.

### PG-12: Retry step expansion

When visiting a RetryStep, the planner MUST visit the inner step N times (where N = Attempts) to create N independent plans, collected into a RetryPlan slice.

### PG-13: Hook step composition (OnSuccess/OnFailure/OnAbort/OnError/Ensure)

When visiting any hook step, the planner MUST visit both the step and the hook, then wrap them in the corresponding plan type (OnSuccessPlan, OnFailurePlan, OnAbortPlan, OnErrorPlan, EnsurePlan).

### PG-14: Nested resource type image resolution

When a resource uses a custom resource type, the planner MUST generate TypeImage containing CheckPlan and GetPlan for the parent type. For nested types (type depends on type), this MUST chain recursively. Privileged types MUST propagate the privileged flag.

### PG-15: Unique plan IDs

Every plan node generated by the planner MUST have a unique PlanID assigned by the PlanFactory.

---

## Section 3: Engine Execution (12 requirements)

### EX-01: Tracking lock acquisition

Before executing a build, the engine MUST acquire a tracking lock (with 1-minute timeout). If the lock cannot be acquired, the build MUST NOT execute.

### EX-02: Build reload and state validation

After acquiring the lock, the engine MUST reload the build from the database. If the build is no longer found, already finished, or not yet active, the engine MUST release the lock and return without executing.

### EX-03: Stepper creation from build plan

The engine MUST create an `exec.Stepper` from the build's plan via `StepperFactory.StepperForBuild()`. If this fails, the engine MUST emit an Error event and finish the build as Errored.

### EX-04: Build variable resolution

The engine MUST resolve build variables (pipeline secrets + var sources) into a RunState before executing the plan. If variable resolution fails, the engine MUST emit an Error event and finish as Errored.

### EX-05: Abort signal monitoring

The engine MUST spawn a goroutine that listens for the build's abort signal. When an abort is received, the context MUST be cancelled, causing the running step to stop.

### EX-06: Successful build finish

When the plan execution completes with `succeeded=true` and no error, the engine MUST call `build.Finish(BuildStatusSucceeded)`.

### EX-07: Failed build finish

When the plan execution completes with `succeeded=false` and no error, the engine MUST call `build.Finish(BuildStatusFailed)`.

### EX-08: Errored build finish

When the plan execution returns a non-nil, non-cancelled, non-retriable error, the engine MUST call `build.Finish(BuildStatusErrored)`.

### EX-09: Aborted build finish

When the plan execution returns `context.Canceled` (or a wrapped version), the engine MUST call `build.Finish(BuildStatusAborted)`.

### EX-10: Retriable error handling for normal builds

When a normal (non-check) build returns a retriable error, the engine MUST NOT call `build.Finish()` — allowing the build to be retried.

### EX-11: Retriable error handling for check builds

When a check build returns a retriable error, the engine MUST call `build.Finish()` (check builds are never retried).

### EX-12: Drain releases without finishing

When the engine's release channel is closed (drain), the engine MUST return immediately from `Run()` without calling `build.Finish()`. This allows graceful shutdown.

---

## Section 4: Step Construction — Stepper Factory (10 requirements)

### SF-01: Plan type dispatch

The stepper factory MUST dispatch on plan type fields to construct the correct step. Supported types: Get, Put, Task, Run, Check, SetPipeline, LoadVar, Do, InParallel, Across, Try, Timeout, Retry, OnSuccess, OnFailure, OnAbort, OnError, Ensure, ArtifactInput, ArtifactOutput.

### SF-02: Schema validation

The stepper factory MUST validate the build schema version. If the schema is wrong (not `exec.v2`), it MUST return an error.

### SF-03: Get step construction with metadata

When constructing a Get step, the factory MUST create container metadata (with step name, attempt info) and step metadata (build ID, team, pipeline, external URL), and wrap the step with LogError.

### SF-04: Put step construction with dependent get

When constructing a Put step, the factory MUST create the put step and its dependent get step (from the OnSuccess wrapping generated by the planner), with correct VersionFrom linkage.

### SF-05: Retry step expansion

When constructing a retry step, the factory MUST create N independent step instances (one per RetryPlan entry) with incrementing attempt numbers in container metadata.

### SF-06: Hook step construction

When constructing hook steps (OnSuccess/OnFailure/OnAbort/OnError/Ensure), the factory MUST recursively build both the step and the hook, then wrap them in the corresponding exec combinator.

### SF-07: Try step construction

When constructing a try step, the factory MUST build the inner step and wrap it with `exec.Try`.

### SF-08: Container metadata generation

For every step, the factory MUST generate ContainerMetadata including: build ID, pipeline info, job name, step name, working directory, attempt number, and container type (Get/Put/Task/Check/Run).

### SF-09: Step metadata generation

For every step, the factory MUST generate StepMetadata including: build ID, team ID, pipeline ID/name, job ID/name, external URL, and expose-build-created-by flag.

### SF-10: Unknown plan returns identity step

When a plan matches no known type, the factory MUST return an `exec.IdentityStep` (no-op).

---

## Section 5: Delegate Event Emission (10 requirements)

### DE-01: Get delegate events

The get delegate MUST emit InitializeGet, StartGet, and FinishGet events with the correct event origin (plan ID).

### DE-02: Put delegate events

The put delegate MUST emit InitializePut, StartPut, and FinishPut events. FinishPut MUST include the created version and metadata.

### DE-03: Task delegate events

The task delegate MUST emit InitializeTask (with TaskConfig shadow), StartTask (with TaskConfig), and FinishTask (with exit status) events.

### DE-04: SetPipeline delegate events

The set pipeline delegate MUST emit SetPipelineChanged events indicating whether the pipeline configuration changed.

### DE-05: Check delegate events

The check delegate MUST emit InitializeCheck events with the plan name.

### DE-06: Sidecar event emission

The task delegate MUST emit a Sidecar event for each sidecar in the task config, with the parent plan ID as origin.

### DE-07: Sidecar log writer

The task delegate MUST provide a writer for each sidecar that produces Log events with the sidecar's plan ID as origin.

### DE-08: Build step delegate initialization/finish

The base build step delegate MUST emit Initialize and Finish events for any step type.

### DE-09: Get delegate resource metadata update

When a get step finishes, the delegate MUST update the pipeline resource's metadata with the fetched version and metadata via `UpdateResourceVersion`.

### DE-10: Put delegate output saving

When a put step finishes, the delegate MUST call `build.SaveOutput()` with the created version, metadata, resource cache, and step/resource name.

---

## Section 6: Image Fetching & Policy (8 requirements)

### IF-01: Policy check before image fetch

Before fetching an image, the delegate MUST check the ActionUseImage policy. If the policy rejects and enforcement is hard, it MUST return an error.

### IF-02: Soft policy enforcement with warnings

When a policy check rejects with non-blocking enforcement, the delegate MUST succeed but log a warning message.

### IF-03: Credential redaction in policy checks

When checking image policy, the delegate MUST redact credential fields from the image source before passing to the policy checker.

### IF-04: Metadata-only image fetch (registry-image)

When resource factories are available and the image type is registry-image, the delegate MUST attempt to resolve the image via cached DB metadata without spawning pods.

### IF-05: Plan-based image fetch fallback

When metadata-only resolution fails (for non-registry types or missing cache), the delegate MUST fall back to running check+get plans to fetch the image.

### IF-06: Image check and get events

When fetching an image, the delegate MUST save ImageCheck and ImageGet events for build log continuity, regardless of whether the metadata-only or plan-based path was used.

### IF-07: Task delegate FetchImage with check+get plans

The task delegate's FetchImage MUST generate check and get plans via FetchImagePlan, save ImageCheck/ImageGet events, and delegate to the base buildStepDelegate.FetchImage.

### IF-08: Docker URL generation for registry-image

When resolving a registry-image type, the delegate MUST produce an ImageURL in `docker://repository@digest` or `docker://repository:tag` format.

---

## Section 7: Planner Error Handling (3 requirements)

### PE-01: Unknown resource error

When the planner encounters a resource name not found in the scheduler resources, it MUST return an `UnknownResourceError` with the resource name.

### PE-02: Unknown prototype error

When the planner encounters a prototype name not found in the prototypes list, it MUST return an `UnknownPrototypeError` with the prototype name.

### PE-03: Version not provided error

When the planner encounters a get step whose resource has no version in the build inputs, it MUST return a `VersionNotProvidedError` with the input name.
