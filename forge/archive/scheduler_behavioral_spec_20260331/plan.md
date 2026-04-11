# ATC Scheduler Behavioral Spec — Coverage Matrix & Implementation Plan

## Coverage Matrix

### Section 1: Job Scheduling Lifecycle (8 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| SL-01 | Full job scan on each tick | ✅ Full | runner_test.go:66-68, 555-558 | Verifies JobsToSchedule called; dedicated "always performs full job scan" test |
| SL-02 | Concurrent scheduling with semaphore | ✅ Full | runner_test.go:227-281 | Tests duplicate job with maxInFlight=2; WaitGroup-based concurrency test |
| SL-03 | Job deduplication via sync.Map | ✅ Full | runner_test.go:227-281 | "when the same job is already being scheduled" — verifies Schedule called once |
| SL-04 | Scheduling lock acquisition | ✅ Full | runner_test.go:127-156 | Tests lock not acquired (silent skip), lock error (skip with log) |
| SL-05 | Job reload before scheduling | ✅ Full | runner_test.go:164-167, 327-349 | Tests reload success, failure, and not-found cases |
| SL-06 | UpdateLastScheduled on success | ✅ Full | runner_test.go:212-225 | Verifies both jobs updated with correct requestedTime |
| SL-07 | Skip UpdateLastScheduled on retry | ✅ Full | runner_test.go:313-324 | needsRetry=true → UpdateLastScheduled not called |
| SL-08 | Panic recovery | ✅ Full | runner_test.go:296-311 | Panic in one job, other job still scheduled and updated |

**Summary:** 8/8 Full (100%)

---

### Section 2: Input Resolution Algorithm (9 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| IR-01 | Resolver construction by input type | ✅ Full | algorithm_test.go (multiple) | Covered implicitly by all algorithm tests — each scenario exercises the correct resolver path |
| IR-02 | Individual resolver — latest | ✅ Full | algorithm_test.go (latest version scenarios) | Multiple DescribeTable entries test latest version resolution |
| IR-03 | Individual resolver — every | ✅ Full | algorithm_test.go (every version scenarios) | NextEveryVersion tested with found/not-found/hasNext cases |
| IR-04 | Pinned resolver | ✅ Full | algorithm_test.go (pinned version scenarios) | FindVersionOfResource tested with found and not-found |
| IR-05 | Resolution failure propagation | ✅ Full | scheduler_test.go:244-261 | Unresolved mapping → BuildStarter still called, no pending build created |
| IR-06 | First occurrence computation | ✅ Full | firstoccurrence_test.go:1-513 | Dedicated 513-line test file; db-backed integration tests |
| IR-07 | HasNextEveryVersion signaling | ✅ Full | scheduler_test.go:121-139, algorithm_test.go | runAgain=true → RequestSchedule called; false → not called |
| IR-08 | Input mapping persistence | ✅ Full | scheduler_test.go:141-161 | SaveNextInputMapping called with correct mapping and resolved flag; error propagated |
| IR-09 | RunAgain triggers re-schedule | ✅ Full | scheduler_test.go:121-129 | runAgain=true → RequestSchedule called once |

**Summary:** 9/9 Full (100%)

---

### Section 3: Passed Constraint Resolution (9 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| PC-01 | Input grouping by shared passed jobs | ✅ Full | algorithm_test.go (fan-in scenarios) | Multiple multi-input passed-constraint scenarios |
| PC-02 | Deterministic job ordering | ✅ Full | algorithm_test.go:3294 | "resolves passed constraints deterministically across multiple jobs" — 3 passed jobs, verifies sorted PassedBuildIDs |
| PC-03 | Build output matching | ✅ Full | algorithm_test.go (build output scenarios) | Resource ID matching, version matching, multi-resource outputs |
| PC-04 | Version mismatch detection | ✅ Full | algorithm_test.go (mismatch/backtrack scenarios) | Tests where one build has conflicting versions; algorithm backtracks |
| PC-05 | Disabled version exclusion | ✅ Full | algorithm_test.go (disabled version scenarios) | Disabled versions skipped during resolution |
| PC-06 | Pinned + passed combination | ✅ Full | algorithm_test.go (pinned with passed scenarios) | Tests pin + passed: resolves only when build outputs match pin |
| PC-07 | Recursive resolution | ✅ Full | algorithm_test.go (multi-input fan-in) | Complex scenarios requiring recursive tryResolve calls |
| PC-08 | Doom detection | ✅ Full | algorithm_test.go:3330 | "doom detection prevents infinite recursion on unsatisfiable passed constraints" — two inputs requiring both jobs but no single build satisfies both |
| PC-09 | Every version with passed | ✅ Full | algorithm_test.go (every + passed scenarios) | Tests unused builds, build pipes, incremental resolution |

