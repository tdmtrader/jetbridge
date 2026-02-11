# Spec: K8s Native Image Fetch

**Track ID:** `k8s_native_image_fetch_20260211`
**Type:** feature

## Overview

In the JetBridge K8s runtime, resource type images are resolved into pod specs where the kubelet handles image pulling natively. However, the current `FetchImage` path (`atc/engine/build_step_delegate.go:247-327`) still runs a full check+get resource pipeline to physically download custom resource type images into the artifact store — even though JetBridge only uses the `ImageURL` string from the result.

This track eliminates that unnecessary overhead by short-circuiting `FetchImage` for the K8s runtime: resolve the image reference (repo + tag/digest) without physically downloading the image. Kubernetes handles the pull.

## Requirements

1. When running on the JetBridge K8s runtime, `FetchImage` must resolve custom resource type images to a docker image reference (`ImageURL`) **without** running a physical `get` step to download the image.
2. The `check` step (or equivalent version resolution) must still run to resolve the latest version/digest for the image, so the correct tag or digest is used.
3. The resolved `ImageURL` must be a valid docker image reference that kubelet can pull (e.g., `registry.example.com/my-resource:v1.2@sha256:abc...`).
4. When an image artifact is **explicitly required** by downstream steps (e.g., `image:` in a task config referencing a prior step's output), the physical download path must still be available.
5. Image pull secrets and registry configuration must continue to work as before.
6. Base resource types (those in `DefaultResourceTypeImages`) are unaffected — they already resolve to image strings without `FetchImage`.

## Acceptance Criteria

- [ ] Custom resource types on JetBridge resolve to `ImageURL` without running a `get` step
- [ ] Check/version resolution still runs so the correct digest/tag is used
- [ ] Existing pipelines with `image_resource:` in task configs work correctly
- [ ] Explicit `image:` artifact references (task step using a prior get step's output) still physically fetch
- [ ] Image pull secrets are propagated correctly for custom resource types
- [ ] Unit tests cover the short-circuit path
- [ ] No regression for non-K8s runtimes (if any remain)

## Out of Scope

- Changing how base resource types are resolved (already optimal)
- Modifying kubelet pull policies or caching behavior
- Multi-arch image resolution
- Image pre-pulling / warming strategies
