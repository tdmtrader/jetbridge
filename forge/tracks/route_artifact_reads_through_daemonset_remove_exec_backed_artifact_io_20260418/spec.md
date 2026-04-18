# Spec: Route artifact reads through DaemonSet; remove exec-backed artifact I/O

**Track ID:** `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
**Type:** bugfix

## Overview (Why)

File-based task configs (`task: foo file: artifact/task-input.yaml`) and cross-step artifact reads intermittently fail with:

```
exec stream: pods "pipeline-build-b1714-get-50ba4c90" not found
```

The failure surfaces whenever a downstream step tries to `StreamOut` an artifact whose producing pod has already been reaped. Two distinct scenarios have been observed in production:

1. A non-rerun build reaching its third task ~10–12 minutes after the producing get step.
2. A rerun-with-old-inputs build reaching a file-config task ~30 seconds after the producing get step.

The deployment runs with `artifactDaemon.enabled=true`, so the DaemonSet artifact cache is available. The DaemonSet is designed to decouple artifact reads from producer-pod lifetime via node-local hostPath + HTTP. However, some read paths still resolve to `DeferredVolume` (exec-backed `tar cf -` into the producing pod) instead of `DaemonSetVolume` (HTTP to the node's DaemonSet pod). When the producer pod has been reaped — which happens as soon as the producer writes its exit-status annotation (`atc/worker/jetbridge/reaper.go:87-98`, `process.go:932-940`) — those exec-backed reads fail.

The DaemonSet artifact cache should be the authoritative source for all artifact reads. Any remaining exec-backed artifact-read path is a reliability liability that should be removed.

## Requirements (What)

1. Every artifact read (`StreamOut` / `StreamFile`) on a DaemonSet-enabled worker resolves to a `DaemonSetVolume` regardless of which step produced the artifact or when the read happens relative to the producer pod's lifecycle.
2. `FileConfigSource.FetchConfig` (`atc/exec/task_config_source.go:48-102`) never execs into a producer pod. The file is fetched via the DaemonSet.
3. Cross-step input materialization succeeds even after the producer pod has been reaped. A downstream task consuming a prior artifact must not depend on the producer pod still existing.
4. The exec-backed `Volume.StreamOut` path (`atc/worker/jetbridge/volume.go:209`) is removed from the artifact-read code paths. `Volume.StreamOut` remains ONLY for capturing a task pod's live in-container outputs into the DaemonSet (the step-output-capture direction, which inherently requires exec into a running pod).
5. When `ArtifactDaemonHostPath` is empty on a K8s-runtime web, the web fails fast at startup with a clear error message. DaemonSet becomes a hard requirement for the K8s runtime.
6. No regression in existing K8s integration or behavioral tests.

## Technical Approach (How)

### Key findings from codebase research

- `NewWorker` at `atc/worker/jetbridge/worker.go:31-46` constructs a `DaemonSetBackend` only if `config.ArtifactDaemonHostPath` is non-empty. The presence of that path is the current gate; there is no explicit "require DaemonSet" switch.
- `CreateVolumeForArtifact` (`worker.go:248`) and `LookupVolume` (`worker.go:296`) route through `storageBackend.WrapVolumeForArtifact` / `WrapVolumeForLookup` when the backend is set. These wrap outputs as `DaemonSetVolume`.
- `buildVolumeMountsForSpec` (`worker.go:158-203`) creates `DeferredVolume` instances for each step pod's input/output mounts. These are exec-backed by design — they stage data INTO running pods.
- `FileConfigSource.FetchConfig` (`atc/exec/task_config_source.go:48-102`) calls `repo.ArtifactFor(sourceName)` and then `Streamer.StreamFile` → `artifact.StreamOut`. The runtime type of `artifact` depends on how the artifact was registered with the repository. If the artifact reference came from a `DeferredVolume` (pre-`RecordOutputs` window, or a code path that didn't route through the storage backend), the read will exec.
- `Volume.StreamOut` (`atc/worker/jetbridge/volume.go:209-268`) execs `tar cf -` in the producer pod. This is the specific call that 404s when the pod has been reaped.
- `Reaper.reap` (`atc/worker/jetbridge/reaper.go:87-98`) deletes pods as soon as they carry a `concourse.ci/exit-status` annotation. There is no coupling to downstream-volume-in-use checks. This is correct behavior given the DaemonSet design — the producer pod should be disposable — but it exposes any remaining exec-backed read path.

### Approach

The fix is not "keep producer pods alive longer." That fights the design. The fix is to guarantee every artifact read goes through the DaemonSet and then remove the exec fallback entirely.

Work proceeds in phases:

1. **Reproduce** the failure deterministically via an integration test that forces producer-pod reaping before a downstream read, covering both file-config and ordinary cross-step input consumption.
2. **Audit** every artifact-read path to identify where a `DeferredVolume` reference is being handed off instead of the DaemonSet wrapper. Likely suspects: the artifact registration on get-step completion, the `ArtifactRepository` entries, and any direct volume references in `TaskStep` / `GetStep` / `PutStep`.
3. **Fix the routing** so that after a step completes and `RecordOutputs` publishes to the DaemonSet, the repository entry is the DaemonSet-backed volume. Also make sure the race window between step-process-exit and `RecordOutputs` doesn't leave a DeferredVolume-typed reference exposed.
4. **Fail fast** on missing DaemonSet config at web startup.
5. **Remove** the exec path from artifact-read code. Keep `Volume.StreamOut` only for the step-output-capture direction (which is the legitimate exec user).

### Key files likely to change

- `atc/exec/task_config_source.go` — ensure `FileConfigSource` resolves via the DaemonSet-backed artifact.
- `atc/exec/get_step.go`, `atc/exec/task_step.go`, `atc/exec/put_step.go` — audit artifact registration; ensure the volume handed to the repository is the DaemonSet-backed one post-`RecordOutputs`.
- `atc/worker/jetbridge/worker.go` — startup validation on `ArtifactDaemonHostPath`.
- `atc/worker/jetbridge/volume.go` — delete or narrow `StreamOut`'s read-path usage once no caller reaches it.
- `atc/worker/streamer.go` — audit the stream flow for type-asserting DaemonSet-capability rather than blindly calling `StreamOut`.
- New K8s integration test: `topgun/k8s/integration/artifact_read_after_reap_test.go` (or nearest equivalent).

### Risks

- The audit in step 3 may surface more paths than expected. If the scope balloons, split into a dedicated "artifact registration audit" track.
- Fail-fast on `ArtifactDaemonHostPath` is a breaking change for any test or dev deployment that relies on the exec path. Phase 4 ordering should be after all tests are updated.
- Removing `Volume.StreamOut`'s read path (Phase 5) is the final, irreversible step. Should only happen after Phases 2–4 are in and a full K8s behavioral run is green.

## Acceptance Criteria

- [ ] New integration test: file-config task succeeds when the producer get step's pod has been deleted before the task runs.
- [ ] New integration test: cross-step input consumption succeeds when the producer pod has been deleted.
- [ ] Grep of the codebase shows no call paths where `FileConfigSource.FetchConfig` or a downstream artifact-read resolves to a `DeferredVolume.StreamOut`.
- [ ] K8s runtime web fails to start when `ArtifactDaemonHostPath` is unset, with a clear error message.
- [ ] `Volume.StreamOut` is called only from step-output-capture code paths (verified by audit + test coverage).
- [ ] Existing K8s integration (`make test-k8s-integration`) and behavioral (`make test-k8s-behavioral`) suites pass with no regressions.
- [ ] No `exec stream: pods ... not found` errors observed during a full k8s_behavioral run.

## Out of Scope

- Removing exec from the step-output-capture direction (`tar cf -` from a live task container into the DaemonSet). This is the one legitimate remaining use of exec for artifact I/O.
- Changes to PVC-mode artifact-helper behavior. PVC mode is not the target deployment mode; it remains as-is or gets deprecated in a follow-up track.
- Reaper logic changes. The reaper's aggressive deletion is correct given the DaemonSet design; the fix is upstream of the reaper.
- Pod naming changes (e.g., sanitizing `.` in rerun build names). Pod names with `.` are valid DNS-1123 subdomains and are not the cause of the failure.
