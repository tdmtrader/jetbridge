# Implementation Plan: Fix Task Cache Persistence in K8s Runtime

## Phase 1: Fix Cache Key to Be Deterministic

**Goal:** Replace the per-container UUID cache key with a stable `(jobID, stepName, path)` key so that cache directories are reused across builds.

- [ ] Task 1.1: Add `stableCacheKey(jobID, stepName, cachePath)` helper to `container.go` that produces a deterministic, filesystem-safe key (e.g. `job-<jobID>-<stepName>-<pathHash>`)
- [ ] Task 1.2: Update `buildVolumeMounts()` PVC branch (line 922) to use `stableCacheKey()` instead of `c.handle`
- [ ] Task 1.3: Unit test the stable key function — same inputs produce same output, different inputs produce different output, output is filesystem-safe
- [ ] Task 1.4: Unit test `buildVolumeMounts()` PVC path — verify SubPath uses stable key, not container handle
- [ ] Task 1.5: Phase 1 Manual Verification — run behavioral caching test suite

## Phase 2: Switch EmptyDir Fallback to HostPath

**Goal:** When no cache PVC is configured, use `hostPath` volumes so caches survive pod termination.

- [ ] Task 2.1: Add `CacheHostPath` field to `jetbridge.Config` (default: `/var/concourse/cache`)
- [ ] Task 2.2: Add `--kubernetes-cache-host-path` CLI flag in `atc/atccmd/command.go` and wire to config
- [ ] Task 2.3: Update `buildVolumeMounts()` else branch (line 929-942) — when `CacheHostPath` is set, use `hostPath` with `type: DirectoryOrCreate` at `<base>/<stableCacheKey>`; when empty, fall back to `emptyDir` (preserving current behavior as opt-in default)
- [ ] Task 2.4: Unit test `buildVolumeMounts()` hostPath branch — verify volume type and mount path
- [ ] Task 2.5: Add `cacheHostPath` to helm chart values and wire into web deployment args
- [ ] Task 2.6: Phase 2 Manual Verification — deploy with hostPath, verify cache persistence across builds

## Phase 3: Cache Cleanup and fly clear-task-cache Support

**Goal:** Ensure `fly clear-task-cache` works with the new key scheme, and stale directories are cleaned up.

- [ ] Task 3.1: Verify `fly clear-task-cache` still works — the DB-side cache clearing should invalidate the stable key on next use (investigate if directory cleanup is needed or if overwriting is sufficient)
- [ ] Task 3.2: If directory cleanup is needed: add a GC component or hook that removes hostPath directories for deleted task_caches entries
- [ ] Task 3.3: Run full behavioral test suite (`topgun/k8s_behavioral/caching_test.go`) — all 4 tests must pass
- [ ] Task 3.4: Phase 3 Manual Verification — end-to-end validation on KinD cluster

---
