# Spec: Cache Backed by GCS Fuse (Artifact Store Consolidation)

**Track ID:** `cache_backed_by_gcs_fuse_20260324`
**Type:** feature

## Overview

Consolidate task caches into the existing artifact store tar/streaming pipeline instead of maintaining a separate cache PVC or hostPath mechanism. When `--kubernetes-artifact-store-claim` is configured (optionally with `--kubernetes-artifact-store-gcs-fuse`), task caches should be stored as tar files on the artifact PVC — just like step outputs already are.

Currently task caches use three separate mechanisms (PVC SubPath, hostPath, emptyDir), none of which integrate with the artifact store. This creates unnecessary complexity and means GCS Fuse support requires duplicated annotation logic.

## Current State

**Artifact store** (`container.go`, `process.go`, `volume_artifactstore.go`):
- Init containers: `tar xf /artifacts/artifacts/{handle}.tar -C {destPath}` — extract inputs before task runs
- Sidecar: `tar cf /artifacts/artifacts/{handle}.tar -C {mountPath} .` — upload outputs after task completes
- Storage format: single tar files on PVC at `artifacts/{handle}.tar` and `caches/{key}.tar`
- GCS Fuse annotation already handled for this PVC

**Task caches** (`container.go:927-990`):
- PVC branch: mounts `CacheVolumeClaim` with SubPath per cache entry (live directories, not tars)
- HostPath branch: node-local directories with stable keys
- EmptyDir branch: ephemeral fallback
- Stable key format: `job-{jobID}-{sanitizedStep}-{hash}` (from `stableCacheKey()`)

## Proposed Approach

When the artifact store is configured, reuse it for task caches:

1. **Save**: After task completes (in `uploadOutputsToArtifactStore`), also tar up each cache directory into the artifact PVC at `caches/{stableKey}.tar`
2. **Restore**: Add init containers (alongside existing input fetchers) that extract cache tars into emptyDir volumes before the main container starts
3. **Cache miss**: If the tar doesn't exist on the PVC, the init container skips gracefully (cache cold start)
4. **Precedence**: When artifact store is configured, it takes priority over `CacheVolumeClaim`/`CacheHostPath` for caches. Existing fallback chain remains for non-artifact-store deployments.

This gives us cross-node cache sharing via GCS Fuse for free — no separate flag needed.

## Requirements

1. When artifact store is configured, task caches are stored as tar files at `caches/{stableKey}.tar` on the artifact PVC
2. Cache restore happens via init containers (same pattern as artifact inputs)
3. Cache save happens via exec in artifact-helper sidecar (same pattern as artifact uploads)
4. Cache miss (tar doesn't exist) is a no-op, not an error
5. Existing PVC/hostPath/emptyDir fallback chain remains for deployments without artifact store
6. `fly clear-task-cache` should still work (DB-side clearing; stale tars are overwritten on next build)

## Acceptance Criteria

- [ ] Task caches are saved as tars to artifact PVC after task completion
- [ ] Task caches are restored from artifact PVC tars via init containers before task start
- [ ] Cache miss (no tar file) is handled gracefully — task starts with empty cache
- [ ] Cache hit on second build of same job/step shows restored data
- [ ] GCS Fuse annotation is inherited from existing artifact store config (no new flag)
- [ ] Existing PVC/hostPath/emptyDir fallback works when artifact store is not configured
- [ ] Unit tests cover save, restore, cache miss, and fallback paths

## Out of Scope

- Resource caches (get step) — already handled by artifact store's `caches/{key}.tar`
- Separate `--kubernetes-cache-gcs-fuse` flag (no longer needed)
- Cache eviction or size limits on the artifact PVC
- Modifying `fly clear-task-cache` behavior
