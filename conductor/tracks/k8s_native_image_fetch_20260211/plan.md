# Implementation Plan: K8s Native Image Fetch

## Phase 1: Runtime-Aware Delegate Plumbing

Thread a `nativeImageFetch` flag through the build step delegate so `FetchImage` knows when it can skip the physical image download and rely on kubelet.

- [x] Write tests for nativeImageFetch flag in buildStepDelegate
- [x] Implement nativeImageFetch flag in buildStepDelegate
- [x] Task: Phase 1 Manual Verification — 201/201 engine specs pass

---

## Phase 2: Short-Circuit FetchImage

When `nativeImageFetch` is true and the resource type is `registry-image`, skip the `get` step and construct the `ImageURL` directly from the source config and resolved version.

- [x] Write tests for short-circuit FetchImage path
- [x] Implement short-circuit FetchImage
- [x] Task: Phase 2 Manual Verification — 201/201 engine specs pass

---

## Phase 3: Task Delegate FetchImage Passthrough

The `taskDelegate.FetchImage` wraps `buildStepDelegate.FetchImage` with extra event saving. Ensure it works correctly with the short-circuit path.

- [x] Write tests for taskDelegate.FetchImage with nativeImageFetch `1d14255bf`
  - Test: ImageGet event is still saved (build log continuity)
  - Test: ImageCheck event is still saved when checkPlan present
  - Test: returned ImageSpec has ImageURL and no ImageArtifact
- [x] Implement taskDelegate.FetchImage adjustments `1d14255bf`
  - Added variadic `nativeImageFetch` param to NewTaskDelegate for backward compat
  - DelegateFactory.TaskDelegate passes nativeImageFetch flag through
  - Events still saved for log consistency; short-circuit happens in BuildStepDelegate
- [x] Task: Phase 3 Manual Verification — 205/205 engine specs pass

---

## Phase 4: Version Resolution Without Get

The check step stores versions in the DB resource config scope. Extract the resolved version without running a get step.

- [x] Write tests for version extraction from check result
  - Test: after check runs, resolved version with digest is used for image URL
  - Test: when check result has no digest, falls back to source tag
  - Test: when no check plan, URL is constructed from static version on get plan
- [x] Implement version extraction
  - Short-circuit path retrieves check result from `fetchState.Result(checkPlan.ID, &version)`
  - Priority: check result digest > static get plan version > source tag
  - Fallback: if no digest available, use source tag (`repo:tag`) or default to `repo:latest`
- [x] Task: Phase 4 Manual Verification — 207/207 engine specs pass

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
