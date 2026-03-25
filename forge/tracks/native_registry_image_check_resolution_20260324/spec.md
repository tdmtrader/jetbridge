# Spec: Native registry-image check resolution

**Track ID:** `native_registry_image_check_resolution_20260324`
**Type:** feature

## Overview

Extend the Lidar scanner to resolve `registry-image` typed **resources** natively via the OCI registry API — the same way it already resolves **resource types**. This eliminates check pods for `registry-image` resources, enables automatic GCP auth via `google.Keychain` (Workload Identity / ADC), and removes the need for custom resource types solely to get GAR/ECR auth.

### Background

Today, resource type version discovery uses native resolution (`resolveResourceType` in `atc/lidar/scanner.go`), which calls `imageresolver.Resolver.Resolve()` with a multi-keychain (`google.Keychain` + `authn.DefaultKeychain`). This runs in-process in the ATC/web pod — no check pod needed.

Regular resource checks, however, always spawn a check pod running the `concourse/registry-image-resource` binary. That binary only supports explicit `username`/`password` auth — it cannot use GCP Workload Identity. Users who need GAR auth must create a custom resource type, but custom types break the `registry-image` short-circuit path (GET step optimization, `imageURLFromGetPlan`, sidecar `image_artifact` resolution).

### Solution

Add a `resolveResource` path in the Lidar scanner for resources whose type is `registry-image`. When the resolver is available, these resources skip the check pod entirely and resolve their digest via the registry API — just like resource types do today. This also fixes sidecar `image_artifact` for GAR images, since `type == "registry-image"` enables the GET short-circuit and `RegisterImageRef`.

## Requirements

1. Resources with `type: registry-image` are resolved natively via the image resolver when available (no check pod).
2. Native resolution uses the same `google.Keychain` → `authn.DefaultKeychain` chain, enabling Workload Identity / ADC for GAR.
3. Explicit `source.username` / `source.password` credentials are forwarded as `BasicAuth` when present (same as `resolveResourceType`).
4. Check interval (`check_every`) and `check_every: never` are respected.
5. The `RegisterImageRef` call on the GET step full-fetch path is also added (fixes `image_artifact` for cases where the short-circuit is bypassed).
6. `imageURLFromGetPlan` works for any resource with `repository` + `digest` (not gated on `plan.Type == "registry-image"`).
7. `stripDockerPrefix` is applied before `parseImageRef` in the sidecar digest pinning loop.
8. Resources with non-`registry-image` types continue to use check pods as before.
9. No regressions in existing scanner, check, or sidecar behavior.

## Acceptance Criteria

- [ ] `registry-image` resources are resolved natively (no check pod) when the resolver is configured
- [ ] GAR images resolve successfully via Workload Identity without explicit credentials
- [ ] `source.username`/`source.password` are used when present (private registries without Workload Identity)
- [ ] `check_every` interval and `check_every: never` are honored
- [ ] Sidecar `image_artifact` works for `registry-image` get steps (both short-circuit and full-fetch paths)
- [ ] `imageURLFromGetPlan` produces valid URLs for any resource with `repository` in source
- [ ] Existing resource type native resolution is unaffected
- [ ] Non-`registry-image` resources continue using check pods
- [ ] All existing scanner, get step, task step, and sidecar tests pass

## Out of Scope

- Native resolution for non-`registry-image` resource types (git, s3, etc.)
- Per-sidecar auth credentials in `SidecarConfig`
- Modifying the `concourse/registry-image-resource` binary itself
- ECR-specific keychain support (can be added later to the multi-keychain)
- Webhook-driven check triggers (orthogonal concern)
