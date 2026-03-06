# Spec: Fix Empty Image for Git-Backed Custom Resource Types on K8s

## Overview

Custom resource types backed by non-registry-image types (e.g., `type: git`) produce check pods with an empty container `image` field on the K8s runtime. This happens because JetBridge's `resolveImage()` only handles `ImageURL` and `ResourceType` — it has no support for the legacy `ImageArtifact` path used by Garden/BaggageClaim to unpack non-Docker rootfs volumes.

In the Kubernetes execution model, all pod container images must reference a container registry. There is no equivalent of "unpack a git repo as a rootfs." The fix must bridge this gap by requiring operators to provide a Docker image mapping for custom resource types that aren't registry-image based.

## Requirements

1. When `FetchImage` returns an `ImageSpec` with empty `ImageURL` for a custom resource type, populate `ImageSpec.ResourceType` with the custom type's name so `resolveImage` can look it up in the operator-provided `ResourceTypeImages` config.

2. Extend `resolveImage()` to return a clear error (or at minimum a diagnostic log) when the resolved image is empty, instead of silently creating a pod with no image.

3. Ensure operators can map custom pipeline resource type names to Docker images via the existing `--resource-type-image` CLI flag (e.g., `--resource-type-image git-with-ado=registry.home/git-with-ado-resource:latest`).

4. Document the constraint: on the K8s runtime, custom resource types that are NOT `type: registry-image` (and don't declare `produces: registry-image`) require an explicit image mapping via `--resource-type-image`.

## Acceptance Criteria

- A custom resource type `type: git` with a corresponding `--resource-type-image git-with-ado=<image>` config produces check pods with the correct container image.
- When no image mapping exists for a non-registry-image custom type, the build fails with a clear error message (not a silent empty-image pod).
- Existing base resource types and registry-image custom types are unaffected.
- Unit tests cover: custom type image resolution via config, missing mapping error, backward compatibility.

## Out of Scope

- Runtime image building from git sources (Kaniko, BuildKit, etc.)
- Automatic image discovery from non-registry sources
- Changes to the `ImageArtifact` / Garden rootfs path
