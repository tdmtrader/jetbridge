# Plan: DaemonSet Direct HostPath Mounts

Phase 1 (locator wiring, sidecar/upload skip) is complete. Remaining phases:

## Phase 2: HostPath output and dir volumes

### [ ] Write tests for hostPath output volumes
- Test that DaemonSet mode creates hostPath volumes for outputs (not emptyDir).
- Test that the path is `<hostPath>/steps/<handle>/<volume-name>/`.
- Test that the dir volume is also hostPath.
- Test that PVC mode still creates emptyDir (regression guard).
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [ ] Implement hostPath volumes in buildVolumeMounts
- In `buildVolumeMounts()`, when `IsDaemonSetBackend()`:
  - Dir volume: hostPath at `<hostPath>/steps/<handle>/dir/`
  - Output volumes: hostPath at `<hostPath>/steps/<handle>/<output-name>/`
  - Input volumes: hostPath at `<hostPath>/steps/<handle>/<input-name>/`
  - Type: `DirectoryOrCreate` for all
- PVC mode unchanged (emptyDir).
- File: `atc/worker/jetbridge/container.go`

## Phase 3: cp -a init containers for local inputs

### [ ] Write tests for cp -a local and HTTP remote fetch
- Test that local init container uses `cp -a` (not tar).
- Test that remote init container uses HTTP GET piped to `tar xf`.
- Test that SOURCE_NODE env var drives the branch.
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [ ] Implement cp -a init containers
- Update `daemonSetFetchCommand()` to:
  - Local: `cp -a <hostPath>/steps/<source-handle>/<name>/. <hostPath>/steps/<this-handle>/<input-name>/`
  - Remote: `wget -qO- http://... | tar xf - -C <hostPath>/steps/<this-handle>/<input-name>/`
- Update init container volume mounts to mount the parent hostPath dir read-write.
- File: `atc/worker/jetbridge/container.go`

## Phase 4: Direct cache hostPath mounts

### [ ] Write tests for direct cache mounts
- Test that DaemonSet caches use hostPath at `<hostPath>/caches/<stable-key>/`.
- Test that no cacheEntries are created (no tar save/restore).
- Test that PVC mode caches are unchanged.
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [ ] Implement cache hostPath volumes
- In `buildVolumeMounts()`, add `CacheStoreDaemonSet` case (or check `IsDaemonSetBackend()` in existing artifact case):
  - HostPath: `<hostPath>/caches/<stableCacheKey>/`
  - Type: `DirectoryOrCreate`
  - Do NOT create cacheEntry (no tar upload needed).
- File: `atc/worker/jetbridge/container.go`

## Phase 5: Artifact-daemon directory serving

### [ ] Write tests for directory-based GET
- Test that GET on a directory tars it on-the-fly and streams.
- Test that DELETE removes the directory tree.
- Test that HEAD checks directory existence.
- File: `cmd/artifact-daemon/server_test.go`

### [ ] Implement directory serving in artifact-daemon
- `GET /artifacts/steps/<handle>/<name>`: walk directory, write tar stream.
- `DELETE /artifacts/steps/<handle>/`: `os.RemoveAll`.
- `HEAD /artifacts/steps/<handle>/<name>`: `os.Stat` on directory.
- Keep backward compat for flat tar files during migration.
- File: `cmd/artifact-daemon/server.go`

## Phase 6: Build-aware TTL sweeper

### [ ] Write tests for build-aware sweep
- Test that sweeper skips directories with active pods.
- Test that sweeper deletes directories with no active pod and expired mtime.
- Test that sweeper ignores /caches/.
- File: `cmd/artifact-daemon/sweeper_test.go`

### [ ] Implement build-aware sweep
- Sweeper scans only `/steps/`.
- For each candidate: query K8s API for pods with `concourse.ci/handle=<dir-name>` on this node.
- Only delete if no matching pod AND mtime > TTL.
- File: `cmd/artifact-daemon/sweeper.go`

## Phase 7: Build log events + reaper update

### [ ] Write tests for fetch source logging
- Test that a log event is emitted per input with local/remote/unknown source.
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [ ] Emit build log events for input fetch source
- Before creating the pod, iterate inputs and emit a log-like message to the build delegate indicating local vs remote fetch.
- Requires access to the delegate's Stdout writer from the container build path.
- Files: `atc/worker/jetbridge/container.go` or `atc/exec/task_step.go`

### [ ] Update reaper DELETE URL for directory paths
- Change from `/artifacts/artifacts/<handle>.tar` to `/artifacts/steps/<handle>/`.
- File: `atc/worker/jetbridge/reaper.go`

## Phase 8: Validation

### [ ] Run jetbridge unit tests (373+ specs)
### [ ] Run artifact-daemon tests (15+ specs)
### [ ] Verify go build and go vet pass
