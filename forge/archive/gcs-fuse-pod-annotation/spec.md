# Spec: GCS Fuse Pod Annotation

## Overview

On GKE, pods that mount a PVC backed by the GCS Fuse CSI driver must have the annotation `gke-gcsfuse/volumes: "true"` in their pod metadata. Without it, the GCS Fuse sidecar injector webhook won't inject the FUSE mount helper and the volume mount will fail.

This annotation is added to task/resource pods only when the artifact store PVC is backed by GCS Fuse.

## Requirements

1. New CLI flag `--kubernetes-artifact-store-gcs-fuse` (bool) on the Concourse web command.
2. New `ArtifactStoreGCSFuse bool` field on `jetbridge.Config`.
3. In `buildPod()`, when both `ArtifactStoreGCSFuse` and `ArtifactStoreClaim` are set, add `gke-gcsfuse/volumes: "true"` to pod annotations.
4. Helm chart passes `--kubernetes-artifact-store-gcs-fuse` when `artifactStorePvc.gcsFuse.enabled` is true.
5. Unit tests cover: annotation present when flag+claim set, absent when flag false, absent when no claim.

## Acceptance Criteria

- [x] Pods mounting GCS Fuse artifact PVC have `gke-gcsfuse/volumes: "true"` annotation
- [x] Pods not using GCS Fuse do NOT have the annotation
- [x] Helm chart auto-passes the flag when `gcsFuse.enabled=true`
- [x] Unit tests cover both cases (3 tests added)
- [x] Full build compiles, 257 jetbridge tests pass

## Out of Scope

- GCS Fuse sidecar resource limits
- Cache PVC GCS Fuse support
- Non-GKE FUSE implementations
