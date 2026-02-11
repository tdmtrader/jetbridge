# Spec: Dir Volume Bug

**Track ID:** `dir_volume_bug_20260211`
**Type:** bugfix

## Overview

`buildVolumeMountsForSpec()` in `worker.go` creates a runtime `-dir` volume for `spec.Dir` (the container's working directory). However, `buildVolumeMounts()` in `container.go` — which produces the actual Kubernetes Pod spec — only creates emptyDir volumes for inputs, outputs, and caches. It never creates an emptyDir for the Dir volume.

This means the pod has no K8s volume backing the working directory. When `uploadOutputsToArtifactStore()` iterates all container volumes and tries to tar them for cross-node passing, the Dir volume tar fails because there's no emptyDir mount at that path.

## Requirements

1. `buildVolumeMounts()` must create a K8s emptyDir volume and mount for `spec.Dir` when it is set.
2. The Dir emptyDir must be mounted before input/output/cache volumes so path ordering is consistent.
3. `uploadOutputsToArtifactStore()` must successfully include Dir volume contents.

## Acceptance Criteria

- [ ] Pod spec includes an emptyDir volume + mount for `spec.Dir` when Dir is non-empty
- [ ] Pod spec has no Dir volume when `spec.Dir` is empty (e.g. check containers)
- [ ] `uploadOutputsToArtifactStore` succeeds for containers with a Dir volume
- [ ] Existing input/output/cache volume tests still pass
- [ ] Unit test covers the Dir emptyDir creation in `buildVolumeMounts()`

## Out of Scope

- Changes to how Dir is used in `buildVolumeMountsForSpec()` (worker-level)
- Changes to artifact store upload logic beyond verifying it works
- Volume passing optimization or refactoring
