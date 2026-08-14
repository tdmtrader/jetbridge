# Mock-Free Engine and Exec Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the generated core-step factory and handwritten engine/exec interaction spies while preserving builder composition, lifecycle, worker-selection, cache, and image-resolution contracts through real components and observable state.

**Architecture:** Production composition uses the concrete `*CoreStepFactory`. Engine lifecycle edge states use a single `StepperFactoryFunc` plus `stepFunc` closures; exec combinators emit named semantic events instead of exposing generic call-recording APIs. Step runtime tests use the real `worker.Pool`, PostgreSQL worker/cache rows, and `runtimetest` state, while image-resolution tests use an in-process OCI registry over HTTP.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL fixtures, `atc/runtime/runtimetest`, `atc/worker.Pool`, `net/http/httptest`, `go-containerregistry`.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Preserve engine success, false result, cancellation, abort, retryable failure, panic recovery, and blocked/released execution.
- Preserve unsupported schema, unknown-plan identity, metadata/attempt derivation, and nested-retry behavior.
- Before deleting builder call assertions, map dispatch/composition to the corresponding `atc/exec` behavioral suite; add one real builder-to-runtime smoke scenario for metadata propagation.
- `stepFunc` may deterministically produce a lifecycle result, block on a channel, or panic; it must not expose `CallCount`, `ArgsForCall`, `Returns`, `Stub`, or `Invocations` APIs.
- Use real PostgreSQL, `worker.Pool`, runtime state, and OCI HTTP protocol messages for ordinary behavior. Drop injected worker-pool/database failures that exist only because a mock could return them.
- Run database-backed suites serially through `ginkgo`; K3s composition remains a CI-only gate on macOS.
- Do not modify the two untracked review documents.

---

### Task 1: Add a Function Adapter for Engine Lifecycle Scenarios

**Files:**
- Modify: `atc/engine/builder.go:19-70`
- Modify: `atc/engine/engine_test.go`
- Modify: `atc/engine/scripted_step_test.go`

**Interfaces:**
- Consumes: `StepperFactory.StepperForBuild(db.Build) (exec.Stepper, error)`.
- Produces: `type StepperFactoryFunc func(db.Build) (exec.Stepper, error)` with a matching `StepperForBuild` method; `type stepFunc func(context.Context, exec.RunState) (bool, error)` in tests.

- [ ] **Step 1: Convert the suite-level engine fixture to the missing function adapter**

Delete the `enginefakes` import and `countingStepperFactory` type. Change the suite variables to:

```go
	stepperFactory StepperFactory
	step           stepFunc
	builtPlans     chan atc.Plan
```

In the root `BeforeEach`, replace the fake core construction with:

```go
builtPlans = make(chan atc.Plan, 1)
step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
	return true, nil
})
stepperFactory = StepperFactoryFunc(func(build db.Build) (exec.Stepper, error) {
	if build.Schema() != "exec.v2" {
		return nil, errors.New("schema not supported")
	}
	return func(plan atc.Plan) exec.Step {
		builtPlans <- plan
		return step
	}, nil
})
```

Keep the existing `when converting the plan to a step fails` engine context with `prepareRow = func() {}`. The unstarted build has an empty schema, so this narrow adapter returns the same stable unsupported-schema error as the production stepper and the context continues to prove the lock-release/error-event boundary. Without this check the adapter would run the default step and silently invalidate that scenario. The production builder characterization in Task 2 remains the owner of the actual schema validation implementation.

Replace the existing `constructs a step from the build's plan` body with:

```go
waitGroup.Wait()
Expect(<-builtPlans).To(Equal(realBuild.PrivatePlan()))
```

Replace every `stepperFactory.stepperCalls` assertion in the already-finished, deleted-build, and lock-acquisition-failure contexts with:

```go
waitGroup.Wait()
Consistently(builtPlans).ShouldNot(Receive())
```

This observes that no plan was built after the engine goroutine completed; it does not substitute another count.

In nested lifecycle contexts, assign a new `stepFunc` to `step`. For example, the successful default remains:

```go
step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
		return true, nil
	})
```

- [ ] **Step 2: Run the focused test and verify the missing adapter fails to compile**

Run: `ginkgo -ginkgo.focus="constructs a step from the build's plan" ./atc/engine`

Expected: compile failure containing `undefined: engine.StepperFactoryFunc`.

- [ ] **Step 3: Implement the production function adapter**

Add immediately below `StepperFactory` in `atc/engine/builder.go`:

