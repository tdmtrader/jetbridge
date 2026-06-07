> **Reconciled & closed 2026-06-07.** Shipped and evolved past its dual-backend framing: the DaemonSet became the SOLE authoritative artifact backend (the proposed ArtifactBackend pvc|daemonset toggle never shipped). Superseded by daemon_mediated_artifact_resolution / daemonset_direct_hostpath / artifact_daemon_resilience.
>
> Reviewed via a parallel track audit; no further work needed (see closure reason). Original plan preserved below for the record.

# Plan: DaemonSet Artifact Cache

## Phase 1: artifact-daemon binary

### [x] Write tests for artifact-daemon HTTP server 9470c1ded
- Test `PUT /artifacts/{key}` stores a file at the expected hostPath location.
- Test `GET /artifacts/{key}` streams back the stored file.
- Test `HEAD /artifacts/{key}` returns 200 for existing, 404 for missing.
- Test `DELETE /artifacts/{key}` removes the file.
- Test `GET /healthz` returns 200.
- Test that nested keys (e.g., `caches/job-42-build-abc.tar`) create subdirectories.
- Test that GET on a missing key returns 404.
- File: `cmd/artifact-daemon/server_test.go`

### [x] Implement artifact-daemon binary c7d0e7417
- Create `cmd/artifact-daemon/main.go` with flag parsing and graceful shutdown.
- HTTP server with `GET/PUT/DELETE/HEAD /artifacts/{key}` and `GET /healthz`.
- Files: `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/server.go`

### [x] Implement node labeling on startup/shutdown bc762a180
- NodeLabeler adds/removes `concourse.dev/artifact-cache=ready` label on the K8s node.
- File: `cmd/artifact-daemon/node_labeler.go`

### [x] Implement TTL-based artifact sweep bc762a180
- Sweeper periodically removes artifacts older than configured TTL.
- File: `cmd/artifact-daemon/sweeper.go`

## Phase 2: Helm chart

### [x] Write Helm chart templates for DaemonSet 75fa9948d
- DaemonSet, headless Service, ServiceAccount, ClusterRole, ClusterRoleBinding templates.
- Conditional on `artifactDaemon.enabled=true`.
- Files: `deploy/chart/templates/artifact-daemon-*.yaml`, `deploy/chart/values.yaml`

## Phase 3: ATC configuration and artifact location tracking

### [x] Write tests for artifact location tracking fd321cd2a
- Tests for Record, Locate, Remove, and concurrent access.
- File: `atc/worker/jetbridge/artifact_locator_test.go`

### [x] Implement artifact location tracking fd321cd2a
- `ArtifactLocator` struct with thread-safe map via `sync.RWMutex`.
- File: `atc/worker/jetbridge/artifact_locator.go`

### [x] Add DaemonSet config fields fd321cd2a
- `ArtifactBackend`, `ArtifactDaemonPort`, `ArtifactDaemonHostPath`, `ArtifactDaemonService` in Config.
- CLI flags wired in `atc/atccmd/command.go`.
- Files: `atc/worker/jetbridge/config.go`, `atc/atccmd/command.go`

## Phase 4: Local artifact upload (step → hostPath)

### [x] Write tests for DaemonSet-mode artifact upload 3df2b1749
- Covered by daemonset_integration_test.go (hostPath volume, init containers).

### [x] Implement DaemonSet-mode artifact upload 3df2b1749
- `hasArtifactStore()` recognizes both PVC and DaemonSet backends.
- `uploadOutputsToArtifactStore` and `streamInputs` check `hasArtifactStore()`.
- Files: `atc/worker/jetbridge/process.go`, `atc/worker/jetbridge/container.go`

### [x] Add hostPath and DaemonSet volume mounts to pod spec 3df2b1749
- `buildArtifactStoreVolume` returns hostPath in DaemonSet mode.
- `artifactVolumeName()` returns correct volume name per mode.
- File: `atc/worker/jetbridge/container.go`

## Phase 5: Soft and hard scheduling affinity

### [x] Write tests for scheduling affinity 485a2f8ee
- Tests for hard affinity, soft affinity, PVC no-affinity.
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [x] Implement scheduling affinity in pod builder 485a2f8ee
- `buildAffinity()` adds hard node affinity for `concourse.dev/artifact-cache=ready`.
- Soft affinity biases toward the node holding most input artifacts.
- `ArtifactLocator` wired into Worker and Container.
- File: `atc/worker/jetbridge/container.go`

## Phase 6: Cross-node artifact fetch (init containers)

### [x] Write tests for local vs remote init container commands f43d052e4
- Tests for init container env vars (MY_NODE_NAME, SOURCE_NODE).
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [x] Implement branching init containers f43d052e4
- `daemonSetFetchCommand()` generates shell script that checks MY_NODE_NAME vs SOURCE_NODE.
- Same node: local tar extraction. Different node: HTTP GET with 3 retries.
- File: `atc/worker/jetbridge/container.go`

## Phase 7: DaemonSetVolume and StreamOut

### [x] Write tests for DaemonSetVolume.StreamOut aacdf1d4d
- Tests for success, 404, and no-source-node error.
- File: `atc/worker/jetbridge/volume_daemonset_test.go`

### [x] Implement DaemonSetVolume aacdf1d4d
- `DaemonSetVolume` implements `runtime.Volume` with HTTP-based StreamOut.
- Retries 3 times with 2s backoff.
- File: `atc/worker/jetbridge/volume_daemonset.go`

### [x] Wire DaemonSetVolume into Worker.LookupVolume aacdf1d4d
- DaemonSet mode returns `DaemonSetVolume`, PVC mode returns `ArtifactStoreVolume`.
- File: `atc/worker/jetbridge/worker.go`

## Phase 8: GC integration

### [x] Write tests for reaper DaemonSet cleanup 1e6ed5aad
- Reaper integration tested via daemonset_integration_test.go locator lifecycle.

### [x] Implement reaper DaemonSet cleanup 1e6ed5aad
- `cleanupDaemonSetArtifacts()` sends HTTP DELETE to DaemonSet pods.
- Removes entries from ArtifactLocator after deletion.
- File: `atc/worker/jetbridge/reaper.go`

## Phase 9: Integration testing

### [x] Write integration test for DaemonSet artifact flow 809d883ab
- Unit-level integration tests covering pod volume, affinity, init containers, locator lifecycle.
- File: `atc/worker/jetbridge/daemonset_integration_test.go`

### [x] Write integration test for spot node preemption 809d883ab
- Covered by hard affinity test (nodes without label won't schedule pods).
- Full KinD-level preemption test deferred to topgun/k8s/integration/.
