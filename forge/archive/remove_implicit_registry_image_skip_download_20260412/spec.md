# Spec: Remove implicit registry-image skip download

**Track ID:** `remove_implicit_registry_image_skip_download_20260412`
**Type:** bugfix

## Overview

JetBridge implicitly skips the physical download for ALL `registry-image` get steps (`isRegistryImage` check in `get_step.go:189`). This prevents users from accessing image content like `labels.json`, `rootfs`, or OCI format output. The `fetch_artifact` param was introduced as a workaround but is a leaky internal flag that shouldn't exist.

The fix removes the implicit skip and restores vanilla Concourse behavior: `skip_download` is the only mechanism that controls whether a get step downloads the resource. Image get plans used for check/step image resolution explicitly set `SkipDownload: true` since JetBridge only needs the image ref URL (K8s pulls images directly).

## Requirements

1. Remove the `isRegistryImage` implicit skip from `get_step.go` — only `SkipDownload` controls the skip path.
2. Remove all `fetch_artifact` param handling from `get_step.go` — no longer needed.
3. Set `SkipDownload: true` on image get plans in `FetchImagePlan` (`atc/config.go`) for registry-image types, preserving the optimization for check and step image resolution.
4. Downstream image ref registration (`RegisterImageRef`) must continue to work on both paths.
5. Resource cache params_hash must no longer include `fetch_artifact` (it's removed) — cache keys are based on actual resource params only.

## Acceptance Criteria

- [ ] `get` of a `registry-image` resource without `skip_download: true` performs the physical download (user can access `labels.json`, format-specific output).
- [ ] `get` of a `registry-image` resource with `skip_download: true` skips the download and registers only the image ref URL (no pod created).
- [ ] Check image resolution still skips the download (no extra pods for checks).
- [ ] `fetch_artifact` param is fully removed — no references in production code.
- [ ] Image ref URL is registered for downstream steps on both skip and non-skip paths.
- [ ] Existing `skip_download` validation still enforced (only valid for registry-image types or types with `image:` field).
- [ ] All existing tests pass with the inverted behavior.

## Out of Scope

- Changes to the resource cache key composition (params_hash already handles format correctly).
- Changes to how `skip_download` is validated at pipeline config time.
- Changes to the daemon artifact cache resolution path.
