# Plan: Artifact Helper Optimization

## Phase 1: Filter unnecessary uploads

### [x] Write tests for output-only upload filtering
- Test that `uploadOutputsToArtifactStore` only uploads volumes matching `containerSpec.Outputs` paths.
- Test that input volumes are skipped (volume whose mount path matches an input `DestinationPath`).
- Test that the working directory volume is skipped.
- Test that cache mount paths are skipped (not double-uploaded when caches are handled separately).
- Test the overlapping input/output path case: when an output path equals an input path, it IS uploaded (the output overwrote the input).
- File: `atc/worker/jetbridge/process_test.go`

### [x] Implement output-only upload filtering
- In `uploadOutputsToArtifactStore`, build a `map[string]bool` of output paths from `containerSpec.Outputs`.
- For the overlapping input/output case, include paths that appear in both `Outputs` and `Inputs`.
- Skip volumes whose `MountPath()` is not in the output set.
- File: `atc/worker/jetbridge/process.go`

### [x] Write tests for cache upload behavior per cache mode
- Test that when `CacheStore` is `artifact`, `uploadCachesToArtifactStore` uploads synchronously (blocking, errors are fatal). This is the only mode that uploads caches to the artifact PVC.
- Test that when `CacheStore` is `hostpath`, no cache upload occurs (`cacheEntries` is empty — data persists on the node's local disk).
- Test that when `CacheStore` is `pvc`, no cache upload occurs (`cacheEntries` is empty — data persists on the cache PVC, which may already be cross-node accessible via NFS/GCS Fuse).
- Test that when `CacheStore` is `emptydir`, no cache upload occurs (ephemeral).
- Verify via existing container_test.go that `cacheEntries` is only populated in `CacheStoreArtifact` mode.
- File: `atc/worker/jetbridge/process_test.go`

### [x] Verify cache upload filtering is already correct (or fix)
- Audit `buildVolumeMounts` to confirm `cacheEntries` is only populated for `CacheStoreArtifact` mode. This should already be the case — hostPath, PVC, and emptyDir modes don't append to `cacheEntries`.
- Add a guard comment to `uploadCachesToArtifactStore` documenting that only artifact-mode caches reach this path.
- If the filtering is not correct, fix it so that only `CacheStoreArtifact` entries are uploaded.
- Files: `atc/worker/jetbridge/container.go`, `atc/worker/jetbridge/process.go`

### [x] Verify output filter excludes cache volumes
- Audit `buildVolumeMounts` to confirm `cacheEntries` is only populated for `CacheStoreArtifact`.
- Confirm cache volumes are excluded from `uploadOutputsToArtifactStore` by the output-path filter.
- If cache volumes leak through (because they're in `p.container.volumes` but not in the output filter), add explicit exclusion.
- Files: `atc/worker/jetbridge/container.go`, `atc/worker/jetbridge/process.go`

## Phase 2: Parallelize uploads

### [x] Write tests for parallel output uploads
- Test that multiple output volumes are uploaded concurrently (verify all artifacts are present on PVC after upload).
- Test that a single upload failure fails the overall upload (error propagation from errgroup).
- Test that context cancellation stops in-flight uploads.
- File: `atc/worker/jetbridge/process_test.go`

### [x] Implement parallel output uploads
- Replace sequential loop in `uploadOutputsToArtifactStore` with `golang.org/x/sync/errgroup.Group`.
- Each volume upload launches a goroutine that execs `tar cf` in the artifact-helper sidecar.
- Collect first error and cancel remaining uploads on failure.
- Apply same pattern to synchronous `uploadCachesToArtifactStore` (artifact mode).
- Files: `atc/worker/jetbridge/process.go`

## Phase 3: Artifact telemetry — upload phase separation

### [x] Write tests for two-phase upload (tar vs transfer)
- Test that the upload command uses a two-stage approach: `tar cf /tmp/<key> -C <path> .` then `mv /tmp/<key> <pvc-path>`.
- Test that span events `artifact.tar.start`/`artifact.tar.end` and `artifact.transfer.start`/`artifact.transfer.end` are emitted with timestamps.
- Test that `artifact.tar_duration` and `artifact.transfer_duration` span attributes are set.
- Test that `artifact.size_bytes` is captured from the intermediate tar file size.
- File: `atc/worker/jetbridge/process_test.go`

### [x] Implement two-phase upload with timing
- Change upload command from `tar cf <pvc-path> -C <emptydir> .` to a two-stage shell script:
  ```
  t0=$(date +%s%N)
  tar cf /tmp/<key>.tar -C <path> .
  t1=$(date +%s%N)
  sz=$(stat -c %s /tmp/<key>.tar 2>/dev/null || stat -f %z /tmp/<key>.tar)
  mv /tmp/<key>.tar <pvc-path>
  t2=$(date +%s%N)
  echo "TAR_NS=$((t1-t0)) TRANSFER_NS=$((t2-t1)) SIZE=$sz"
  ```
- Parse stdout to extract tar duration, transfer duration, and size.
- Set span attributes: `artifact.tar_duration`, `artifact.transfer_duration`, `artifact.size_bytes`.
- Emit span events for phase boundaries.
- Files: `atc/worker/jetbridge/process.go`

### [x] Write tests for file count capture
- Test that `artifact.file_count` is captured and set as span attribute.
- File: `atc/worker/jetbridge/process_test.go`

### [x] Implement file count capture
- Add `find <path> -type f | wc -l` to the upload command (before tar), parse result.
- Set `artifact.file_count` span attribute.
- File: `atc/worker/jetbridge/process.go`

## Phase 4: OTel metrics and init container telemetry

### [x] Write tests for OTel artifact metrics
- Test that `concourse.artifact.upload_duration` histogram is recorded per upload.
- Test that `concourse.artifact.upload_size` histogram is recorded per upload.
- Test that `concourse.artifact.file_count` histogram is recorded per upload.
- Test that `concourse.artifact.tar_duration` histogram is recorded per upload.
- Test that `concourse.artifact.transfer_duration` histogram is recorded per upload.
- Test that metric attributes include `artifact.type`, `step.type`.
- File: `atc/metric/otel_artifact_test.go` (new) or `atc/worker/jetbridge/process_test.go`

### [x] Implement OTel artifact metrics
- Add `InitOTelArtifactMetrics()` in `atc/metric/` following the `InitOTelStepDuration` pattern.
- Register histograms: `concourse.artifact.upload_duration`, `concourse.artifact.upload_size`, `concourse.artifact.file_count`, `concourse.artifact.tar_duration`, `concourse.artifact.transfer_duration`.
- Add `RecordArtifactUpload(ctx, artifactType, size, fileCount, totalDuration, tarDuration, transferDuration)`.
- Call from upload paths after each artifact upload.
- Wire `InitOTelArtifactMetrics()` into `atc/atccmd/command.go` alongside existing metric init calls.
- Files: `atc/metric/otel_artifact.go` (new), `atc/worker/jetbridge/process.go`, `atc/atccmd/command.go`

### [x] Add init container extraction telemetry
- Augment init container commands in `buildArtifactInitContainers` to capture extracted size and file count: `tar xf <src> -C <dest> && du -sb <dest> >&2 && find <dest> -type f | wc -l >&2` so data appears in container logs.
- In `podEventTracker`, when emitting `init.container.completed`, add `artifact.key` attribute (derive from init container name → input index mapping stored on the Container).
- Record `concourse.artifact.extract_duration` metric from init container `startedAt`/`finishedAt` in container status.
- Files: `atc/worker/jetbridge/container.go`, `atc/worker/jetbridge/process.go`
