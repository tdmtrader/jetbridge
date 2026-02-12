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

- [x] Write tests for empty image detection
  - Test: `resolveImage` returning empty causes `buildPod` to return an error
  - Test: error message suggests `--resource-type-image`
- [x] Implement empty image validation
  - In `buildPod`: if resolved image is empty, return error with diagnostic guidance
  - Error propagates cleanly through `createPod`/`createPausePod` to `Run`

## Phase 3: Verification

- [x] Verify on concourse.home
  - Configured `--kubernetes-base-resource-type git-with-ado=concourse/git-resource`
  - Without mapping: check pod created with image `git-with-ado` → `ImagePullBackOff` (type name propagated correctly)
  - With mapping: check pod created with image `concourse/git-resource` → `Running` → check succeeded
  - Verified end-to-end: git clone from github.com/concourse/concourse succeeded via custom type
