# Spec: Artifact Helper Optimization

## Overview

The artifact helper sidecar uploads step outputs to the artifact store PVC after every step completes. Production traces show this phase taking 30-40s for typical builds — often longer than the step itself. Investigation revealed three categories of waste:

1. **Unnecessary uploads** — all volumes (inputs, outputs, caches) are uploaded, even inputs that were just extracted from the same PVC and hostPath-backed caches that are already persisted on disk.
2. **Sequential execution** — each volume is uploaded via a separate SPDY exec, run one at a time.
3. **No visibility into artifact characteristics** — we don't know the file count, total size, or transfer rate of artifacts, which is critical for evaluating future architectural options (shared PVC vs ATC broker vs distributed cache).

This track ships targeted optimizations to the upload path and adds artifact telemetry to inform the next evolution of the artifact passing architecture.

## Requirements

### Optimize uploads

1. **Skip input re-uploads** — `uploadOutputsToArtifactStore` must only upload volumes that correspond to step outputs (from `containerSpec.Outputs`), not inputs or the working directory. Inputs were just extracted from the PVC by init containers and have not changed.
2. **Skip cache uploads except in artifact mode** — each cache storage mode has its own persistence mechanism; only `CacheStoreArtifact` uses the artifact PVC as primary storage:
   - `CacheStoreArtifact` — upload synchronously (current behavior). The artifact PVC IS the cache store.
   - `CacheStoreHostPath` — data persists on the node's local disk. No upload to artifact PVC.
   - `CacheStorePVC` — data persists on the cache PVC (which may be GCS Fuse-backed, NFS, etc. and already cross-node accessible). No upload to artifact PVC.
   - `CacheStoreEmptyDir` — ephemeral, no persistence. No upload.
3. **Parallelize remaining uploads** — output volumes and artifact-mode caches should be uploaded concurrently via parallel SPDY execs. The artifact-helper sidecar can handle concurrent exec sessions.
4. **Batch uploads into a single SPDY exec** — where possible, combine multiple tar commands into a single `sh -c` invocation to eliminate per-exec SPDY connection overhead.

### Artifact telemetry

5. **Separate tar and transfer phases** — each upload must have visibility into two distinct sub-phases:
   - **Tar phase** (`artifact.tar_duration`) — time to walk the directory tree and produce the tar stream. This is CPU + local read I/O.
   - **Transfer phase** (`artifact.transfer_duration`) — time to write the tar to the PVC. This is storage I/O (local disk for hostPath, network for NFS/GCS Fuse).
   These phases should be separate child spans or span events with timestamps, so operators can see whether slowness is in tar creation or storage write.
6. **Record artifact size** — after each tar upload, record the size of the tar file written to the PVC as a span attribute (`artifact.size_bytes`).
7. **Record file count** — before or during tar, capture the number of files in the volume and record as a span attribute (`artifact.file_count`).
8. **Record transfer duration per artifact** — each upload should be an individual span (already planned in the telemetry track) with duration visible.
9. **Emit summary metrics** — OTel histogram metrics for artifact upload:
   - `concourse.artifact.upload_duration` (seconds, per artifact) with attributes: `artifact.type` (output/cache), `step.type`, `pipeline`, `job`
   - `concourse.artifact.upload_size` (bytes, per artifact) with same attributes
   - `concourse.artifact.file_count` (per artifact) with same attributes
   - `concourse.artifact.tar_duration` (seconds) — tar creation phase only
   - `concourse.artifact.transfer_duration` (seconds) — storage write phase only
10. **Record init container extraction telemetry** — on the read side, capture the size and file count of artifacts extracted by init containers, to understand the full read/write profile.

### Safety

11. All optimizations must be backwards-compatible — no change to the artifact PVC layout or tar format.
12. Downstream steps that consume outputs must continue to find their artifacts on the PVC unchanged.
13. Steps with mixed output/input paths (output overlapping an input path) must still upload correctly.

## Technical Approach

### Filtering uploads (requirements 1-2)

In `uploadOutputsToArtifactStore`, build a set of output mount paths from `containerSpec.Outputs` and only upload volumes whose `MountPath()` matches. Skip volumes that are inputs or the working directory.

