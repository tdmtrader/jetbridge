# Implementation Plan: Storage Backend Interface Extraction

## Phase 1: Define interface and create DaemonSetBackend skeleton

Create the interface and an empty implementation that compiles. No behavior changes yet.

- [x] Create `atc/worker/jetbridge/storage.go` with `StorageBackend` interface definition
- [x] Create `atc/worker/jetbridge/storage_daemonset.go` with `DaemonSetBackend` struct and constructor
- [x] Stub all interface methods on `DaemonSetBackend` (panic with "not yet migrated" so missing migrations are obvious)
- [x] Add `storageBackend StorageBackend` field to `Worker` and `Container` structs
- [x] Wire up backend construction in `NewWorker`: if `ArtifactDaemonHostPath != ""`, create `DaemonSetBackend`
- [x] Pass backend from Worker → Container in `newContainer()`
- [x] Verify: `go build ./atc/worker/jetbridge/...` compiles, all tests still pass (stubs not called yet)
- [x] Phase 1 verification: interface exists, wired up, no behavior change

---

## Phase 2: Migrate volume creation (stepVolume, CacheVolume, ArtifactStoreVolume)

Move the hostPath vs emptyDir decision out of container.go.

- [x] Implement `DaemonSetBackend.StepVolume(name, handle, subdir)` — move hostPath logic from `container.stepVolume()`
- [x] Refactor `container.stepVolume()` to delegate to `storageBackend.StepVolume()` when backend is non-nil, else return emptyDir
- [x] Implement `DaemonSetBackend.CacheVolume(name, jobID, stepName, cachePath)` — move hostPath cache logic from `buildVolumeMounts()`
- [x] Refactor `buildVolumeMounts()` cache section to delegate to `storageBackend.CacheVolume()` when backend is non-nil, else emptyDir
- [x] Implement `DaemonSetBackend.ArtifactStoreVolume()` — move logic from `buildArtifactStoreVolume()`
- [x] Implement `DaemonSetBackend.ArtifactStoreVolumeName()` — return constant
- [x] Refactor `buildArtifactStoreVolume()` to delegate to backend
- [x] Remove `ArtifactDaemonHostPath` checks from `stepVolume()`, `buildVolumeMounts()` cache section, and `buildArtifactStoreVolume()`
- [x] Run all tests — verify no regressions
- [x] Phase 2 verification: volume creation delegated, 3 fewer conditional branches in container.go

---

## Phase 3: Migrate init containers (fetch + cleanup)

Move init container generation and bash scripts into DaemonSetBackend.

- [x] Implement `DaemonSetBackend.BuildFetchInitContainers(handle, inputs, volumes, mounts)` — move logic from `buildArtifactInitContainers()` including `daemonResolveCommand()`, `artifactLocate()`, and `hostPathForVolume()`
- [x] Refactor `container.buildArtifactInitContainers()` to delegate to backend (nil backend = return nil)
- [x] Implement `DaemonSetBackend.BuildCleanupInitContainer(handle, containerType, reused)` — move logic from `buildCleanupInitContainer()`
- [x] Refactor `container.buildCleanupInitContainer()` to delegate to backend (nil backend = return nil)
- [x] Move `daemonResolveCommand()` to `DaemonSetBackend` (private method)
- [x] Move `artifactLocate()` and `artifactSourceNode()` to `DaemonSetBackend` (private methods)
- [x] Move `hostPathForVolume()` to `DaemonSetBackend` (private helper or package-level utility)
- [x] Remove `artifactLocator` field from `Container` — it's now internal to DaemonSetBackend
- [x] Remove `ArtifactDaemonHostPath`, `ArtifactDaemonPort`, `ArtifactHelperImage` references from container.go init container functions
- [x] Run all tests — verify no regressions
- [x] Phase 3 verification: init containers delegated, no bash scripts in container.go

---

## Phase 4: Migrate scheduling affinity

Move affinity logic into DaemonSetBackend.

- [x] Implement `DaemonSetBackend.BuildAffinity(inputs)` — move logic from `buildAffinity()` and `preferredInputNode()`
- [x] Refactor `container.buildAffinity()` to delegate to backend (nil backend = return nil)
- [x] Move `preferredInputNode()` to `DaemonSetBackend` (private method)
- [x] Remove `artifactLocator` references from `buildAffinity()` and `preferredInputNode()` in container.go
- [x] Run all tests — verify no regressions
- [x] Phase 4 verification: affinity delegated, ArtifactLocator fully internal to backend

---

## Phase 5: Migrate output recording (process.go)

