# CGX: file-config read after producer pod reap

## How this was found (2026-05-30)
While fixing CI image staleness (`ci_reliability_k8s_e2e_20260530`), the k8s-e2e
integration job began running FRESH code (it now builds from a `repo` git
resource instead of the kind-runner image's stale baked `/src`). That exposed a
real, consistent failure that stale CI had masked: the file-based task-config
read after the producing get pod is reaped execs into the dead pod instead of
routing through the DaemonSet artifact cache.

CI build #181 (k8s-e2e/k8s-integration-tests) confirmed it: the spec failed on
BOTH `attempts: 2` runs (so it is NOT a flake — the sibling "cross-step input"
spec is the OOM-flaky one and passed on retry).

## Key code references
- Test: `topgun/k8s/integration/artifact_read_after_reap_test.go:77-129`
  (file-config case; sibling cross-step case at :131-194 is OOM-flaky, separate).
- `atc/exec/task_config_source.go:68-102` FileConfigSource.FetchConfig →
  `Streamer.StreamFile(artifact, path)` at :81.
- `atc/worker/streamer.go:23-24` StreamFile → `artifact.StreamOut(...)`.
- `atc/exec/get_step.go:296-306` registers `worker.ArtifactFromVolume(volume)`
  (wrap at `atc/worker/jetbridge/worker.go:380-393`; defined-behavior note at
  `atc/worker/jetbridge/volume.go:215`).
- `atc/worker/jetbridge/executor.go:122` `fmt.Errorf("exec stream: %w", err)` —
  the exec site that errors with "pods not found" (proves the read execs into the
  reaped pod rather than routing via the daemon).

## Related tracks
- `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
  (archived) — intended to remove ALL exec-backed reads; this file-config /
  get-output path was missed.
- `fix_cache_locator_pod_ip_poisoning_20260423` (archived) — re-probe on lookup;
  check whether this path goes through that re-probe.
- `artifact_daemon_resilience_20260425` — peer-fallback / mirror on producer
  death; check whether get outputs are mirrored.
- MEMORY.md: "Step producers MUST call worker.ArtifactFromVolume(vol) before
  RegisterArtifact — without this wrap, downstream StreamOut execs into the
  producer pod (exec stream: pods not found)." The wrap IS present here, so the
  gap is deeper (likely the daemon has no copy of the get output to serve).

## Leading hypothesis
H1: get-step outputs are not registered/mirrored with the DaemonSet (unlike task
outputs / resource caches), so the wrapped artifact's StreamOut has nothing to
fetch from the daemon and falls back to exec. Confirm in Phase 0.

## Phase 0 finding (2026-05-30, static trace — NOT yet confirmed against CI)
Traced the full happy path. The steady-state routing is actually wired CORRECTLY,
which refutes pure-H1 and rules out the literal "StreamOut execs" mechanism:

1. Get container spec: `Type=ContainerTypeGet`, `Dir=/tmp/build/get`
   (`get_step.go:238-251`). Runtime Dir volume handle = `{containerHandle}-dir`,
   mounted at `/tmp/build/get` (`worker.go:170-172`).
2. The Dir volume is hostPath-backed at `steps/{containerHandle}/dir`
   (`container.go:873-878 stepVolume` → `storageBackend.StepVolume(name,handle,"dir")`,
   since Get != Check). So the get output SURVIVES pod reap (hostPath is node-level).
3. `RecordOutputs` (`storage_daemonset.go:386-443`) DOES match the get Dir volume:
   `spec.Dir != "" && Type != Task && Type != Check` adds `outputPaths["/tmp/build/get"]`,
   subdir="dir", and registers a daemon alias `ArtifactKey({handle}-dir) → steps/{handle}/dir`
   + locator entry. (Runs at end of `execProcess.Run`, `process.go:889`, while the get
   pod is still alive.)
4. `get_step.go:302` registers `worker.ArtifactFromVolume(dirVolume)` →
   `WrapVolumeForLookup(key=ArtifactKey({handle}-dir), ...)` → a `*DaemonSetVolume`.
5. `DaemonSetVolume.StreamOut` (`volume_daemonset.go:89`) is **HTTP-ONLY — it never
   execs**. So the file-config read on a correctly-wrapped artifact CANNOT reach
   `executor.go:122`. The test's "exec stream: pods not found" string is the test's
   own *assertion message guess* (line 125-126), not necessarily the real runtime error.
   The confirmed fact is exit 2 (errored step).

### Why the sibling cross-step (task→task) case passes but this fails
Task INPUTS are materialized server-side by init containers fetching the daemon
hostPath (`process.go:902 streamInputs is a no-op`). They never touch the
web-process `artifact.StreamOut`. The **file-config read is unique**: the config
must be read in the WEB PROCESS (before the task container exists), so it is the
only path that exercises `DaemonSetVolume.StreamOut` for a *get* output.

### Revised root-cause (H2-flavored, a routing-resilience gap — needs CI confirm)
The lookup-wrapped `DaemonSetVolume` from `ArtifactFromVolume`/`WrapVolumeForLookup`
is created **without a daemonClient** (`storage_daemonset.go:516-539`), unlike
`WrapVolumeForArtifact` (:508-514) which sets it. Consequence: this volume has
**no peer-fallback and no daemon-discovery recovery**. Its StreamOut depends
ENTIRELY on (a) a non-empty `sourceNode` from the in-mem locator, (b) NodeIPResolver
resolving that node (needs nodes/get RBAC), and (c) the alias POST having succeeded
(best-effort, errors only to stderr). If any fail (e.g. `fetchPodNodeName`==""
→ alias skipped at `:427` AND sourceNode==""→ "no source node known"; or node-IP
RBAC/`ErrNodeNameIsIP`), the read hard-fails with NO recovery → errored step.
Candidate fix: have `WrapVolumeForLookup` set the daemonClient (so StreamOut can
probe peers / discover daemons), mirroring `WrapVolumeForArtifact`.

**BLOCKER:** static path looks correct, so the exact runtime failure must be
confirmed from CI build #181's auto-dumped diagnostics (AfterEach events + web
logs). `fly -t home` token is currently invalid — need re-auth to read
`fly -t home watch -j k8s-e2e/k8s-integration-tests -b 181`.

## CI CONFIRMATION (2026-05-30, build #181) — DIAGNOSIS CHANGED: it's a TEST RACE
Pulled build #181 (and the attempts:2 retry). The real failure is NOT in the
file-config read path — the build errors during the GET STEP ITSELF:

    get-step.perform-get.container-attach.failed-to-get-pod:
        pods "...-file-config-after-re-b1-get-81eb9c6a" not found
    get-step.perform-get.exec-process-wait.failed-to-wait-for-pod-running:
        "pod deleted externally before reaching Running: Pending"
    run.errored: "waiting for pod running: pod deleted externally before reaching Running: Pending"

Decisive evidence:
- The spec FAILED IN 6.565s (both the run and the attempts:2 retry, ~6.4s). The
  pipeline's `hold` task sleeps 45s — a genuine read-after-reap run cannot fail in
  <7s. It errored before `hold` (and the file-config read) ever ran.
- `hold-started` never appears in the build output → `hold` never ran.
- k8s events show the get pod `Scheduled` 0s before the failure → created and
  force-deleted almost immediately, while still `Pending`.
- Build exit code = 2 (errored), matching the get-step error above. The test's
  "exec stream: pods not found" assertion message was a wrong guess.

ROOT CAUSE: `deleteProducerPod` (artifact_read_after_reap_test.go:37-75) only waits
for the get pod to EXIST, then force-deletes it. The mock get pod takes a few
seconds to schedule+run, so the test catches it while `Pending`/mid-fetch and kills
it — the get step errors before producing the artifact. The test's INTENT (per its
own comments) is to delete the get pod DURING the `hold` task, AFTER the get
completes. It never reaches that state.

IMPLICATION: the production artifact-routing path is probably FINE (matches the
static trace). The test simply never exercises it. Fix is TEST-ONLY: gate the
get-pod deletion on the get having completed + `hold` having started (e.g. wait for
the type=task `hold` pod to be Running, or for "hold-started" in build output)
before force-deleting the get pod. THEN re-run CI: only if it then fails in the
file-config read do we have a real artifact bug (candidate fix already scoped:
give WrapVolumeForLookup a daemonClient for peer-fallback/discovery).

Note: the sibling cross-step spec (:131) has the SAME racy deleteProducerPod (type=task)
— it passed on attempts:2 retry here, but it's racy for the same reason and should
get the same gating fix.

## Environment notes
- Integration suite needs real K8s; cannot run on local Colima (MEMORY.md).
  Runs in CI (k8s-e2e/k8s-integration-tests, concourse.home).
- CI now builds fresh from `repo` (no image tag bump needed for code changes).
- Integration job is OOM-flaky (~half of runs error at DinD setup); `attempts: 2`
  retries, but early worker-OOM-kills are not recovered — may need a retry or two.

## Learnings
- [2026-05-30] anti-pattern: the spec's whole H1/H2 artifact-routing premise came
  from the TEST'S OWN assertion-message ("if this fails with `exec stream: pods not
  found`...") — a guess baked into `Expect(...).To(gexec.Exit(0), "<guess>")`. The
  actual error was unrelated (get-step race). Don't treat a test's failure-message
  hypothesis as the diagnosis; read the real build output.
- [2026-05-30] good-pattern: static trace said the happy path was CORRECT, so I
  pulled CI build #181 instead of fixing on the hypothesis. The 6.5s runtime (< the
  45s `hold`) + "hold-started" never printing instantly proved the get step died
  first. Duration vs. expected-minimum is a cheap, decisive signal for "did the
  scenario even run."
- [2026-05-30] When a test force-deletes a pod to simulate reap, gate the delete on
  a LATER step running (not just the target pod existing) — otherwise you race the
  target step's own create/run lifecycle. Target by `concourse.ci/step` when
  `type` is ambiguous (multiple task pods).
