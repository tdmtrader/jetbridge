# Spec: Ephemeral Storage Requests and Limits

**Track ID:** `ephemeral_storage_requests_and_limits_20260325`
**Type:** feature

## Overview

Concourse task containers on K8s can specify CPU and memory requests/limits via `container_limits` and `container_requests`. However, there is no support for `ephemeral-storage`, which means pods using scratch paths or other ephemeral disk can be scheduled on nodes without enough space, or evicted unpredictably.

This track adds `ephemeral_storage` as a new field alongside `cpu` and `memory` in `ContainerLimits`, following the exact same pattern.

## Requirements

1. `container_limits.ephemeral_storage` sets a K8s `ephemeral-storage` limit on the container.
2. `container_requests.ephemeral_storage` sets a K8s `ephemeral-storage` request on the container.
3. Values use the same byte-quantity format as `memory` (e.g., `1G`, `512M`, `5Gi`).
4. QoS behavior matches CPU/Memory: limits-only implies Guaranteed, both implies Burstable, requests-only implies uncapped Burstable.
5. No changes to scratch_paths — this is purely a container-level resource declaration.

## Acceptance Criteria

- [ ] A task YAML with `container_limits: { ephemeral_storage: 5Gi }` produces a pod with `resources.limits.ephemeral-storage: 5Gi`.
- [ ] A task YAML with `container_requests: { ephemeral_storage: 2Gi }` produces a pod with `resources.requests.ephemeral-storage: 2Gi`.
- [ ] Specifying both works correctly (Burstable QoS).
- [ ] Existing CPU/Memory behavior is unchanged.
- [ ] Unit tests cover parsing, wiring, and pod spec generation.

## Out of Scope

- Per-volume `sizeLimit` on emptyDir mounts.
- Auto-summing scratch path sizes into ephemeral-storage requests.
- Changes to the Helm chart or global defaults.
