# Implementation Plan: Cache Backed by GCS Fuse (Artifact Store Consolidation)

## Phase 1: Cache Restore via Init Containers [checkpoint: 9dcf1f41d]

**Goal:** When the artifact store is configured, add init containers that extract cached tar files from the PVC into emptyDir cache volumes before the main container starts.

- [x] Task 1.1: Add `CacheKey(jobID, stepName, cachePath)` helper to `config.go` that returns `caches/{stableKey}.tar` using the existing `stableCacheKey()` function 19538df60
- [x] Task 1.2: Update `buildVolumeMounts()` — when `ArtifactStoreClaim != ""` and caches are present, use emptyDir volumes for caches (same as current fallback) but track them for init container generation cef00862b
- [x] Task 1.3: Update `buildArtifactInitContainers()` — for each cache, add an init container: `sh -c "test -f /artifacts/caches/{key}.tar && tar xf /artifacts/caches/{key}.tar -C {cachePath} || true"` (graceful cache miss via `|| true`) 59b2cd050
- [x] Task 1.4: Unit test cache restore init containers — verify: (a) init container generated per cache, (b) correct tar path and mount, (c) artifact PVC mounted readonly, (d) cache miss doesn't fail 59b2cd050
- [x] Task 1.5: Phase 1 Manual Verification 9dcf1f41d

## Phase 2: Cache Save via Artifact-Helper Sidecar

**Goal:** After task completion, tar up cache directories and upload them to the artifact PVC alongside step outputs.

- [x] Task 2.1: Add `uploadCachesToArtifactStore()` in `process.go` that execs `tar cf /artifacts/caches/{key}.tar -C {cachePath} .` in the artifact-helper sidecar for each cache directory aa91612b0
- [x] Task 2.2: Call `uploadCachesToArtifactStore()` from the task completion flow (after `uploadOutputsToArtifactStore()`) — needs access to container metadata (jobID, stepName) and cache paths aa91612b0
- [x] Task 2.3: Ensure artifact-helper sidecar mounts all cache emptyDir volumes (update `buildArtifactHelperSidecar()` to include cache mounts) aa91612b0
- [x] Task 2.4: Unit test cache save — verify: (a) tar command per cache, (b) correct artifact PVC path, (c) sidecar has cache volume mounts aa91612b0
- [~] Task 2.5: Phase 2 Manual Verification

## Phase 3: Precedence and Fallback

**Goal:** When artifact store is configured, it takes priority over CacheVolumeClaim/CacheHostPath for task caches. Existing fallback chain remains otherwise.

- [ ] Task 3.1: Update `buildVolumeMounts()` cache branch ordering — check `ArtifactStoreClaim` first, then `CacheVolumeClaim`, then `CacheHostPath`, then emptyDir
- [ ] Task 3.2: Unit test precedence — verify artifact store path is chosen when both artifact store and cache PVC are configured
- [ ] Task 3.3: Unit test fallback — verify PVC/hostPath/emptyDir still work when artifact store is not configured
- [ ] Task 3.4: Phase 3 Manual Verification — run caching behavioral tests on KinD

---
