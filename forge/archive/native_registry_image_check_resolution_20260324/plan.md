# Implementation Plan: Native registry-image check resolution

> **Reconciled & closed 2026-06-07.** The feature shipped via a cleaner approach than
> this plan assumed. Phase 3 targeted **Option A** (intercept `TryCreateCheck`); the team
> instead implemented **Option B** (intercept `CheckStep`), which the plan itself called
> "simpler": `atc/exec/check_step.go:218` resolves `registry-image` natively, and
> `atc/engine/step_factory.go:144` wires `WithCheckResolver(imageResolver)` into **every**
> `CheckStep` (resolver created in `atc/atccmd/command.go:1143`). Commit `f8a29d4e31`.
> Because manual UI checks, webhooks, and job-trigger input checks all run `CheckStep`,
> ALL check paths now resolve natively — no check pods — with `authn.NewMultiKeychain(
> google.Keychain, authn.DefaultKeychain)` (`imageresolver/resolver.go:40`) for GCP WI.
>
> Verification (Phase 4) was live-validated 2026-06-03 by the sibling track
> `fix_native_check_self_notification_feedback_loop_20260413` on theborg/`cicd`. Phase 5
> (sidecar `image_artifact`) is covered by the integration test
> `topgun/k8s/integration/skip_image_get_test.go:174`. The Option-A tasks below are
> therefore **moot** (marked done-via-Option-B).
>
> **Residual gap → new track:** the native resolver ignores `source.insecure` and
> `source.ca_certs`, so registry-image resources on private/self-signed/insecure registries
> fail native resolution. Spun off to `native_resolver_insecure_ca_certs_20260607`.

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

> **Done via Option B (CheckStep), not Option A (TryCreateCheck).** The TryCreateCheck-specific
> tasks below are superseded — the goal (native resolution on all check paths, no pods) is met
> because every CheckStep is wired with the resolver. Implemented in `f8a29d4e31`.

- [x] Task: ~~native resolution bypass tests in `TryCreateCheck`~~ → covered by `atc/exec/check_step_test.go` (`WithCheckResolver`, native vs container paths)
- [x] Task: ~~Wire resolver into `checkFactory`~~ → resolver wired into `CheckStep` instead (`step_factory.go:144`)
- [x] Task: ~~native resolution bypass in `TryCreateCheck`~~ → implemented in `CheckStep.run()` / `resolveNatively` (`check_step.go:218,352`)
- [x] Task: Wire resolver through `atc/atccmd/command.go` → done (`command.go:1143` `imgResolver`, threaded to the step factory)
- [x] Task: ~~Update API callers for nil build~~ → moot; Option B keeps the build, so callers are unchanged
- [x] Task: All check paths (Lidar `s.check()`, manual, webhook, job-trigger) benefit → confirmed: all run `CheckStep`, which now resolves natively

## Phase 4: End-to-end verification

- [x] Task: No check pods for `registry-image` (any trigger) → all paths run `CheckStep.resolveNatively` (no container); live-verified 2026-06-03 on theborg via sibling track `fix_native_check_self_notification_feedback_loop_20260413`
- [x] Task: GAR via Workload Identity without explicit creds → `imageresolver/resolver.go:40` multi-keychain (google.Keychain); GCP Artifact Auth track (`35aaacbfb1`) live on theborg
- [x] Task: Manual "check resource" from UI returns digest, no pod → manual checks run `CheckStep` → native path
- [x] Task: `source.username`/`source.password` still work → forwarded as `BasicAuth` in `resolveNatively` (`check_step.go:359-366`)
- [x] Task: Non-`registry-image` resources still spawn check pods → `check_step.go:220` `else` branch runs the container as before

## Phase 5: End-to-end sidecar image_artifact verification

- [x] Task: Behavioral test for sidecar `image_artifact` → registry-image get step → exists at `topgun/k8s/integration/skip_image_get_test.go:174`
- [x] Task: Full flow check (native) → get (`skip_download`) → task with sidecar `image_artifact` → covered by the same integration test (asserts output + digest-pinned sidecar image)
- [x] Task: ~~GAR-hosted sidecar image via `image_artifact` without creds~~ → inherently a live-GAR scenario (not automatable in CI/testcontainers); validated by the GAR path being live on theborg. Not separately tracked.

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
