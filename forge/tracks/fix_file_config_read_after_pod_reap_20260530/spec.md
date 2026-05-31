# Spec: Fix file-based task-config read after producer get-pod is reaped

**Track ID:** `fix_file_config_read_after_pod_reap_20260530`
**Type:** bugfix
**Branch:** jetbridge
**Origin:** surfaced by `ci_reliability_k8s_e2e_20260530` — once CI stopped testing
stale images (it now builds from a fresh git resource), this real regression
became visible.

## Symptom

The integration spec **`Artifact Read After Producer Pod Reap: loads a
file-based task config even after the producing get pod has been reaped`**
(`topgun/k8s/integration/artifact_read_after_reap_test.go:77-129`) fails
**consistently** (failed both attempts of CI build #181):

```
[FAILED] expected build to succeed; if this fails with `exec stream: pods ... not found`,
the file-config fetch is still resolving through the reaped pod instead of the DaemonSet
Expected <int>: 2 to match exit code: <int>: 0
```

Exit 2 = the Concourse build **errored** (NOT exit 137/OOM — confirmed via the
new `dumpDiagnosticsOnFailure`, which showed normal scheduling events, no
OOMKilled). The sibling spec "materializes a cross-step input…" (line 131) is a
separate, OOM-flaky case that now passes on `attempts: 2` retry — do NOT conflate
the two.

## Reproduction (the test)

Pipeline: `get: task-source` (mock resource producing `tasks/after-reap.yml`) →
`task: hold` (sleep 45) → `task: from-reaped-artifact` with
`file: task-source/tasks/after-reap.yml`. The test **force-deletes the get
step's pod** during `hold`, then expects the file-config task to load its config
and the build to succeed. It fails because the config fetch still resolves
through the now-deleted get pod.

## Causal chain (traced 2026-05-30)

1. `atc/exec/task_config_source.go:68` `FileConfigSource.FetchConfig` →
   `repo.ArtifactFor("task-source")` → `Streamer.StreamFile(artifact, path)` (:81).
2. `atc/worker/streamer.go:23-24` `StreamFile` → `artifact.StreamOut(ctx, path, …)`.
3. The artifact's `StreamOut` is expected to route through the DaemonSet artifact
   cache (HTTP) so it survives the producer pod's death. Instead it **execs into
   the reaped get pod** → `atc/worker/jetbridge/executor.go:122`
   `return fmt.Errorf("exec stream: %w", err)` → "pods not found" → build errors.

The get step DOES register a wrapped artifact:
`atc/exec/get_step.go:296-306` registers `worker.ArtifactFromVolume(volume)`
(wrap defined at `atc/worker/jetbridge/worker.go:380-393`). So the wrap is
present, yet StreamOut still execs.

## Root-cause hypotheses (for the next agent to confirm)

**H1 (most likely):** **Get-step output artifacts are not registered/mirrored
with the DaemonSet cache.** Task outputs (`RecordOutputs`) and resource caches
(`RegisterResourceCache`) are registered with the daemon (see the resilience /
route-artifact tracks), but a *get step's* output volume may not be — so the
daemon has no copy to serve, and `ArtifactFromVolume(...).StreamOut` falls back
to execing into the producing pod. This matches why the cross-step **task-output**
case routes via the daemon (passes) while the **get-output** file-config case does
not (fails). Verify: does anything register the get step's output with the daemon
(DaemonClient mirror / alias / `RegisterResourceCache`)?

**H2:** `ArtifactFromVolume(...).StreamOut` (or the cache-locator lookup it uses)
fails to re-probe/route to the daemon when the producing pod is gone for this
artifact, and falls back to exec. Compare against
`fix_cache_locator_pod_ip_poisoning_20260423` (re-probe on lookup) and the
peer-fallback in the resilience track — the file-config/get-output path may not
go through that re-probe.

The earlier track `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
(archived) intended to remove ALL exec-backed reads; this path was missed.

## Requirements

1. Reading a get-step (or any step) output via `file:` task config must resolve
   through the DaemonSet and succeed even after the producing pod is deleted.
2. No exec-backed StreamOut into a producing pod on the file-config read path.
3. Consistent with the existing DaemonSet artifact architecture (the daemon is
   authoritative for artifact reads — see MEMORY.md project_artifact_architecture).

## Acceptance Criteria

- [ ] Reproduce the failure in a unit/integration test against the real flow
      (deleting the producer volume/pod, then reading via `file:` config),
      asserting it currently execs/fails.
- [ ] Fix so the read routes through the DaemonSet (register/mirror the get-step
      output with the daemon, and/or make the wrapped artifact's StreamOut
      re-probe the daemon instead of execing).
- [ ] `topgun/k8s/integration` spec "loads a file-based task config even after
      the producing get pod has been reaped" passes (build exits 0,
      `file-config-after-reap-done` in output).
- [ ] No regression in the sibling specs or get/task artifact behavior.

## Out of Scope

- The OOM-flaky "materializes a cross-step input…" spec (handled by `attempts: 2`
  in the CI reliability track; investigate separately only if it fails
  *consistently*, not intermittently).
- The CI staleness / OOM infra (the `ci_reliability_k8s_e2e_20260530` track).

## How to run / observe

- The integration suite needs a real K8s env; it CANNOT run on the local Colima
  (per MEMORY.md). It runs in CI: `k8s-e2e/k8s-integration-tests` on
  concourse.home. CI now builds from fresh source (`cd repo`) — no image tag
  bump needed for code changes (per the CI reliability track).
- Trigger + watch: `fly -t home trigger-job -j k8s-e2e/k8s-integration-tests -w`
  (it has `attempts: 2`; this spec fails on BOTH attempts — that is the bug, not
  a flake). Read a past build: `fly -t home watch -j k8s-e2e/k8s-integration-tests -b <N>`.
- Focused (inside a kind-runner pod / real cluster):
  `ginkgo --focus="loads a file-based task config" ./topgun/k8s/integration/`.
- On-failure diagnostics (events + web logs) are now dumped automatically by the
  integration suite's AfterEach (`dumpDiagnosticsOnFailure`).
