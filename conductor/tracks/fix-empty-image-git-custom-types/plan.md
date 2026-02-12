# Implementation Plan: Fix Empty Image for Git-Backed Custom Resource Types on K8s

## Phase 1: Wire Custom Type Name into ImageSpec

Thread the custom resource type name through the `FetchImage` path so that `resolveImage` can look it up in the `ResourceTypeImages` config map.

- [x] Write tests for custom type name propagation
  - Test: `FetchImage` for a non-registry-image custom type sets `ImageSpec.ResourceType` to the custom type name
  - Test: `resolveImage` maps the custom type name via `ResourceTypeImages`
  - Test: registry-image custom types still use `ImageURL` (no regression)
  - Test: base resource types still resolve via `ResourceType` (no regression)
- [x] Implement custom type name propagation
  - In `FetchImage` (build_step_delegate.go): when `imageURL` is empty, set `ImageSpec.ResourceType` to `getPlan.Get.Name` (the custom type name from `ImageForType`)

## Phase 2: Error on Empty Image

Add validation in JetBridge to catch empty images before creating pods.

- [ ] Write tests for empty image detection
  - Test: `resolveImage` returning empty causes `buildPod` to return an error
  - Test: error message names the resource type and suggests `--resource-type-image`
- [ ] Implement empty image validation
  - In `resolveImage` or `buildPod`: if the resolved image is empty, return an error with diagnostic guidance
  - Ensure the error propagates cleanly to the build log

## Phase 3: Verification

- [ ] Verify on concourse.home
  - Configure `--resource-type-image git-with-ado=<image>` on the worker
  - Confirm custom type check pods use the mapped image
  - Confirm error message when mapping is missing
