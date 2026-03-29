# Spec: K8s Runtime Telemetry Detail

## Overview

Production traces from the K8s runtime show long spans (`k8s.exec-process.wait-for-running`, `k8s.spdy.exec`) that lack the detail needed to diagnose performance bottlenecks. Operators can see *that* a step is slow but not *why* — was it scheduling, image pull, init containers, or storage I/O? Similarly, a series of `k8s.spdy.exec` spans all look identical despite representing fundamentally different operations (step commands, artifact uploads, cache saves, GC cleanup).

This track adds targeted span attributes and events so that production traces become self-explanatory without requiring log correlation.

## Requirements

### wait-for-running enrichment

1. Add `node.name` attribute to the `pod.scheduled` event (identifies node-specific delays).
2. Add `container.image` attribute to the `image.pulling` event (identifies which image is being pulled).
3. Emit an `image.pulled` event when a container transitions out of `ContainerCreating` (gives image pull duration).
4. Emit a `pod.initialized` event when the `Initialized` condition becomes True (single marker for "all init containers done").
5. Add `container.image` attribute to `init.container.completed` and `init.container.failed` events.
6. Add `init.container.count` and `container.count` as span attributes on the `k8s.exec-process.wait-for-running` span at creation time, so operators know what to expect.

### k8s.spdy.exec enrichment

7. Add `exec.command` attribute to the `k8s.spdy.exec` span — record `command[0]` plus args for short commands, or `command[0]` only for long ones.
8. Add `exec.purpose` attribute to distinguish the semantic operation. Callers pass one of: `"step-command"`, `"artifact-upload"`, `"cache-upload"`, `"stream-in"`, `"stream-out"`, `"gc-cleanup"`.
9. Add `artifact.key` and `volume.mount_path` attributes from artifact upload/download callers.
10. Wrap `uploadOutputsToArtifactStore` and `uploadCachesToArtifactStore` in dedicated parent spans (`k8s.artifact.upload-outputs`, `k8s.artifact.upload-caches`) so they appear as grouped operations in the trace, not a flat list of SPDY execs.

## Technical Approach

### ExecInPod signature change
Add an optional `purpose` string parameter (or an attrs/options map) to the `PodExecutor.ExecInPod` interface so callers can pass semantic context. The SPDY executor records these as span attributes alongside the existing pod/container/namespace attributes.

### podEventTracker additions
Extend the existing tracker with:
- A `pulledImages` map to detect the `ContainerCreating` → `Running` transition and emit `image.pulled`.
- An `initialized` bool to track the `Initialized` pod condition.
- Capture `pod.Spec.NodeName` when emitting `pod.scheduled`.

### Caller-site changes
Each `ExecInPod` call site passes appropriate purpose + context attributes:
- `process.go:717` (step command) — `"step-command"`
- `process.go:831` (output upload) — `"artifact-upload"` + artifact key + mount path
- `process.go:862` (cache upload) — `"cache-upload"` + cache key + mount path
- `volume.go:197` (stream-in) — `"stream-in"` + volume path
- `volume.go:246` / `volume_artifactstore.go:118` (stream-out) — `"stream-out"` + volume key
- `reaper.go:220,254` (GC cleanup) — `"gc-cleanup"`

## Acceptance Criteria

- [ ] Production traces show node name, image name, and pull duration within `wait-for-running` spans.
- [ ] Each `k8s.spdy.exec` span has `exec.purpose` and `exec.command` attributes.
- [ ] Artifact upload/cache upload SPDY spans include `artifact.key` and `volume.mount_path`.
- [ ] `uploadOutputsToArtifactStore` and `uploadCachesToArtifactStore` have dedicated parent spans.
- [ ] All existing tests pass; new tests cover the added attributes/events.
- [ ] No behavioral changes — purely additive observability.

## Out of Scope

- Performance optimizations to the artifact upload path (separate track).
- Adding new metrics (this track focuses on trace detail).
- Changes to the trace exporter configuration or sampling.
