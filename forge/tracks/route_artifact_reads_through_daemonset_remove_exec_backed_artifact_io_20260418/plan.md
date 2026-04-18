# Implementation Plan: Route artifact reads through DaemonSet; remove exec-backed artifact I/O

All work follows the Red-Green-Refactor cycle from `forge/workflow.md`. Each functional task has a "Write tests" sub-task followed by an "Implement" sub-task. Each phase ends with a manual verification step that runs the relevant test tier.

Key reference files:
- `atc/worker/jetbridge/worker.go` (storage backend wiring, volume construction)
- `atc/worker/jetbridge/volume.go` (DeferredVolume, exec-backed `StreamOut`)
- `atc/worker/jetbridge/volume_daemonset.go` (DaemonSetVolume, HTTP-backed `StreamOut`)
- `atc/worker/jetbridge/storage_daemonset.go` (artifact locator, RecordOutputs, init-container build)
- `atc/worker/jetbridge/reaper.go` (pod deletion on exit-status annotation)
- `atc/worker/streamer.go` (StreamFile entry point)
- `atc/exec/task_config_source.go` (FileConfigSource → StreamFile)
- `atc/exec/get_step.go`, `atc/exec/task_step.go`, `atc/exec/put_step.go` (artifact registration)
- `atc/runtime/` (Artifact / Volume / Container interfaces)

---

## Phase 1: Reproduce the failure deterministically

Goal: Land a failing K8s integration test that exhibits the exact production error before making any production code changes. This test becomes the Red in Red-Green-Refactor for the track.

- [~] Task: Write failing K8s integration test for file-config after producer-pod reap
  - File: `topgun/k8s/integration/artifact_read_after_reap_test.go` (new)
  - Scenario: pipeline with a `get` step that produces an artifact containing `task-input.yaml`, followed by a `task` step that uses `file: artifact/task-input.yaml`. Between the two steps, force-delete the get step's pod via the K8s client.
  - Expected (failing today): the task step errors with `exec stream: pods ... not found`. Expected (after fix): the task step succeeds.
  - Follow the patterns in existing `topgun/k8s/integration/*_test.go` suites. Use `CONCOURSE_IMAGE` env var (default `concourse-local:latest`) and the K3s-via-testcontainers setup noted in MEMORY.md.
- [ ] Task: Write failing K8s integration test for cross-step input read after producer-pod reap
  - Same file.
  - Scenario: three-task pipeline where task A produces artifact X, task B runs without touching X, task C consumes X as an input. Force-delete task A's pod after task B completes but before task C starts (or use a short reaper interval to get the same effect).
  - Expected (failing today): task C errors with `exec stream: pods ... not found`.
- [ ] Task: Phase 1 Manual Verification
  - Run: `go test ./topgun/k8s/integration/ -v -count=1 -timeout 30m -run ArtifactReadAfterReap`
  - Confirm both tests fail with the expected error message.
  - Capture the failing output and save to the track's `cgx.md` as baseline evidence.

---

## Phase 2: Audit artifact-read code paths

Goal: Produce a concrete list of every call site that may resolve an artifact read to a `DeferredVolume.StreamOut` instead of `DaemonSetVolume.StreamOut`. No production code changes in this phase — only audit notes + targeted tests that pin current behavior.

- [ ] Task: Enumerate all callers of `Volume.StreamOut` and `Artifact.StreamOut`
  - Use Grep to find all call sites. For each, record in the track's `cgx.md`:
    - File:line of the call
    - Runtime type of the receiver (DeferredVolume vs DaemonSetVolume vs something else)
    - Whether the producer pod must still exist for this call to succeed
- [ ] Task: Trace artifact registration paths in the three step types
  - `atc/exec/get_step.go`: where is the produced artifact registered into the `ArtifactRepository`? Which volume type is registered?
  - `atc/exec/task_step.go`: same question for task outputs.
  - `atc/exec/put_step.go`: same question for put inputs/outputs.
  - Record findings in `cgx.md` with file:line references.
