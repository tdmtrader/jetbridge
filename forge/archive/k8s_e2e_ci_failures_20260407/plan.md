# Implementation Plan: K8s E2E CI Failures

## Phase 1: Diagnose Behavioral Test Pod Termination

- [x] Task: Check pod resource limits — cluster healthy, no LimitRange/ResourceQuota, BestEffort pods
- [x] Task: Check kubelet events — no OOM, no evictions. Pod killed by Concourse, not K8s
- [x] Task: Root cause identified — serial_groups conflict. Both behavioral and integration tests in `serial_groups: [k8s-tests]` trigger simultaneously after build-kind-runner. Integration aborts running behavioral test ~50s in
- [x] Task: Fix — chain behavioral after integration via `passed: [k8s-integration-tests]`, remove serial_groups from behavioral
- [x] Task: Reduce Docker build context — staged binaries to /tmp/docker-build/ (bonus fix, ~315MB → ~165MB)

## Phase 2: Fix Integration Test Check Exec Race

- [x] Task: Analyze the exec path — SPDY exec fails with "container not found" when check pod completes before exec connects
- [x] Task: Fix the race — classify "container not found" and "unable to upgrade connection" as transient errors in `isTransientK8sError()` so they're retried
- [x] Task: Tests pass — `TestWrapIfTransientSPDYExecErrors` added and passing

## Phase 3: Fix Layer Corruption and Exec Retry

- [x] Task: Fix K3s layer corruption — move K3s image pull after Docker builds to prevent containerd shim disconnect corruption
- [x] Task: Add exec retry loop in `execProcess.Wait()` with `recreatePausePodIfTerminal()` for GC-terminated pause pods
- [x] Task: Add pod recreation in `waitForRunning()` when pod found in terminal state
- [x] Task: Bump kind-runner image tag (v28→v29→v30) to bust K8s node image cache

## Phase 4: Integration Tests GREEN ✓

- [x] Task: Integration build #116 passed — 122/122 specs, 0 failures

## Phase 5: Behavioral Test Fix

- [x] Task: Diagnose `cmd/oom-trigger` missing from v29 kind-runner image (transient tar truncation via kubectl exec pipe)
- [x] Task: Add oom-trigger pre-compilation to Dockerfile.kind-runner to catch missing files at build time
- [x] Task: Bump to v30, push, update pipeline, trigger build #117
- [x] Task: Monitor behavioral tests with v30 image (behavioral #102/#103 both green: 298 Passed | 0 Failed)

## Phase 6: Verify and Cleanup

- [x] Task: Confirm both integration and behavioral jobs are green (integration #184 succeeded; behavioral #103 succeeded)
- [x] Task: Update `FAILURES.md` to reflect current state (cited build #103, dual FK-guard + image-tag fix)
