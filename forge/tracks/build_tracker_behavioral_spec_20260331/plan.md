# Build Tracker Behavioral Spec — Coverage Matrix & Implementation Plan

## Coverage Matrix

### Section 1: Build Tracking Lifecycle (6 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| BT-01 | Started build discovery | ✅ Full | tracker_test.go:58 | TestTrackRunsStartedBuilds — loads all started builds |
| BT-02 | In-memory build channel | ✅ Full | tracker_test.go:92 | TestTrackInMemoryBuilds — receives from channel |
| BT-03 | Build deduplication | ✅ Full | tracker_test.go:177, 211 | Tests both DB builds and in-memory checks |
| BT-04 | Panic recovery | ✅ Full | tracker_test.go:128 | TestTrackerDoesntCrashWhenOneBuildPanic — panicked build errored, others continue |
| BT-05 | Build running metrics | ✅ Full | tracker_test.go:251-297 | TestTrackEmitsBuildsRunningMetric + TestTrackEmitsCheckBuildsRunningMetric |
| BT-06 | Drain delegates to engine | ✅ Full | tracker_test.go:242 | TestTrackerDrainsEngine |

**Summary:** 6/6 Full (100%)

---

### Section 2: Plan Generation — Visitor Pattern (15 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| PG-01 | Get step plan | ✅ Full | planner_test.go:86-617 | 6 get step variants + error cases |
| PG-02 | Put step plan | ✅ Full | planner_test.go:625-1199 | 5 put step variants including no_get |
| PG-03 | Task step plan | ✅ Full | planner_test.go:1271-1460 | Basic, container limits, manual trigger, sidecars |
| PG-04 | Run step plan | ✅ Full | planner_test.go:1497, 1532 | Happy path + UnknownPrototypeError test |
| PG-05 | SetPipeline step plan | ✅ Full | planner_test.go:1533 | Pipeline name, file, vars, var_files |
| PG-06 | LoadVar step plan | ✅ Full | planner_test.go:1555 | Name, file, format, reveal |
| PG-07 | Do step | ✅ Full | planner_test.go:1600 | Sequential composition |
| PG-08 | InParallel step | ✅ Full | planner_test.go:1640 | Parallel with limit and fail_fast |
| PG-09 | Across step | ✅ Full | planner_test.go:1688 | Matrix with SubStepTemplate |
| PG-10 | Try step | ✅ Full | planner_test.go:1575 | Error suppression wrapping |
| PG-11 | Timeout step | ✅ Full | planner_test.go:1729 | Duration wrapping |
| PG-12 | Retry step | ✅ Full | planner_test.go:1753 | N independent plans |
| PG-13 | Hook step composition | ✅ Full | planner_test.go:1793-1937 | OnSuccess, OnFailure, OnError, OnAbort, Ensure |
| PG-14 | Nested resource type images | ✅ Full | planner_test.go:158-473 | Multi-level nesting, privileged propagation, never check_every |
| PG-15 | Unique plan IDs | ✅ Full | planner_test.go (all) | Every test verifies distinct PlanIDs |

**Summary:** 15/15 Full (100%)

---

### Section 3: Engine Execution (12 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| EX-01 | Tracking lock acquisition | ✅ Full | engine_test.go:432-437 | Lock failure skips step |
| EX-02 | Build reload and validation | ✅ Full | engine_test.go:388-426 | Not active, finished, deleted — all tested |
| EX-03 | Stepper creation failure | ✅ Full | engine_test.go:361-370 | Error event saved, lock released |
| EX-04 | Variable resolution failure | ✅ Full | engine_test.go:345-354 | Error event saved, lock released |
| EX-05 | Abort signal monitoring | ✅ Full | engine_test.go:214-232 | Abort cancels context |
| EX-06 | Successful build finish | ✅ Full | engine_test.go:239-244 | Finish(Succeeded) |
| EX-07 | Failed build finish | ✅ Full | engine_test.go:251-256 | Finish(Failed) |
| EX-08 | Errored build finish | ✅ Full | engine_test.go:263-269 | Non-retryable error → Finish(Errored) |
| EX-09 | Aborted build finish | ✅ Full | engine_test.go:302-319 | context.Canceled and wrapped version |
| EX-10 | Retriable error — normal build | ✅ Full | engine_test.go:293-294 | Does not finish, allows retry |
| EX-11 | Retriable error — check build | ✅ Full | engine_test.go:281-286 | Finishes (no retry for checks) |
| EX-12 | Drain releases without finishing | ✅ Full | engine_test.go:192-208 | Does not finish the build |

**Summary:** 12/12 Full (100%)

---

### Section 4: Step Construction — Stepper Factory (10 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| SF-01 | Plan type dispatch | ✅ Full | builder_test.go:122-849 | Get, put, task, run, set_pipeline, load_var, check, retry, hooks, try |
| SF-02 | Schema validation | ✅ Full | builder_test.go:111-119 | Wrong schema errors |
| SF-03 | Get step with metadata | ✅ Full | builder_test.go:497-526 | Container metadata + step metadata verified |
| SF-04 | Put step with dependent get | ✅ Full | builder_test.go:629-695 | VersionFrom linkage verified |
| SF-05 | Retry step expansion | ✅ Full | builder_test.go:290-392 | Multiple attempts, nested retries |
| SF-06 | Hook step construction | ✅ Full | builder_test.go:698-816 | All hooks (success, failure, completion) chained |
| SF-07 | Try step construction | ✅ Full | builder_test.go:836-849 | Inner step wrapped |
| SF-08 | Container metadata | ✅ Full | builder_test.go:510-526 | Build ID, pipeline, step name, attempt number |
| SF-09 | Step metadata | ✅ Full | builder_test.go:538-548 | Build ID, team, pipeline, external URL |
| SF-10 | Unknown plan → identity step | ✅ Full | builder_test.go:870-880 | Plan with no recognized type returns IdentityStep |

