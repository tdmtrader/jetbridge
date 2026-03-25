# Implementation Plan: Native registry-image check resolution

## Phase 1: GET step and sidecar fixes (already done)

These changes were made during investigation and are ready to commit.

- [x] Task: Add `RegisterImageRef` on the GET step full-fetch path (`get_step.go:314`) c953ca89c
- [x] Task: Remove `plan.Type != "registry-image"` gate from `imageURLFromGetPlan` — use `source.repository` presence instead c953ca89c
- [x] Task: Add `stripDockerPrefix` helper in `task_step.go` and apply before `parseImageRef` in sidecar digest pinning loop c953ca89c
- [x] Task: Phase 1 tests — verify existing get step, task step, and sidecar tests pass c953ca89c

## Phase 2: Native resource resolution in Lidar scanner

- [x] Task: Add `resolveResource` method to `atc/lidar/scanner.go` mirroring `resolveResourceType` — extract `repository`/`tag`/`username`/`password` from source, call `resolver.Resolve()`, save version with `{"digest": digest}` a733640fe
- [x] Task: Filter `registry-image` typed resources in `scanResources` — route to `resolveResource` when `s.resolver != nil` and `resource.Type() == "registry-image"`, fall through to `s.check()` for all other types a733640fe
- [x] Task: Honor `check_every` interval and `check_every: never` in `resolveResource` (same pattern as `resolveResourceType`) a733640fe
- [x] Task: Wire `resourceConfigFactory` through `resolveResource` for `FindOrCreateResourceConfig` / scope / version saving a733640fe
- [x] Task: Unit tests for `resolveResource` — successful resolution, auth forwarding, interval skipping, non-registry-image passthrough a733640fe
- [~] Task: Phase 2 Manual Verification — deploy and verify no check pods are created for `registry-image` resources; verify GAR images resolve via Workload Identity

## Phase 3: End-to-end sidecar image_artifact verification

- [ ] Task: Add behavioral test for sidecar `image_artifact` referencing a `registry-image` get step
- [ ] Task: Verify full pipeline flow: check (native) → get (short-circuit) → task with sidecar `image_artifact` → pod runs successfully
- [ ] Task: Phase 3 Manual Verification — run a pipeline with GAR-hosted sidecar image using `image_artifact`, confirm it works without explicit credentials

---
