# Plan: Slim Check Pods

## Phase 1: Skip artifact-helper sidecar for check steps

- [ ] Write tests for skipping artifact-helper on check pods
  - Test: container with check step metadata does NOT get artifact-helper sidecar
  - Test: container with task step metadata still gets artifact-helper sidecar
  - Test: container with get step metadata still gets artifact-helper sidecar
- [ ] Implement artifact-helper skip for check steps
  - Add a signal to `ContainerSpec` or use `ContainerMetadata.Type` to identify check containers
  - Gate `buildArtifactHelperSidecar` on step type (skip for checks)

## Phase 2: Skip GCS FUSE annotation for check pods

- [ ] Write tests for skipping GCS FUSE annotation on check pods
  - Test: check pod does NOT have `gke-gcsfuse/volumes` annotation even when GCS FUSE is enabled
  - Test: task pod still has `gke-gcsfuse/volumes` annotation when GCS FUSE is enabled
- [ ] Implement GCS FUSE annotation skip for check pods
  - Gate `buildPodAnnotations` GCS FUSE logic on the same step type signal

## Phase 3: Verification

- [ ] Run full test suites (engine, exec, jetbridge)
- [ ] Build and deploy to concourse.home
- [ ] Verify check pods have 1 container (no artifact-helper, no GCS FUSE sidecar)
- [ ] Verify task/get/put pods still have artifact-helper + GCS FUSE sidecar
