# Spec: Image Ref Hardening for Tasks, etc.

**Track ID:** `image_ref_hardening_for_tasks_etc_20260311`
**Type:** feature

## Overview

On the K8s runtime, task and sidecar containers only need an image reference
(repository + SHA digest) — kubelet handles the actual pull. Today, resolving
task `image_resource:` definitions spawns check+get pods just to determine the
digest, which is wasteful and adds startup latency.

This track replaces pod-based image resolution with in-memory OCI registry API
calls for all runtime container images (tasks, sidecars). Check steps for images
become native OCI HEAD requests. Get steps are only needed when the actual image
artifact (tarball/volume) is required — not when the runtime just needs a ref.

Additionally, sidecars gain the ability to reference prior step outputs as their
image source, enabling workflows like "build a database image in step 1, run it
as a sidecar in step 2."

## Requirements

1. `image_resource:` on task steps resolves image digests via in-memory OCI
   registry API calls — no check or get pods spawned on K8s.
2. `metadataFetchImage()` performs on-demand resolution (calls
   `imageresolver.Resolve()`) when no cached version exists, instead of falling
   back to check+get pods.
3. Resolution extends beyond `registry-image` to any OCI-compatible image type.
4. Sidecar bare-string images (e.g., `redis:7`) are resolved to pinned digests
   (`redis@sha256:...`) via in-memory OCI calls for reproducibility.
5. Sidecars support an `image:` field referencing a prior step's output (artifact
   name), enabling built images to run as sidecars.
6. Get steps remain explicit — users control `skip_download`. When set, the get
   step passes the version/ref forward without downloading the artifact.
7. Auth support: GCP workload identity, anonymous (Docker Hub public), basic auth
   (username/password in source config) — same as existing resolver.
8. Errors are logged and surfaced to the build — no silent failures.

## Technical Approach

### In-memory image resolution

Inject `imageresolver.Resolver` into `buildStepDelegate` alongside the existing
`resourceConfigFactory` and `resourceCacheFactory`. When `metadataFetchImage()`
finds no cached version via `scope.LatestVersion()`, call `resolver.Resolve()`
inline to get the digest, save it to DB, and return the image URL. This replaces
the fallback to check+get pod execution.

### Skip get for image_resource on K8s

`taskDelegate.FetchImage()` currently generates check+get plans via
`FetchImagePlan()` and delegates to `buildStepDelegate.FetchImage()`. On K8s,
the enhanced `metadataFetchImage()` resolves the digest and returns
`ImageSpec{ImageURL: "docker:///repo@sha256:..."}` directly — no plans are
executed.

### Sidecar artifact references

Extend `SidecarConfig` (or the sidecar loading flow in `task_step.go`) to
support an artifact name reference. At build time, look up `ImageRefFor()` from
the artifact repository. This follows the same pattern task steps use with
`ImageArtifactName`.

### Sidecar digest pinning

For bare-string sidecar images, resolve the tag to a digest via
`imageresolver.Resolve()` before passing to the pod spec. This ensures
reproducibility and enables version tracking.

## Acceptance Criteria

- [ ] Task `image_resource:` resolves without spawning any pods on K8s.
- [ ] First build with an uncached image resolves on-demand (no lidar pre-cache
      required).
- [ ] Sidecar with `image: "redis:7"` is pinned to `redis@sha256:...` in pod
      spec.
- [ ] Sidecar with artifact reference (`image: my-built-db`) resolves from
      artifact repository.
- [ ] Get steps with `skip_download` pass version forward without artifact
      download.
- [ ] GCP Artifact Registry, Docker Hub, and basic-auth registries all resolve.
- [ ] Registry failures surface as build errors with clear messages.
- [ ] All existing unit and integration tests pass.
- [ ] No regressions in pipeline execution on K8s runtime.

## Out of Scope

- Lidar pre-caching of task image resources (on-demand resolve is sufficient).
- Changes to how resource checks (git, S3, etc.) run — still in pods.
- Changes to the version format or DB schema.
- Non-K8s runtime changes (Garden/containerd path unchanged).
- mTLS / custom CA support — not adding new, not removing existing.
