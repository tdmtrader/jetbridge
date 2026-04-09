# Spec: Native registry-image check resolution

**Track ID:** `native_registry_image_check_resolution_20260324`
**Type:** feature

## Overview

Eliminate check containers for `registry-image` resources across ALL check paths by resolving them natively via the OCI registry API. This enables automatic GCP auth via `google.Keychain` (Workload Identity / ADC) and removes the need for custom resource types solely to get GAR/GCR auth.

### Background

Today, three operations exist for `registry-image` resources:

| Operation | Container? | Auth |
|-----------|-----------|------|
| **CHECK (resource types)** | No — native OCI resolution in ATC via `imageresolver.Resolver` | `google.Keychain` (Workload Identity) |
| **CHECK (resources, Lidar periodic)** | No — native OCI resolution (Phase 2, implemented) | `google.Keychain` (Workload Identity) |
| **CHECK (resources, manual/webhook/job-trigger)** | **Yes — spawns check pod** | Only explicit `username`/`password` from source |
| **GET** | No — implicit `skip_download` for `type == "registry-image"` | Kubelet pulls via pod ServiceAccount (Workload Identity) |
| **PUT** | Yes — runs `/opt/resource/out` in container | Operator-provided image handles auth |

Phase 2 (implemented) added native resolution to the Lidar scanner's periodic background scan. However, **multiple other code paths bypass this and still spawn check containers:**

1. **Manual "check resource" from UI/API** — `atc/api/resourceserver/check.go:52`
2. **Webhook-triggered checks** — `atc/api/resourceserver/check_webhook.go:76`
3. **Manual job trigger** — `atc/api/jobserver/create_build.go:85` (calls `TryCreateCheck` for every input resource)

All three call `TryCreateCheck()` → create a check build → run `CheckStep` → spawn a `concourse/registry-image-resource` container. This container only supports explicit `username`/`password` auth and fails for GCP Artifact Registry with Workload Identity ("anonymous is not entitled to pull image").

### Solution

Intercept at `TryCreateCheck` in `atc/db/check_factory.go` — the single function all check paths flow through. When the checkable type is `registry-image` and the image resolver is available, resolve the digest natively and save the version directly without creating a build or spawning a container.

### Post-implementation state

| Operation | Container? | Auth |
|-----------|-----------|------|
| **CHECK (all paths)** | No — native OCI resolution | `google.Keychain` (Workload Identity) automatically |
| **GET** | No — skip_download | Kubelet pulls via Workload Identity |
| **PUT** | Yes — operator-provided image | Operator's image handles auth (e.g., `google.Keychain`) |
| **GET (fetch_artifact)** | Yes — operator-provided image | Same as PUT |

## Requirements

1. ALL check paths for `registry-image` resources use native OCI resolution when the resolver is available — including manual checks, webhooks, and job-trigger checks.
2. Native resolution uses the same `google.Keychain` → `authn.DefaultKeychain` chain, enabling Workload Identity / ADC for GAR.
3. Explicit `source.username` / `source.password` credentials are forwarded as `BasicAuth` when present.
4. Check interval (`check_every`) and `check_every: never` are respected.
5. The `RegisterImageRef` call on the GET step full-fetch path is also added (fixes `image_artifact` for cases where the short-circuit is bypassed).
6. `imageURLFromGetPlan` works for any resource with `repository` + `digest` (not gated on `plan.Type == "registry-image"`).
7. `stripDockerPrefix` is applied before `parseImageRef` in the sidecar digest pinning loop.
8. Resources with non-`registry-image` types continue to use check pods as before.
9. No regressions in existing scanner, check, or sidecar behavior.
10. API callers that expect a Build return from `TryCreateCheck` handle the native resolution case gracefully.

## Acceptance Criteria

- [ ] Manual "check resource" from UI resolves `registry-image` natively (no check pod) and returns correct digest
- [ ] Webhook-triggered checks for `registry-image` resolve natively (no check pod)
- [ ] Manual job trigger checks for `registry-image` input resources resolve natively (no check pod)
- [ ] Lidar periodic scans continue to resolve natively (existing behavior preserved)
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
- Modifying the upstream `concourse/registry-image-resource` binary
- ECR-specific keychain support (can be added later to the multi-keychain)
- Webhook-driven check triggers (orthogonal concern)
- Credential variable `((var))` resolution in the native scanner path (source credentials are raw from DB; Workload Identity path doesn't need them)
- Swapping base resource type images via `--kubernetes-base-resource-type` (deployment config, no code changes needed)
