# Spec: Direct Image References for Resource Types

## Overview

Add an `image:` field to resource type definitions that allows pipeline authors to specify a
container image reference directly (e.g., `image: concourse/git-resource:latest`), bypassing
the entire resource type image resolution chain (check + get). On K8s, the image ref is passed
straight to the pod spec and kubelet handles the pull natively.

This is the pipeline-level equivalent of `rootfs_uri` for task steps, and the user-facing
equivalent of the operator's `--kubernetes-base-resource-type` flag.

## Requirements

1. Add an `image` string field to the `atc.ResourceType` struct, serialized as `"image"` in
   JSON/YAML pipeline configs.
2. When `image:` is set on a resource type, `ImageForType()` must return a `TypeImage` that
   carries the image reference directly — no `GetPlan` or `CheckPlan`.
3. `FetchImage()` in `build_step_delegate.go` must short-circuit when `TypeImage` carries a
   direct image ref, returning an `ImageSpec` with `ImageURL` set — no check/get execution.
4. Validation: `image:` and `type:`+`source:` are mutually exclusive. Setting both is a
   pipeline config error. Setting neither is also an error.
5. The `image:` field accepts any valid Docker image reference: `repo:tag`,
   `repo@sha256:digest`, `registry/repo:tag`, etc.
6. JetBridge's `resolveImage()` already handles `ImageURL` values — no changes needed in the
   container layer.
7. Existing `type:` + `source:` resource type definitions continue to work unchanged.

## Acceptance Criteria

- [ ] Pipeline config with `resource_types: [{name: foo, image: bar:latest}]` is accepted
      and the resource type image resolves to `bar:latest` without spawning any check/get pods.
- [ ] Pipeline config with both `image:` and `type:` on the same resource type is rejected
      with a clear validation error.
- [ ] Existing resource type definitions using `type:` + `source:` are unaffected.
- [ ] Unit tests cover: ImageForType short-circuit, FetchImage short-circuit, config
      validation (valid, invalid), and end-to-end pod spec image resolution.
- [ ] Verified on concourse.home with a real pipeline using `image:` resource type.

## Out of Scope

- Removing existing registry-image special-casing (metadataFetchImage, skip-image-get, etc.)
  — that's a separate follow-up track.
- Variable interpolation in the `image:` field (standard Concourse `((var))` interpolation
  already applies to all config fields).
- Image pull secrets or auth configuration — kubelet handles this via K8s imagePullSecrets.
- Dynamic image refs from previous pipeline steps (cross-job image passing) — that uses the
  existing task step `image:` artifact mechanism.
