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

## Environment notes
- Integration suite needs real K8s; cannot run on local Colima (MEMORY.md).
  Runs in CI (k8s-e2e/k8s-integration-tests, concourse.home).
- CI now builds fresh from `repo` (no image tag bump needed for code changes).
- Integration job is OOM-flaky (~half of runs error at DinD setup); `attempts: 2`
  retries, but early worker-OOM-kills are not recovered — may need a retry or two.