For caches, `uploadCachesToArtifactStore` already only runs when `cacheEntries` is populated, which only happens in `CacheStoreArtifact` mode — hostPath, PVC, and emptyDir modes don't populate `cacheEntries`, so no cache upload occurs. Verify this is correct and add a guard comment. The `uploadOutputsToArtifactStore` loop also needs to exclude cache mount paths to avoid double-upload.

### Parallelization (requirement 3)

Replace the sequential loop in `uploadOutputsToArtifactStore` with a `sync.WaitGroup` + goroutine per volume. Collect errors via an `errgroup.Group`. Same for `uploadCachesToArtifactStore`.

### Batch exec (requirement 4)

For the parallel approach, each goroutine still needs its own SPDY exec (since SPDY exec is a streaming connection). However, for small numbers of volumes, a single `sh -c "tar cf ... && tar cf ..."` command in one SPDY exec eliminates connection overhead while remaining sequential. Evaluate the tradeoff: if N <= 3, batch; if N > 3, parallelize.

### Telemetry (requirements 5-10)

**Separating tar vs transfer phases:** The current upload command is `mkdir -p ... && tar cf <pvc-path> -C <emptydir> .` — this is a single operation where tar writes directly to the PVC, so tar creation and storage write are interleaved. To separate them:
- Option A: **Two-stage command** — `tar cf /tmp/artifact.tar -C <emptydir> . && mv /tmp/artifact.tar <pvc-path>`. The `tar cf` to `/tmp` (emptyDir or tmpfs) is pure local I/O (tar phase), and the `mv`/`cp` to the PVC is pure storage I/O (transfer phase). Report timing for each via shell timestamps or separate SPDY execs.
- Option B: **Pipe with tee** — `tar cf - -C <emptydir> . | tee >(wc -c > /tmp/size) > <pvc-path>`. Single pass, captures size, but tar and transfer are still interleaved. Less useful for phase separation.
- **Recommended: Option A** — it cleanly separates the phases and the intermediate file also gives an exact byte count. The tradeoff is temporary disk usage in the sidecar (bounded by artifact size), which is acceptable given the sidecar's emptyDir.

Add a pre-upload `find <path> -type f | wc -l` exec to capture file count. Record results as span attributes. For the metrics, register OTel histograms in `atc/metric/` following the existing `InitOTelStepDuration` pattern.

For init container telemetry, the init container command can be augmented to report the extracted tar size and file count (e.g., `tar xf ... -C <dest> && du -sb <dest> && find <dest> -type f | wc -l`) with output to stderr so the `podEventTracker` can capture it via container logs or annotations.

## Acceptance Criteria

- [ ] Steps with inputs + outputs only upload output volumes to the artifact PVC.
- [ ] Only `CacheStoreArtifact` caches are uploaded to the artifact PVC. hostPath, PVC, and emptyDir caches are not uploaded (they have their own persistence).
- [ ] Multiple output uploads run concurrently.
- [ ] Each artifact upload span includes `artifact.size_bytes`, `artifact.file_count`, `artifact.tar_duration`, and `artifact.transfer_duration`.
- [ ] Tar phase and transfer phase are separately visible in traces.
- [ ] OTel metrics include `concourse.artifact.upload_duration`, `concourse.artifact.upload_size`, `concourse.artifact.file_count`, `concourse.artifact.tar_duration`, and `concourse.artifact.transfer_duration`.
- [ ] All existing tests pass; new tests cover the filtering, parallelization, and cache upload logic.
- [ ] No change to artifact PVC tar format or directory layout.
- [ ] Production traces clearly show which artifacts were uploaded, how large they were, how many files they contained, and where the time was spent (tar vs storage).

## Out of Scope

- Changing the artifact PVC architecture (shared PVC, ATC broker, DaemonSet sync) — those are future tracks informed by the telemetry from this work.
- Compression (tar czf) — potential follow-up, but adds CPU load to the sidecar and interacts with the GCS Fuse write path. Evaluate after telemetry shows size distribution.
- Init container parallelization — K8s runs init containers sequentially by design. Sidecar containers with `restartPolicy: Always` (K8s 1.28+ native sidecars) could parallelize, but that's a separate investigation.
- Optimizing the read side (init container extraction) — this track focuses on the write path.