```go
type StepperFactoryFunc func(db.Build) (exec.Stepper, error)

func (f StepperFactoryFunc) StepperForBuild(build db.Build) (exec.Stepper, error) {
	return f(build)
}
```

- [ ] **Step 4: Replace the engine test's generic scripted-step API**

Replace `atc/engine/scripted_step_test.go` with:

```go
package engine_test

import (
	"context"

	"github.com/concourse/concourse/atc/exec"
)

type stepFunc func(context.Context, exec.RunState) (bool, error)

func (f stepFunc) Run(ctx context.Context, state exec.RunState) (bool, error) {
	return f(ctx, state)
}
```

In `engine_test.go`, construct static outcomes with `stepFunc(func(context.Context, exec.RunState) (bool, error) { return ok, err })`. For abort cancellation, send the received context on a buffered channel and assert `Eventually(receivedContext.Done()).Should(BeClosed())`; do not retain a recorded argument slice. For drain/blocking and panic, keep the existing channel and panic bodies inside `stepFunc`.

- [ ] **Step 5: Run the engine lifecycle suite**

Run:

```bash
gofmt -w atc/engine/builder.go atc/engine/engine_test.go atc/engine/scripted_step_test.go
ginkgo -ginkgo.focus='Engine' ./atc/engine
```

Expected: PASS for success, false, cancellation, abort, retryable failure, panic, drain/release, and plan construction.

- [ ] **Step 6: Commit the lifecycle seam**

```bash
git add atc/engine/builder.go atc/engine/engine_test.go atc/engine/scripted_step_test.go
git commit -m "test(engine): model lifecycle with function steps"
```

### Task 2: Replace Builder Dispatch Spies with Real Composition Behavior

**Files:**
- Modify: `atc/engine/builder.go:19-65`
- Modify: `atc/engine/step_factory.go:20-70`
- Rewrite: `atc/engine/builder_test.go`
- Modify: `atc/runtime/runtimetest/worker.go:12-85`
- Delete: `atc/engine/enginefakes/fake_core_step_factory.go`
- Verify production wiring: `atc/atccmd/command.go:1980-2030`

**Interfaces:**
- Consumes: `NewCoreStepFactory(...) *CoreStepFactory`, real `worker.Pool`, and `runtimetest.WorkerContainer`.
- Produces: `NewStepperFactory(coreFactory *CoreStepFactory, ...) StepperFactory`; `WorkerContainer.Metadata db.ContainerMetadata` as observable runtime state; `CoreStepFactory` is a struct rather than an interface.

- [ ] **Step 1: Add runtime-state support for the metadata the production worker receives**

Add this field to `runtimetest.WorkerContainer`:

```go
Metadata db.ContainerMetadata
```

In `FindOrCreateContainer`, persist both inputs before returning:

```go
c.Metadata = metadata
c.Spec = &spec
return c.Container, c.Mounts, nil
```

This extends the existing deterministic runtime model; it does not add configurable behavior or an interaction API.

- [ ] **Step 2: Write one real builder-to-runtime scenario before deleting the fake matrix**

In `builder_test.go`, retain the real `UseEngineDB` build fixture, save a compatible database worker, and map its name to a `runtimetest.Worker` through a local `runtimeWorkerFactory` implementing `worker.Factory`. Pre-seed that worker with `db.NewBuildStepContainerOwner(build.ID(), taskPlan.ID, team.ID())` and a `runtimetest.Container` whose `task` process matches the inline task config and returns exit status zero. Construct the real `worker.Pool`, core factory, and stepper factory. Start the build with the inline task plan carrying `Attempts: []int{2, 1}`, run the returned step through `exec.NewRunState`, and compute the task working directory exactly as production does:

```go
taskNameHash := sha256.Sum256([]byte("some-task"))
expectedWorkingDirectory := filepath.Join("/tmp", "build", fmt.Sprintf("%x", taskNameHash[:4]))
```

Then assert:

```go
Expect(runtimeContainer.Metadata).To(Equal(db.ContainerMetadata{
	Type:                 db.ContainerTypeTask,
	PipelineID:           pipeline.ID(),
	JobID:                job.ID(),
	BuildID:              build.ID(),
	PipelineName:         pipeline.Name(),
	PipelineInstanceVars: `{"branch":"master"}`,
	JobName:              job.Name(),
	BuildName:            build.Name(),
	StepName:             "some-task",
	Attempt:              "2.1",
	WorkingDirectory:     expectedWorkingDirectory,
}))
Expect(runtimeContainer.Spec.TeamName).To(Equal("some-team"))
```

