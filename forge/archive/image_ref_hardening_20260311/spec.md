# Spec: Image Ref Hardening

**Track ID:** `image_ref_hardening_20260311`
**Type:** feature

## Overview

Resource types in Concourse are OCI/Docker images. On the K8s runtime, the only
thing that matters about a resource type is its image reference (repository + SHA
digest). Today, checking a resource type's version requires spawning pods — a
registry-image container runs the check script to query the Docker registry. For
nested custom types, this creates chains of check/get pods just to resolve a tag
to a digest.

This track replaces the pod-based resource type checking with direct OCI registry
API calls from the ATC process. Lidar resolves image digests natively via HTTP,
stores them as versions in the existing DB tables, and eliminates all pod overhead
for resource type checks. Resource checks (e.g., "has the git repo changed?")
continue to run in containers as before.

## Requirements

1. Lidar resolves resource type image versions by querying OCI registry APIs
   directly from the ATC process — no pods spawned for resource type checks.
2. Tag-to-digest resolution produces versions in the existing format:
   `{"digest": "sha256:..."}`.
3. GCP Artifact Registry works via workload identity with zero credentials
   configuration.
4. Anonymous registries (Docker Hub public, etc.) work by default.
5. Basic auth (username/password in source config) is supported for private
   registries.
6. The nesting concept disappears — regardless of how many levels deep a custom
   resource type is defined, each resource type resolves directly to an image
   digest.
7. Existing check intervals are preserved (default 1m, configurable via
   check_every).
8. Registry API calls are non-blocking — network latency on one check does not
   block others.
9. Errors are logged and retried on the next scan interval.
10. Version storage, notifications, and scheduler integration remain unchanged.

## Technical Approach

### New package: `atc/imageresolver/`
- Uses `google/go-containerregistry` to query OCI registries
- Resolves image tags to SHA256 digests via manifest HEAD requests
- Supports keychain-based auth: GCP default credentials, basic auth, anonymous
- Implements a `Resolver` interface for testability

### Lidar scanner changes
- Add a dedicated resource type scan pass in `scanner.Run()`
- For each resource type, extract repository+tag from source config, call the
  resolver, and store the digest version via `ResourceConfigScope.SaveVersions()`
- Runs concurrently using the same worker pool pattern (goroutines + channel)
- Respects check intervals and `check_every` configuration
- Skips resource types that use the `image:` field (already have a direct ref)

### Cleanup
- Remove nested TypeImage check/get plan generation for resource type checks
- Simplify `ImageForType()` — resource types always resolve to an ImageRef
- Remove the pod-based check path for resource types from check_step.go

## Acceptance Criteria

- [ ] Resource type checks produce correct digest versions without spawning pods.
- [ ] A pipeline with a custom resource type (type: registry-image) resolves its
      image via native registry call.
- [ ] A pipeline with nested custom types (A depends on B depends on
      registry-image) resolves each type independently via native registry calls.
- [ ] GCP Artifact Registry images resolve using workload identity (no creds).
- [ ] Docker Hub public images resolve anonymously.
- [ ] Private registry images with username/password in source config resolve
      correctly.
- [ ] Check intervals are respected — no more frequent than configured.
- [ ] Registry failures are logged and do not block other resource type checks.
- [ ] All existing unit and integration tests pass.

## Out of Scope

- Task step image download optimization (skip_download) — already done
- Base resource type rethinking — separate discussion
- mTLS / custom CA support — not adding new, not removing existing
- Changes to how resource checks (git clone, S3 list, etc.) run — still in pods
- Changes to the version format or DB schema
