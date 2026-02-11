# Implementation Plan: K8s Native Image Fetch

## Phase 1: Runtime-Aware Delegate Plumbing

Thread a `nativeImageFetch` flag through the build step delegate so `FetchImage` knows when it can skip the physical image download and rely on kubelet.

- [x] Write tests for nativeImageFetch flag in buildStepDelegate
- [x] Implement nativeImageFetch flag in buildStepDelegate
- [ ] Task: Phase 1 Manual Verification

---

## Phase 2: Short-Circuit FetchImage

When `nativeImageFetch` is true and the resource type is `registry-image`, skip the `get` step and construct the `ImageURL` directly from the source config and resolved version.

- [x] Write tests for short-circuit FetchImage path
- [x] Implement short-circuit FetchImage
- [ ] Task: Phase 2 Manual Verification

---

## Phase 3: Task Delegate FetchImage Passthrough

The `taskDelegate.FetchImage` wraps `buildStepDelegate.FetchImage` with extra event saving. Ensure it works correctly with the short-circuit path.

- [ ] Write tests for taskDelegate.FetchImage with nativeImageFetch
  - Test: ImageGet event is still saved (build log continuity)
  - Test: ImageCheck event is still saved when checkPlan present
  - Test: returned ImageSpec has ImageURL and no ImageArtifact
- [ ] Implement taskDelegate.FetchImage adjustments
  - Review `atc/engine/task_delegate.go:106-148` — may need to conditionally skip ImageGet event when get is not run, or keep it for log consistency
  - Ensure the short-circuit result flows through correctly
- [ ] Task: Phase 3 Manual Verification

---

## Phase 4: Version Resolution Without Get

The check step stores versions in the DB resource config scope. Extract the resolved version without running a get step.

- [ ] Write tests for version extraction from check result
  - Test: after check runs, latest version can be retrieved from resource config scope
  - Test: version contains digest for registry-image type
  - Test: when no check plan, URL is constructed from source config alone (repo:tag)
- [ ] Implement version extraction
  - After check plan runs in the short-circuit path, query the resource config scope for the latest checked version
  - Use that version's digest (if present) to construct a pinned image reference (`repo@sha256:...`)
  - Fallback: if no digest available, use source tag (`repo:tag`) or default to `repo:latest`
- [ ] Task: Phase 4 Manual Verification

---

## Phase 5: Integration Verification

End-to-end validation that custom resource types work correctly on the K8s runtime with the short-circuit path.

- [ ] Write integration test for custom resource type image resolution on K8s
  - Pipeline with a custom `registry-image`-based resource type
  - Verify the resource's check/get/put steps use the correct image without a physical get
  - Verify task steps with `image_resource:` resolve correctly
- [ ] Verify no regression for base resource types
  - Base types (git, time, s3, etc.) should still resolve via `DefaultResourceTypeImages` and never hit `FetchImage`
- [ ] Task: Phase 5 Manual Verification

---