Also retain the existing unsupported-schema and unknown-plan/`exec.IdentityStep{}` specs.

- [ ] **Step 3: Verify the real composition scenario passes before deleting coverage**

Run: `ginkgo -ginkgo.focus='Builder' ./atc/engine`

Expected: PASS with the real core factory, real worker pool, PostgreSQL build, and runtime container.

- [ ] **Step 4: Map and remove redundant interaction assertions**

Use this exact coverage map while deleting fake call-count/argument contexts from `builder_test.go`:

| Removed builder interaction | Behavioral owner that must remain green |
| --- | --- |
| `in_parallel` and legacy `parallel` child construction | `atc/exec/in_parallel_test.go` plus the builder real-composition smoke |
| retry count/order and nested attempts | `atc/exec/retry_step_test.go` plus the `Attempt: "2.1"` runtime assertion |
| `on_abort`, `on_error`, `on_failure`, `on_success`, `ensure` | matching `atc/exec/on_*_test.go` and `atc/exec/ensure_step_test.go` |
| `try` wrapping | `atc/exec/try_step_test.go` |
| get/check/put/task/run/set-pipeline/load-var construction | matching exported step suites in `atc/exec`, plus persisted engine delegate scenarios |
| artifact input/output construction | `atc/exec/artifact_input_step_test.go` and `artifact_output_step_test.go` |

Do not delete the only test for unsupported schema, unknown plan identity, build metadata, container metadata, or attempts.

- [ ] **Step 5: Make the core factory concrete**

Delete the `CoreStepFactory` interface and directive. Change these exact signatures and fields:

```go
func NewStepperFactory(
	coreFactory *CoreStepFactory,
	externalURL string,
	rateLimiter RateLimiter,
	policyChecker policy.Checker,
	dbWorkerFactory db.WorkerFactory,
	lockFactory lock.LockFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resolver imageresolver.Resolver,
) StepperFactory
```

```go
type stepperFactory struct {
	coreFactory           *CoreStepFactory
	externalURL           string
	rateLimiter           RateLimiter
	policyChecker         policy.Checker
	dbWorkerFactory       db.WorkerFactory
	lockFactory           lock.LockFactory
	resourceConfigFactory db.ResourceConfigFactory
	resourceCacheFactory  db.ResourceCacheFactory
	imageResolver         imageresolver.Resolver
}
```

```go
func NewCoreStepFactory(
	pool worker.Pool,
	streamer worker.Streamer,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	defaultCheckTimeout time.Duration,
	defaultGetTimeout time.Duration,
	defaultPutTimeout time.Duration,
	defaultTaskTimeout time.Duration,
	opts ...CoreStepFactoryOption,
) *CoreStepFactory
```

Rename `type coreStepFactory struct` to `type CoreStepFactory struct`, change every method receiver to `*CoreStepFactory`, and change `CoreStepFactoryOption` to `func(*CoreStepFactory)`. Keep every method body unchanged. Confirm the value returned by `engine.NewCoreStepFactory` still passes directly to `engine.NewStepperFactory` in `atc/atccmd/command.go`.

- [ ] **Step 6: Delete the generated fake and verify composition suites**

Delete `atc/engine/enginefakes/fake_core_step_factory.go`, then run:

