# Spec: Exec Step Behavioral Specification

**Track ID:** `exec_step_behavioral_spec_20260331`
**Type:** refactor

## Overview

The `atc/exec/` subsystem is the core execution engine of Concourse — every pipeline step flows through it. It has 526 test specs across 26 files, but nearly all are implementation-coupled: they mock internal interfaces (Pool, Delegate, ResourceCacheFactory, LockFactory) with counterfeiter fakes and assert on call counts and internal wiring rather than observable behavioral contracts.

This means any refactoring of the delegation chain, worker selection, or internal step composition will break tests even if external behavior is unchanged. This track produces a comprehensive behavioral specification for the exec step subsystem, covering every observable contract from step execution semantics through artifact flow to composite step aggregation.

## Why

- 526 tests that are tightly coupled to implementation details provide a false sense of safety — they break on refactors that don't change behavior
- No specification means no way to distinguish "test broke because behavior changed" from "test broke because internals changed"
- The exec layer is the most refactoring-sensitive part of the system: get/put/task steps, hooks, parallel, retry, timeout all compose through it
- Future work (agent step type, new step primitives) needs a clear contract to implement against
- Audit of existing tests against the spec will identify genuine coverage gaps vs. redundant implementation tests

## Scope

**In scope:**
- Get step execution (resource fetch, caching, skip_download, artifact registration)
- Put step execution (input resolution, resource push, output saving)
- Task step execution (config resolution, input/output handling, image resolution)
- Set-pipeline step execution (config loading, policy checks, pipeline mutation)
- Load-var step execution (file loading, variable scoping, redaction)
- Composite steps: do, in_parallel, across, retry, timeout
- Hook steps: on_success, on_failure, on_error, on_abort, ensure, try
- Artifact repository contract (register, lookup, scoping, image refs)
- RunState result storage contract (StoreResult/Result)
- Step success/failure/error semantics
- Context propagation (cancellation, timeout, abort)
- Delegate lifecycle callback ordering

**Out of scope:**
- Event types and payloads (follow-up track)
- Check step (separate lidar concern)
- Resource script internals (/opt/resource/in, /opt/resource/out)
- Worker/pool implementation (covered by worker behavioral tests)
- Delegate implementation (implementation detail of build engine)
- Credential interpolation internals (vars package)
- Artifact daemon / volume streaming (covered by storage behavioral spec)

---

## Section 1: Step Execution Contract

### SE-01: Step interface
Every step implements `Run(context.Context, RunState) (bool, error)` where:
- `(true, nil)` means the step succeeded
- `(false, nil)` means the step ran but failed (e.g., non-zero exit code)
- `(false, error)` means the step encountered an unexpected error
- `(true, error)` is NOT a valid return combination

### SE-02: Context cancellation
When the context is canceled during step execution, the step MUST:
- Stop execution as soon as practical
- Return `(false, context.Canceled)` or propagate the cancellation error
- NOT leave resources in an inconsistent state

### SE-03: Delegate lifecycle ordering
For leaf steps (get, put, task, set_pipeline, load_var), the delegate callback order MUST be:
1. `Initializing()` — always called first
2. `BeforeSelectWorker()` — before worker selection (get, put, task only)
3. `SelectedWorker()` — after worker selected (get, put, task only)
4. `Starting()` — before process execution begins
5. One of: `Finished()` or `Errored()` — never both, always exactly one

### SE-04: Delegate callback invariant
A step MUST call exactly one terminal callback: either `Finished()` or `Errored()`. If the step returns an error, it SHOULD have called `Errored()`. If the step returns success or failure without error, it MUST have called `Finished()`.

---

## Section 2: Get Step

### GS-01: Successful resource fetch
When a get step executes successfully (exit status 0), it MUST:
- Register the fetched volume as an artifact in the repository under `plan.Name`
- Store a `GetResult` (containing the ResourceCache) in RunState results under the plan ID
- Call `UpdateResourceVersion()` on the delegate with the version result
- Call `Finished()` with exit status 0 and the version result
- Return `(true, nil)`

### GS-02: Failed resource fetch
When a get step's resource script returns a non-zero exit status, the step MUST:
- NOT register any artifact in the repository
- Call `Finished()` with the non-zero exit status
- Return `(false, nil)`

### GS-03: Resource caching — lock acquisition
The get step MUST acquire a per-(resourceCache, workerName) lock before checking the cache or running the resource script. If the lock is not available, the step MUST:
- Call `WaitingForWorker()` on the delegate
- Retry lock acquisition at 5-second intervals
- Eventually acquire the lock and proceed

