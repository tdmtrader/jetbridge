# Coverage Matrix: Exec Step Behavioral Specification

**Generated:** 2026-03-31
**Audited against:** `atc/exec/` and `atc/exec/build/` test files
**Legend:** ✅ Covered | ⚠️ Partial | ❌ Missing | 🔍 Implicit (no dedicated test, inferred from structure)

---

## Section 1: Step Execution Contract (SE-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| SE-01 | `(true,nil)` / `(false,nil)` / `(false,err)` semantics | ✅ | All step tests | Every step test verifies (stepOk, stepErr) combinations |
| SE-02 | Context cancellation → stop + return error | ✅ | `retry_step_test.go`, `ensure_step_test.go`, `timeout_step_test.go` | Cancellation tested in retry (propagates), ensure (fresh ctx for hook), timeout (forwards ctx) |
| SE-03 | Delegate callback ORDER: Initializing → BeforeSelectWorker → SelectedWorker → Starting → Finished/Errored | ⚠️ | `get_step_test.go`, `put_step_test.go`, `task_step_test.go` | Individual callbacks tested in isolation. **No test verifies the ordering** of all callbacks in sequence |
| SE-04 | Exactly one terminal callback (Finished XOR Errored) | ⚠️ | `get_step_test.go` | Success cases verify `FinishedCallCount()==1`. Error path verifies `ErroredCallCount()==1`. But **no single test verifies both are never both called** |

**Gaps:** SE-03 (callback ordering), SE-04 (mutual exclusion of Finished/Errored)

---

## Section 2: Get Step (GS-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| GS-01 | Successful fetch: artifact registered, GetResult stored, UpdateResourceVersion called, Finished(0), (true,nil) | ✅ | `get_step_test.go` — "when the script succeeds" | All 4 post-conditions verified |
| GS-02 | Failed fetch (non-0 exit): no artifact, Finished(non-0), no UpdateResourceVersion, (false,nil) | ✅ | `get_step_test.go` — "when get script fails" | Checks all negative conditions |
| GS-03 | Lock retry at 5s intervals; WaitingForWorker callback when waiting | ⚠️ | `get_step_test.go` — "when lock isn't initially acquired" | Lock retry tested (logs "waiting to acquire resource lock"). **WaitingForWorker callback NOT verified** |
| GS-04 | Cache hit: use cached volume, artifact with `fromCache=true`, return (true,nil) | ⚠️ | `get_step_test.go` — "when cache is present on selected worker" | Tests succeed + log message. **`fromCache=true` on `RegisterArtifact` NOT verified** |
| GS-05 | Cache miss: run script, init cache, artifact with `fromCache=false` | ✅ | `get_step_test.go` — "when cache is missing" | `ResourceCacheInitialized` checked on volume |
| GS-06 | Skip download (SkipDownload or registry-image without fetch_artifact): no container, GetResult stored, image ref registered | ✅ | `get_step_test.go` — "skip_download" + "registry-image short-circuit" | 8+ specs covering both modes and edge cases |
| GS-07 | Static version from plan | 🔍 | `get_step_test.go` (BeforeEach sets `Version`) | Used in all default tests but not explicitly named as a contract |
| GS-08 | Dynamic version (VersionFrom): look up from RunState; error if missing | ✅ | `get_step_test.go` — "when using dynamic version source" | Both found and not-found cases |
| GS-09 | Source/params interpolation via RunState variables | ✅ | `get_step_test.go` — "constructs resource cache correctly" | Checks `super-secret-source` interpolated |
| GS-10 | Timeout → Errored() called, (false,nil) returned | ✅ | `get_step_test.go` — "when plan specifies timeout" | Both stepOk=false and Errored call verified |

**Gaps:** GS-03 (WaitingForWorker callback), GS-04 (fromCache=true not asserted)

---

## Section 3: Put Step (PS-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| PS-01 | Successful push: SaveOutput, StoreResult(version), Finished(0), (true,nil) | ✅ | `put_step_test.go` — "when script succeeds" | All post-conditions verified |
| PS-02 | Failed push: no SaveOutput, Finished(non-0), (false,nil) | ✅ | `put_step_test.go` — "when running exits unsuccessfully" | |
| PS-03 | Input resolution — all | ✅ | `put_step_test.go` — "when inputs are specified with 'all' keyword" | All 3 artifacts attached |
| PS-04 | Input resolution — detect (from params) | ✅ | `put_step_test.go` — "when the inputs are detected" | Multiple sub-cases: strings, maps, slices, `.`/`..` paths |
| PS-05 | Input resolution — specific list | ✅ | `put_step_test.go` — "when only some inputs are specified" | |
| PS-06 | Input resolution failure (specified artifact not found) | ❌ | **No test exists** | No test verifies the step errors when a named input artifact is not in the repository |
| PS-07 | Timeout → Errored() called, (false,nil) | ✅ | `put_step_test.go` — "when plan specifies timeout" | |