```bash
set -e
gofmt -w atc/engine/builder.go atc/engine/step_factory.go atc/engine/builder_test.go atc/runtime/runtimetest/worker.go
ginkgo ./atc/engine
ginkgo ./atc/exec
if git grep -n -E 'FakeCoreStepFactory|enginefakes|type CoreStepFactory interface|counterfeiter:generate . CoreStepFactory' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: both suites pass and the search has no matches.

- [ ] **Step 7: Commit the concrete composition**

```bash
git add atc/engine/builder.go atc/engine/step_factory.go atc/engine/builder_test.go atc/runtime/runtimetest/worker.go
git add -u atc/engine/enginefakes
git commit -m "refactor(engine): use the concrete core step factory"
```

### Task 3: Convert Engine and Exec Scripted Steps to Semantic Functions

**Files:**
- Modify: `atc/engine/task_delegate_test.go`
- Modify: `atc/engine/build_step_delegate_test.go`
- Modify: `atc/exec/scripted_step_test.go`
- Modify: `atc/exec/across_step_test.go`
- Modify: `atc/exec/ensure_step_test.go`
- Modify: `atc/exec/in_parallel_test.go`
- Modify: `atc/exec/on_abort_test.go`
- Modify: `atc/exec/on_error_test.go`
- Modify: `atc/exec/on_failure_test.go`
- Modify: `atc/exec/on_success_test.go`
- Modify: `atc/exec/put_step_test.go`
- Modify: `atc/exec/retry_error_step_test.go`
- Modify: `atc/exec/retry_step_test.go`
- Modify: `atc/exec/run_state_test.go`
- Modify: `atc/exec/timeout_step_test.go`
- Modify: `atc/exec/try_step_test.go`
- Modify: `atc/exec/log_error_step_test.go`

**Interfaces:**
- Consumes: `exec.Step.Run(context.Context, exec.RunState) (bool, error)`.
- Produces: one package-local `stepFunc` per package; test-local buffered start/outcome channels named for domain events, with no generic history or call-query methods.

- [ ] **Step 1: Replace both package-local scripted step structs**

Package `engine_test` must reuse the `stepFunc` established in Task 1 of this plan; do not declare it again. Package `exec_test` already has the same adapter in `put_step_test.go`. Move that existing declaration and its `Run` method into `scripted_step_test.go` while replacing `scriptedStep`, then delete the original declaration from `put_step_test.go` so the package has exactly one definition:

```go
type stepFunc func(context.Context, exec.RunState) (bool, error)

func (f stepFunc) Run(ctx context.Context, state exec.RunState) (bool, error) {
	return f(ctx, state)
}
```

Remove every `RunStub`, `RunReturns`, `RunCallCount`, and `RunArgsForCall` method or field.

- [ ] **Step 2: Convert static results without adding a helper API**

Replace `step.RunReturns(true, nil)` with:

```go
step = stepFunc(func(context.Context, exec.RunState) (bool, error) {
	return true, nil
})
```

Use the same literal shape for false and error results. Keep panic and blocking behavior inside the function literal.

- [ ] **Step 3: Convert ordering and cancellation assertions to named events**

For hook/combinator order, use a buffered channel:

```go
events := make(chan string, 2)
step := stepFunc(func(context.Context, exec.RunState) (bool, error) {
	events <- "main-started"
	return false, nil
})
hook := stepFunc(func(context.Context, exec.RunState) (bool, error) {
	events <- "hook-started"
	return true, nil
})
```

After running the exported combinator, drain exactly the expected messages and assert their ordered values. For “must not run,” use `Consistently(events).ShouldNot(Receive())`. For cancellation, send `ctx` on a channel inside the function and assert its `Done()` channel closes. Do not reconstruct a generic calls slice.

- [ ] **Step 4: Remove redundant exact-count assertions**

Where the public result, persisted event, cancellation, or ordered event already proves execution, delete exact `RunCallCount() == 1` assertions. Retain cardinality only when cardinality is the exported behavior (for example retry attempts), and express it as a fixed sequence of named `attempt-started` events.

- [ ] **Step 5: Run the affected suites and structural search**

Run:

```bash
set -e
gofmt -w atc/engine/task_delegate_test.go atc/engine/build_step_delegate_test.go atc/exec/scripted_step_test.go atc/exec/across_step_test.go atc/exec/ensure_step_test.go atc/exec/in_parallel_test.go atc/exec/on_abort_test.go atc/exec/on_error_test.go atc/exec/on_failure_test.go atc/exec/on_success_test.go atc/exec/put_step_test.go atc/exec/retry_error_step_test.go atc/exec/retry_step_test.go atc/exec/run_state_test.go atc/exec/timeout_step_test.go atc/exec/try_step_test.go atc/exec/log_error_step_test.go
ginkgo ./atc/engine
ginkgo ./atc/exec
if git grep -n -E 'RunStub|RunReturns|RunCallCount|RunArgsForCall' -- 'atc/engine/*.go' 'atc/exec/*.go'; then false; else test $? -eq 1; fi
```

Expected: both suites pass and the final search has no matches.

- [ ] **Step 6: Commit the semantic lifecycle fixtures**

```bash
git add atc/engine/task_delegate_test.go atc/engine/build_step_delegate_test.go atc/exec
git commit -m "test(exec): express step behavior as semantic events"
```

### Task 4: Replace `FakeResolver` with an OCI Protocol Fixture

**Files:**
- Create: `atc/imageresolver/imageresolvertesting/registry.go`
- Create: `atc/imageresolver/imageresolvertesting/registry_test.go`
- Delete: `atc/imageresolver/imageresolvertesting/fake_resolver.go`
- Modify: `atc/imageresolver/resolver_test.go`
- Modify: `atc/engine/task_delegate_test.go`
- Modify: `atc/engine/build_step_delegate_test.go`
- Modify: `atc/exec/check_step_test.go`
- Modify: `atc/exec/task_step_test.go`
- Modify: `atc/lidar/scanner_test.go`

**Interfaces:**
- Consumes: `imageresolver.NewResolver(authn.Keychain, ...remote.Option) Resolver` and the OCI Distribution HTTP protocol.
- Produces: `imageresolvertesting.Registry` with `Host() string`, `Push(repository, tag string) (string, error)`, `RequireBasicAuth(username, password string)`, `DrainRequests() []Request`, and `Close()`.

- [ ] **Step 1: Write the protocol fixture contract test**

The test must start the registry, push `repo/image:v1`, resolve it with the real resolver, and assert the returned digest. A second test must require basic auth, verify unauthenticated resolution fails, and verify the configured credentials succeed. `DrainRequests` must expose immutable protocol records containing HTTP method, URL path, and whether Basic Auth was present; it must not expose generic call counts or configurable method returns.

- [ ] **Step 2: Implement the in-process registry**

Wrap `registry.New()` with `httptest.NewServer`. Strip the `http://` scheme from the server URL and build references with `name.ParseReference(serverHost+"/"+repository+":"+tag, name.Insecure)`; this fixture intentionally speaks plain loopback HTTP. Implement `Push` with that reference, `mutate.MediaType(empty.Image, types.OCIManifestSchema1)`, `remote.Write`, and `remote.Head`. Record only HTTP protocol fields in middleware. `RequireBasicAuth` must answer `401` with `WWW-Authenticate` before delegating to the registry handler.