### GS-04: Resource caching — cache hit
When the worker has a cached volume for the requested resource version, the get step MUST:
- Use the cached volume (no resource script execution)
- Register the cached volume as an artifact with `fromCache=true`
- Store the GetResult in RunState
- Return `(true, nil)`

### GS-05: Resource caching — cache miss
When no cached volume exists, the get step MUST:
- Execute the resource's get script in a container
- On success, initialize the volume as a ResourceCache
- Register the volume as an artifact with `fromCache=false`

### GS-06: Skip download optimization
When `plan.SkipDownload` is true, or the resource type is `registry-image` (without `fetch_artifact`), the get step MUST:
- NOT create a container or execute the resource script
- Create a ResourceCache entry directly
- Register the image ref URL in the artifact repository (if available)
- Store the GetResult in RunState
- Return `(true, nil)`

### GS-07: Version resolution — static
When `plan.Version` is set directly, the get step MUST use that version for the resource fetch.

### GS-08: Version resolution — dynamic (VersionFrom)
When `plan.VersionFrom` references another plan ID, the get step MUST:
- Look up the result from RunState using the referenced plan ID
- Extract the version from the stored result
- Use that version for the resource fetch
- Return an error if the referenced result is not found

### GS-09: Source and params interpolation
The get step MUST interpolate `((var))` references in source and params using the RunState's variable store before passing them to the resource script.

### GS-10: Timeout handling
When a get step's context deadline is exceeded, the step MUST:
- Log a timeout message
- Return `(false, nil)` — timeout is a failure, not an error

---

## Section 3: Put Step

### PS-01: Successful resource push
When a put step executes successfully (exit status 0), the step MUST:
- Call `SaveOutput()` on the delegate (if `plan.Resource` is not empty)
- Store the version result in RunState under the plan ID
- Call `Finished()` with exit status 0 and the version result
- Return `(true, nil)`

### PS-02: Failed resource push
When a put step's resource script returns a non-zero exit status, the step MUST:
- NOT call `SaveOutput()`
- Call `Finished()` with the non-zero exit status
- Return `(false, nil)`

### PS-03: Input resolution — all
When `plan.Inputs` is nil or set to "all", the put step MUST attach ALL artifacts from the repository as inputs to the container.

### PS-04: Input resolution — detect
When `plan.Inputs` is "detect", the put step MUST:
- Parse the params to detect which artifacts are referenced
- Attach only the detected artifacts as inputs

### PS-05: Input resolution — specific
When `plan.Inputs` is a list of named inputs, the put step MUST attach only those named artifacts from the repository.

### PS-06: Input resolution failure
If any specified input artifact is not found in the repository, the put step MUST return an error.

### PS-07: Timeout handling
When a put step's context deadline is exceeded, the step MUST:
- Log a timeout message
- Return `(false, nil)` — timeout is a failure, not an error

---

## Section 4: Task Step

### TS-01: Successful task execution
When a task step's process exits with status 0, the step MUST:
- Register each named output as an artifact in the repository
- If no outputs are defined, register the working directory as the artifact under `plan.Name`
- Call `Finished()` with exit status 0
- Return `(true, nil)`

### TS-02: Failed task execution
When a task step's process exits with a non-zero status, the step MUST:
- NOT register any artifacts in the repository
- Call `Finished()` with the non-zero exit status
- Return `(false, nil)`

### TS-03: Config resolution — embedded
When `plan.Config` is set directly, the task step MUST use that config after applying overrides (params, limits, image).

### TS-04: Config resolution — external (ConfigPath)
When `plan.ConfigPath` is set, the task step MUST:
- Stream the config file from the artifact referenced by the path
- Parse the YAML config
- Apply overrides
- Return an error if the file cannot be found or parsed

### TS-05: Config validation
The task step MUST validate the resolved config. If validation fails (missing run command, invalid config), the step MUST return an error.

### TS-06: Missing inputs
If the task config declares inputs that are not available in the artifact repository, the step MUST return a `MissingInputsError` listing all missing inputs.

### TS-07: Image resolution — artifact reference
When `plan.ImageArtifactName` is set, the task step MUST resolve the image from the artifact repository.

### TS-08: Image resolution — image_resource
When the task config specifies `image_resource`, the task step MUST fetch the image via the delegate's `FetchImage()`.

### TS-09: Default resource limits
If the task config does not specify CPU or memory limits, the task step MUST apply default limits from the system configuration.

### TS-10: Sidecar image resolution
When sidecars are configured with `image_artifact`, the task step MUST resolve each sidecar's image from the artifact repository. If resolution fails, the step SHOULD log a warning but continue (best-effort).

