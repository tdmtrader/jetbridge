# Plan: Behavioral Test Fixes

## Phase 1: Sidecar log streaming race condition

### [ ] Write test reproducing the sidecar log race
- Add a unit test in `atc/worker/jetbridge/process_test.go` that verifies sidecar log events are saved before the pod is deleted.
- File: `atc/worker/jetbridge/process_test.go`

### [ ] Fix sidecar log stream synchronization
- In `execProcess.Wait()` (process.go), the pod deletion currently happens immediately after the main container exits. Sidecar log streams run in detached goroutines with no synchronization.
- Add a `sync.WaitGroup` for sidecar log goroutines in `streamLogs()`.
- Gate pod deletion on the WaitGroup completing (with a bounded timeout, e.g., 5s, to avoid blocking indefinitely if a sidecar hangs).
- Files: `atc/worker/jetbridge/process.go`

## Phase 2: Custom resource type chain resolution

### [ ] Write test for type chain resolution in metadataFetchImage
- Test that `metadataFetchImage()` resolves a custom type back to `registry-image` through one level of indirection.
- Test that an unresolvable type chain returns an appropriate error.
- File: `atc/engine/build_step_delegate_test.go`

### [ ] Fix metadataFetchImage to resolve custom type chains
- In `atc/engine/build_step_delegate.go`, when `getPlan.Get.Type != "registry-image"`:
  - Look up the type in the build's `resource_types` to find its parent type
  - If the parent type is `registry-image`, resolve the image from the custom type's `source.repository`
  - If not, continue walking the chain (bounded to prevent infinite loops)
  - Fall back to plan-based fetch if chain resolution fails
- Files: `atc/engine/build_step_delegate.go`

## Phase 3: Multi-input job scheduling reliability

### [ ] Diagnose whether this is a test or system issue
- Check if `newMockVersion()` waits for the check to complete or returns immediately.
- Check if the scheduler processes both version notifications within one cycle.
- Determine if the fix should be in the test (wait for versions) or the system (reliable notification delivery).
- Files: `topgun/k8s_behavioral/job_scheduling_test.go`, `topgun/k8s_behavioral/behavioral_suite_test.go`

### [ ] Fix multi-input scheduling reliability
- If test issue: add `waitForResourceVersion("src-a")` and `waitForResourceVersion("src-b")` calls before triggering/waiting for the job.
- If system issue: ensure the scheduler polls after check completion rather than relying solely on notifications.
- Files: depends on diagnosis

## Phase 4: Validation

### [ ] Rerun the 4 previously-failing tests
- Build image, deploy to KinD, run only the 4 failing tests:
  - `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m -run "streams_sidecar_logs|two-level_type_chain|custom_type_check|schedules_a_job_with_multiple_inputs"`
- Verify all 4 now pass.