- [ ] **Step 3: Verify the fixture independently**

Run: `go test ./atc/imageresolver/imageresolvertesting -count=1`

Expected: PASS without an external registry or network dependency.

- [ ] **Step 4: Migrate resolver consumers through observable outcomes**

For successful resolution, seed a real image and assert the persisted plan, resource config scope, task/check behavior, or returned digest contains that image's digest. For auth, inspect the registry's HTTP record or require credentials server-side. For cache/pinned/`check_every: never` bypass, clear setup traffic with `DrainRequests`, run the SUT, and assert no OCI `HEAD` request arrived. Preserve likely missing-image and unauthorized failures; replace vague injected `registry down` errors with a closed server or a missing manifest only where the resulting user-visible behavior is distinct.

- [ ] **Step 5: Delete the fake resolver and verify all consumers**

Run:

```bash
set -e
gofmt -w atc/imageresolver/imageresolvertesting/registry.go atc/imageresolver/imageresolvertesting/registry_test.go atc/imageresolver/resolver_test.go atc/engine/task_delegate_test.go atc/engine/build_step_delegate_test.go atc/exec/check_step_test.go atc/exec/task_step_test.go atc/lidar/scanner_test.go
go test ./atc/imageresolver/... -count=1
ginkgo ./atc/engine
ginkgo ./atc/exec
ginkgo ./atc/lidar
if git grep -n -E 'FakeResolver|Resolve(CallCount|ArgsForCall|Returns|Stub)' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: all suites pass and the search has no matches.

- [ ] **Step 6: Commit the protocol migration**

```bash
git add atc/imageresolver atc/engine/task_delegate_test.go atc/engine/build_step_delegate_test.go atc/exec/check_step_test.go atc/exec/task_step_test.go atc/lidar/scanner_test.go
git commit -m "test(imageresolver): use an OCI protocol fixture"
```

### Task 5: Drive Exec Worker Selection Through the Real Pool

**Files:**
- Modify: `atc/exec/exec_suite_test.go`
- Delete contents and remove: `atc/exec/worker_pool_test.go`
- Modify: `atc/exec/artifact_input_step_test.go`
- Modify: `atc/exec/artifact_output_step_test.go`
- Modify: `atc/exec/check_step_test.go`
- Modify: `atc/exec/get_step_test.go`
- Modify: `atc/exec/put_step_test.go`
- Modify: `atc/exec/task_step_test.go`

**Interfaces:**
- Consumes: `worker.NewPool(worker.Factory, worker.DB) worker.Pool`, real DB factories, and `runtimetest.Worker`.
- Produces: `runtimeWorkerFactory` and `saveRuntimeWorkerPool` as deterministic state adapters; no `recordingPool` or `scriptedPool`.

- [ ] **Step 1: Add a deterministic runtime worker adapter to the exec suite**

Add:

```go
type runtimeWorkerFactory map[string]runtime.Worker

