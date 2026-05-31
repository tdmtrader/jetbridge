# Implementation Plan: file-config read after producer pod reap

## Phase 0: Confirm the root cause (read-only)

- [ ] Trace whether get-step output volumes are registered/mirrored with the
      DaemonSet. Grep: `RegisterResourceCache`, `RecordOutputs`, `TriggerMirror`,
      `DaemonClient` usage in `atc/exec/get_step.go`, `atc/engine/`, and
      `atc/worker/jetbridge/`. Compare to how task OUTPUTS get registered
      (the cross-step case that works).
- [ ] Inspect `ArtifactFromVolume(...).StreamOut` in `atc/worker/jetbridge`
      (volume.go / worker.go around the DaemonSet-backed volume): when the
      producing pod is gone, does it re-probe the daemon (cf.
      fix_cache_locator_pod_ip_poisoning) or exec? Find the exec fallback that
      reaches `executor.go:122`.
- [ ] Decide H1 (get output not registered with daemon) vs H2 (StreamOut doesn't
      re-probe). Record the finding in cgx.md.

## Phase 1: Reproduce (RED)

- [ ] Add a focused test that exercises the file-config read path with the
      producing volume/pod removed and asserts it routes via the DaemonSet
      (not exec). Prefer a unit/integration test in `atc/worker/jetbridge` or
      `atc/exec` that can run locally against fakes/real-DB where possible; the
      full behavior is covered by the existing topgun integration spec.
- [ ] Confirm it reproduces the exec-into-reaped-pod failure.

## Phase 2: Fix (GREEN)

- [ ] If H1: register/mirror the get-step output with the DaemonSet so the
      daemon can serve it after the pod dies (mirror on get completion, or
      register a cache alias — mirror the approach used for task outputs /
      resource caches).
- [ ] If H2: make the wrapped artifact's StreamOut re-probe the daemon and route
      via HTTP instead of falling back to exec when the producing pod is absent.
- [ ] Ensure no exec-backed StreamOut remains on this read path.

## Phase 3: Verify

- [ ] `make test-unit` + focused `ginkgo ./atc/exec/ ./atc/worker/jetbridge/`.
- [ ] CI: `k8s-e2e/k8s-integration-tests` spec "loads a file-based task config
      even after the producing get pod has been reaped" passes (build exits 0).
      CI builds from fresh `repo` now, so just push + trigger (no tag bump).
- [ ] Confirm no regression in the sibling read-after-reap / get / task specs.
