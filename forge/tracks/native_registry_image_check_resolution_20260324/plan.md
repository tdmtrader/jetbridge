# Implementation Plan: Native registry-image check resolution

## Phase 1: GET step and sidecar fixes (complete)

These changes were made during investigation and are committed.

- [x] Task: Add `RegisterImageRef` on the GET step full-fetch path (`get_step.go:314`) c953ca89c
- [x] Task: Remove `plan.Type != "registry-image"` gate from `imageURLFromGetPlan` — use `source.repository` presence instead c953ca89c
- [x] Task: Add `stripDockerPrefix` helper in `task_step.go` and apply before `parseImageRef` in sidecar digest pinning loop c953ca89c
- [x] Task: Phase 1 tests — verify existing get step, task step, and sidecar tests pass c953ca89c

## Phase 2: Native resource resolution in Lidar scanner (complete)

Lidar scanner routes `registry-image` resources to `resolveResource` when `s.resolver != nil`. This bypasses check pods for periodic background scans only.

- [x] Task: Add `resolveResource` method to `atc/lidar/scanner.go` mirroring `resolveResourceType` — extract `repository`/`tag`/`username`/`password` from source, call `resolver.Resolve()`, save version with `{"digest": digest}` a733640fe
- [x] Task: Filter `registry-image` typed resources in `scanResources` — route to `resolveResource` when `s.resolver != nil` and `resource.Type() == "registry-image"`, fall through to `s.check()` for all other types a733640fe
- [x] Task: Honor `check_every` interval and `check_every: never` in `resolveResource` (same pattern as `resolveResourceType`) a733640fe
- [x] Task: Wire `resourceConfigFactory` through `resolveResource` for `FindOrCreateResourceConfig` / scope / version saving a733640fe
- [x] Task: Unit tests for `resolveResource` — successful resolution, auth forwarding, interval skipping, non-registry-image passthrough a733640fe

## Phase 3: Native resolution for ALL check paths (remaining — this is the critical gap)

Phase 2 only covers the Lidar scanner's periodic background scan. Several other code paths also trigger check containers for `registry-image` resources, bypassing native resolution entirely:

- **Manual check via UI/API** — `atc/api/resourceserver/check.go:52`
- **Webhook-triggered checks** — `atc/api/resourceserver/check_webhook.go:76`
- **Manual job trigger** — `atc/api/jobserver/create_build.go:85` (checks all input resources)

All of these call `TryCreateCheck()` → `CreateBuild/CreateInMemoryBuild` → `CheckStep` → spawns a container running `concourse/registry-image-resource`, which fails for GCP registries because it lacks `google.Keychain`.

### Approach: Intercept at `TryCreateCheck` or `CheckStep` level

**Option A (recommended): Native resolution in `TryCreateCheck`**

Add the same native resolution bypass to `atc/db/check_factory.go:TryCreateCheck()`:
- When `checkable.Type() == "registry-image"` and the resolver is available
- Resolve natively via the image resolver (same as `resolveResource`)
- Save the version directly, skip build creation
- Return a synthetic or no-op build (callers expect a Build return)

This catches ALL callers (Lidar scanner can also be simplified to use this path).

**Option B: Native resolution in `CheckStep.run()`**

Add the bypass in `atc/exec/check_step.go:run()`:
- Before creating a container, check if type is `registry-image` and resolver is available
- Resolve natively, save version, return success
- This catches builds that have already been created but avoids spawning a container

Option A is cleaner (prevents the build from being created at all), but Option B is simpler to implement.

### Tasks

- [ ] Task: Write tests for native resolution bypass in `TryCreateCheck` — verify `registry-image` checks resolve natively, non-registry-image checks still create builds, manual triggers work, webhook triggers work
- [ ] Task: Wire `imageresolver.Resolver` into `checkFactory` (constructor + field)
- [ ] Task: Add native resolution bypass in `TryCreateCheck` for `registry-image` checkables when resolver is available — resolve digest, save version, update check timestamps, return without creating a build
- [ ] Task: Wire the resolver through `atc/atccmd/command.go` into the check factory constructor (same `imgResolver` instance used by Lidar scanner)
- [ ] Task: Update `atc/api/resourceserver/check.go`, `check_webhook.go`, and `atc/api/jobserver/create_build.go` callers if return type changes (they may need to handle nil build gracefully)
- [ ] Task: Verify Lidar scanner's `s.check()` path also benefits from the `TryCreateCheck` bypass (consider simplifying scanner to use `TryCreateCheck` instead of separate `resolveResource`)

## Phase 4: End-to-end verification

- [ ] Task: Deploy and verify no check pods are created for `registry-image` resources — not from Lidar, not from manual trigger, not from webhook, not from job creation
- [ ] Task: Verify GAR images resolve successfully via Workload Identity without explicit `username`/`password` in source
- [ ] Task: Verify manual "check resource" from UI returns correct digest without spawning a pod
- [ ] Task: Verify `source.username`/`source.password` still work for non-GCP private registries
- [ ] Task: Verify existing non-registry-image resources still spawn check pods normally

## Phase 5: End-to-end sidecar image_artifact verification

- [ ] Task: Add behavioral test for sidecar `image_artifact` referencing a `registry-image` get step
- [ ] Task: Verify full pipeline flow: check (native) → get (short-circuit) → task with sidecar `image_artifact` → pod runs successfully
- [ ] Task: Verify pipeline with GAR-hosted sidecar image using `image_artifact` works without explicit credentials

---

## Key code locations

| File | What | Lines |
|------|------|-------|
| `atc/lidar/scanner.go` | `resolveResource` + routing logic (Lidar only) | 285-412 |
| `atc/db/check_factory.go` | `TryCreateCheck` — ALL check paths flow through here | 88-170 |
| `atc/exec/check_step.go` | `CheckStep.run()` — executes check in container | 72-302 |
| `atc/api/resourceserver/check.go` | Manual check API endpoint | 52 |
| `atc/api/resourceserver/check_webhook.go` | Webhook check trigger | 76 |
| `atc/api/jobserver/create_build.go` | Manual job trigger checks all inputs | 85 |
| `atc/imageresolver/resolver.go` | OCI resolver with `google.Keychain` | 34-95 |
| `atc/exec/get_step.go` | skip_download + `imageURLFromGetPlan` | 182-240, 560-579 |
| `atc/atccmd/command.go` | Resolver wiring | 1145, 1191-1196 |
| `atc/worker/jetbridge/config.go` | `--kubernetes-base-resource-type` override | 69-86, 185 |

## Known limitations

- **`((var))` credentials in source are NOT resolved** by the native path. `rs.Source()` / `checkable.Source()` returns raw DB config. This is fine for Workload Identity (no source creds needed) but means explicit `((password))` vars won't work in native resolution. The `username`/`password` extraction only works with literal values.
- **No fallback to check pod** if native resolution fails. If the registry is unreachable or auth fails, the check errors and the resource is skipped until next attempt.
