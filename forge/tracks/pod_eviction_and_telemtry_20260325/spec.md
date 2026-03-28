# Spec: Pod Eviction and Telemetry

**Track ID:** `pod_eviction_and_telemtry_20260325`
**Type:** feature

## Overview

When a pod is killed by the kubelet (OOM, disk pressure, spot node preemption, node drain), exec-mode operations (artifact upload, input streaming, command execution) fail with raw K8s API errors like `"exec stream: unable to upgrade connection: container not found"`. These errors give zero visibility into *why* the pod vanished.

The non-exec (sidecar/baked) code path already has diagnostics via `pollUntilDone`, but exec mode has a gap: once the pod reaches Running, any subsequent pod death during `streamInputs`, `ExecInPod`, or `uploadOutputsToArtifactStore` produces only the raw transport error.

This track closes that gap by fetching pod (and node) status after exec-mode failures and writing human-readable diagnostics to the build log.

## Requirements

1. When an exec-mode operation fails (streamInputs, ExecInPod, uploadOutputsToArtifactStore), fetch the pod's current status and write failure diagnostics to stderr before returning the error.
2. Diagnostics must include: pod phase, reason, message, node name, container termination reasons (especially OOMKilled), restart counts, and last termination state.
3. For eviction and external deletion, fetch node-level conditions (MemoryPressure, DiskPressure, PIDPressure, NotReady, cordoned) and spot/preemptible labels (GKE, EKS, AKS).
4. Add explicit OOM detection (`isPodOOMKilled`) that checks both current and last termination state, and surface it in the `pollUntilDone` loop alongside the existing eviction/unschedulable checks.
5. Enrich the existing `writePodDiagnostics` to include node name, container termination messages, restart counts, and last termination state.

## Acceptance Criteria

- [ ] A pod OOM-killed during artifact upload produces a build log showing `OOMKilled`, the container name, and the node name.
- [ ] A pod evicted for disk pressure produces a build log showing `Evicted`, the kubelet message, and node conditions (DiskPressure=True).
- [ ] A pod deleted due to spot preemption produces a build log showing `pod deleted externally`, node spot label, and cordoned status.
- [ ] Non-exec mode (pollUntilDone) also detects OOMKilled containers explicitly.
- [ ] Unit tests cover: `isPodOOMKilled`, `writeNodeDiagnostics`, `fetchPodFailureContext`, and the enriched `writePodDiagnostics`.

## Out of Scope

- New Prometheus/OTel metrics or counters (visibility only, no new telemetry).
- Handling of network-level transient errors (API server unreachable, etc.).
- Automatic retry or recovery from pod eviction.
- Changes to the non-exec `Process.Wait` / `pollUntilDone` flow beyond adding the OOM check.
