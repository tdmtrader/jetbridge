# Spec: Fix hostPath Reuse and Enable Resource Caching

**Track ID:** `fix_hostpath_reuse_and_enable_resource_caching_20260328`
**Type:** bugfix

## Overview

Two related issues with the DaemonSet artifact backend:

### Bug 1: hostPath directory not empty on container reuse

In Concourse, `FindOrCreateContainer` looks up a container by its owner `{buildID, planID, teamID}`. If a container already exists in the DB (e.g., ATC restarted mid-build), it **reuses the same container handle**. This is the "Find" in "FindOrCreateContainer" — it's crash recovery behavior from upstream Concourse.

In the Garden runtime, this is fine — the Garden container has its own isolated filesystem. But in DaemonSet mode, the container handle determines the **hostPath directory** (`steps/<handle>/dir/`). When the first pod ran, it populated this directory (e.g., `git clone`). When the ATC retries with the same handle, a new pod mounts the same hostPath, and the resource script fails because the directory isn't empty.

**Symptom:** `fatal: destination path '/tmp/build/get' already exists and is not an empty directory.`

### Bug 2: Resource cache hits don't work (`SkipResourceCache = true`)

`SkipResourceCache()` is forced to `true`, disabling resource caching entirely. Every get step re-executes the resource script even when the exact version was already fetched. On a cache hit, the get step would return a `DaemonSetVolume` with the cached volume's handle. But:

1. The ArtifactLocator has no entry for this handle (it was recorded under the *original* get step's container handle, not the volume handle)
2. The daemon's filesystem scan can't map the volume handle to a disk path either — the on-disk layout is `steps/<container-handle>/<output>/`, not `steps/<volume-handle>/`

The mapping from **volume handle → container handle + output name** only exists in the ArtifactLocator, which is ephemeral.

## Root Cause Analysis

Both bugs stem from the same architectural mismatch: upstream Concourse assumes containers have their own isolated filesystems, but DaemonSet mode uses **shared hostPath directories keyed by container handle**. The hostPath is neither cleaned on reuse nor discoverable from a volume handle.

### Why containers are reused

The `buildStepContainerOwner{buildID, planID, teamID}` lookup is intentional crash recovery. In upstream Concourse:
- ATC restarts → finds existing Garden container → attaches to it → process continues
- The container filesystem is intact and isolated

In DaemonSet mode:
- ATC restarts → finds existing DB container row → creates NEW pod with same handle
- The hostPath directory has stale data from the first pod's execution

### Why volume handles don't map to disk paths

When a get step produces output:
- Volume handle: `vol-abc123` (random UUID from DB)
- Container handle: `container-xyz789` (different random UUID)
- Disk path: `steps/container-xyz789/dir/`
- Locator records: `vol-abc123 → {node-a, container-xyz789/dir}`

On cache hit, we have `vol-abc123` but no way to derive `container-xyz789/dir` without the locator.

## Design

### Fix 1: Clean hostPath on container reuse

When `FindOrCreateContainer` finds an existing container (the "Find" path), clean the hostPath directory before creating the new pod. This is safe because:
- The old pod was either completed (data already consumed by downstream) or failed (data is stale)
- If the ATC is retrying, we want a fresh execution

Implementation: in `Container.Run()`, before creating the pod, check if the hostPath directory exists and is non-empty. If so, remove its contents. The `stepVolume()` uses `HostPathDirectoryOrCreate`, so the directory will be recreated empty.

Concretely: add an init container (or pre-run cleanup) that runs `rm -rf /artifacts/steps/<handle>/*` before the main execution.

### Fix 2: Register volume-handle → disk-path in the daemon

When `recordOutputLocations` runs after a step completes, register the **volume handle** as an alias for the disk path in the artifact-daemon:

```
POST /register {key: "vol-abc123", local_path: "/var/concourse/artifacts/steps/container-xyz789/dir"}
```

The daemon then knows both keys:
- `container-xyz789/dir` → disk path (from output recording)
- `vol-abc123` → same disk path (alias for cache lookups)

On a cache hit, the downstream init container resolves `vol-abc123` via the daemon, which returns the data.

The daemon's registry survives within its pod lifecycle. On daemon restart, the filesystem scan doesn't recover these aliases — but the ArtifactLocator provides the mapping for in-flight builds, and the daemon's `/resolve` filesystem fallback can check both `steps/<key>` patterns.

### Fix 3: Enable `SkipResourceCache = false`

With fixes 1 and 2 in place:
- Cache hits return a volume handle that the daemon can resolve (via registered alias)
- The downstream step's init container calls the daemon, which serves the cached data
- No stale directory issues because cache hits skip the get step entirely (no pod created)

## Requirements

1. When `FindOrCreateContainer` reuses an existing container handle, the hostPath directory must be cleaned before the new pod runs
2. `recordOutputLocations` must register volume handles as daemon aliases pointing to the output disk path
3. `SkipResourceCache()` returns `false` — cache hits skip the get step and serve cached data via the daemon
4. ATC restart mid-build retries the get step successfully (clean hostPath, fresh clone)
5. Cache hit → downstream task step resolves the cached volume via the daemon

## Acceptance Criteria

- [ ] Get step retry after ATC restart succeeds (no "destination path already exists")
- [ ] Resource cache hit skips the get step (build log shows "found existing resource cache")
- [ ] Downstream task step after cache hit receives the correct input data
- [ ] `go test ./atc/worker/jetbridge/...` passes
- [ ] CI pipeline: build-and-vet, unit-tests, k8s-runtime-tests all green

## Out of Scope

- Persisting daemon registry aliases across daemon restarts (filesystem scan fallback is sufficient)
- Cross-node cache sharing (cache hits only work on the same node as the original get)
- Resource cache garbage collection changes
