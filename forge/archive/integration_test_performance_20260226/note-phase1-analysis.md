# Phase 1 Analysis: Integration Test Performance

## Suite Overview

- **Location:** `topgun/k8s/integration/`
- **Tests:** 126 individual `It()` blocks across 20 test files
- **Runtime:** ~4 hours sequential (≈1.9 min/test average)
- **Architecture:** Ephemeral KinD cluster → single Concourse Helm deploy → all tests share instance

## Images Used (and preload status)

| Image | Used By | Preloaded into KinD? |
|-------|---------|---------------------|
| `concourse-local:latest` | Concourse itself (web, worker) | **YES** — loaded via `kind load docker-image` |
| `busybox` | Every single task test (`rootfs_uri: docker:///busybox`) | **NO** — pulled by kubelet at runtime |
| `concourse/mock-resource` | Every resource test (`type: mock`) | **NO** — pulled by kubelet at runtime |
| `concourse/registry-image-resource` | Tests using `image_resource` or `registry-image` type | **NO** |

**Impact:** First pod using `busybox` or `concourse/mock-resource` triggers a Docker Hub pull inside KinD. This adds 5-30s depending on network. After first pull, images are cached in the KinD node, so subsequent pulls are fast. But the first few tests pay the full cost. More importantly, `concourse/mock-resource` may be pulled for EVERY check/get/put pod if the image reference doesn't exactly match the cached version.

## Per-Test Overhead (BeforeEach / AfterEach)

### BeforeEach (runs before EVERY test):
1. `SetDefaultEventuallyTimeout(5 * time.Minute)` — sets globals
2. `os.MkdirTemp()` + `os.Mkdir()` — trivial
3. `fly.Login()` — **runs fly CLI → hits ATC API → saves token. ~1-2s per test. Same credentials every time.**
4. `randomPipelineName()` — UUID generation, trivial
5. `newKubeClient()` — loads kubeconfig + creates client. Fast.

### AfterEach (runs after EVERY test):
1. `destroyPipeline()` — fly CLI call, ~1-2s
2. `cleanupOrphanedPods()` — K8s API: list all worker pods, delete Succeeded/Failed ones. ~1-3s
3. `os.RemoveAll(tmp)` — trivial

**Per-test overhead estimate: ~4-7 seconds just for setup/teardown**

## Minimum Test Execution Time (simplest test: smoke_test.go)

| Phase | Operation | Estimated Time |
|-------|-----------|---------------|
| Setup | BeforeEach (login, tmp, client) | ~2-3s |
| Pipeline | `set-pipeline` + `unpause-pipeline` (2 fly CLI calls) | ~3-5s |
| Trigger | `trigger-job` (1 fly CLI call) | ~1-2s |
| Wait | `waitForBuildAndWatch` polling until build exists | ~2-5s |
| Pod | K8s pod scheduling | ~5-10s |
| Image | Container image pull (busybox, if not cached) | 0-15s |
| Execute | `echo "hello"` | <1s |
| Stream | Build output streaming back to fly | ~2-3s |
| Teardown | AfterEach (destroy pipeline, cleanup pods) | ~3-5s |
| **Total** | | **~20-45s** |

So even the absolute simplest test takes 20-45 seconds. This establishes the floor.

## Test Pattern Categories and Their Overhead

### Pattern A: Simple task test (smoke, task_test, most hook/step tests)
- 1 pipeline with 1 job, 1 task
- set-pipeline → trigger → watch → assert
- **Pods created:** 1 task pod (busybox)
- **Expected time:** 30-60s

### Pattern B: Resource test (resource_test, artifact_test)
- Pipeline with resource(s) + task(s)
- Additional `newMockVersion()` call (fly check-resource, ~1-2s)
- **Pods created:** check pod (mock-resource image) + get pod + task pod
- **Expected time:** 45-90s

### Pattern C: Multi-step pipeline (resource_test "get → task → put", pod_cleanup "multi-step")
- get + task + put steps
- **Pods created:** check + get + task + put = 4 pods minimum
- **Expected time:** 60-120s

### Pattern D: Abort/cancel test (build_lifecycle, pod_cleanup, step_combinations, hook_combinations, error_handling)
- Start a `sleep 3600` task, wait for "started" status, then abort
- Polling phase 1: `Eventually(2*time.Minute, 2*time.Second)` → wait for "started"
- Abort action
- Polling phase 2: `Eventually(1*time.Minute, 2*time.Second)` → wait for "aborted"
- **Pods created:** 1 task pod + sometimes 1 hook pod
- **Expected time:** 40-90s (mostly polling overhead)

### Pattern E: Timeout test (error_handling, step_combinations, hook_combinations)
- Task with `timeout: 10s` and `sleep 120`
- Must wait the full 10 seconds for timeout, then hook execution
- **Pods created:** 1 task pod + 1 hook pod
- **Expected time:** 40-70s (10s is hard floor from timeout)

### Pattern F: Loop-in-test (error_handling "various exit codes", edge_cases "rapid triggers", pod_cleanup "consecutive builds")
- Run multiple full build cycles in one `It()` block
- "various exit codes": 5 cycles × set-pipeline + trigger + watch = **~2.5-5 minutes**
- "rapid triggers": 5 triggers + 5 watches = **~2.5-5 minutes** 
- "consecutive builds": 3 cycles = **~1.5-3 minutes**
- **These are the worst offenders for total time**

### Pattern G: Multi-task pipelines (hook_combinations, step_combinations, edge_cases)
- Multiple tasks in one pipeline (2-4 typically)
- Each task = separate pod
- `nested-hooks.yml`: 3 tasks = 3 pods
- `do-step.yml`: 3 sequential tasks = 3 pods (sequential!)
- **Expected time:** 60-180s depending on count

