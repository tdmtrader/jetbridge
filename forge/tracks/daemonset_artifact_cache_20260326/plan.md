# Plan: DaemonSet Artifact Cache

## Phase 1: artifact-daemon binary

### [ ] Write tests for artifact-daemon HTTP server
- Test `PUT /artifacts/{key}` stores a file at the expected hostPath location.
- Test `GET /artifacts/{key}` streams back the stored file.
- Test `HEAD /artifacts/{key}` returns 200 for existing, 404 for missing.
- Test `DELETE /artifacts/{key}` removes the file.
- Test `GET /healthz` returns 200.
- Test that nested keys (e.g., `caches/job-42-build-abc.tar`) create subdirectories.
- Test that GET on a missing key returns 404.
- File: `cmd/artifact-daemon/main_test.go` or `cmd/artifact-daemon/server_test.go`

### [ ] Implement artifact-daemon binary
- Create `cmd/artifact-daemon/main.go` with:
  - Flag parsing: `--port`, `--storage-path`, `--ttl`, `--node-name`, `--namespace`, `--label-key`
  - HTTP server with `GET/PUT/DELETE/HEAD /artifacts/{key}` and `GET /healthz`
  - Storage: read/write files under `<storage-path>/artifacts/` and `<storage-path>/caches/`
  - Graceful shutdown on SIGTERM
- File: `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/server.go`

### [ ] Implement node labeling on startup/shutdown
- On startup: use K8s client to label the node `concourse.dev/artifact-cache=ready`
- On shutdown (SIGTERM): remove the label via preStop or in the shutdown handler
- Requires a ServiceAccount with permission to patch node labels
- File: `cmd/artifact-daemon/node_labeler.go`

### [ ] Implement TTL-based artifact sweep
- Background goroutine that scans the storage directory periodically (e.g., every 5 minutes)
- Deletes files older than the configured TTL
- Logs deletions for observability
- File: `cmd/artifact-daemon/sweeper.go`

## Phase 2: Helm chart

### [ ] Write Helm chart templates for DaemonSet
- DaemonSet template: `deploy/chart/templates/artifact-daemon-daemonset.yaml`
  - Image: same Concourse image (artifact-daemon binary baked in) or configurable
  - hostPath volume mount at configurable path
  - Container port for HTTP server
  - Liveness/readiness probes on `/healthz`
  - Resource requests/limits (50m/64Mi request, 200m/256Mi limit)
  - Configurable tolerations (for spot nodes, GPU nodes, etc.)
  - ServiceAccount with node label patch permissions
- Headless Service template: `deploy/chart/templates/artifact-daemon-service.yaml`
  - `clusterIP: None` for per-pod DNS
- ServiceAccount + ClusterRole + ClusterRoleBinding for node labeling
- Values: `artifactDaemon.enabled`, `artifactDaemon.port`, `artifactDaemon.hostPath`, `artifactDaemon.ttl`, `artifactDaemon.resources`, `artifactDaemon.tolerations`
- Conditional rendering: only create resources when `artifactDaemon.enabled=true`
- Files: `deploy/chart/templates/artifact-daemon-*.yaml`, `deploy/chart/values.yaml`

## Phase 3: ATC configuration and artifact location tracking

### [ ] Write tests for artifact location tracking
- Test that when a step completes, its artifact keys are associated with the node name.
- Test that looking up an artifact key returns the correct source node.
- Test that entries are removed when artifacts are cleaned up.
- Test that the map is safe for concurrent access.
- File: `atc/worker/jetbridge/artifact_locator_test.go`

### [ ] Implement artifact location tracking
- New `ArtifactLocator` struct in `atc/worker/jetbridge/` with:
  - `Record(key string, nodeName string)` — store artifact → node mapping
  - `Locate(key string) (nodeName string, found bool)` — look up source node
  - `Remove(key string)` — clean up on GC
  - Thread-safe via `sync.RWMutex`
- Wire into `Worker` — populated after step output uploads complete
- File: `atc/worker/jetbridge/artifact_locator.go`

### [ ] Add DaemonSet config fields
- Add to `jetbridge.Config`: `ArtifactBackend`, `ArtifactDaemonPort`, `ArtifactDaemonHostPath`, `ArtifactDaemonService`, `ArtifactDaemonTTL`
- Add corresponding command-line flags in `atc/atccmd/`
- Wire into Helm chart values
- Files: `atc/worker/jetbridge/config.go`, `atc/atccmd/command.go`

## Phase 4: Local artifact upload (step → hostPath)

### [ ] Write tests for DaemonSet-mode artifact upload
- Test that when `ArtifactBackend=daemonset`, step outputs are tarred to the hostPath mount (not the artifact PVC).
- Test that only output volumes are uploaded (reuse output filtering from the optimization track).
- Test that cache volumes are uploaded to the caches subdirectory on hostPath.
- Test that the artifact locator is updated with key → node name after upload.
- File: `atc/worker/jetbridge/process_test.go`

### [ ] Implement DaemonSet-mode artifact upload
- In `uploadOutputsToArtifactStore`, branch on `ArtifactBackend`:
  - `"pvc"`: existing behavior (tar to PVC via artifact-helper sidecar)
  - `"daemonset"`: tar to hostPath mount directly (no SPDY exec needed — the sidecar or main container writes to the mounted hostPath)
