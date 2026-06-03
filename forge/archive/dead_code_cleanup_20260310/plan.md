# Implementation Plan: Dead Code Cleanup

## Phase 1: Fix component_factory interval column (Critical) [checkpoint: 0f14f74e4]

- [x] Task: Remove `interval` column from `CreateOrUpdate` INSERT/UPSERT in `atc/db/component_factory.go` (lines 59-65). Change to INSERT name only with `ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name`. 0f14f74e4
- [x] Task: Remove `interval` column references from test setup SQL in `atc/db/component_test.go` (line 17) and `atc/db/component_factory_test.go` (line 20). 0f14f74e4
- [x] Task: Run `go test ./atc/db/...` to verify tests pass. 0f14f74e4
- [x] Task: Phase 1 Manual Verification 0f14f74e4
- [x] Task: Remove `tuneReaperInterval` function and its callers from `topgun/k8s/integration/cluster_lifecycle_test.go`, `topgun/k8s/integration/integration_suite_test.go`, `topgun/k8s_behavioral/cluster_lifecycle_test.go`, and `topgun/k8s_behavioral/behavioral_suite_test.go`. 0f14f74e4

---

## Phase 2: Remove conditionNotifier abstraction [checkpoint: cdbc18fe9]

- [x] Task: In `atc/db/build_event_source.go`, replace `newConditionNotifier(bus, channel, func(){return true,nil})` with direct `bus.ListenSignal(channel)` call. Update `Close()` to call `UnlistenSignal`. Update the select loop to use `signal.C()` instead of `notifier.Notify()`. cdbc18fe9
- [x] Task: Change `build.AbortNotifier()` in `atc/db/build.go` to `AbortSignal()` returning `(*NotifySignal, error)`. Update the `Build` interface in `atc/db/build.go`. cdbc18fe9
- [x] Task: Update `atc/engine/engine.go` abort handling to use `*NotifySignal` directly with inline condition check (query `builds.aborted` on signal, cancel context if true). cdbc18fe9
- [x] Task: Update all test files that reference `AbortNotifier` — engine tests, build fakes, dbfakes. cdbc18fe9
- [x] Task: Delete `atc/db/notifier.go` (conditionNotifier, Notifier interface, newConditionNotifier). cdbc18fe9
- [x] Task: Delete `atc/db/dbfakes/fake_notifier.go`. cdbc18fe9
- [x] Task: Regenerate counterfeiter fakes for Build interface (`go generate ./atc/db/...`). cdbc18fe9
- [x] Task: Run `go test ./atc/db/... ./atc/engine/...` to verify. cdbc18fe9
- [x] Task: Phase 2 Manual Verification cdbc18fe9

---

## Phase 3: Drop paused column from components [checkpoint: 5e5a8a32e]

- [x] Task: Write migration `1773105501_drop_component_paused.up.sql` with `ALTER TABLE components DROP COLUMN IF EXISTS paused`. Write corresponding `.down.sql`. 5e5a8a32e
- [x] Task: Remove `paused` from `componentsQuery` select list in `atc/db/component.go`. Remove `Paused() bool` from Component interface. Remove `paused` field from component struct. Remove from `scanComponent()`. 5e5a8a32e
- [x] Task: Remove `RETURNING ... paused` from `component_factory.go` CreateOrUpdate query and the corresponding Scan target. 5e5a8a32e
- [x] Task: Remove paused check in `atc/component/coordinator.go` (`RunImmediately` method). 5e5a8a32e
- [x] Task: Remove paused-related test cases in `atc/component/coordinator_test.go`. 5e5a8a32e
- [x] Task: Regenerate fakes: `go generate ./atc/db/... ./atc/component/...` 5e5a8a32e
- [x] Task: Run `go test ./atc/db/... ./atc/component/...` to verify. 5e5a8a32e
- [x] Task: Phase 3 Manual Verification 5e5a8a32e

---

## Phase 4: Delete gc.Collector interface [checkpoint: 9d3dc7850]

- [x] Task: Delete `atc/gc/collector.go`. 9d3dc7850
- [x] Task: Run `go build ./atc/gc/...` and `go test ./atc/gc/...` to verify no references. 9d3dc7850
- [x] Task: Phase 4 Manual Verification 9d3dc7850

---