Move recordOutputLocations and registerDaemonAlias into DaemonSetBackend.

- [x] Implement `DaemonSetBackend.RecordOutputs(ctx, handle, nodeName, volumes, outputPaths)` — move logic from `recordOutputLocations()` and `registerDaemonAlias()`
- [x] Add `storageBackend StorageBackend` field to `execProcess` struct
- [x] Pass backend from Container → execProcess in `newExecProcess()`
- [x] Refactor `execProcess.uploadOutputsToArtifactStore()` / `recordOutputLocations()` to delegate to backend
- [x] Move `registerDaemonAlias()` to `DaemonSetBackend` (private method)
- [x] Remove `artifactLocator` and `nodeIPResolver` fields from `execProcess` — now internal to backend
- [x] Remove `ArtifactDaemonHostPath` and `ArtifactDaemonPort` references from process.go
- [x] Run all tests — verify no regressions
- [x] Phase 5 verification: process.go is storage-agnostic, no daemon references

---

## Phase 6: Migrate worker volume wrapping

Move DaemonSetVolume construction into DaemonSetBackend.

- [x] Implement `DaemonSetBackend.WrapVolumeForArtifact(key, handle, workerName, dbVolume)` — returns DaemonSetVolume
- [x] Implement `DaemonSetBackend.WrapVolumeForLookup(key, handle, workerName, dbVolume)` — returns DaemonSetVolume with source node from locator
- [x] Refactor `worker.CreateVolumeForArtifact()` to call `storageBackend.WrapVolumeForArtifact()` (nil backend = return StubVolume or DeferredVolume)
- [x] Refactor `worker.LookupVolume()` to call `storageBackend.WrapVolumeForLookup()` (nil backend = return StubVolume)
- [x] Remove `nodeIPResolver` and `artifactLocator` fields from `Worker` — now internal to backend
- [x] Remove direct `DaemonSetVolume` construction from worker.go
- [x] Run all tests — verify no regressions
- [x] Phase 6 verification: worker.go has no storage-specific types, DaemonSetVolume is internal to backend

---

## Phase 7: Cleanup and verification

Remove all remaining storage coupling from container.go/process.go/worker.go.

- [x] Grep for `ArtifactDaemonHostPath` in container.go, process.go, worker.go — must be zero
- [x] Grep for `ArtifactDaemonPort` in container.go, process.go, worker.go — must be zero
- [x] Grep for `ArtifactHelperImage` in container.go, process.go — must be zero
- [x] Grep for `artifactLocator` in container.go, process.go, worker.go — must be zero (only in storage_daemonset.go)
- [x] Grep for `nodeIPResolver` in container.go, process.go, worker.go — must be zero (except Worker constructor wiring)
- [x] Grep for `DaemonSetVolume` in worker.go — must be zero
- [x] Grep for `registerDaemonAlias` in process.go — must be zero
- [x] Grep for `daemonResolveCommand` in container.go — must be zero
- [x] Remove `artifactVolumeName()` from container.go (moved to backend)
- [x] Remove dead helper functions no longer needed in container.go
- [x] Run full test suite: `go test ./atc/worker/jetbridge/... ./cmd/artifact-daemon/...`
- [x] Run `go vet ./atc/worker/jetbridge/...`
- [x] Phase 7 verification: clean separation, all tests pass, zero coupling leaks

---

## Phase 8: Write interface-level tests

Add tests that validate the StorageBackend contract independent of Container.

- [x] Create `atc/worker/jetbridge/storage_daemonset_test.go`
- [x] Test `DaemonSetBackend.StepVolume()` returns hostPath with correct path format
- [x] Test `DaemonSetBackend.CacheVolume()` returns hostPath with stable key
- [x] Test `DaemonSetBackend.ArtifactStoreVolume()` returns hostPath volume
- [x] Test `DaemonSetBackend.BuildFetchInitContainers()` with N inputs, nil artifacts, locator hit/miss
- [x] Test `DaemonSetBackend.BuildCleanupInitContainer()` with reused/fresh/check variations
- [x] Test `DaemonSetBackend.BuildAffinity()` with inputs on multiple nodes
- [x] Test `DaemonSetBackend.RecordOutputs()` records in locator and calls daemon
- [x] Test `DaemonSetBackend.WrapVolumeForArtifact()` returns DaemonSetVolume
- [x] Test `DaemonSetBackend.WrapVolumeForLookup()` with/without locator entry
- [x] Test nil backend (no StorageBackend) — container falls back to emptyDir, no init containers, no affinity
- [x] Phase 8 verification: interface-level tests pass, backend is independently testable

---
