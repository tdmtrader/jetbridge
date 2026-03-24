# Spec: Sidecar Image Handoff

**Track ID:** `sidecar_image_handoff_20260323`
**Type:** bugfix

## Overview

Sidecar containers in the K8s runtime have two image resolution bugs that prevent private registry images from working correctly:

1. **`image_artifact` handoff broken**: When a sidecar references an image via `image_artifact` (resolved from a prior get step), the image URL retains the `docker:///` prefix that Concourse uses internally. The main container strips this prefix via `resolveImage()`, but `buildSidecarContainers()` passes `sc.Image` directly to the K8s pod spec. Kubernetes cannot pull `docker:///repo@sha256:...` — it needs `repo@sha256:...`.

2. **Auth not forwarded for tag-based resolution**: When a sidecar uses a tag-based image (e.g., `us-docker.pkg.dev/proj/repo/img:latest`), the ATC resolves it to a digest via `imageResolver.Resolve()` but passes `nil` for auth credentials. The main container path extracts `username`/`password` from the resource's `source` config, but sidecars have no such config. The resolver's default keychain may lack credentials that the kubelet's `imagePullSecrets` do have, causing "unauthenticated" errors against private registries like Google Artifact Registry.

Digest-pinned sidecars (`image: repo@sha256:...`) work correctly because they skip resolution entirely.

## Requirements

1. Strip `docker:///` (and other Concourse URL prefixes) from sidecar image references before building the K8s pod spec.
2. Make sidecar digest pinning best-effort: if resolution fails (e.g., auth error), fall through to the tag-based reference and let the kubelet pull it using pod-level `imagePullSecrets`, emitting a warning log.
3. No changes to the `SidecarConfig` API surface (no new auth fields).
4. No regression for existing sidecar behaviors (inline images, digest-pinned images, resource limits, volume sharing).

## Technical Approach

### Bug 1: `docker:///` prefix stripping
- In `buildSidecarContainers()` (`atc/worker/jetbridge/container.go`), strip `docker:///`, `docker://`, and `raw:///` prefixes from `sc.Image` before assigning to the K8s container spec — same logic as `resolveImage()`.

### Bug 2: Best-effort digest resolution
- In `task_step.go` sidecar resolution loop (lines 310-325), catch errors from `imageResolver.Resolve()` and log a warning instead of failing the step. Leave the original tag-based image reference in place so the kubelet can pull it with its own credentials.

## Acceptance Criteria

- [x] A sidecar using `image_artifact` referencing a registry-image get step produces a valid K8s image ref (no `docker:///` prefix)
- [x] A sidecar using a tag-based private registry image (e.g., GAR) does not fail the task step when ATC-side resolution lacks credentials
- [x] A sidecar using a tag-based public image still gets digest-pinned when resolution succeeds
- [x] A sidecar using a digest-pinned image continues to work unchanged
- [x] Existing sidecar unit and behavioral tests pass without modification

## Out of Scope

- Adding per-sidecar auth credentials to `SidecarConfig`
- Forwarding resource-level credentials to sidecar resolution (sidecars don't have a `source` config)
- Changing the `imageResolver` interface or keychain configuration
