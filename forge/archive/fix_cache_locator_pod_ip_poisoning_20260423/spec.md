# Spec: fix cache locator pod ip poisoning

**Track ID:** `fix_cache_locator_pod_ip_poisoning_20260423`
**Type:** bugfix

## Overview

`FindDaemonResourceCache` writes a daemon pod IP into the `ArtifactLocator`
under the `NodeName` field, corrupting the locator. Downstream
`LookupVolume(rc-{id})` reads back the IP-as-nodeName and hands it to
`NodeIPResolver`, which queries the K8s API for a Node object named
`100.68.228.107` and fails with:

    resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found

Regression introduced in commit 864eba169f (2026-04-01,
"fix(cache): use hostPath symlinks instead of daemon API for cache
registration"). Breaks resource cache reuse on any JetBridge cluster
that actually gets a probe hit.

Tests missed it because the existing coverage at
`atc/worker/jetbridge/worker_test.go:466-524` only asserts properties
of the volume returned by `FindDaemonResourceCache` itself
(`Handle()`, `Source()`, concrete type) — never the downstream
`LookupVolume` path where the poisoned entry surfaces.

## Requirements

1. Downstream `LookupVolume(cacheKey)` after a `FindDaemonResourceCache`
   hit must return a volume whose `StreamOut` succeeds without touching
   `NodeIPResolver`.
2. The `ArtifactLocator` must stop receiving daemon pod IPs in its
   `NodeName` field.
3. When a resource-cache-shaped handle (`rc-{digits}`) is looked up and
   has no locator entry, `DaemonSetBackend` must re-probe the live
   daemons and construct a daemon-IP-pinned volume on hit.
4. Regular (non-`rc-*`) lookups must retain their existing behavior
   (locator first, `NodeIPResolver` on `StreamOut`).
5. `FindDaemonResourceCache` must no longer write to the locator on
   the probe-hit path.
6. As a safety net, `NodeIPResolver.Resolve` must return a clear typed
   error when the input is IP-shaped — so any future misuse surfaces
   loudly instead of masquerading as a generic "node not found".

## Technical Approach (Option D)

- Delete the `dsb.artifactLocator.Record(cacheKey, daemonIP, cacheKey)`
  call inside `FindDaemonResourceCache`
  (`atc/worker/jetbridge/worker.go:365-367`).
- Plumb `ctx context.Context` through
  `StorageBackend.WrapVolumeForLookup` (one-method interface change).
- In `DaemonSetBackend.WrapVolumeForLookup`: on a locator miss for a
  `rc-*` key, probe daemons via the existing
  `DaemonClient.ProbeResourceCache` with a 5s timeout. Hit →
  `NewDaemonSetVolumeFromIP`. Miss/error → fall through to today's
  legacy behavior (empty `sourceNode`, `StreamOut` returns the existing
  "no source node known" error).
- Add `isResourceCacheKey(key string) bool` next to `ResourceCacheKey`
  in `atc/worker/jetbridge/resource_cache_key.go`.
- Add IP-shape detection (`net.ParseIP != nil`) at the top of
  `NodeIPResolver.Resolve`. Export sentinel `ErrNodeNameIsIP` so tests
  and future callers can `errors.Is`.

## Acceptance Criteria

- [ ] Regression test: `FindDaemonResourceCache` hit →
      `LookupVolume(cacheKey)` → `StreamOut` round-trips tar bytes
      from a `httptest` daemon without the "resolve node IP" error.
- [ ] `FindDaemonResourceCache` hit writes nothing to the locator
      (new assertion).
- [ ] `WrapVolumeForLookup(ctx, "rc-42", ...)` with no locator entry
      probes and returns an IP-pinned `*DaemonSetVolume`.
- [ ] `WrapVolumeForLookup(ctx, "rc-42", ...)` with a real locator
      entry does NOT re-probe — honors the recorded node name.
- [ ] `WrapVolumeForLookup(ctx, "artifact-handle", ...)` never probes
      on a non-`rc-*` key, even when the locator is empty.
- [ ] `WrapVolumeForLookup(ctx, "rc-42", ...)` on probe error falls
      through without panicking.
- [ ] `NodeIPResolver.Resolve(ctx, "10.0.0.5")` returns
      `ErrNodeNameIsIP` without calling the K8s API.
- [ ] `make test-unit` and `ginkgo ./atc/worker/jetbridge/...` pass.
- [ ] No behavior change in tests that exercise node-name-based flows
      (producer-recorded artifacts, reaper, affinity scheduling).

## Out of Scope

- `ArtifactLocator` / `ArtifactLocation` struct changes (Option B
  rejected — see decision in cgx.md).
- RBAC changes — this is ATC-internal logic.
- Behavioral / K3s integration suites — unit + package coverage is
  sufficient for the regression; behavioral suites are slow and not
  needed to prove this fix.
- Cache-reprobe debouncing or a probe-result cache — not warranted at
  current probe cost (one EndpointSlice list + a few HEAD requests).
