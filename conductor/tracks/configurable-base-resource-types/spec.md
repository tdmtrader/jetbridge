# Spec: Configurable Base Resource Types + Metadata-Only FetchImage

## Overview

Allow operators to specify their own base resource types at install time, and eliminate the pod-spawning FetchImage chain for custom resource type image resolution on K8s. On K8s, FetchImage never needs to download an image — it only needs an image URL string (`docker:///repo@sha256:...`) and a `ResourceCache` DB record. Today, resolving a custom type's image spawns a recursive check+get pod chain. This is wasteful: the check pod discovers a version (digest), the get pod "fetches" an image that's never downloaded — only its URL is used by `resolveImage()` to set the K8s pod's container image. This track eliminates those pods by making FetchImage a pure metadata operation on K8s.

**Separate concern**: If a pipeline actually needs image content (rootfs for Docker-in-Docker, build contexts), it uses a regular `get` step with `fetch_artifact: true`. That path is unchanged.

## Background

### Current architecture

1. **Base resource types** are hardcoded in `DefaultResourceTypeImages` (config.go:58-67): time, registry-image, git, s3, docker-image, pool, semver, mock. No CLI flag exists to add or override types.

2. **Custom resource types** defined in pipeline YAML go through `ImageForType()` → `FetchImagePlan()` which generates recursive check+get plans. Each level spawns pods.

3. **FetchImage** (`build_step_delegate.go:250-311`) runs the check plan (if version unknown), runs the get plan, then returns:
   - `ImageSpec.ImageURL` — constructed by `imageURLFromSource()` from source+version
   - `ImageSpec.ImageArtifact` — the fetched artifact (IGNORED by K8s runtime)
   - `ResourceCache` — for `FindOrCreateResourceConfig` DB chain

4. **resolveImage()** (`container.go:638-664`) only uses `ImageURL` or `ResourceType` — never `ImageArtifact`.

5. **imageURLFromSource()** (`build_step_delegate.go:317-336`) constructs `docker:///repo@digest` from source config + version. Only works for `registry-image` type today.

### Key insight

On K8s, FetchImage output is **never used as downloaded content**. The check pod discovers a digest, the get pod "downloads" an image, but only the URL string matters. Both operations can be replaced:
- **Check** → Look up latest cached version from `resource_cache_versions` (already populated by lidar scanner)
- **Get** → Construct URL string from source + cached version (pure string operation)
- **ResourceCache** → `FindOrCreateResourceConfig` with the cached version (pure DB operation)

## Requirements

### R1: Configurable base resource types via CLI flag

Operators can specify additional or replacement base resource types at deploy time via `--base-resource-type` flag (repeatable). Format: `name=image` (e.g., `--base-resource-type git=my-registry/custom-git-resource`). These merge into `DefaultResourceTypeImages`, with operator values taking precedence.

### R2: Metadata-only FetchImage for K8s runtime

When running on K8s, `FetchImage` resolves custom type images without spawning pods:
1. Look up the custom type's latest cached version (digest) from the DB
2. Construct the `ImageURL` from the type's source config + cached version
3. Create/find the `ResourceCache` record via DB operations
4. Return `ImageSpec{ImageURL: url}` and the `ResourceCache`

No check pod. No get pod. Pure metadata.

### R3: Graceful fallback when no cached version exists

If a custom type has never been checked (no cached version in DB), fall back to the existing pod-based FetchImage. This handles first-run scenarios before lidar has populated the cache.

### R4: Extend imageURLFromSource beyond registry-image

Today `imageURLFromSource` only constructs URLs for `registry-image` types. Extend it to work with any type that has `produces: registry-image` or whose parent chain resolves to a registry-compatible image format.

## Acceptance Criteria

- [ ] `--base-resource-type name=image` flag is accepted by the ATC and overrides/extends `DefaultResourceTypeImages`
- [ ] Custom resource types with cached versions resolve via metadata-only FetchImage (no pods spawned)
- [ ] First-run scenario (no cached version) falls back to pod-based FetchImage
- [ ] ResourceCache chain is correctly maintained (custom type → parent type → base type)
- [ ] Existing `fetch_artifact: true` path continues to work for image-as-content use cases
- [ ] Pipeline operations (check, get, put, task) work correctly with metadata-resolved images

## Out of Scope

- Changing lidar scanner intervals or check scheduling
- Modifying the `fetch_artifact` param behavior (already implemented)
- Garden runtime changes (metadata-only path is K8s-specific)
- Removing `ImageArtifact` from `ImageSpec` (backward compat with Garden)
