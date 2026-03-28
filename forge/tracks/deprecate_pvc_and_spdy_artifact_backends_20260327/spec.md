# Spec: Deprecate PVC and SPDY Artifact Backends

**Track ID:** `deprecate_pvc_and_spdy_artifact_backends_20260327`
**Type:** refactor

## Overview

The JetBridge K8s runtime has three artifact management approaches that evolved incrementally:

1. **SPDY/DeferredVolume** — exec-based tar streaming between pods (no persistence, no cross-node)
2. **PVC Backend** — shared PVC with tar/untar cycles and artifact-helper sidecar
3. **DaemonSet Backend** — node-local hostPath with HTTP daemon for cross-node transfers

The DaemonSet approach is strictly superior for our deployment model. The other two add complexity, code surface, and configuration burden with no benefit. This track removes them, simplifying the codebase and making DaemonSet the sole artifact backend.

## Why

- **PVC backend** adds per-pod sidecar overhead (50m CPU, 64Mi RAM), tar/untar on every read/write, PVC contention under concurrency, and GKE-specific GCS Fuse coupling
- **SPDY streaming** cannot pass artifacts across builds or nodes — it's effectively broken for real pipelines
- Neither is used in production (both `theborg` and the multi-node cluster run DaemonSet mode)
- Three code paths for the same concern means three places to debug, test, and maintain
- Config surface is confusing: `ArtifactStoreClaim`, `ArtifactBackend`, `ArtifactStoreGCSFuse`, `ArtifactHelperImage` — users shouldn't need to choose

## Requirements

1. Remove the PVC artifact store backend (`ArtifactStoreClaim` config path, `ArtifactStoreVolume` type, artifact-helper sidecar injection, tar upload/download logic)
2. Remove the SPDY streaming artifact path (`streamInputs()` in process.go, `StreamIn`/`StreamOut` on DeferredVolume for cross-step artifact passing)
3. Remove `CacheStoreArtifact` cache mode (tar-based cache save/restore on PVC)
4. Remove `CacheStorePVC` cache mode (SubPath mounts on dedicated cache PVC)
5. Simplify config: remove `--kubernetes-artifact-store-claim`, `--kubernetes-artifact-store-gcs-fuse`, `--kubernetes-artifact-helper-image`, `--kubernetes-artifact-backend`, `--kubernetes-cache-pvc`
6. Simplify `LookupVolume` / `CreateVolumeForArtifact` to only produce DaemonSet volume types
7. Keep `CacheStoreHostPath` and `CacheStoreEmptyDir` as valid cache modes
8. Keep `DeferredVolume` for within-pod volume mounts (dir, inputs, outputs) — only remove its use for cross-step artifact streaming
9. All existing DaemonSet tests must continue to pass
10. Remove PVC-specific and SPDY-specific test suites that test deleted code paths

## Acceptance Criteria

- [ ] `go build ./cmd/concourse` succeeds with no PVC/SPDY artifact code
- [ ] `go vet ./atc/worker/jetbridge/...` clean
- [ ] `go test ./atc/worker/jetbridge/...` passes (DaemonSet tests green)
- [ ] No references to `ArtifactStoreClaim`, `ArtifactStoreGCSFuse`, `ArtifactHelperImage`, `ArtifactBackendPVC` in production code
- [ ] No `artifact-helper` sidecar injection code remains
- [ ] Config struct has no PVC-related fields
- [ ] CI pipeline passes (`build-and-vet`, `unit-tests`, `k8s-runtime-tests`)

## Out of Scope

- Changes to DaemonSet artifact logic itself
- Adding new features to the DaemonSet backend
- Helm chart changes (those reference env vars, not Go code)
- Removing the `artifact-daemon` binary or DaemonSet K8s manifests
