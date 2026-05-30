# Conductor Growth Experience (CGX)

**Track:** `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

- [2026-04-18] Local verification of `topgun/k8s/integration` specs cannot run on Colima — testcontainers-go fails with `rootless Docker not found` when trying to start K3s. Phase 1 Red verification has to happen on CI or a Linux/Docker Desktop host. Noted as a missing-capability candidate.

---

## Patterns Observed

### Good Patterns (to encode)
- Three-subagent audit sequence (rerun flow → exec flow → DaemonSet flow) narrowed a vague "pods not found" report to a concrete registration-path bug in under an hour. The pattern: each subagent gets a single-hypothesis question and a tight scope, and the next subagent refines based on the previous's findings.

### Anti-Patterns (to prevent)
- First-pass subagent audit claimed "GetStep registers DaemonSetVolume" — this was wrong. GetStep registers the DeferredVolume from its container's mounts (`get_step.go:521` → `resourceMountVolume(mounts)` → `*Volume` from `buildVolumeMountsForSpec` which always builds `*Volume`, never DaemonSet-wrapped). Lesson: trust-but-verify subagent claims about "what this function returns" by reading the source directly before acting on the finding.

---

## Missing Capabilities

- [2026-04-18] K8s integration tests cannot run locally on Colima-based macOS dev machines (K3s-in-Docker-in-Colima produces namespace errors; testcontainers-go also needs rootless Docker or Docker Desktop). Candidate: a `make test-k8s-integration-ci` that pushes the test to a remote runner, or a local stub using envtest/fake clientset for fast iteration on the Ginkgo spec itself (not the end-to-end behavior).

---

## Insights & Suggestions

### Phase 2 Audit Findings

The root cause of `exec stream: pods "..." not found`:

**The `build.Repository` (`atc/exec/build/repository.go`) is a dumb `sync.Map[name → runtime.Artifact]`.** Whatever you pass to `RegisterArtifact` comes back unchanged from `ArtifactFor`. No wrapping, no lazy resolution.

**Every producer step registers a DeferredVolume (exec-backed), not a DaemonSetVolume (HTTP-backed):**

| Step | Registration Site | Volume Source | Type at Registration |
|---|---|---|---|
| `GetStep` | `atc/exec/get_step.go:302` | `step.resourceMountVolume(mounts)` (line 521) | `*Volume` (DeferredVolume) |
| `GetStep` (skip_download) | `atc/exec/get_step.go:211` | literal `nil` | nil (handled by `RegisterImageRef`) |
| `TaskStep` outputs | `atc/exec/task_step.go:728` | `mount.Volume` from `buildVolumeMountsForSpec` | `*Volume` (DeferredVolume) |
| `ArtifactInputStep` | `atc/exec/artifact_input_step.go:72` | `ArtifactFor` result (whatever was registered before) | polymorphic passthrough |

**Why this is a DeferredVolume:**
- `Worker.buildVolumeMountsForSpec` (`atc/worker/jetbridge/worker.go:158-203`) creates all step mounts via `newVolumeForMount`.
- `newVolumeForMount` (line 210) always returns `NewDeferredVolume` when the worker has an executor; otherwise `NewStubVolume`. It never calls `storageBackend.WrapVolumeForArtifact`.
- So any volume handed back to `exec/` as a `runtime.VolumeMount.Volume` is a DeferredVolume pointing at the step's own pod.

**Where the HTTP-backed alternative exists but isn't used by step registration:**
- `Worker.CreateVolumeForArtifact` (`worker.go:217-251`) wraps via `storageBackend.WrapVolumeForArtifact` → `*DaemonSetVolume` when backend is set. **Only called by the HTTP upload path** (`atc/api/artifactserver/create.go:29`). Steps never call this.
- `Worker.LookupVolume` (`worker.go:274-299`) wraps via `storageBackend.WrapVolumeForLookup` → `*DaemonSetVolume`. **Not called during step execution** for artifacts registered by the step itself.
- Fallback at `worker.go:298`: when `storageBackend == nil`, still returns `NewDaemonSetVolume(..., "", ...)` with empty sourceNode — probably a stale branch (should return `*Volume` or error). Flagging for Phase 4.

**Data IS on the DaemonSet by the time RegisterArtifact runs:**
- `execProcess.uploadOutputsToArtifactStore` (`atc/worker/jetbridge/process.go:910-926`) calls `DaemonSetBackend.RecordOutputs`.
- This is invoked at `process.go:870` (non-zero exit) or `process.go:889` (zero exit), **before** the process returns to the step.
- `RecordOutputs` writes the locator entry (ArtifactKey → node + daemon key) and registers the HTTP alias on the node's daemon.
- So when `GetStep.retrieveFromCacheOrPerformGet` returns at `get_step.go:271`, the artifact is already resolvable via the DaemonSet — but the step registers the DeferredVolume anyway.

**The fix (Phase 3):** After the step's process returns, wrap the registered volume as a DaemonSet-backed artifact before calling `RegisterArtifact`. The simplest path is to call `worker.LookupVolume(ctx, vol.Handle())` which returns the DaemonSet wrapper when the backend is set. Alternative: add a dedicated `Worker.AsArtifact(vol) runtime.Artifact` method to avoid reusing a method named "Lookup" for a fresh registration.

### StreamOut / StreamIn Caller Table

Each row: call site → receiver type (interface-level) → concrete runtime type in production → safety note.

**`StreamOut` callers (read side):**

| Call site | Receiver type (compile) | Runtime type (production) | Safety |
|---|---|---|---|
| `atc/worker/streamer.go:24` (`Streamer.StreamFile` → `artifact.StreamOut`) | `runtime.Artifact` | Whatever `ArtifactFor` returned — currently `*Volume` (DeferredVolume) for step-produced artifacts | **UNSAFE**: execs into producer pod; fails if reaped. This is the bug path exercised by Phase 1 tests. |
| `atc/api/artifactserver/get.go:52` (HTTP `GET /api/v1/teams/.../artifacts/:id`) | `runtime.Volume` from `LookupVolume` | `*DaemonSetVolume` | Safe — LookupVolume wraps. |
| `atc/runtime/runtimetest/{volume,artifact}.go` | test stubs | test stubs | N/A |
| `atc/worker/jetbridge/volume.go:209` — the implementation | itself (`*Volume`) | exec into `v.podName` | Correctness depends on who calls it (see above). |

**`StreamIn` callers (write side):**

| Call site | Receiver type | Runtime type | Safety |
|---|---|---|---|
| `atc/api/artifactserver/create.go:36` (HTTP upload) | `runtime.Volume` from `CreateVolumeForArtifact` | `*DaemonSetVolume` (HTTP upload to daemon) | Safe. |
| Step input staging | container's input mount (`*Volume`) | `*Volume` (writes into the live pod via exec tar) | This is the legitimate exec-write use; stays. Note: init containers already fetch via HTTP from the DaemonSet, so step-time exec-StreamIn is a no-op in DaemonSet mode. |

**`Streamer.StreamFile` callers:**

| Call site | Artifact source | Safety |
|---|---|---|
| `atc/exec/task_config_source.go:81` (`FileConfigSource.FetchConfig`) | `repo.ArtifactFor(sourceName)` | **UNSAFE** today: gets DeferredVolume, execs into (possibly reaped) producer pod. Phase 3 fix: registration stores DaemonSetVolume instead. |

### Suspect Call Sites Summary

Any path where `artifact.StreamOut` is called on a step-registered artifact is suspect, because all step-registered artifacts are currently DeferredVolume. Concretely:

1. `FileConfigSource.FetchConfig` for `file: artifact/path.yml` task configs.
2. Any downstream step consuming an artifact via `ArtifactFor` and then calling `StreamOut` (init-container fetch is fine — that path goes through the DaemonSet's HTTP endpoint directly, not through the artifact reference).
3. `load_var` step — `atc/exec/load_var_step.go` (should audit in Phase 3 as a specific test target).
4. `set_pipeline` step — similar `file:` pattern.
5. `image_resource` / `image_artifact` on task steps — resolve through the same repository.

### Key Code Locations for the Fix

- `atc/exec/get_step.go:302` — swap `volume` for DaemonSet wrapper before registration.
- `atc/exec/task_step.go:728` — same swap for task output volumes.
- `atc/exec/artifact_input_step.go:72` — probably fine (passes through), but verify.
- `atc/worker/jetbridge/worker.go` — optionally add `AsArtifact(vol runtime.Volume) runtime.Artifact` helper.
- `atc/runtime/types.go:46-49` — update `Worker` interface if we add `AsArtifact`.

### Phase 2 Decision: Skip pinning tests

The plan's "Write unit tests that pin the current (broken) resolution" task is redundant with the Phase 1 integration tests. Any Phase 2 unit tests asserting "this resolves to DeferredVolume today" would be immediately flipped or removed in Phase 3 when we change the registration. Rolling that work into Phase 3's new unit tests (which assert the CORRECT behavior — "this resolves to DaemonSetVolume") instead of writing then deleting pinning tests.

---

## Improvement Candidates

### [Type: skill] subagent-verify
- **Scope:** global
- **Rationale:** Subagent audits can confidently misreport types / flow when the function they're reading has non-trivial indirection. Adding a "trust-but-verify the top-N claims" step before using audit findings would catch this class of error. In this track, the first subagent claimed GetStep wraps to DaemonSet; reading `get_step.go:521` directly refuted it. A skill that encodes "for each claim the subagent makes about a function's return type, read that function's source" would have prevented a wrong Phase 3 plan.
- **Source:** Phase 2 audit; see Anti-Patterns above.
