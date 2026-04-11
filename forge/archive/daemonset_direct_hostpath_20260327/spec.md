# Spec: DaemonSet Direct HostPath Mounts

**Track ID:** `daemonset_direct_hostpath_20260327`
**Type:** feature
**Depends on:** `daemonset_artifact_cache_20260326`

## Problem

DaemonSet mode is currently broken end-to-end. Phase 1 (locator wiring, sidecar/upload skip) is complete, but step outputs still use emptyDir volumes. With uploads skipped, data written to emptyDir is never persisted to the hostPath — so downstream steps can't find it. Init containers attempt `tar xf` from hostPath, but no tar files exist there.

The root issue: the PVC pattern (emptyDir → tar → PVC → tar → emptyDir) was carried over into DaemonSet mode. HostPath storage doesn't need serialization — steps should write directly to hostPath subdirectories.

## Design

### Directory layout

```
<ArtifactDaemonHostPath>/          (default: /var/concourse/artifacts/)
  steps/
    <container-handle>/
      <volume-name>/               output/input data as plain directories
  caches/
    <stable-cache-key>/            persistent cache directories
```

### Output volumes

Replace emptyDir with hostPath subdirectory for each output when `IsDaemonSetBackend()`:

```
hostPath: <ArtifactDaemonHostPath>/steps/<handle>/<volume-name>/
type: DirectoryOrCreate
```

Step writes directly to disk. Zero copy. No upload needed.

### Dir volume (working directory)

Same treatment — mount as hostPath subdirectory so get/put step working directories are also persisted for downstream consumption.

### Input volumes (local — same node)

Init container copies from source step's hostPath subdirectory into this step's hostPath subdirectory using `cp -a` (preserves symlinks, permissions, timestamps). Each consumer gets a writable copy.

```sh
cp -a <hostPath>/steps/<source-handle>/<name>/. <hostPath>/steps/<this-handle>/<input-name>/
```

### Input volumes (remote — different node)

Init container HTTP GETs from the source node's artifact-daemon. The daemon tars the source directory on-the-fly and streams it. Init container extracts into this step's hostPath subdirectory.

```sh
wget -qO- http://<source-node>.<service>.<ns>:<port>/artifacts/steps/<source-handle>/<name> \
  | tar xf - -C <hostPath>/steps/<this-handle>/<input-name>/
```

### Cache volumes

Direct hostPath mount at `<hostPath>/caches/<stable-key>/`, type `DirectoryOrCreate`. No init container, no copy. Warm on same node, cold start on different node. Soft affinity biases toward cache-warm nodes.

### Artifact-daemon directory serving

The daemon currently stores and serves tar files. For the direct hostPath design, it needs to:
- `GET /artifacts/steps/<handle>/<name>`: tar a directory on-the-fly, stream response
- `DELETE /artifacts/steps/<handle>/`: remove entire step directory tree
- `HEAD /artifacts/steps/<handle>/<name>`: check directory existence

### Build log events

The ATC emits a log event per input before container creation:
- `input "src": copying from local artifact cache`
- `input "config": fetching from node gke-pool-abc123`
- `input "data": location unknown, will attempt local first`

### Build-aware TTL sweeper

The sweeper scans `/steps/` only (not `/caches/`). A directory is eligible for deletion only when:
1. No pod on this node has a matching container handle label, AND
2. The directory's mtime exceeds the TTL

This prevents deleting artifacts for in-progress builds.

## Acceptance Criteria

- [ ] Output and dir volumes use hostPath subdirectories in DaemonSet mode
- [ ] Local inputs use `cp -a` init containers (no tar)
- [ ] Remote inputs use HTTP tar-on-the-fly from artifact-daemon
- [ ] Caches are direct hostPath mounts with stable keys
- [ ] Artifact-daemon serves directories (tar on-the-fly for GET)
- [ ] Build logs show fetch source per input
- [ ] TTL sweeper is build-aware (checks pod existence)
- [ ] Reaper sends DELETE for step directories (not tar files)
- [ ] Existing PVC mode completely unaffected
- [ ] All jetbridge + artifact-daemon tests pass

## Out of Scope

- Disk pressure eviction (LRU) — future enhancement
- OTel instrumentation for fetch operations
- Cross-node cache transfer
- Compression on HTTP transfer path