- [ ] Task: Trace the `Streamer.StreamFile` → `artifact.StreamOut` path for file-config
  - `atc/worker/streamer.go` and `atc/exec/task_config_source.go`.
  - Confirm whether the `artifact` seen by `StreamFile` is always the DaemonSet-wrapped one when `ArtifactDaemonHostPath` is set, or whether there are paths that bypass the wrapper.
- [ ] Task: Write unit tests that pin the current (broken) resolution for each suspect path
  - Using `atc/worker/jetbridge/...` existing test patterns (`fake.NewSimpleClientset`, `dbfakes`).
  - Each test should assert "this caller currently resolves to DeferredVolume" so that after the fix, the test will flip to assert "this caller resolves to DaemonSetVolume."
- [ ] Task: Phase 2 Manual Verification
  - Run: `ginkgo ./atc/worker/jetbridge/ ./atc/exec/`
  - All new pinning tests pass (documenting current behavior).
  - Audit checklist in `cgx.md` is complete.

---

## Phase 3: Route all artifact reads through the DaemonSet

Goal: Fix the routing so every artifact-read resolves to `DaemonSetVolume.StreamOut`. Phase 1 tests go green.

- [ ] Task: Write tests for artifact registration returning DaemonSet-backed volumes
  - For each step type (get/task/put), add a test asserting that after the step completes, `ArtifactRepository.ArtifactFor(name).StreamOut(...)` does NOT call into the K8s exec client.
  - Use a `PodExecutor` fake with zero expected calls.
- [ ] Task: Implement artifact registration to always hand a DaemonSetVolume to the repository
  - Likely change in `get_step.go` / `task_step.go` / `put_step.go` or in the common artifact-registration helper.
  - After `RecordOutputs` publishes to the DaemonSet, the repository entry must be the DaemonSet-backed reference, not the producer's DeferredVolume.
  - Address the race between step-process-exit and `RecordOutputs` completion — either by making registration synchronous with `RecordOutputs`, or by making the repository entry a lazy DaemonSet lookup.
- [ ] Task: Write tests for FileConfigSource resolving via DaemonSet
  - `atc/exec/task_config_source_test.go`.
  - Assert that `FileConfigSource.FetchConfig` invokes `DaemonSetVolume.StreamOut` (HTTP path), not `DeferredVolume.StreamOut` (exec path).
- [ ] Task: Implement FileConfigSource routing fix
  - In `atc/exec/task_config_source.go` and/or `atc/worker/streamer.go` — ensure the artifact resolved for file-config is always DaemonSet-backed.
- [ ] Task: Write tests for cross-step input consumption after producer-pod reap
  - Unit-test level: fake the pod being deleted, confirm the downstream consumer still gets its data via the DaemonSet path.
- [ ] Task: Implement cross-step input routing fix
  - Ensure init-container-based fetch (`storage_daemonset.go:104-186`) is used for all downstream step inputs referencing upstream artifacts.
  - Ensure `StreamIn` on a consumer's input mount pulls from the DaemonSet, not from the producer pod.
- [ ] Task: Phase 3 Manual Verification
  - Run: `ginkgo ./atc/worker/jetbridge/ ./atc/exec/`
  - Run: `go test ./topgun/k8s/integration/ -v -count=1 -timeout 30m -run ArtifactReadAfterReap`
  - Phase 1 tests now PASS.
  - All existing unit tests still pass: `make test-unit`.

---

## Phase 4: Fail fast when DaemonSet is not configured

Goal: Make the DaemonSet a hard requirement for the K8s runtime. An unset `ArtifactDaemonHostPath` becomes a startup error.

