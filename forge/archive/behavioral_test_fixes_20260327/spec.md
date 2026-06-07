# Spec: Behavioral Test Fixes

**Track ID:** `behavioral_test_fixes_20260327`
**Type:** bugfix

## Overview

The K8s behavioral test suite (303 specs) has 4 failing tests. These are pre-existing issues unrelated to the DaemonSet artifact cache work, but they need to be fixed for a clean CI signal. The failures span three distinct root causes: a sidecar log race condition, broken custom resource type chain resolution, and a multi-input scheduling timing issue.

## Failures

### 1. Sidecar log streaming race condition
**Test:** `sidecar_test.go:126` — "streams sidecar logs through dedicated event origins"
**Symptom:** `fly watch` output contains `MAIN_OUTPUT` but not `SIDECAR_LOG_LINE`

**Root cause:** The sidecar container echoes immediately (`echo SIDECAR_LOG_LINE && sleep 30`), but the main container sleeps 5s then exits. When the main container exits, `execProcess.Wait()` calls `uploadOutputsToArtifactStore()`, then annotates the exit status and deletes the pod. The sidecar log stream goroutine (`streamContainerLogsDirect`) is racing against pod deletion — if the pod is deleted before the sidecar log event is saved to the DB, `fly watch` never sees it.

**Fix direction:** Ensure sidecar log events are flushed before the pod is deleted. The `streamLogs` goroutine in `process.go` already runs sidecar log streams in background goroutines, but there's no synchronization between sidecar log completion and pod teardown. Need a `sync.WaitGroup` (or channel) that gates pod deletion on all sidecar log streams finishing (or a short grace period).

### 2. Custom resource type chain resolution (2 tests)
**Tests:**
- `custom_resource_types_test.go:58` — "6.2: two-level type chain resolves correctly"
- `custom_resource_types_test.go:370` — "6.8: custom type check detects new versions"

**Symptom:** `metadata-only fetch not supported for type "level-b"` (test 6.2) and similar for custom-mock types (test 6.8).

**Root cause:** In `atc/engine/build_step_delegate.go:372`, `metadataFetchImage()` only supports `registry-image` types. When a resource uses a custom type (e.g., `type: level-b` where `level-b` is itself `type: registry-image`), the system passes the custom type name (`level-b`) to `metadataFetchImage()` which rejects it. The metadata-only fetch path was introduced for the K8s native image resolution optimization but doesn't recursively resolve custom type chains.

**Fix direction:** In `metadataFetchImage()`, when the type is not `registry-image`, resolve the type chain by walking `resource_types` until we reach `registry-image` (or fail if the chain is broken). Alternatively, fall back to the full plan-based image fetch path instead of erroring.

### 3. Job scheduling with multiple triggered inputs
**Test:** `job_scheduling_test.go:218` — "schedules a job with multiple inputs"
**Symptom:** Job fails or times out when both `src-a` and `src-b` have `trigger: true`

**Root cause:** The test calls `newMockVersion("src-a", "v1")` then `newMockVersion("src-b", "v1")` sequentially. Each calls `fly check-resource` which triggers an async check. The scheduler needs both resources to have versions before it can schedule the job. Due to the notification system's non-blocking send (per memory: notifications are silently dropped when the channel is full), the scheduler may miss one of the version notifications and not schedule the job until the next polling interval (10s default). Combined with the test's 2-minute Eventually timeout, this is marginal.

**Fix direction:** Two options:
1. **Test fix:** Add a `waitForResourceVersion()` helper that polls until both resources show versions, before asserting the job runs. This makes the test robust against notification timing.
2. **System fix:** Ensure the scheduler always catches up within one polling cycle after a check completes, regardless of notification delivery. This may involve the scheduler re-scanning after `check-resource` completes.

## Acceptance Criteria

- [ ] Sidecar log streaming test passes reliably (no race condition)
- [ ] Two-level custom type chain resolves correctly
- [ ] Custom type check detects new versions for custom-typed resources
- [ ] Multi-input triggered job schedules reliably
- [ ] All 303 behavioral specs pass (298 pass + fix 4 failures + 1 pending remains)
- [ ] No regression in existing unit tests

## Out of Scope

- Three-level+ type chains (test 6.3 is already pending)
- DaemonSet-specific behavioral tests (separate track)
