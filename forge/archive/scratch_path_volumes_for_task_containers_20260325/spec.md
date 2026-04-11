# Spec: Scratch Path Volumes for Task Containers

**Track ID:** `scratch_path_volumes_for_task_containers_20260325`
**Type:** feature

## Overview

buildkitd (and similar tools) require a real filesystem (ext4/xfs) for overlayfs to work. On GKE, the container's root filesystem is itself an overlay mount, so nested overlayfs fails and buildkitd falls back to the native snapshotter — copying full image layers instead of using overlay diffs. This causes builds to consume ~100GB instead of 5-15GB.

The existing `caches` mechanism is not appropriate because all non-emptyDir cache modes (artifact, PVC, hostPath) would persist the entire buildkitd working directory between builds, which is large and ephemeral by nature.

## Requirements

1. Users can declare `scratch_paths` in a task config — a list of paths that receive ephemeral emptyDir volumes with no cache semantics.
2. Scratch volumes are pod-scoped and destroyed when the pod terminates. They are never saved, restored, or exported.
3. Scratch volumes are parallel-safe — concurrent builds of the same job each get independent volumes.
4. Scratch volumes are mounted in sidecars alongside other volumes (matching existing sidecar mount behavior).
5. Relative scratch paths are resolved against the task's working directory (matching cache path behavior).

## Technical Approach

### Data flow

```
task.yml scratch_paths:
  → atc.TaskConfig.ScratchPaths []TaskScratchConfig
  → atc/exec/task_step.go containerSpec()
  → runtime.ContainerSpec.ScratchPaths []string
  → jetbridge/container.go buildVolumeMounts()
  → corev1.Volume{EmptyDir} + corev1.VolumeMount
```

### Key files

| File | Change |
|------|--------|
| `atc/task.go` | Add `ScratchPaths []TaskScratchConfig` to `TaskConfig`, add `TaskScratchConfig{Path}` type |
| `atc/runtime/types.go` | Add `ScratchPaths []string` to `ContainerSpec` |
| `atc/exec/task_step.go` | Map `TaskConfig.ScratchPaths` → `ContainerSpec.ScratchPaths` (parallel to caches wiring at line ~534) |
| `atc/worker/jetbridge/container.go` | Add scratch volume creation in `buildVolumeMounts()` — plain emptyDir, no cache store logic |
| `atc/worker/jetbridge/container_test.go` | Test scratch volumes are created as emptyDir and mounted correctly |
| `atc/task_test.go` | Test parsing and validation of scratch_paths in task config |

### Design decisions

- **Separate from caches:** scratch_paths intentionally bypass all cache store logic. They are never part of artifact upload, PVC SubPath, hostPath, or init container restore flows.
- **Same type pattern as caches:** `TaskScratchConfig{Path}` mirrors `TaskCacheConfig{Path}` for consistency and future extensibility (e.g., size limits via `emptyDir.sizeLimit`).
- **Sidecar sharing is automatic:** sidecars already receive all main container mounts, so scratch paths are shared without additional code.

## Acceptance Criteria

- [ ] A task config with `scratch_paths: [{path: /scratch}]` creates a pod with an emptyDir volume at `/scratch`.
- [ ] The scratch volume does NOT appear in cache entries, artifact uploads, or init container restore commands.
- [ ] Two concurrent builds of the same job each get independent scratch volumes (no shared state).
- [ ] Sidecars receive the scratch volume mounts.
- [ ] Relative paths are resolved against the working directory.
- [ ] Task config parsing accepts and validates scratch_paths (rejects empty paths, etc.).
- [ ] Existing cache behavior is completely unaffected.

## Out of Scope

- Size limits on scratch volumes (future enhancement via `emptyDir.sizeLimit`)
- tmpfs-backed scratch (future enhancement via `emptyDir.medium: Memory`)
- UI rendering of scratch paths
- fly CLI changes (scratch_paths flows through task config YAML, no CLI flags needed)
