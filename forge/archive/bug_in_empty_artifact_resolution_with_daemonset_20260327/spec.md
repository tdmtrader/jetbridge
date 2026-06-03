# Spec: Bug in empty artifact resolution with DaemonSet

**Track ID:** `bug_in_empty_artifact_resolution_with_daemonset_20260327`
**Type:** bug

## Overview

DaemonSet artifact fetching always fails — `artifactLocate(key)` returns `hasLoc = false` because recording and lookup use different handle namespaces. This is not intermittent; it is 100% broken.

### Root Cause: Handle Mismatch

**Recording path** (`process.go:1165`):
```go
key := ArtifactKey(vol.Handle())  // vol from buildVolumeMountsForSpec
```
`vol.Handle()` returns step-local handles like `"<containerHandle>-output-<name>"` (e.g., `"abc123-output-result"`), constructed at `worker.go:144`.

**Lookup path** (`container.go:533`):
```go
key := ArtifactKey(input.Artifact.Handle())  // artifact from LookupVolume
```
`input.Artifact` is a `DaemonSetVolume` created by `LookupVolume` (`worker.go:260-266`) using the **DB volume handle** (e.g., `"vol-12345"`).

`ArtifactKey` wraps to `"artifacts/<handle>.tar"` (vestigial naming — used as a map key, not a file path), so:
- Recorded as: `"artifacts/abc123-output-result.tar"`
- Looked up as: `"artifacts/vol-12345.tar"`
- **They never match.**

### Secondary Bug: No Fail-Fast

When `hasLoc` is false (`container.go:548-554`), `sourceHostDir` is `""` and `SOURCE_NODE` is unset. The generated shell script hits `[ -z "$SOURCE_NODE" ]` → true, then runs `cp -a /. <dest>/` — attempting to copy from the filesystem root instead of failing immediately.

### What Works Correctly

The underlying DaemonSet artifact architecture is sound:
- Outputs are stored directly on hostPath (`<hostPath>/steps/<handle>/<output-name>/`) — no tar files on disk
- Local fetch uses `cp -a` from the source hostPath directory
- Remote (cross-node) fetch uses HTTP GET from the artifact daemon, which tars the directory on-the-fly and streams it
- No sidecar, no PVC mounts — all hostPath

Only the key used for locator recording/lookup is wrong.

## Requirements

1. Fix the handle mismatch so `recordOutputLocations` records using the DB volume handle (the same handle that `LookupVolume` will use downstream)
2. Add fail-fast when artifact location is unknown (defense in depth)

## Fix Strategy: Option A — Record using DB volume handle

At recording time (`recordOutputLocations` in `process.go`), the container has access to its DB container via `p.container.dbContainer`. The DB container knows its created volumes and their mount paths. Use this to map each output mount path → DB volume handle, then record `ArtifactKey(dbVolumeHandle)` instead of `ArtifactKey(stepLocalHandle)`.

This aligns the recording key with what `LookupVolume` uses downstream, requiring no changes to the lookup side.

## Acceptance Criteria

- [ ] Artifact locator records using DB volume handles, matching what `LookupVolume` uses
- [ ] `buildArtifactInitContainers` fails fast when locate returns false in DaemonSet mode
- [ ] `daemonSetFetchCommand` generates `exit 1` when `sourceHostDir` is empty (defense in depth)
- [ ] Unit test: DaemonSet artifact round-trip (record with DB handle → lookup) succeeds
- [ ] Unit test: missing artifact location produces fail-fast error
- [ ] All existing `make test-unit` tests pass

## Out of Scope

- Persistent artifact location storage (locator is ephemeral by design)
- ATC restart recovery for in-flight builds
- Renaming `ArtifactKey` format (vestigial `.tar` suffix is harmless)