**Summary:** 9/9 Full (100%)

---

### Section 4: Trigger Detection & Pending Build Creation (5 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| TD-01 | First occurrence trigger creates pending build | ✅ Full | scheduler_test.go:328-369 | FirstOccurrence=true + Trigger=true → EnsurePendingBuildExists called |
| TD-02 | Non-trigger first occurrence sets hasNewInputs | ✅ Full | scheduler_test.go:263-325 | FirstOccurrence=true + Trigger=false → no pending build, hasNewInputs set |
| TD-03 | Unsatisfiable inputs skip build creation | ✅ Full | scheduler_test.go:244-261 | satisfiableInputs=false → no pending build, no SetHasNewInputs |
| TD-04 | HasNewInputs state tracking | ✅ Full | scheduler_test.go:290-325, 372-409 | Tests: changed→set, unchanged→skip, true→false transition, false→true transition |
| TD-05 | Multiple trigger inputs — first match wins | ✅ Full | scheduler_test.go:413-468 | Two trigger inputs, both FirstOccurrence → EnsurePendingBuildExists called once; linked span verified |

**Summary:** 5/5 Full (100%)

---

### Section 5: Build Startup (12 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| BS-01 | Build type classification | ✅ Full | buildstarter_test.go:553-569, job_scheduling_test.go:112-125 | All three types tested in integration scenario |
| BS-02 | Aborted build finalization | ✅ Full | buildstarter_test.go:110-173, job_scheduling_test.go:34-45 | Aborted → Finish(Aborted), next build continues |
| BS-03 | Max-in-flight enforcement | ✅ Full | buildstarter_test.go:586-599, job_scheduling_test.go:47-58 | ScheduleBuild=false → no input adoption, needsRetry=true |
| BS-04 | Manual trigger readiness check | ✅ Full | buildstarter_test.go:176-265 | ResourcesChecked false → needsRetry; true → proceed to algorithm |
| BS-05 | Manual trigger recomputes algorithm | ✅ Full | buildstarter_test.go:266-417 | Compute called, SaveNextInputMapping, RequestSchedule, AdoptInputsAndPipes |
| BS-06 | Scheduler build always ready | ✅ Full | buildstarter_test.go:470-472 | No Compute call for scheduler builds; uses AdoptInputsAndPipes |
| BS-07 | Rerun build input adoption | ✅ Full | buildstarter_test.go:662-670 | AdoptRerunInputsAndPipes called, NOT AdoptInputsAndPipes |
| BS-08 | Build plan creation | ✅ Full | buildstarter_test.go:673-699 | Planner.Create with correct args (step config, resources, types, prototypes, inputs, manuallyTriggered) |
| BS-09 | Plan failure marks build errored | ✅ Full | buildstarter_test.go:604-651, job_scheduling_test.go:86-97 | Finish(Errored) called; continues to next build |
| BS-10 | Build start | ✅ Full | buildstarter_test.go:700-795 | Start returns true → success; false → Finish(Aborted); error → propagated |
| BS-11 | Rerun build doesn't block others | ✅ Full | buildstarter_test.go:874-891, job_scheduling_test.go:169-181 | Rerun with no inputs → next build still scheduled |
| BS-12 | Regular build failure stops scheduling | ✅ Full | buildstarter_test.go:812-825, job_scheduling_test.go:183-195 | InputsNotDetermined → stops, needsRetry=false |

**Summary:** 12/12 Full (100%)

---

