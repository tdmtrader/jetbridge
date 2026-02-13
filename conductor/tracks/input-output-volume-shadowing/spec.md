# Spec: Fix Input/Output Volume Shadowing When Names Match

## Overview

When a task declares an input and output with the same name (a common Concourse pattern for
in-place modification), JetBridge creates two separate emptyDir volumes mounted at the same
path. Kubernetes mounts them in order, causing the output volume to shadow the input volume.
The task sees an empty directory instead of its input data.

The fix: detect overlapping mount paths in `buildVolumeMounts()` and share a single volume
when an input and output target the same path.

## Bug Details

In `container.go` `buildVolumeMounts()`, inputs are processed first (creating `input-0`,
`input-1`, etc.) then outputs (creating `output-0`, `output-1`, etc.). Each gets its own
`emptyDir` volume. When paths collide, K8s applies both mounts — the later output mount
shadows the earlier input mount, making input data inaccessible.

Example pipeline trigger:
```yaml
task: build
config:
  inputs:  [{name: repo}]
  outputs: [{name: repo}]
  run: {path: ./repo/build.sh}
```

Expected: `repo/` contains input data and modifications are captured as output.
Actual: `repo/` is empty (output emptyDir shadows input).

## Requirements

1. In `buildVolumeMounts()`, detect when an output path matches an input path.
2. When paths overlap, reuse the input's volume for the output — do not create a second
   volume or mount.
3. The shared volume must be populated with input data (via init container) AND captured
   as output after the task completes.
4. Non-overlapping inputs and outputs are unaffected.

## Acceptance Criteria

- [ ] Task with same-name input and output shares a single volume mount.
- [ ] Input data is present in the shared mount when the task runs.
- [ ] Output is correctly registered from the shared mount after the task completes.
- [ ] Non-overlapping inputs and outputs continue to work as before.
- [ ] Unit tests cover: same-name overlap, different-name no overlap, custom path overlap.

## Out of Scope

- Multiple outputs sharing the same path (undefined behavior, not a supported pattern).
- Input/output path overlap via custom `path:` fields pointing to the same directory
  (stretch goal — detect by resolved path, not just name).
