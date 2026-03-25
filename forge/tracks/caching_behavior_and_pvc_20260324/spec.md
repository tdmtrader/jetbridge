# Spec: Fix Task Cache Persistence in K8s Runtime

**Track ID:** `caching_behavior_and_pvc_20260324`
**Type:** bug

## Overview

Task caches in the jetbridge K8s runtime are completely broken. Two independent bugs prevent caches from ever being reused across builds:

1. **Cache key is per-container UUID.** `buildVolumeMounts()` in `container.go:922` generates SubPath keys as `<container-handle>-cache-<index>`, where `handle` is a unique UUID per container. Every build creates a new container, so every build gets a fresh cache directory — even with a persistent PVC backing it.

2. **Fallback uses emptyDir.** When no cache PVC is configured (`container.go:929-942`), caches use `emptyDir` volumes which are destroyed when the pod terminates. This means caches are always empty on the next build.

The DB already models cache identity correctly: `task_caches` is keyed by `(job_id, step_name, path)`. The fix is to use this stable identity as the on-disk cache key, and switch the non-PVC fallback from `emptyDir` to `hostPath` for node-local persistence.

## Root Cause

**File:** `atc/worker/jetbridge/container.go`, lines 905–942

```go
// BUG: c.handle is a per-container UUID — never reused
cacheHandle := fmt.Sprintf("%s-cache-%d", c.handle, i)
```

The metadata needed for a stable key (`c.metadata.JobID`, `c.metadata.StepName`, cache path) is already available on the Container struct at mount-building time.

## Requirements

1. Generate a deterministic, job-scoped cache key from `(jobID, stepName, cachePath)` instead of the container handle
2. When a cache PVC is configured, use the stable key as the SubPath
3. When no cache PVC is configured, use `hostPath` volumes at a well-known base directory (e.g. `/var/concourse/cache/<key>`) instead of `emptyDir`
4. Make the hostPath base directory configurable (CLI flag + helm value)
5. Add GC logic to clean up stale host cache directories for jobs/steps that no longer exist
6. Existing behavioral tests (`topgun/k8s_behavioral/caching_test.go`) must pass — "persists caches across builds" should now actually persist

## Acceptance Criteria

- [x] Cache SubPath key is deterministic: same job + step + path always maps to same directory
- [ ] Builds of the same job reuse cached data (behavioral test: CACHE HIT on second build)
- [ ] Different jobs have isolated caches (behavioral test: scope-a vs scope-b)
- [ ] `fly clear-task-cache` clears the correct cache directory
- [ ] hostPath fallback works when no PVC is configured
- [ ] hostPath base directory is configurable via `--kubernetes-cache-host-path`
- [ ] Helm chart exposes `cacheHostPath` value
- [ ] Unit tests cover stable key generation and both PVC/hostPath code paths

## Out of Scope

- GCS Fuse or RWX storage backends for caches (future track)
- Resource caches (get step) — this track focuses on task caches only
- Cross-node cache sharing — hostPath is inherently node-local; that's acceptable for caches
