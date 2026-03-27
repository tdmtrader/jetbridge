# Spec: DaemonSet Direct HostPath Mounts

**Track ID:** `daemonset_direct_hostpath_20260327`
**Type:** feature
**Depends on:** `daemonset_artifact_cache_20260326`

## Overview

The DaemonSet artifact cache has implementation gaps: the ArtifactLocator is never instantiated in production wiring, uploads still run unnecessarily, and the sidecar is created when it shouldn't be. Additionally, the current implementation stores artifacts as tar files (carried over from the PVC pattern) when direct hostPath directories would eliminate serialization overhead.

This track:
1. Wires the ArtifactLocator into production code (atccmd → Worker → Reaper)
2. Reworks DaemonSet mode to use direct hostPath directory mounts
3. Eliminates tar/untar for local artifact passing (cp -a for inputs)
4. Adds build log events showing fetch source (local vs remote)
5. Makes the TTL sweeper build-aware (skip directories with active pods)

## Design

### Directory layout

```
/var/concourse/artifacts/
  steps/                    ← per-build step artifacts
    <handle>/               ← one directory per container handle
      <output-name>/        ← output data, written directly by step
  caches/                   ← per-job task caches
    <stable-key>/           ← persistent across builds
```

### Output volumes
Step outputs mount hostPath subdirectories directly (not emptyDir). Zero copy.

### Input volumes (local)
Init container does `cp -a` from source hostPath to this step's own hostPath subdir. Each consumer gets a writable copy.

### Input volumes (remote)
Init container HTTP GETs from source node's daemon (tars on-the-fly), extracts to local hostPath subdir.

### Cache volumes
Direct hostPath mount, read-write. No copy, no init container. Warm on same node, cold start on different node.

### GC
- Reaper: HTTP DELETE on step directory when container destroyed
- TTL sweeper: only sweeps /steps/ dirs with no active pod AND mtime > TTL
- Cache cleanup: DB-driven only (job/pipeline deletion)

## Acceptance Criteria

- [ ] ArtifactLocator instantiated in atccmd and wired to Worker + Reaper
- [ ] Step outputs use hostPath subdirectories (not emptyDir)
- [ ] No sidecar in DaemonSet mode
- [ ] No upload phase in DaemonSet mode
- [ ] Local inputs use cp -a init containers
- [ ] Remote inputs use HTTP tar stream from daemon
- [ ] Caches are direct hostPath mounts
- [ ] Build logs show fetch source per input
- [ ] TTL sweeper is build-aware
- [ ] Existing PVC mode unaffected
- [ ] All jetbridge tests pass