- Update `uploadCachesToArtifactStore` similarly for DaemonSet mode
- After upload, call `artifactLocator.Record(key, nodeName)` where nodeName comes from the pod's scheduled node
- Files: `atc/worker/jetbridge/process.go`, `atc/worker/jetbridge/container.go`

### [ ] Add hostPath and DaemonSet volume mounts to pod spec
- When `ArtifactBackend=daemonset`, add a hostPath volume mount for the artifact storage directory to the pod spec (replaces the artifact PVC volume)
- The artifact-helper sidecar mounts the same hostPath instead of the PVC
- Files: `atc/worker/jetbridge/container.go`

## Phase 5: Soft and hard scheduling affinity

### [ ] Write tests for scheduling affinity
- Test that step pods get a hard node affinity for `concourse.dev/artifact-cache=ready`.
- Test that when input artifacts have a known source node, a soft affinity is added for that node.
- Test that when no source node is known (first step, or locator miss), only the hard affinity is set.
- Test that multiple input sources pick the most common node (or first).
- File: `atc/worker/jetbridge/container_test.go`

### [ ] Implement scheduling affinity in pod builder
- In `buildPod`, when `ArtifactBackend=daemonset`:
  - Always add `requiredDuringSchedulingIgnoredDuringExecution` for `concourse.dev/artifact-cache=ready`
  - Query `artifactLocator` for each input's source node
  - If a preferred node is identified, add `preferredDuringSchedulingIgnoredDuringExecution` with weight 100
- Files: `atc/worker/jetbridge/container.go`

## Phase 6: Cross-node artifact fetch (init containers)

### [ ] Write tests for local vs remote init container commands
- Test that when source node == current node, init container uses local tar extraction from hostPath.
- Test that when source node != current node, init container uses HTTP GET from the DaemonSet on the source node.
- Test that init container handles DaemonSet unavailability (retry + timeout + error).
- File: `atc/worker/jetbridge/container_test.go`

### [ ] Implement branching init containers
- In `buildArtifactInitContainers`, when `ArtifactBackend=daemonset`:
  - Inject source node info as env vars or command args in the init container
  - Init container command: check if source node == current node (via downward API `spec.nodeName`):
    - Same node: `tar xf /artifacts/<key> -C <dest>`
    - Different node: `wget -qO- http://<source-pod>.<service>.<ns>.svc.cluster.local:<port>/artifacts/<key> | tar xf - -C <dest>`
  - Add retry logic (3 attempts, 2s backoff) for the HTTP path
- Files: `atc/worker/jetbridge/container.go`

## Phase 7: DaemonSetVolume and StreamOut

### [ ] Write tests for DaemonSetVolume.StreamOut
- Test that StreamOut HTTP GETs from the DaemonSet and returns a tar reader.
- Test that StreamOut handles 404 (artifact not found).
- Test that StreamOut handles connection errors with retries.
- File: `atc/worker/jetbridge/volume_daemonset_test.go`

### [ ] Implement DaemonSetVolume
- New `DaemonSetVolume` type implementing `runtime.Volume`
- `StreamOut` → HTTP GET from `http://<daemon-pod>.<service>.<ns>.svc.cluster.local:<port>/artifacts/<key>`
- Pod name for the DaemonSet on the source node is resolved via K8s API (list DaemonSet pods, filter by node)
- `Handle()`, `Source()`, `DBVolume()` delegate like `ArtifactStoreVolume`
- File: `atc/worker/jetbridge/volume_daemonset.go`

### [ ] Wire DaemonSetVolume into Worker.LookupVolume
- In `Worker.LookupVolume`, when `ArtifactBackend=daemonset`, return a `DaemonSetVolume` instead of `ArtifactStoreVolume`
- File: `atc/worker/jetbridge/worker.go`

## Phase 8: GC integration

### [ ] Write tests for reaper DaemonSet cleanup
- Test that the reaper calls HTTP DELETE on the DaemonSet for destroyed container artifact keys.
- Test that DELETE failures are logged but don't block GC.
- Test that the reaper resolves the correct DaemonSet pod for the artifact's source node.
- File: `atc/worker/jetbridge/reaper_test.go`

### [ ] Implement reaper DaemonSet cleanup
- In `cleanupArtifactStoreEntries`, when `ArtifactBackend=daemonset`:
  - For each destroying container handle, look up the artifact's source node from the locator
  - HTTP DELETE to the DaemonSet pod on that node
  - Call `artifactLocator.Remove(key)` after successful delete
  - Best-effort: log errors, don't block
- Files: `atc/worker/jetbridge/reaper.go`

## Phase 9: Integration testing

### [ ] Write integration test for DaemonSet artifact flow
- End-to-end test: step A produces output → step B on same node reads locally → step C on different node fetches via HTTP
- Verify artifacts are correctly produced, transferred, and consumed
- Verify GC cleans up after build completion
- File: `atc/worker/jetbridge/integration_test.go` or `topgun/k8s/integration/`

### [ ] Write integration test for spot node preemption
- Simulate node loss (delete DaemonSet pod + remove node label)
- Verify that new step pods don't schedule on the affected node
- Verify that builds fail gracefully when artifact source node is lost
- File: `atc/worker/jetbridge/integration_test.go`