### Section 6: Metrics & Observability (6 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| MO-01 | JobsScheduling gauge | ✅ Full | metrics_test.go:48-84 | Captures gauge value during scheduling; asserts >=1 while in progress |
| MO-02 | JobsScheduled counter | ✅ Full | metrics_test.go:48-84 | Asserts JobsScheduled.Delta() >=1 after scheduling completes |
| MO-03 | SchedulingJobDuration emission | ✅ Full | metrics_test.go:89-115 | Verifies full scheduleJob path including Emit() call (UpdateLastScheduled after Emit) |
| MO-04 | BuildsStarted counter | ✅ Full | metrics_test.go:130-159 | Non-check build (name="42") → BuildsStarted=1, CheckBuildsStarted=0 |
| MO-05 | CheckBuildsStarted counter | ✅ Full | metrics_test.go:161-186 | Check build (name="check") → CheckBuildsStarted=1, BuildsStarted=0 |
| MO-06 | Tracing spans | ✅ Full | metrics_test.go:190-270, scheduler_test.go:413-468 | schedule-job span with team/pipeline/job attrs; try-start-pending-build span with build_id/build attrs; linked span for trigger detection |

**Summary:** 6/6 Full (100%)

---

## Overall Coverage Summary

| Section | Requirements | Full | Partial | Missing | Coverage |
|---------|-------------|------|---------|---------|----------|
| 1. Job Scheduling Lifecycle | 8 | 8 | 0 | 0 | 100% |
| 2. Input Resolution Algorithm | 9 | 9 | 0 | 0 | 100% |
| 3. Passed Constraint Resolution | 9 | 9 | 0 | 0 | 100% |
| 4. Trigger Detection | 5 | 5 | 0 | 0 | 100% |
| 5. Build Startup | 12 | 12 | 0 | 0 | 100% |
| 6. Metrics & Observability | 6 | 6 | 0 | 0 | 100% |
| **Total** | **49** | **49** | **0** | **0** | **100%** |

---

## Identified Gaps

### P1 (Must-Have) — None
All critical behavioral paths have existing test coverage.

### P2 (Should-Have) — All Resolved

| Gap | Req | Status | Description |
|-----|-----|--------|-------------|
| Deterministic job ordering test | PC-02 | ✅ Done | algorithm_test.go:3294 — 3 passed jobs, verifies sorted PassedBuildIDs |
| Doom detection cycle test | PC-08 | ✅ Done | algorithm_test.go:3330 — unsatisfiable cross-job constraints trigger doom path |
| Metrics assertion for gauges | MO-01/02 | ✅ Done | metrics_test.go:48-84 — captures gauge during scheduling, asserts counter after |
| Metrics assertion for duration | MO-03 | ✅ Done | metrics_test.go:89-115 — verifies full path including Emit() |
| BuildsStarted metric assertion | MO-04/05 | ✅ Done | metrics_test.go:130-186 — distinguishes check vs non-check by build name |
| Tracing span assertions | MO-06 | ✅ Done | metrics_test.go:190-270 — schedule-job and try-start-pending-build spans with attributes |

### P3 (Nice-to-Have)

| Gap | Req | Status | Description |
|-----|-----|--------|-------------|
| Algorithm integration with real DB | IR-01–09 | ⚠️ Exists | algorithm_test.go already uses real PostgreSQL; very thorough |
| Semaphore backpressure test | SL-02 | ⚠️ Exists | Tested implicitly via maxInFlight=1 with 2 jobs |

---

## Implementation Plan

### Phase 1: Spec & Matrix (current)
- [x] Survey scheduler codebase
- [x] Write behavioral specification (spec.md)
- [x] Build coverage matrix (plan.md)
- [x] Identify gaps

### Phase 2: P2 Gap-Filling Tests
- [x] PC-02: Add deterministic job ordering unit test (algorithm_test.go:3294)
- [x] PC-08: Add doom detection cycle test (algorithm_test.go:3330)
- [x] MO-01/02: Add JobsScheduling/JobsScheduled metric assertions (metrics_test.go:48)
- [x] MO-03: Add SchedulingJobDuration emission test (metrics_test.go:89)
- [x] MO-04/05: Add BuildsStarted vs CheckBuildsStarted metric test (metrics_test.go:130)
- [x] MO-06: Add tracing span structure assertions (metrics_test.go:190)

### Phase 3: Verification
- [x] Run `ginkgo ./atc/scheduler/` — 131/131 passed
- [x] Run `ginkgo ./atc/scheduler/algorithm/` — 92/92 passed (6 skipped, Jaeger-dependent)
- [x] Update coverage matrix with new test locations
- [x] Final coverage: 49/49 Full (100%)