### TS-11: Timeout handling
When a task step's context deadline is exceeded, the step MUST:
- Return `(false, nil)` — timeout is a failure, not an error

### TS-12: Environment variable injection
The task step MUST inject build metadata as environment variables:
- `BUILD_ID`, `BUILD_NAME`, `BUILD_JOB_NAME`, `BUILD_PIPELINE_NAME`, `BUILD_PIPELINE_INSTANCE_VARS`
- `BUILD_TEAM_NAME`, `BUILD_CREATED_BY`, `ATC_EXTERNAL_URL`

---

## Section 5: Set-Pipeline Step

### SP-01: Pipeline configuration
When set_pipeline runs, it MUST:
- Load the pipeline config from the specified file artifact
- Interpolate variables from instance vars and var files
- Validate the config against the set-pipeline policy
- Set or update the pipeline in the database
- Call `SetPipelineChanged()` with whether the pipeline was actually modified
- Return `(true, nil)` on success

### SP-02: Self-targeting
When `plan.Name` is "self", the step MUST update the pipeline that owns the current build (not create a new pipeline named "self").

### SP-03: Policy check failure
When `CheckRunSetPipelinePolicy()` returns an error, the step MUST:
- Call `Errored()` with the policy error
- Return `(false, error)`

### SP-04: Pipeline not found
When the config file artifact is not found in the repository, the step MUST return an error.

---

## Section 6: Load-Var Step

### LV-01: File loading
When load_var runs, it MUST:
- Stream the specified file from the artifact repository
- Parse the content based on the format (raw, json, yaml, trim)
- Store the value as a local variable in RunState
- Return `(true, nil)`

### LV-02: Variable scoping
The loaded variable MUST be available to subsequent steps in the same scope and child scopes, but NOT to sibling or parent scopes.

### LV-03: Redaction
When `plan.Reveal` is false (default), the loaded variable MUST be marked as sensitive/redacted so it does not appear in build logs.

### LV-04: Format handling
- `raw`: Value is the file contents as a string
- `json`: Value is parsed JSON
- `yaml`: Value is parsed YAML
- `trim`: Value is the file contents with leading/trailing whitespace removed

### LV-05: File not found
When the specified file does not exist in the artifact, the step MUST return an error.

---

## Section 7: Composite Steps — Sequential and Parallel

### DO-01: Do step — sequential execution
The do step MUST execute its child steps in order. If any step fails or errors, execution stops and the do step returns that result.

### DO-02: Do step — success propagation
The do step returns `(true, nil)` only if ALL child steps return `(true, nil)`.

### IP-01: In-parallel — concurrent execution
The in_parallel step MUST execute child steps concurrently, up to the configured `limit` (0 = unlimited).

### IP-02: In-parallel — limit enforcement
When `limit` is set to N, at most N child steps may execute concurrently. The step MUST use a semaphore pattern to enforce this.

### IP-03: In-parallel — fail fast
When `fail_fast` is true, the in_parallel step MUST cancel remaining steps as soon as one fails or errors. `context.Canceled` errors from canceled steps MUST be ignored.

