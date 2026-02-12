# Plan: Slim Check Pods

## Phase 1: Skip artifact-helper sidecar for check steps

- [x] Write tests for skipping artifact-helper on check pods `ff21455f1`
  - Test: container with check step metadata does NOT get artifact-helper sidecar
  - Test: container with task step metadata still gets artifact-helper sidecar
  - Test: container with get step metadata still gets artifact-helper sidecar
- [x] Implement artifact-helper skip for check steps `ff21455f1`
  - Used `ContainerMetadata.Type == db.ContainerTypeCheck` to identify check containers
  - Gate `buildArtifactHelperSidecar` on step type (skip for checks)
  - Also gated `buildArtifactStoreVolume` to skip PVC volume for checks

## Phase 2: Skip GCS FUSE annotation for check pods

- [x] Write tests for skipping GCS FUSE annotation on check pods `ff21455f1`
  - Test: check pod does NOT have `gke-gcsfuse/volumes` annotation even when GCS FUSE is enabled
  - Test: task pod still has `gke-gcsfuse/volumes` annotation when GCS FUSE is enabled
- [x] Implement GCS FUSE annotation skip for check pods `ff21455f1`
  - Gate `buildPodAnnotations` GCS FUSE logic on `ContainerTypeCheck`

## Phase 3: Verification

- [x] Run full test suites (engine, exec, jetbridge) — all passing (286 jetbridge, engine, exec)
- [ ] Build and deploy to concourse.home
- [ ] Verify check pods have 1 container (no artifact-helper, no GCS FUSE sidecar)
- [ ] Verify task/get/put pods still have artifact-helper + GCS FUSE sidecar
