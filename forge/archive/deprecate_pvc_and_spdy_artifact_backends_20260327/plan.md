> **Reconciled & closed 2026-06-07.** Done in 1b3972e893 (refactor: deprecate PVC and SPDY artifact backends, -6378 LOC) ~30 min after track creation. DaemonSetBackend is now the sole StorageBackend; PVC/SPDY files deleted; cache modes are hostpath/emptydir only.
>
> Reviewed via a parallel track audit; no further work needed (see closure reason). Original plan preserved below for the record.

# Implementation Plan: Deprecate PVC and SPDY Artifact Backends

## Phase 1: Remove ArtifactStoreVolume and PVC volume type

- [ ] Delete `volume_artifactstore.go` (ArtifactStoreVolume struct, NewArtifactStoreVolume, StreamOut/StreamIn, SetExecutor)
- [ ] Delete `volume_artifactstore_test.go`
- [ ] Update `worker.go`: remove ArtifactStoreVolume returns from `CreateVolumeForArtifact()` and `LookupVolume()`
- [ ] Update `worker.go`: remove `ArtifactStoreClaim` branches — `CreateVolumeForArtifact` always returns ArtifactStoreVolume via DaemonSet path; `LookupVolume` always returns DaemonSetVolume
- [ ] Verify build compiles: `go build ./cmd/concourse`
- [ ] Phase 1 Manual Verification

---

## Phase 2: Remove artifact-helper sidecar and PVC upload logic

- [ ] Remove `buildArtifactHelperSidecar()` from `container.go` (~lines 764-819)
- [ ] Remove sidecar injection from `buildPod()` (~line 421-423)
- [ ] Remove `buildArtifactStoreVolume()` PVC volume creation (~lines 459-491) — keep only the DaemonSet hostPath branch
- [ ] Remove `uploadOutputsToArtifactStore()` PVC upload path from `process.go` (~lines 881-952, the sidecar exec branch) — keep only `recordOutputLocations()` for DaemonSet
- [ ] Remove `uploadCachesToArtifactStore()` from `process.go` (~lines 955-1001)
- [ ] Remove `uploadArtifact()` two-phase tar upload from `process.go` (~lines 1039-1128)
- [ ] Remove GCS Fuse annotation logic from `buildPodAnnotations()` in `container.go`
- [ ] Verify build compiles: `go build ./cmd/concourse`
- [ ] Phase 2 Manual Verification

---

## Phase 3: Remove SPDY streaming artifact path

- [ ] Remove `streamInputs()` from `process.go` (~lines 844-879) — init containers handle all artifact extraction in DaemonSet mode
- [ ] Remove `StreamIn()` and `StreamOut()` from `volume.go` (~lines 167-277) — no longer needed for cross-step artifact passing
- [ ] Remove `NewCacheVolume()` from `volume.go` (~lines 110-124) — only used by PVC cache path in LookupVolume
- [ ] Remove `SetExecutor()` / executor wiring on Volume that was only for SPDY streaming
- [ ] Delete `live_streaming_test.go` (SPDY end-to-end tests)
- [ ] Delete `live_artifact_store_test.go` (PVC artifact store live tests)
- [ ] Verify build compiles: `go build ./cmd/concourse`
- [ ] Phase 3 Manual Verification

---

## Phase 4: Remove CacheStoreArtifact and CacheStorePVC modes

- [ ] Remove `CacheStoreArtifact` constant and cache tar save/restore logic from `container.go` (~lines 1328-1375)
- [ ] Remove `CacheStorePVC` constant and SubPath mount logic from `container.go` (~lines 1235-1293)
- [ ] Remove `TaskCacheKey()` helper from `config.go` (only used by tar-based cache)
- [ ] Remove `CacheVolumeClaim` config field and `--kubernetes-cache-pvc` flag
- [ ] Update cache mode auto-selection in `container.go` (~lines 1311-1325): only hostpath or emptydir
- [ ] Update `factory.go`: remove `CacheVolumeClaim` condition from SetVolumeRepo wiring
- [ ] Verify build compiles: `go build ./cmd/concourse`
- [ ] Phase 4 Manual Verification

---

## Phase 5: Config and command flag cleanup

- [ ] Remove from `config.go`: `ArtifactStoreClaim`, `ArtifactStoreGCSFuse`, `ArtifactHelperImage`, `ArtifactBackendPVC` constant, `CacheStoreArtifact`/`CacheStorePVC` constants
- [ ] Remove `ArtifactBackend` field and `IsDaemonSetBackend()` / `IsPVCBackend()` — DaemonSet is now the only mode, no branching needed
- [ ] Remove `hasArtifactStore()` checks in container.go — artifact store is always present (DaemonSet)
- [ ] Remove from `command.go`: `--kubernetes-artifact-store-claim`, `--kubernetes-artifact-store-gcs-fuse`, `--kubernetes-artifact-helper-image`, `--kubernetes-artifact-backend` flags
- [ ] Remove `artifactHelperContainerName`, `artifactPVCVolumeName`, `DefaultArtifactHelperImage` constants from config.go
- [ ] Verify build compiles: `go build ./cmd/concourse`
- [ ] Phase 5 Manual Verification

---

## Phase 6: Test cleanup and final validation

- [ ] Remove PVC-specific test cases from `container_test.go` (~lines 993-1160 cache-on-PVC tests, ~lines 3043-3622 artifact store pod tests)
- [ ] Remove PVC-specific test cases from `artifact_integration_test.go`
- [ ] Update remaining tests that reference removed config fields
- [ ] Run full jetbridge test suite: `go test -count=1 ./atc/worker/jetbridge/...`
- [ ] Run go vet: `go vet ./atc/worker/jetbridge/...`
- [ ] Run CI-scoped vet: `go vet ./atc/worker/jetbridge/... ./vars/... ./tracing/... ./fly/commands/... ./fly/rc/...`
- [ ] Verify DaemonSet integration tests pass: `go test -v -count=1 ./atc/worker/jetbridge/ -run DaemonSet`
- [ ] Phase 6 Manual Verification

---
