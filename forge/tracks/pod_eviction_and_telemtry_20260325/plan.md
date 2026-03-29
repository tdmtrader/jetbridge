# Implementation Plan: Pod Eviction and Telemetry

## Phase 1: Enrich Diagnostics Helpers [checkpoint: aae669b1c]

- [x] Task: Write tests for `isPodOOMKilled`, enhanced `writePodDiagnostics`, and `writeNodeDiagnostics` aae669b1c
  - Test `isPodOOMKilled` with: current state OOMKilled, last state OOMKilled, non-OOM termination, no termination
  - Test `writePodDiagnostics` includes: node name, container termination messages, restart counts, last termination state
  - Test `writeNodeDiagnostics` with: MemoryPressure, DiskPressure, spot labels (GKE/EKS/AKS), cordoned node, node not found
  - File: `atc/worker/jetbridge/process_test.go`

- [x] Task: Implement `isPodOOMKilled`, enhance `writePodDiagnostics`, add `writeNodeDiagnostics` aae669b1c
  - Add `isPodOOMKilled(pod) (containerName string, oomKilled bool)` — checks both `State.Terminated` and `LastTerminationState.Terminated` for Reason=="OOMKilled"
  - Enhance `writePodDiagnostics` to include: `pod.Spec.NodeName`, container `State.Terminated.Message`, `RestartCount`, `LastTerminationState`
  - Add `writeNodeDiagnostics(ctx, clientset, pod, w)` — fetches node, writes pressure conditions, spot/preemptible labels (GKE, EKS, AKS), cordon status
  - File: `atc/worker/jetbridge/process.go`

- [x] Task: Phase 1 Manual Verification aae669b1c

## Phase 2: Add `fetchPodFailureContext` and Wire Into Exec Mode [checkpoint: cb6b31403]

- [x] Task: Write tests for `fetchPodFailureContext` and exec-mode error enrichment cb6b31403
  - Test that `fetchPodFailureContext` fetches pod status and writes diagnostics + node diagnostics to stderr
  - Test that when pod is not found (already GC'd), a helpful message is still written
  - Test that `execProcess.Wait` calls `fetchPodFailureContext` on streamInputs, ExecInPod, and uploadOutputsToArtifactStore failures
  - File: `atc/worker/jetbridge/process_test.go`

- [x] Task: Implement `fetchPodFailureContext` and wire into `execProcess.Wait` cb6b31403
  - Add `fetchPodFailureContext(ctx, clientset, config, podName, w)` — fetches pod via Get, calls `writePodDiagnostics` + `writeNodeDiagnostics`, handles pod-not-found gracefully
  - Call it in `execProcess.Wait` at the three gap points:
    1. After `streamInputs` error (process.go ~line 677)
    2. After `ExecInPod` error for non-ExitError cases (process.go ~line 641)
    3. After `uploadOutputsToArtifactStore` error (process.go ~lines 717, 734)
  - File: `atc/worker/jetbridge/process.go`

- [x] Task: Phase 2 Manual Verification cb6b31403

## Phase 3: OOM Detection in pollUntilDone and External Deletion Enrichment [checkpoint: 4dab1f61c]

- [x] Task: Write tests for OOM detection in `pollUntilDone` and node diagnostics on external deletion 4dab1f61c
  - Test that `pollUntilDone` returns OOMKilled error with container name when a container is OOM-killed
  - Test that `pollUntilDone` calls `writeNodeDiagnostics` on eviction and external pod deletion
  - File: `atc/worker/jetbridge/process_test.go`

- [x] Task: Implement OOM detection in `pollUntilDone` and enrich deletion/eviction paths 4dab1f61c
  - Add `isPodOOMKilled` check in `pollUntilDone` loop between `isPodFailedFast` and `isPodEvicted`
  - Add `writeNodeDiagnostics` call to the `isPodEvicted` and `ErrPodDeleted` branches in `pollUntilDone`
  - Add `writeNodeDiagnostics` call to the `ErrPodDeleted` branch in `waitForRunning`
  - File: `atc/worker/jetbridge/process.go`

- [x] Task: Phase 3 Manual Verification 4dab1f61c

---