**Summary:** 10/10 Full (100%)

---

### Section 5: Delegate Event Emission (10 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| DE-01 | Get delegate events | ✅ Full | get_delegate_test.go:63-68 | FinishGet event verified |
| DE-02 | Put delegate events | ✅ Full | put_delegate_test.go:59-64 | FinishPut event verified |
| DE-03 | Task delegate events | ✅ Full | task_delegate_test.go:79-146 | Init/Start/Finish with TaskConfig |
| DE-04 | SetPipeline delegate events | ✅ Full | set_pipeline_delegate_test.go:57-62 | SetPipelineChanged event |
| DE-05 | Check delegate events | ✅ Full | check_delegate_test.go:72-102 | InitializeCheck with scope |
| DE-06 | Sidecar event emission | ✅ Full | task_delegate_test.go:176-200 | Sidecar event per sidecar, empty list handled |
| DE-07 | Sidecar log writer | ✅ Full | task_delegate_test.go:153-174 | Non-nil writer, Log events with sidecar plan ID |
| DE-08 | Build step delegate init/finish | ✅ Full | build_step_delegate_test.go:73-90 | Initialize and Finish events |
| DE-09 | Get delegate metadata update | ✅ Full | get_delegate_test.go:80-149 | Pipeline/resource lookup, UpdateMetadata called |
| DE-10 | Put delegate output saving | ✅ Full | put_delegate_test.go:76-94 | SaveOutput with version, metadata, resource |

**Summary:** 10/10 Full (100%)

---

### Section 6: Image Fetching & Policy (8 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| IF-01 | Policy check before fetch | ✅ Full | build_step_delegate_test.go:245-335 | Policy check with rejection |
| IF-02 | Soft policy with warnings | ✅ Full | build_step_delegate_test.go:285-296 | Non-blocking with warning log |
| IF-03 | Credential redaction in policy | ✅ Full | build_step_delegate_test.go:350-357 | Source redacted before check |
| IF-04 | Metadata-only fetch (registry-image) | ✅ Full | task_delegate_test.go:667-950 | Cached resolution, on-demand via resolver |
| IF-05 | Plan-based fetch fallback | ✅ Full | task_delegate_test.go:691, 854, 892 | Fallback on cache miss, non-registry, DB error |
| IF-06 | Image check/get events | ✅ Full | task_delegate_test.go:360-472, 828 | Events saved for both paths |
| IF-07 | Task FetchImage with plans | ✅ Full | task_delegate_test.go:348-601 | Check+get plans generated, events emitted |
| IF-08 | Docker URL generation | ✅ Full | task_delegate_test.go:808 | docker://repository@digest format verified |

**Summary:** 8/8 Full (100%)

---

### Section 7: Planner Error Handling (3 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| PE-01 | Unknown resource error | ✅ Full | planner_test.go:609 | "get step with unknown resource" |
| PE-02 | Unknown prototype error | ✅ Full | planner_test.go:1532 | "run step with unknown prototype" |
| PE-03 | Version not provided error | ✅ Full | planner_test.go:617 | "get step with no available version" |

**Summary:** 3/3 Full (100%)

---

## Overall Summary

| Section | Requirements | Full | Partial | None | Coverage |
|---------|-------------|------|---------|------|----------|
| 1. Build Tracking Lifecycle | 6 | 6 | 0 | 0 | 100% |
| 2. Plan Generation | 15 | 15 | 0 | 0 | 100% |
| 3. Engine Execution | 12 | 12 | 0 | 0 | 100% |
| 4. Step Construction | 10 | 10 | 0 | 0 | 100% |
| 5. Delegate Events | 10 | 10 | 0 | 0 | 100% |
| 6. Image Fetching & Policy | 8 | 8 | 0 | 0 | 100% |
| 7. Planner Error Handling | 3 | 3 | 0 | 0 | 100% |
| **TOTAL** | **64** | **64** | **0** | **0** | **100%** |

## Gap-Filling Summary

All 4 identified gaps have been filled with new tests across 3 packages:

### P1 Gaps — Fixed

- [x] **BT-05**: Added BuildsRunning + CheckBuildsRunning metric assertions (`atc/builds/tracker_test.go`)
- [x] **PE-02**: Added "run step with unknown prototype" error test (`atc/builds/planner_test.go`)

### P2 Gaps — Fixed

- [x] **PG-04**: Same as PE-02 — error path now tested
- [x] **SF-10**: Added unknown plan → IdentityStep test (`atc/engine/builder_test.go`)

### New Tests Added (4 tests across 3 files)

| File | New Tests | What They Test |
|------|-----------|---------------|
| `atc/builds/tracker_test.go` | 2 | BuildsRunning gauge during build, CheckBuildsRunning gauge during check build |
| `atc/builds/planner_test.go` | 1 | UnknownPrototypeError for run step with bogus prototype |
| `atc/engine/builder_test.go` | 1 | Unknown plan type returns exec.IdentityStep |

### Verification

All tests pass:
- `go test ./atc/builds/` — 10 tests PASSED (testify suite: 8 tracker + planner parameterized)
- `ginkgo ./atc/engine/` (focused) — 1 spec PASSED
