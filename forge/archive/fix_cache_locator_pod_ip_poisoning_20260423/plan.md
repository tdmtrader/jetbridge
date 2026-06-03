# Implementation Plan: fix cache locator pod ip poisoning

## Phase 1: Reproduce, fix, and harden [checkpoint: 8574f59bea]

- [x] Task: Write failing test — `FindDaemonResourceCache` hit → 12b0743df1
      downstream `LookupVolume(cacheKey)` → `StreamOut` must succeed
      without the "resolve node IP" error.
      File: `atc/worker/jetbridge/worker_test.go` (new Context under
      the existing `Describe("FindDaemonResourceCache")`).

- [x] Task: Write failing test — `FindDaemonResourceCache` hit writes 1389a73877
      nothing to the `ArtifactLocator` for the cache key.
      File: `atc/worker/jetbridge/worker_test.go`.

- [x] Task: Write failing test — `NodeIPResolver.Resolve(ctx, "10.0.0.5")` ec489efaa8
      returns `ErrNodeNameIsIP` without calling the K8s API
      (assert via fake clientset action recorder).
      File: `atc/worker/jetbridge/node_ip_resolver_test.go` (new file).

- [x] Task: Plumb `ctx context.Context` through febae10233
      `StorageBackend.WrapVolumeForLookup` — update interface, impl,
      both production call sites (`worker.LookupVolume`,
      `worker.ArtifactFromVolume`), and all test call sites.
      Files: `storage.go`, `storage_daemonset.go`, `worker.go`,
      `storage_daemonset_test.go`, plus any other tests that call
      `WrapVolumeForLookup`.

- [x] Task: Add `isResourceCacheKey(key string) bool` helper next to 638c2a8b00
      `ResourceCacheKey`. Match `^rc-\d+$`.
      File: `atc/worker/jetbridge/resource_cache_key.go`.

- [x] Task: Implement Option D in `DaemonSetBackend.WrapVolumeForLookup`: b19a0fb2bc
      on locator miss + `rc-*` key + non-nil `daemonClient`, probe
      with a 5s timeout. Hit → `NewDaemonSetVolumeFromIP`. Miss/error
      → fall through to today's legacy path.
      File: `atc/worker/jetbridge/storage_daemonset.go`.

- [x] Task: Add IP-shape guard + exported `ErrNodeNameIsIP` to 04eade3c88
      `NodeIPResolver.Resolve`. Use `net.ParseIP`.
      File: `atc/worker/jetbridge/node_ip_resolver.go`.

- [x] Task: Delete the poisoning `Record` call in fc36f66737
      `FindDaemonResourceCache`. Update the surrounding comment to
      explain why probe hits are no longer recorded (re-probing on
      lookup avoids stale-entry risk).
      File: `atc/worker/jetbridge/worker.go:365-367`.

- [x] Task: Write positive test —
      `WrapVolumeForLookup(ctx, "rc-42", ...)` with no locator entry cf0b7fa894
      probes and returns an IP-pinned volume whose `StreamOut` hits the
      probed daemon.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Write positive test —
      `WrapVolumeForLookup(ctx, "rc-42", ...)` with a real locator cf0b7fa894
      entry does NOT re-probe — fake daemon sees zero hits; volume
      uses the recorded node name path.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Write negative test —
      `WrapVolumeForLookup(ctx, "artifact-handle", ...)` (non-`rc-*`) cf0b7fa894
      never probes even when the locator is empty.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Write edge-case test —
      `WrapVolumeForLookup(ctx, "rc-42", ...)` with a probe that cf0b7fa894
      errors (no endpoint slices) falls through to the empty-`sourceNode`
      path without panicking.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Audit every call site of `ArtifactLocator.Record(...)`. efc827aec0
      Confirm the `nodeName` argument is always a real K8s Node name
      at each site. Document the audit (sites visited and findings)
      in `cgx.md`.

- [x] Task: Run `make test-unit` and efc827aec0
      `ginkgo ./atc/worker/jetbridge/...`; verify all pass and the
      three originally-red tests are now green.

- [ ] Task: Phase 1 Manual Verification

---
