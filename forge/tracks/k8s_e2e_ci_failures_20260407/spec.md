# Spec: K8s E2E CI Failures (April 2026)

**Track ID:** `k8s_e2e_ci_failures_20260407`
**Type:** bugfix
**Created:** 2026-04-07

## Overview

Both k8s-e2e CI jobs (`k8s-integration-tests` and `k8s-behavioral-tests`) are red on the `k8s-e2e` pipeline at concourse.home. These are two separate issues:

1. **Behavioral tests — infrastructure failure:** The last 4 builds (#37–#40) all die at ~50 seconds during `docker build`, with containerd logging `cleaning up after shim disconnected`. The pod is being killed (likely OOM or ephemeral storage eviction) before tests ever start. Build #36 was the last run that actually executed tests (33 min, failed).

2. **Integration tests — check exec race condition:** Build #109 fails with 1 test failure in `runs parallel gets feeding into a single task` (`k8s_pipeline_e2e_test.go:220`). The check pod completes successfully (exit 0) but the exec connection fails with `container not found ("main")` — the pod was cleaned up before output could be streamed. Builds #107 and #108 passed, so this is intermittent.

## Requirements

1. Diagnose and fix the behavioral test pod termination during Docker image build
2. Fix the check exec race condition in integration tests where the container is gone before output is read
3. Ensure both jobs return to green
4. Update stale `FAILURES.md` to reflect current state (it was last updated Feb 15 and is wildly out of date)

## Technical Approach

### Issue 1: Behavioral test pod killed during docker build

The `docker build` step sends a ~315MB build context (the full repo) to the Docker daemon inside a DinD container. Combined with the Go compilation artifacts, K3s image pull (~76MB), and the tmpfs mount for Docker storage (8GB), this likely exceeds the pod's ephemeral storage or memory limits.

Investigation path:
- Check the pod resource requests/limits in the Concourse worker config
- Check if the k8s-cicd worker node has resource pressure
- Check kubelet eviction logs on the worker node
- Consider reducing the Docker build context (use `.dockerignore` or copy only needed binaries)
- Consider pre-building the concourse image in `build-kind-runner` instead of in-task

### Issue 2: Check exec race condition

The check pod runs a `check-resource` operation. The resource check completes quickly (exit 0), causing K8s to move the pod to `Succeeded` phase. When `exec.go:81` (`Wait()`) tries to stream output, the container is already terminated.

Fix path:
- The exec logic needs to handle the case where the container has already completed
- Options: read logs instead of exec for completed containers, or add a retry/fallback

## Acceptance Criteria

- [ ] `k8s-behavioral-tests` job passes on CI (tests actually run to completion)
- [ ] `k8s-integration-tests` job passes on CI (parallel gets test is stable)
- [ ] `FAILURES.md` updated to reflect current test state

## Out of Scope

- Fixing the 26 failures documented in the stale Feb 15 FAILURES.md (most are already fixed)
- Adding new behavioral tests
- Performance optimization of test suite
