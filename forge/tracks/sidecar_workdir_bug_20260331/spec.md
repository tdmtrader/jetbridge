# Spec: Fix Sidecar Working Directory Not Inheriting Main Container Dir

## Overview

Sidecar containers in the K8s runtime do not inherit the main container's working directory. When a pipeline step uses `cd` to navigate to an input directory (a relative path like `my-input`), this works in the main task container but fails in sidecars because the sidecar starts in the image's default `WORKDIR` (often `/`) instead of the step's working directory (e.g., `/tmp/build/workdir`).

The volumes are mounted correctly — the sidecar receives the same `volumeMounts` as the main container — but relative path resolution fails because the sidecar's `WorkingDir` is not set.

## Root Cause

In `atc/worker/jetbridge/container.go`:

- **Main container** (line 377-380, 414): `WorkingDir` is set to `processSpec.Dir` or `containerSpec.Dir`
- **Sidecar container** (line 512): `WorkingDir` is set only to `sc.WorkingDir` from the sidecar config, which is typically empty

`buildSidecarContainers()` does not receive or apply the main container's effective working directory as a fallback.

## Requirements

1. When a sidecar does not specify its own `workingDir`, it must inherit the main container's effective working directory (`containerSpec.Dir`).
2. When a sidecar specifies its own `workingDir`, that takes precedence.
3. Existing sidecars that explicitly set `workingDir` must not be affected.

## Technical Approach

Pass the main container's effective `dir` into `buildSidecarContainers()` and use it as the default when `sc.WorkingDir` is empty.

### Key Files
- `atc/worker/jetbridge/container.go` — `buildSidecarContainers()` and `buildPod()`
- `atc/worker/jetbridge/container_test.go` — sidecar unit tests

## Acceptance Criteria

- [ ] Sidecar containers start in the same working directory as the main container when no explicit `workingDir` is set
- [ ] Sidecars with explicit `workingDir` still use their configured value
- [ ] Unit tests cover both cases (inherit and override)
- [ ] Existing sidecar tests continue to pass

## Out of Scope

- Adding a new `dir` field to the sidecar YAML config
- Changes to `processSpec.Dir` runtime override behavior
- Sidecar-specific volume mount changes (volumes are already correct)