func (workers runtimeWorkerFactory) NewWorker(_ lager.Logger, dbWorker db.Worker) runtime.Worker {
	worker, found := workers[dbWorker.Name()]
	Expect(found).To(BeTrue(), "runtime model for database worker %q", dbWorker.Name())
	return worker
}
```

Add this state seed next to the adapter:

```go
type runtimeWorkerSeed struct {
	Model *runtimetest.Worker
	Team  db.Team // nil means a global worker
}
```

Add `saveRuntimeWorkerPool(fixture *execDBFixture, seeds ...runtimeWorkerSeed) worker.Pool`. For each seed, persist `atc.Worker{Name: seed.Model.Name(), Platform: "linux"}` with `seed.Team.SaveWorker(..., 0)` when `Team != nil`, otherwise with `fixture.WorkerFactory.SaveWorker(..., 0)`. Register the model in the name-keyed adapter. Return:

```go
worker.NewPool(runtimeWorkers, worker.DB{
	WorkerFactory: fixture.WorkerFactory,
	TeamFactory:   fixture.TeamFactory,
	VolumeRepo:    db.NewVolumeRepository(fixture.Conn),
})
```

The seed describes persisted worker scope; it exposes no calls or configurable behavior.

- [ ] **Step 2: Replace `recordingPool` in artifact input tests**

Use the real pool directly in both artifact input and artifact output tests. Prove the correct team and handle through the resulting artifact registered in `RunState.ArtifactRepository()` and the matching persisted `worker_artifacts`/volume row; remove `LocateVolumeCallCount`, `LocateVolumeArgsForCall`, and the final `stubWorkerFactory` consumer in `artifact_output_step_test.go`.

- [ ] **Step 3: Replace `scriptedPool` in check/get/put/task tests**

Persist the existing `chosenWorker` runtime model through `saveRuntimeWorkerPool`. `worker.Spec` now contains only `TeamID`, so pass the exact persisted target team whose ID the step places in `worker.Spec.TeamID` on the team-scoped seed, plus a global seed; assert that the team-scoped worker's runtime container runs and the global worker's does not. A mismatched team would legitimately fall back to the global worker and must not be used as the positive fixture. Where a spec asserts zero selections, assert no runtime process started and no container/volume/database state changed. Where a cache volume is needed, create the real resource-cache volume row on the chosen database worker and attach the matching `runtimetest.Volume`.

- [ ] **Step 4: Prune mock-only unlikely failures**

Delete contexts whose only setup is `FindOrSelectWorkerReturns(nil, errors.New("nope"))` or `FindResourceCacheVolumeOnWorkerReturns(..., errors.New(...))`. Retain the likely no-compatible-worker path by persisting no matching worker and asserting `worker.ErrNoWorkers`, and retain artifact/cache-missing paths through actual absent rows or volumes.

- [ ] **Step 5: Remove `worker_pool_test.go` after its last consumer is gone**

First migrate every consumer and run the suite while `worker_pool_test.go` still supplies the legacy types:

```bash
gofmt -w atc/exec/exec_suite_test.go atc/exec/artifact_input_step_test.go atc/exec/artifact_output_step_test.go atc/exec/check_step_test.go atc/exec/get_step_test.go atc/exec/put_step_test.go atc/exec/task_step_test.go
ginkgo ./atc/exec
```

After that passes, delete `atc/exec/worker_pool_test.go`, then run:

```bash
set -e
ginkgo ./atc/exec
if git grep -n -E 'recordingPool|scriptedPool|FindOrSelectWorker(CallCount|ArgsForCall|Returns)|FindResourceCacheVolumeOnWorker(CallCount|ArgsForCall|Returns)|LocateVolume(CallCount|ArgsForCall)' -- 'atc/exec/*.go'; then false; else test $? -eq 1; fi
```

Expected: the suite passes both before and after deletion, and the search has no matches.

- [ ] **Step 6: Re-run engine and measure both suites**

Run:

```bash
ginkgo ./atc/engine
/usr/bin/time -p ginkgo ./atc/engine
/usr/bin/time -p ginkgo ./atc/exec
```

Expected: PASS and no unexplained material runtime regression. Real pool selection should reuse the PostgreSQL fixture each spec already starts; it must not introduce K3s.

- [ ] **Step 7: Commit the worker-pool migration**

```bash
git add atc/exec
git commit -m "test(exec): remove interaction-style fixtures"
```