**Gaps:** PS-06 (missing input error case)

---

## Section 4: Task Step (TS-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| TS-01 | Successful execution: named outputs registered, Finished(0), (true,nil) | ✅ | `task_step_test.go` — "configuration specifies paths for outputs" | Output volumes mapped to repo |
| TS-02 | Failed execution (non-0): no artifact registration, Finished(non-0), (false,nil) | ✅ | `task_step_test.go` — exit nonzero context | |
| TS-03 | Config resolution — embedded | ✅ | `task_step_test.go` — "when plan has a config" (main context) | |
| TS-04 | Config resolution — external (ConfigPath, streamed) | ✅ | `task_step_test.go` (fakeStreamer used; separate ConfigPath contexts exist) | |
| TS-05 | Config validation (missing run command → error) | ✅ | `task_config_source_test.go` | Config validation is separately tested |
| TS-06 | Missing required inputs → MissingInputsError | ✅ | `task_step_test.go` — "when any of the inputs are missing" | Lists missing input names |
| TS-07 | Image from artifact reference | ✅ | `task_step_test.go` (image_artifact reference tested) | |
| TS-08 | Image from image_resource (FetchImage) | ✅ | `task_step_test.go` — custom resource type contexts use FetchImage | |
| TS-09 | Default CPU/memory limits applied when not set | ✅ | `task_step_test.go` — "uses correct container limits" | Both set and override cases covered |
| TS-10 | Sidecar image resolution (best-effort, log warning on failure) | ⚠️ | `task_step_test.go` — sidecar image resolution tested, but best-effort failure path unclear | |
| TS-11 | Timeout → (false,nil), Errored called | ✅ | `task_step_test.go` — "when timeout is configured" | |
| TS-12 | Environment variable injection (BUILD_*, ATC_EXTERNAL_URL) | ✅ | `task_step_test.go` — "Task env includes atc external url" | |

**Gaps:** TS-10 (sidecar failure best-effort path not verified)

---

## Section 5: Set-Pipeline Step (SP-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| SP-01 | Load config, interpolate vars, validate, set pipeline, SetPipelineChanged, (true,nil) | ✅ | `set_pipeline_step_test.go` | Comprehensive coverage including var interpolation |
| SP-02 | Self-targeting ("self" → own pipeline) | ✅ | `set_pipeline_step_test.go` — "self" pipeline name context | |
| SP-03 | Policy check failure → Errored, (false,error) | ✅ | `set_pipeline_step_test.go` — policy check contexts | |
| SP-04 | Config file not found → error | ✅ | `set_pipeline_step_test.go` — file-not-found contexts | |

**Gaps:** None — set_pipeline is well covered

---

## Section 6: Load-Var Step (LV-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| LV-01 | File loading, format parsing, AddLocalVar called, (true,nil) | ✅ | `load_var_step_test.go` | Multiple format contexts |
| LV-02 | Variable scoping (AddLocalVar, available to current and child scopes) | ✅ | `load_var_step_test.go` — `AddLocalVar` call verified | |
| LV-03 | Redaction when Reveal=false | ✅ | `load_var_step_test.go` — `expectLocalVarAdded` checks redact flag | |
| LV-04 | Format handling: raw, json, yaml, trim | ✅ | `load_var_step_test.go` — explicit format contexts for each | |
| LV-05 | File not found → error | ✅ | `load_var_step_test.go` — file not found returns error | |

**Gaps:** None — load_var is well covered

---

