# Spec: Storage Backend Interface Extraction

**Track ID:** `storage_backend_interface_20260330`
**Type:** refactor

## Overview

The JetBridge storage layer has no abstraction boundary. DaemonSet-specific logic is scattered across 4 files, ~20 functions, and ~15 conditional branches on `config.ArtifactDaemonHostPath`. Every refactor to the storage layer requires touching all of these sites in lockstep, and a mismatch between any two (e.g., hostPath subdir name in `buildVolumeMounts` vs daemon key in `recordOutputLocations`) produces silent failures.

This track extracts a `StorageBackend` interface that encapsulates all storage-specific decisions behind a single seam. The existing DaemonSet logic moves into a `DaemonSetBackend` implementation. Container orchestration code calls the interface — no more `if config.ArtifactDaemonHostPath != ""` branches.

## Why

- **Reliability**: Every storage refactor has introduced bugs because the same decision (key format, path layout, resolution strategy) is made independently in 5+ places. An interface forces a single source of truth.
- **Testability**: Currently, testing storage behavior requires constructing a full `Container` with real K8s volume specs. With an interface, storage can be tested in isolation and faked in container tests.
- **Future flexibility**: NFS, S3, GCS Fuse, reflink — all become a new implementation rather than 15 new conditional branches.
- **Debuggability**: When artifact resolution fails, the current code has bash scripts in init containers, HTTP calls in process.go, and path surgery in container.go all contributing. With an interface, the resolution path is in one place.

## Current State: 20 Coupling Points

The following functions in `container.go`, `process.go`, and `worker.go` contain DaemonSet-specific logic that must move behind the interface:

### container.go (12 functions)
1. `stepVolume(name, subdir)` — hostPath vs emptyDir based on config
2. `buildArtifactStoreVolume()` — creates the DaemonSet hostPath volume
3. `buildArtifactInitContainers(volumes, mounts)` — generates fetch init containers with wget scripts
4. `daemonResolveCommand(key, hostDest)` — embedded bash script for daemon /resolve
5. `buildCleanupInitContainer()` — rm -rf stale hostPath data
6. `artifactVolumeName()` — returns constant volume name
7. `buildVolumeMounts()` — cache mode auto-detection, hostPath cache paths
8. `buildAffinity()` — node label affinity for artifact-cache=ready
9. `preferredInputNode()` — queries ArtifactLocator for scheduling
10. `artifactLocate(key)` — nil-check wrapper around locator
11. `artifactSourceNode(key)` — nil-check wrapper around locator
12. `hostPathForVolume(volumes, name)` — extracts hostPath from volume spec

### process.go (2 functions)
13. `recordOutputLocations(nodeName)` — records daemon keys in ArtifactLocator + calls registerDaemonAlias
14. `registerDaemonAlias(nodeName, volumeKey, diskPath)` — HTTP POST to daemon /register

### worker.go (2 functions)
15. `CreateVolumeForArtifact(ctx, teamID)` — always returns `DaemonSetVolume`
16. `LookupVolume(ctx, handle)` — always returns `DaemonSetVolume`

### volume_daemonset.go (entire file)
17. `DaemonSetVolume.StreamOut()` — HTTP GET to daemon
18. `DaemonSetVolume.StreamIn()` — rejects with error
19. `DaemonSetVolume.daemonURL()` — constructs daemon HTTP URL

### Additional coupling
20. `ArtifactLocator` — in-memory map only meaningful for DaemonSet scheduling

## Design

### The Interface

```go
// StorageBackend encapsulates all storage-specific decisions for artifact
// lifecycle: how step volumes are created, how artifacts are fetched into
// containers, how outputs are recorded, and how scheduling affinity is
// determined.
type StorageBackend interface {
    // StepVolume creates a K8s Volume for a step's dir/input/output.
    // The name is the K8s volume name (e.g. "input-1"), handle is the
    // container handle, and subdir identifies the step output (e.g. "result").
    StepVolume(name, handle, subdir string) corev1.Volume

    // CacheVolume creates a K8s Volume for a task cache.
    // jobID/stepName/cachePath are used to generate stable keys.
    CacheVolume(name string, jobID int, stepName, cachePath string) corev1.Volume

    // ArtifactStoreVolume returns the pod-level volume that init containers
    // need to read/write artifact data, or nil if not needed.
    ArtifactStoreVolume() *corev1.Volume

    // ArtifactStoreVolumeName returns the K8s volume name for the artifact
    // store, or "" if not applicable.
    ArtifactStoreVolumeName() string

    // BuildFetchInitContainers creates init containers to resolve input
    // artifacts into the pod's volumes before the main container starts.
    // Returns nil when no init containers are needed.
    BuildFetchInitContainers(
        handle string,
        inputs []runtime.Input,
        podVolumes []corev1.Volume,
        mainMounts []corev1.VolumeMount,
    ) []corev1.Container

    // BuildCleanupInitContainer creates an init container to clean stale
    // data when a container handle is reused (crash recovery).
    // Returns nil when cleanup is not needed.
    BuildCleanupInitContainer(handle string, containerType db.ContainerType, reused bool) *corev1.Container

    // BuildAffinity returns pod scheduling affinity based on where input
    // artifacts are located. Returns nil when no affinity is needed.
    BuildAffinity(inputs []runtime.Input) *corev1.Affinity

    // RecordOutputs records where step outputs landed after execution.
    // Called after a step completes so downstream steps can locate artifacts.
    RecordOutputs(ctx context.Context, handle, nodeName string, volumes []*Volume, outputPaths map[string]string)

    // WrapVolumeForArtifact creates a runtime.Volume for an artifact created
    // via CreateVolumeForArtifact (fly execute uploads).
    WrapVolumeForArtifact(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume

    // WrapVolumeForLookup creates a runtime.Volume for a volume found via
    // LookupVolume (cache hit resolution).
    WrapVolumeForLookup(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume
}
```