### IP-04: In-parallel — success aggregation
The in_parallel step returns `(true, nil)` only if ALL child steps return `(true, nil)`. If any step fails, the result is `(false, nil)`. If any step errors (and it's not a canceled error from fail-fast), the error propagates.

### AC-01: Across — dynamic parallelism
The across step MUST:
- Evaluate the cartesian product of all variable value lists
- Create a substep for each combination
- Execute substeps with parallelism governed by `max_in_flight` per variable level

### AC-02: Across — variable scoping
Each substep combination MUST execute in a local scope with the across variables set to their combination values. Variables MUST NOT leak between substep executions.

### AC-03: Across — fail fast
When `fail_fast` is true on any variable level, the across step MUST cancel remaining combinations in that level when one fails.

---

## Section 8: Composite Steps — Hooks and Control Flow

### RT-01: Retry — sequential attempts
The retry step MUST execute its attempts in order, stopping at the first successful attempt. It returns the result of the last attempted step.

### RT-02: Retry — error handling
If an attempt errors (not just fails), the retry step MUST continue to the next attempt. Only `context.Canceled` is propagated immediately.

### RT-03: Retry — all attempts exhausted
If all attempts fail or error, the retry step returns the result of the final attempt.

### TO-01: Timeout — deadline enforcement
The timeout step MUST create a context with the specified timeout duration. If the deadline is exceeded:
- Return `(false, nil)` — timeout is a failure, not an error
- The `context.DeadlineExceeded` error is NOT propagated

### TO-02: Timeout — normal completion
If the inner step completes before the deadline, the timeout step returns the inner step's result unchanged.

### HS-01: OnSuccess — hook fires on success
The on_success step MUST:
- Run the primary step
- If primary returns `(true, nil)`: run the hook step and return its result
- If primary returns `(false, nil)`: return `(false, nil)` without running the hook
- If primary returns error: return the error without running the hook

### HF-01: OnFailure — hook fires on failure
The on_failure step MUST:
- Run the primary step
- If primary returns `(false, nil)`: run the hook step
  - The hook's success/failure does NOT change the result — always returns `(false, nil)`
  - Hook errors DO propagate
- If primary returns `(true, nil)`: return `(true, nil)` without running the hook
- If primary returns error: return the error without running the hook

### HE-01: OnError — hook fires on error
The on_error step MUST:
- Run the primary step
- If primary returns error: run the hook step with a fresh context (not the canceled one)
  - Both errors are aggregated (multierror)
  - Returns primary success value with aggregated error
- If primary returns without error: return primary result without running the hook

### HA-01: OnAbort — hook fires on cancellation
The on_abort step MUST:
- Run the primary step
- If the context was canceled (`context.Canceled`): run the hook step with a fresh context
- If the context was NOT canceled: return primary result without running the hook

### EN-01: Ensure — hook always fires
The ensure step MUST:
- Run the primary step
- ALWAYS run the hook step, regardless of primary result
- If primary context was canceled: run hook with fresh context
- Aggregate all errors via multierror
- Return success only if BOTH primary and hook succeeded: `success = (primaryOk AND hookOk)`

### TR-01: Try — swallow failures
The try step MUST:
- Run the wrapped step
- If step succeeds: return `(true, nil)`
- If step fails: return `(true, nil)` — failures are swallowed
- If step errors: return `(true, nil)` — errors are swallowed
- Exception: `context.Canceled` MUST be propagated (not swallowed)

---

## Section 9: Artifact Repository Contract

### AR-01: Artifact registration
`RegisterArtifact(name, artifact, fromCache)` MUST store the artifact so that subsequent `ArtifactFor(name)` calls return it with the correct `fromCache` flag.

### AR-02: Artifact lookup — current scope
`ArtifactFor(name)` MUST check the current scope first. If found, return `(artifact, fromCache, true)`.

### AR-03: Artifact lookup — parent scope
If the artifact is not in the current scope, `ArtifactFor(name)` MUST check the parent scope recursively. This enables artifact inheritance across step composition boundaries.

### AR-04: Local scope isolation
`NewLocalScope()` MUST create a child repository where:
- Artifacts registered in the child are NOT visible in the parent
- Artifacts registered in the parent ARE visible in the child
- The child has its own independent artifact map

### AR-05: Image ref registration
`RegisterImageRef(name, url)` MUST store the URL so that `ImageRefFor(name)` returns it. Image refs follow the same scoping rules as artifacts.

### AR-06: AsMap enumeration
`AsMap()` MUST return all artifacts from the current scope AND all ancestor scopes, with child entries taking precedence over parent entries for the same name.

---

## Section 10: RunState Result Contract

### RS-01: Result storage
`StoreResult(planID, value)` MUST store the value so that `Result(planID, target)` can retrieve it. The storage MUST be thread-safe.

### RS-02: Result retrieval
`Result(planID, target)` MUST:
- Return `true` if a result exists for the plan ID, populating `target` via reflection
- Return `false` if no result exists

### RS-03: Cross-step result access
Results stored by one step MUST be accessible to any subsequent step in the same RunState scope. This enables version passing (get step stores version, put step reads it via VersionFrom).

### RS-04: Local variable addition
`AddLocalVar(name, value, redact)` MUST make the variable available to the current scope and child scopes. If `redact` is true, the variable's value MUST be filtered from build output.

---

## Acceptance Criteria

- [ ] Every requirement (SE-01 through RS-04) has at least one corresponding test case identified or written
- [ ] All test cases pass with `ginkgo ./atc/exec/...`
- [ ] Existing test coverage is mapped to requirements (coverage matrix)
- [ ] Gaps are identified and prioritized
- [ ] Composite step aggregation rules (hooks, parallel, retry) are fully validated
- [ ] No regressions in existing test suites

## Out of Scope

- Writing new test code (separate implementation track after audit)
- Event types and payloads
- Check step behavioral spec
- Resource script internals
- Worker/pool behavioral spec
- Delegate implementation details
- Credential interpolation internals