## Section 7: Composite Steps — Sequential and Parallel (DO-*, IP-*, AC-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| DO-01 | Do step: sequential execution, stop on first failure | ⚠️ | **No `do_step_test.go` exists** | `do` is a planner-level transform (DoPlan → sequential atc.Plans); it has no exec-layer step type. Sequential execution is tested via `in_parallel_test.go` with limit=1. The DO requirement should reference the planner layer instead |
| DO-02 | Do step: success only if ALL steps succeed | ⚠️ | Same as above | |
| IP-01 | in_parallel: concurrent execution | ✅ | `in_parallel_test.go` — "happens concurrently" | Uses WaitGroup to prove actual concurrency |
| IP-02 | Limit enforcement (semaphore) | ✅ | `in_parallel_test.go` — "when parallel limit is 1" + "happens sequentially" | |
| IP-03 | Fail fast: cancel remaining, ignore context.Canceled from siblings | ✅ | `in_parallel_test.go` — "it cancels remaining steps" | |
| IP-04 | Success aggregation: all must succeed | ✅ | `in_parallel_test.go` — success/fail/error combination specs | |
| AC-01 | Across: cartesian product of var values | ✅ | `across_step_test.go` — "correctly computes combinations" | |
| AC-02 | Across: variable scoping per combination | ✅ | `across_step_test.go` — "initializes the step", variables set per substep | |
| AC-03 | Across: fail fast per var level | ✅ | `across_step_test.go` — "stops running steps after failure" | |

**Gaps:** DO-01/DO-02 — the spec incorrectly characterizes `do` as an exec-layer step. It's a planner-level transform. The requirements should be rewritten to reference `atc/builds/planner.go`.

---

## Section 8: Hook and Control Flow Steps (RT-*, TO-*, HS-*, HF-*, HE-*, HA-*, EN-*, TR-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| RT-01 | Retry: stop at first success | ✅ | `retry_step_test.go` — "attempt 1 succeeds" / "attempt 1 fails, attempt 2 succeeds" | |
| RT-02 | Retry: continue on errors AND on DeadlineExceeded | ✅ | `retry_step_test.go` — "attempt 1 errors, attempt 2 succeeds" + "attempt 2 times out, attempt 3 succeeds" | **Spec should mention DeadlineExceeded is also retried** |
| RT-03 | Retry: all exhausted returns last result | ✅ | `retry_step_test.go` — "all fail" + "all fail, last errors" | |
| TO-01 | Timeout: DeadlineExceeded → (false,nil), error NOT propagated | ✅ | `timeout_step_test.go` — "when step exceeds timeout" | |
| TO-02 | Timeout: normal completion passes through | ✅ | `timeout_step_test.go` — "when step is successful/fails" | |
| HS-01 | OnSuccess: hook on (true,nil); skip on (false,nil) or error | ✅ | `on_success_test.go` — all 3 primary outcomes tested | |
| HF-01 | OnFailure: hook on (false,nil); skip on (true,nil) or error; result always false | ✅ | `on_failure_test.go` — all cases + "hook succeeds but result is still false" | |
| HE-01 | OnError: hook on error; skip on fail or success | ✅ | `on_error_test.go` — all cases | |
| **HE-02** | **[MISSING FROM SPEC]** OnError: Retriable errors do NOT trigger the hook | ✅ | `on_error_test.go` — "when step error is retriable, does not run hook" | **Spec gap: this behavior is tested but not specified** |
| HA-01 | OnAbort: hook only on context.Canceled; not on fail/error | ✅ | `on_abort_test.go` — all cases | |
| EN-01 | Ensure: always runs hook; fresh ctx if canceled; success = primary AND hook | ✅ | `ensure_step_test.go` — all cases including cancel propagation | |
| TR-01 | Try: swallow fail and errors; propagate context.Canceled | ✅ | `try_step_test.go` — fail, interrupted, other-error cases | |

**Gaps:** HE-02 not in spec (behavior tested but unspecified)

---

## Section 9: Artifact Repository Contract (AR-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| AR-01 | RegisterArtifact/ArtifactFor roundtrip with fromCache flag | ✅ | `build/repository_test.go` — "yields artifact by name" | |
| AR-02 | Thread safety (RWMutex) | ⚠️ | No explicit concurrency test in repository_test.go | `sync.Map` provides safety but no race-condition test |
| AR-03 | Lookup falls through to parent scope | ✅ | `build/repository_test.go` — "contains same artifacts as parent" | |
| AR-04 | Local scope isolation: child doesn't pollute parent | ✅ | `build/repository_test.go` — "is present in child but not parent" | |
| AR-05 | Image ref registration and lookup | ✅ | `build/repository_test.go` — "stores and retrieves image ref" + parent/child scoping | |
| AR-06 | AsMap merges all ancestor scopes | ✅ | `build/repository_test.go` — "correctly merges all ancestors in AsMap" | |

