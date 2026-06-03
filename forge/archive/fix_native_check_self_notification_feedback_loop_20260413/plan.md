# Implementation Plan: Fix native check self-notification feedback loop

## Phase 1: Gate SaveVersions notification on new versions

- [x] Write tests for conditional SaveVersions notification (`atc/db/resource_config_scope_test.go`) c3e9f6d48b
- [x] Bubble `containsNewVersion` from `saveVersions` to `SaveVersions` and gate notification (`atc/db/resource_config_scope.go:91-100`) c3e9f6d48b
- [x] Phase 1 Manual Verification — theborg/cicd 2026-06-03 (see Cluster Verification below)

---

## Phase 2: Remove UpdateLastCheckEndTime notification

- [x] Write tests verifying UpdateLastCheckEndTime does not notify scanner (`atc/db/resource_config_scope_test.go`) c3e9f6d48b
- [x] Remove `Bus().Notify(atc.ComponentLidarScanner)` from `UpdateLastCheckEndTime` (`atc/db/resource_config_scope.go:269`) c3e9f6d48b
- [x] Phase 2 Manual Verification — theborg/cicd 2026-06-03 (see Cluster Verification below)

---

## Phase 3: Gate SetResourceConfigScope notification on actual change

- [x] Write tests for conditional SetResourceConfigScope notification on Resource (`atc/db/resource_test.go`) c3e9f6d48b
- [x] Update `Resource.SetResourceConfigScope` to only notify when scope ID changes (`atc/db/resource.go:269-290`) c3e9f6d48b
- [x] Write tests for conditional SetResourceConfigScope notification on ResourceType (`atc/db/resource_type_test.go`) c3e9f6d48b
- [x] Update `ResourceType.SetResourceConfigScope` to only notify when scope ID changes (`atc/db/resource_type.go:291-300`) c3e9f6d48b
- [x] Phase 3 Manual Verification — theborg/cicd 2026-06-03 (scanner calls SetResourceConfigScope every scan at `scanner.go:216,405`; 209s window with scanner ticking every 10s produced 0 spurious notifies → no-change suppression confirmed; new probe-img scope assignment exercised the change→notify path)

---

## Phase 4: Integration verification

- [x] Run `make test-unit` and confirm all existing tests pass c3e9f6d48b
- [x] Run `make test-integration` and confirm no regressions c3e9f6d48b
- [x] Verify via debug logging that native checks respect `check_every` without extra scanner cycles — theborg/cicd 2026-06-03: registry-image `probe-img` resolved natively on the web node (no pod) every 15s; 8 native resolves of the same digest produced exactly 1 saved version and 0 scanner notifications over a 46s steady-state window
- [x] Phase 4 Manual Verification — theborg/cicd 2026-06-03 (see Cluster Verification below)

---

## Cluster Verification (theborg / `cicd` namespace, 2026-06-03)

**Target:** live JetBridge web `JetBridge 0.2.116-rc (Concourse 8.0.1)`, image `registry.home/jetbridge:latest` built 2026-06-02 from commit `f6a6a8833d` (clean tree; fix `c3e9f6d48b` is an ancestor, verified via embedded `vcs.revision`).

**Instrument:** a dedicated Postgres session `LISTEN scanner` (the literal channel emitted by `Bus().Notify(atc.ComponentLidarScanner)` — `atc.ComponentLidarScanner == "scanner"`), validated by capturing a manually injected `NOTIFY scanner`. Every remaining notify source in the build was enumerated: gated `SaveVersions`, gated `Resource/ResourceType.SetResourceConfigScope`, and the external `PinVersion`/`UnpinVersion`/`toggleVersion`/`NotifyResourceScanner` paths.

**Natural experiment (pod-path checks, 209s window 11:07:19–11:10:48 UTC):**
- Web logs showed **13 check completions** (each calls `UpdateLastCheckEndTime`); pre-fix every one would emit `NOTIFY scanner`.
- Observed **exactly 2 `NOTIFY scanner` events**, both precisely aligned with the 90s `time` resource saving a *new* version. The same timer's two no-new-version checks, plus all git/trigger no-change checks, produced **zero** notifications.
- Scanner ran a flawless 10s poll cadence (32 ticks, all 10s gaps) — no notification storm.

**Native headline path:** a throwaway `registry-image` resource resolved natively on the web node (`atc.scanner.tick.resolve-resource.resolved-resource`, no pod). 8 native resolves of the same digest → **1 saved version, 0 steady-state notifications**. (Also surfaced, out of scope: the native resolver is HTTPS-strict with no insecure/`ca_certs` support, so `registry.home`'s Traefik-fronted cert fails native checks — see notes.)

**Positive controls (legitimate notifications preserved):** genuine new versions notified (timer); `fly pin-resource` and `unpin-resource` each emitted `NOTIFY scanner` (web PID). Cluster restored afterward (throwaway pipeline destroyed, pins cleared).

**Conclusion:** the self-notification feedback loop is eliminated at the source — bookkeeping (`UpdateLastCheckEndTime`) and no-op re-saves/re-scopes no longer wake the scanner, while genuine new versions and external actions still do.