## Timeout Catalog

| Timeout | Location | Purpose | Assessment |
|---------|----------|---------|-----------|
| **5 min** | `integration_suite_test.go:162` | Default `Eventually` | **WAY too generous.** If any operation takes >2 min, it's broken, not slow. |
| **1 min** | `integration_suite_test.go:164` | Default `Consistently` | Reasonable but could be 30s for most |
| **2 min / 2s poll** | abort tests (6 occurrences) | Wait for build "started" | Could be 60s safely |
| **1 min / 2s poll** | abort tests (6 occurrences) | Wait for build "aborted" | Reasonable |
| **3 min / 2s poll** | `helpers_test.go:112,165` | `waitForNoConcourseWorkloadPods`, `waitForPodCleanupByPipeline` | **Generous.** GC should finish in 60-90s |
| **2 min / 1s poll** | `integration_suite_test.go:386` | `waitForPodWithLabel` | Reasonable |
| **2 min / 2s poll** | `helpers_test.go:100` | `waitForConcoursePodsAtLeast` | Reasonable |

## Categorized Issues (by estimated impact)

### HIGH IMPACT

**1. Images not preloaded into KinD** (busybox, concourse/mock-resource)
- `busybox` used by ~120+ tests. First pull from Docker Hub: 5-15s.
- `concourse/mock-resource` used by ~30+ resource tests. First pull: 5-15s.
- After first pull, cached in KinD node. But variable network conditions add unpredictability.
- **Fix:** Add `kind load docker-image busybox` and pre-pull `concourse/mock-resource` in `loadImagesIntoKind()`.
- **Estimated savings:** 10-30s total (on first run), plus reduced variability.

**2. Per-test `fly.Login()` in BeforeEach**
- Every test does: create new fly home → run `fly login` CLI → ATC roundtrip → save token.
- 126 tests × ~1.5s = **~3 minutes** wasted on redundant logins.
- All tests use the same credentials (test/test) against the same ATC.
- **Fix:** Login once in `SynchronizedBeforeSuite`, share the fly home dir.
- **Estimated savings:** ~2-3 minutes total.

**3. Default Eventually timeout of 5 minutes**
- Doesn't save time on passing tests, but when a test fails, it wastes up to 5 minutes before reporting.
- During development, a single failing test blocks for 5 minutes.
- **Fix:** Reduce to 2 minutes. Nothing legitimate takes >2 min in these tests.
- **Impact:** Better failure feedback loop.

### MEDIUM IMPACT

**4. "handles task that exits with various non-zero codes" — 5 builds in one test**
- Runs `setAndUnpausePipeline` + `triggerJob` + `waitForBuildAndWatch` in a loop for 5 different exit codes.
- Each iteration is a full build cycle (~30-60s).
- Total: ~2.5-5 minutes for one "test".
- **Fix:** Single pipeline with 5 jobs (exit-1-job, exit-2-job, etc.), trigger all 5, then watch all 5.
- **Estimated savings:** ~2-3 minutes (removes 4 redundant set-pipeline + pipeline scheduling cycles).

**5. `waitForPodCleanupByPipeline()` 3-minute timeout**
- Used by all 9 `pod_cleanup_test.go` tests + 1 in `error_handling_test.go`.
- If GC is even slightly slow, tests wait 30-60s for cleanup.
- **Fix:** Reduce to 90 seconds. If GC isn't done in 90s, something is actually wrong.
- **Estimated savings:** Faster failure detection; marginal on happy path.

**6. "rapid triggers" test — 5 sequential build watches**
- Triggers 5 builds, then watches each sequentially.
- 5 × pod scheduling + execution = ~2.5-5 minutes.
- **Fix:** Can't easily avoid (tests sequential processing), but could verify just build 1 and build 5 instead of all 5.
- **Estimated savings:** ~1-2 minutes.

**7. "consecutive builds" test — 3 sequential build cycles**
- Similar to above: 3 × full build cycle.
- **Estimated savings if reduced:** ~1-2 minutes.

### LOW IMPACT

**8. Redundant `flyTable("builds")` assertions after `waitForBuildAndWatch()`**
- Several tests call both `waitForBuildAndWatch()` (verifies build completes) and then `flyTable("builds")` (verifies status).
- The second call is redundant when the first already asserts exit code.
- ~1-2s per occurrence, ~10-15 occurrences.
- **Estimated savings:** ~15-30 seconds total.

**9. Hook tests create many busybox pods**
- `hook_combinations_test.go` has 13 tests, many with 2-3 tasks each.
- That's ~30 pods, all needing busybox.
- If busybox is preloaded (fix #1), this is fine. If not, potential image pull contention.

**10. `build_lifecycle_test.go` "streams logs incrementally" has `sleep 1` × 2 in the pipeline**
- The pipeline YAML itself contains `sleep 1` between log lines (to test streaming).
- Adds 2 seconds to the test. Minimal but unnecessary — could be `sleep 0.1`.

## Summary

The ~4 hour runtime is explained by:
- **126 tests × ~1.9 min average = ~4 hours** ✓
- Floor of ~30s per test from pipeline/pod/build overhead
- Several tests with 3-5× overhead from loops, multi-step pipelines, abort flows
- 5-minute default timeout making failures slow to surface
- Repeated fly.Login() adding ~3 min total

**Most impactful quick wins (Phase 2):**
1. Preload busybox + mock-resource into KinD (~30s total savings, plus reliability)
2. Login once, share fly home (~2-3 min savings)
3. Reduce default Eventually to 2 min (faster failure feedback)
4. Consolidate the 5-exit-code loop into one pipeline (~2-3 min savings)
5. Tighten pod cleanup timeout from 3 min to 90s (faster failure detection)
