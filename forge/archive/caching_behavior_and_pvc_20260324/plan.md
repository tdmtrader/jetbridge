# Implementation Plan: Fix Task Cache Persistence in K8s Runtime

> **Reconciled & closed 2026-06-07.** Both original bugs are fixed and live: the
> stable `(jobID, stepName, path)` cache key (`stableCacheKey`, `159fc0c482`) and
> the hostPath fallback. The later artifact-daemon work **adopted** this fix —
> `DaemonSetBackend.CacheVolume` (storage_daemonset.go:58) reuses `stableCacheKey`
> and mounts `<ArtifactDaemonHostPath>/caches/<key>`, so production (daemon enabled
> by default) persists task caches with the stable key; the standalone `CacheHostPath`
> path is now the no-daemon fallback. The "manual verification" tasks are satisfied by
> the CI behavioral suite `topgun/k8s_behavioral/caching_test.go` (persists/"CACHE HIT",
> scoping, clear-task-cache, PVC-backed, mounts) — no skips, and caching is not among
> the known k8s-e2e failures. Task 3.2 (on-disk cache-dir GC) is **de-scoped** to a
> follow-up: DB rows are collected (`atc/gc/task_cache_collector.go`) but stale
> `<hostPath>/caches/<key>` dirs accumulate (node-local, low urgency).

## Phase 1: Fix Cache Key to Be Deterministic

**Goal:** Replace the per-container UUID cache key with a stable `(jobID, stepName, path)` key so that cache directories are reused across builds.

- [x] Task 1.1: Add `stableCacheKey(jobID, stepName, cachePath)` helper to `container.go` 159fc0c48 that produces a deterministic, filesystem-safe key (e.g. `job-<jobID>-<stepName>-<pathHash>`)
- [x] Task 1.2: Update `buildVolumeMounts()` PVC branch (line 922) to use `stableCacheKey()` instead of `c.handle` 159fc0c48
- [x] Task 1.3: Unit test the stable key function 159fc0c48 — same inputs produce same output, different inputs produce different output, output is filesystem-safe
- [x] Task 1.4: Unit test `buildVolumeMounts()` PVC path 159fc0c48 — verify SubPath uses stable key, not container handle
- [x] Task 1.5: Phase 1 Manual Verification — validated by CI behavioral suite `topgun/k8s_behavioral/caching_test.go` ("persists caches across builds" → CACHE HIT); not among known k8s-e2e failures

## Phase 2: Switch EmptyDir Fallback to HostPath

**Goal:** When no cache PVC is configured, use `hostPath` volumes so caches survive pod termination.

- [x] Task 2.1: Add `CacheHostPath` field to `jetbridge.Config` 159fc0c48 (default: `/var/concourse/cache`)
- [x] Task 2.2: Add `--kubernetes-cache-host-path` CLI flag in `atc/atccmd/command.go` and wire to config 159fc0c48
- [x] Task 2.3: Update `buildVolumeMounts()` else branch 159fc0c48 (line 929-942) — when `CacheHostPath` is set, use `hostPath` with `type: DirectoryOrCreate` at `<base>/<stableCacheKey>`; when empty, fall back to `emptyDir` (preserving current behavior as opt-in default)
- [x] Task 2.4: Unit test `buildVolumeMounts()` hostPath branch 159fc0c48 — verify volume type and mount path
- [x] Task 2.5: Add `cacheHostPath` to helm chart values and wire into web deployment args 159fc0c48
- [x] Task 2.6: Phase 2 Manual Verification — hostPath persistence is live via the daemon path (`DaemonSetBackend.CacheVolume`) and validated by the CI behavioral suite; standalone `--kubernetes-cache-host-path` remains as the no-daemon fallback

## Phase 3: Cache Cleanup and fly clear-task-cache Support

**Goal:** Ensure `fly clear-task-cache` works with the new key scheme, and stale directories are cleaned up.

- [x] Task 3.1: Verify `fly clear-task-cache` still works — validated by CI behavioral scenario "clears task cache via fly clear-task-cache" (green); DB-side clearing invalidates on next use, overwriting is sufficient (no directory cleanup required for correctness)
- [x] Task 3.2: ~~If directory cleanup is needed: add a GC component for stale hostPath cache dirs~~ — **DE-SCOPED to a follow-up.** DB rows are GC'd by `atc/gc/task_cache_collector.go`, but on-disk `<hostPath>/caches/<key>` dirs are not swept (daemon TTL covers `/artifacts/steps`, not caches). Node-local, low urgency; spun off as a separate hygiene task.
- [x] Task 3.3: Run full behavioral test suite (`topgun/k8s_behavioral/caching_test.go`) — runs in the k8s-e2e behavioral CI job (no skips); caching is not among the 4 known failures
- [x] Task 3.4: Phase 3 Manual Verification — end-to-end validation now provided by the CI behavioral suite (testcontainers K3s), superseding manual KinD validation

---