- [ ] Task: Write tests for startup validation
  - Test: starting a web with K8s runtime enabled and `ArtifactDaemonHostPath=""` returns a clear error.
  - Test: starting with `ArtifactDaemonHostPath="/some/path"` succeeds.
  - Follow existing startup-config test patterns in `atc/atccmd/` and `atc/worker/jetbridge/worker_test.go`.
- [ ] Task: Implement startup validation
  - `atc/worker/jetbridge/worker.go:31-46` — return an error instead of silently skipping DaemonSetBackend construction.
  - `atc/atccmd/command.go` wiring — surface the error at web startup.
  - Error message: "K8s runtime requires `artifactDaemon.enabled=true` (ArtifactDaemonHostPath must be set). The legacy exec-backed artifact path has been removed."
- [ ] Task: Update any existing test/dev configs that rely on the exec path
  - Grep tests and helm values files for uses that leave `ArtifactDaemonHostPath` empty; update them to set the path.
- [ ] Task: Phase 4 Manual Verification
  - Run: `make test-unit`
  - Run: `make test-k8s-integration`
  - Starting a web without the DaemonSet path returns the expected startup error.

---

## Phase 5: Remove the exec path from artifact-read code

Goal: Delete the now-unreachable exec-backed artifact-read code. `Volume.StreamOut` remains only for step-output capture (the `tar cf -` inside a live task container that publishes outputs into the DaemonSet).

- [ ] Task: Write tests asserting `DeferredVolume.StreamOut` is only called from step-output-capture paths
  - Add an audit test (a grep-level or call-graph assertion, or an explicit list maintained in test code) that enumerates allowed callers.
- [ ] Task: Remove or narrow `Volume.StreamOut` read usages
  - If `Volume.StreamOut` is still needed for step-output capture, split it into two methods (or a capture-only helper) so callers cannot accidentally re-introduce an artifact-read usage.
  - Delete any `StreamOut` code paths now confirmed dead.
- [ ] Task: Remove dead helpers related to exec-backed artifact I/O
  - Grep for now-unused functions in `volume.go` and related files; delete them.
- [ ] Task: Phase 5 Manual Verification
  - Run: `make test-unit`
  - Run: `make test-k8s-integration`
  - Run: `make test-k8s-behavioral` (~2-3 hrs, K8S_PROCS=4 in CI)
  - No `exec stream: pods ... not found` errors in any test log.
  - `git grep 'Volume.*StreamOut'` shows only expected (step-output-capture) callers.

---

## Phase 6: Documentation and memory updates

- [ ] Task: Update MEMORY.md `project_artifact_architecture.md` note
  - Clarify: DaemonSet is required (not optional) for the K8s runtime.
  - Current config field: `ArtifactDaemonHostPath` (a path string), not an enum.
  - Exec is retained ONLY for step-output capture; all artifact READS go through the DaemonSet.
- [ ] Task: Update `deploy/chart/values.yaml` documentation
  - Flip `artifactDaemon.enabled` default to `true` if not already, and add a note that disabling it is unsupported on the K8s runtime.
- [ ] Task: Update `CLAUDE.md` if K8s runtime sections reference the exec path
  - Adjust any dev/test instructions that implied exec-backed artifact I/O was a supported mode.
- [ ] Task: Phase 6 Manual Verification
  - Spot-check updated docs render correctly.
  - Confirm track is ready for close-out via `/forge:complete`.

---

## Risk Register

- Phase 3 audit may surface more paths than expected. If the set grows beyond what fits in one track, spin off a dedicated "artifact registration audit" follow-up and scope this track to the known file-config + cross-step paths.
- Phase 4 is a breaking change for any deployment (including dev/test) that left the DaemonSet disabled. Phase 4 must land after Phase 3 has soaked, or be gated behind a `--allow-exec-artifact-io` transition flag if backwards compat is needed during rollout. Default stance per the spec: no transition flag, hard fail-fast.
- Phase 5 deletion is irreversible. Only run it after a full `make test-k8s-behavioral` is green on Phase 4.
