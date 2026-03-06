# Spec: Slim Check Pods — Remove unnecessary sidecar containers from check step pods

## Overview

Check step pods on K8s currently include an `artifact-helper` sidecar container (and, on GKE with GCS FUSE, a `gke-gcsfuse-sidecar` injected by the webhook). The artifact-helper exists to receive `tar` uploads of step outputs to the artifact PVC. Check steps never produce artifacts — they only return a version to the ATC via the exec API. The unnecessary sidecar wastes resources (CPU/memory requests per pod) and, on GKE, triggers the GCS FUSE webhook injection of a third container.

Removing the artifact-helper from check pods reduces each check pod from 3 containers to 1 on GKE (or 2 to 1 without GCS FUSE), cutting per-check resource overhead and eliminating the GCS FUSE sidecar injection entirely for checks.

## Requirements

1. **R1: Skip artifact-helper sidecar for check containers.** When jetbridge builds a pod for a check step, do not include the `artifact-helper` sidecar container. The existing gate (`ArtifactStoreClaim != ""`) is too broad — it should also consider whether the step actually needs artifact transfer.

2. **R2: Skip GCS FUSE annotation for check pods.** When the artifact-helper sidecar is absent, the `gke-gcsfuse/volumes` pod annotation should also be omitted. Without the sidecar mounting the PVC, the FUSE mount is unnecessary, and omitting the annotation prevents the GKE webhook from injecting the third container.

3. **R3: No regression for task/get/put steps.** Steps that do produce artifacts (task, get, put) must continue to receive the artifact-helper sidecar and GCS FUSE annotation as before.

## Acceptance Criteria

- [ ] Check pods have exactly 1 container (`main`) when `ArtifactStoreClaim` is configured
- [ ] Check pods do NOT have the `gke-gcsfuse/volumes` annotation
- [ ] Task/get/put pods still have the artifact-helper sidecar when `ArtifactStoreClaim` is configured
- [ ] Task/get/put pods still have the `gke-gcsfuse/volumes` annotation when GCS FUSE is enabled
- [ ] Existing unit and integration tests pass
- [ ] Pipeline check steps work correctly after the change (versions still discovered)

## Out of Scope

- Changing how the artifact-helper works for non-check steps
- Modifying the GCS FUSE webhook behavior (GKE-managed)
- Removing the artifact-helper from steps that don't have outputs but aren't checks (e.g., a task with no outputs — that's a separate optimization)