**Gaps:** AR-02 (no concurrent access test)

---

## Section 10: RunState Result Contract (RS-*)

| ID | Requirement | Status | Test File / Evidence | Notes |
|----|-------------|--------|---------------------|-------|
| RS-01 | StoreResult/Result roundtrip; thread-safe | ✅ | `run_state_test.go` — "Result" describe block | Type-matching, absent key, different-id all tested |
| RS-02 | Result returns false when type mismatch | ✅ | `run_state_test.go` — "with different type" | |
| RS-03 | Results accessible across parent/child scopes | ✅ | `run_state_test.go` — "results set in parent accessible in child" + vice versa | |
| RS-04 | AddLocalVar with redaction | ✅ | `run_state_test.go` — "AddLocalVar redact" section | Verified via TrackedVarsMap |

**Gaps:** None

---

## Summary

| Section | Requirements | ✅ Covered | ⚠️ Partial | ❌ Missing | Key Gaps |
|---------|-------------|------------|------------|------------|----------|
| SE (Step Contract) | 4 | 1 | 3 | 0 | Callback ordering, Finished XOR Errored |
| GS (Get Step) | 10 | 8 | 2 | 0 | WaitingForWorker, fromCache=true |
| PS (Put Step) | 7 | 6 | 0 | 1 | Named input not found |
| TS (Task Step) | 12 | 11 | 1 | 0 | Sidecar failure path |
| SP (Set Pipeline) | 4 | 4 | 0 | 0 | None |
| LV (Load Var) | 5 | 5 | 0 | 0 | None |
| DO (Do Step) | 2 | 0 | 2 | 0 | Spec incorrectly targets exec layer; do is planner-level |
| IP (In Parallel) | 4 | 4 | 0 | 0 | None |
| AC (Across) | 3 | 3 | 0 | 0 | None |
| RT (Retry) | 3 | 3 | 0 | 0 | None |
| TO (Timeout) | 2 | 2 | 0 | 0 | None |
| Hooks (HS/HF/HE/HA/EN/TR) | 6 | 6 | 0 | 0 | HE-02 missing from spec |
| AR (Artifact Repo) | 6 | 5 | 1 | 0 | No concurrency test |
| RS (RunState) | 4 | 4 | 0 | 0 | None |
| **TOTAL** | **72** | **62 (86%)** | **9 (13%)** | **1 (1%)** | |

---

## Priority Recommendations

### High Priority (add tests)

1. **GS-04**: Cache hit should assert `fromCache=true` on the registered artifact. Currently only tests that the step succeeds.
2. **PS-06**: Specified input not found in repository. Currently there's no error path test for named inputs.
3. **SE-03**: Add a single test that verifies the delegate callback ordering: Initializing → BeforeSelectWorker → SelectedWorker → Starting → Finished. A simple ordered-call recorder would suffice.

### Medium Priority (add tests)

4. **GS-03**: Verify that `WaitingForWorker()` is called when lock acquisition requires retries.
5. **AR-02**: Add a concurrent access test using parallel goroutines registering and looking up artifacts.
6. **SE-04**: Add a test (possibly table-driven) that verifies for each error outcome, exactly one of Finished/Errored is called but never both.

### Low Priority (spec corrections, no new tests needed)

7. **DO-01/DO-02**: Rewrite these requirements to target `atc/builds/planner.go` (where `VisitDo` converts a DoStep into a sequential DoPlan). The exec layer has no "do step" type.
8. **HE-02**: Add spec requirement: retriable errors do NOT trigger the on_error hook.
9. **RT-02**: Clarify spec to mention `context.DeadlineExceeded` is also retried (not just generic errors).
10. **TS-10**: Clarify the expected sidecar image failure behavior more precisely.

### Tests to Consider Rewriting (implementation-coupled → behavioral)

The following test patterns assert on fake call counts without verifying actual behavior. These are low-priority refactors but would improve spec traceability:

- `get_step_test.go`: "emits a BeforeSelectWorker event" / "emits a SelectedWorker event" — currently just checks call count, not that the correct worker name was passed in the right context
- `put_step_test.go`: "saves the build output" — currently verifies all args to `SaveOutputArgsForCall`; this is already behavioral and fine
- `task_step_test.go`: "sets the config on the TaskDelegate" — checks SetTaskConfig was called; could additionally verify the config matches expectations