### DaemonSetBackend Implementation

All existing DaemonSet logic moves into `DaemonSetBackend`:

```go
type DaemonSetBackend struct {
    config          Config
    artifactLocator *ArtifactLocator
    nodeIPResolver  *NodeIPResolver
    helperImage     string
}
```

This struct owns:
- `StepVolume` → hostPath under `<daemonPath>/steps/<handle>/<subdir>/`
- `CacheVolume` → hostPath with stable keys under `<daemonPath>/caches/`
- `BuildFetchInitContainers` → wget scripts calling daemon /resolve
- `BuildCleanupInitContainer` → rm -rf + mkdir scripts
- `BuildAffinity` → hard label + soft preferred node from locator
- `RecordOutputs` → ArtifactLocator.Record + registerDaemonAlias HTTP POST
- `WrapVolume*` → returns DaemonSetVolume

### EmptyDirBackend (implicit fallback)

When no storage backend is configured, the zero-value behavior should be:
- `StepVolume` → emptyDir
- `CacheVolume` → emptyDir
- All other methods → return nil (no init containers, no affinity, no recording)

This is not a separate implementation — it's what `Container` does when `StorageBackend` is nil. This preserves the current behavior for non-DaemonSet deployments without requiring explicit configuration.

### Wiring

```go
// In Worker constructor or factory:
var backend StorageBackend
if config.ArtifactDaemonHostPath != "" {
    backend = NewDaemonSetBackend(config, artifactLocator, nodeIPResolver)
}
worker.storageBackend = backend  // nil = emptyDir fallback

// In Container, the backend is passed through:
container.storageBackend = worker.storageBackend
```

### Container After Refactor

container.go becomes storage-agnostic:

```go
func (c *Container) stepVolume(name, subdir string) corev1.Volume {
    if c.storageBackend != nil {
        return c.storageBackend.StepVolume(name, c.handle, subdir)
    }
    return emptyDirVolume(name)  // default fallback
}

func (c *Container) buildArtifactInitContainers(...) ([]corev1.Container, error) {
    if c.storageBackend == nil {
        return nil, nil
    }
    return c.storageBackend.BuildFetchInitContainers(c.handle, c.containerSpec.Inputs, volumes, mounts), nil
}
```

No more `if config.ArtifactDaemonHostPath != ""` anywhere in container.go.

## Requirements

1. Define `StorageBackend` interface in `atc/worker/jetbridge/storage.go`
2. Implement `DaemonSetBackend` in `atc/worker/jetbridge/storage_daemonset.go`
3. Move all 20 coupling points behind the interface — zero `ArtifactDaemonHostPath` checks remain in container.go, process.go, or worker.go
4. Container and Worker receive `StorageBackend` (nil = emptyDir fallback)
5. All 120+ existing behavioral tests pass without modification
6. No new config flags — `ArtifactDaemonHostPath` still triggers DaemonSet mode, just at the wiring layer not inside every function
7. `ArtifactLocator` and `NodeIPResolver` become internal to `DaemonSetBackend`, not passed through Container/Worker
8. The `DaemonSetVolume` type remains but is only constructed inside `DaemonSetBackend`

## Acceptance Criteria

- [ ] `StorageBackend` interface defined with all methods
- [ ] `DaemonSetBackend` implements `StorageBackend` with all existing DaemonSet logic
- [ ] container.go has zero references to `ArtifactDaemonHostPath`, `ArtifactDaemonPort`, `ArtifactHelperImage`, `artifactLocator`, or `nodeIPResolver`
- [ ] process.go has zero references to `registerDaemonAlias` or `ArtifactDaemonHostPath`
- [ ] worker.go constructs `StorageBackend` once and passes it through
- [ ] `go test ./atc/worker/jetbridge/... ./cmd/artifact-daemon/...` passes
- [ ] No behavior change — existing behavioral tests pass unmodified
- [ ] `go vet ./atc/worker/jetbridge/...` clean

## Out of Scope

- Implementing additional backends (NFS, S3, etc.) — future tracks
- Changing the artifact daemon itself — it stays as-is
- Modifying the exec step layer (get/put/task steps)
- Changing the database schema
- Adding new config flags
