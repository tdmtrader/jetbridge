# Spec: Dead Code Cleanup

**Track ID:** `dead_code_cleanup_20260310`
**Type:** refactor

## Overview

The K8s-native runtime migration and notification-driven component system left behind several dead code paths, broken references, and vestigial abstractions. This track removes them to reduce confusion, prevent runtime errors, and shrink the surface area for future work.

## Requirements

1. **Fix component_factory interval column reference (Critical)** — `component_factory.go` `CreateOrUpdate` still INSERT/UPSERTs the `interval` column, which migration `1773105500` already dropped. This causes a runtime database error. Remove the column reference from production code and tests.

2. **Remove conditionNotifier abstraction** — `build_event_source.go` wraps a `ListenSignal` in a `conditionNotifier` with a condition that always returns `true`. `build.AbortNotifier()` is the only caller with a real condition. Replace both with direct `*NotifySignal` usage, inline the abort condition check in `engine.go`, and delete the `Notifier` interface, `conditionNotifier` type, `newConditionNotifier` function, and `FakeNotifier`.

3. **Drop paused column from components table** — No code path ever sets `paused = true`. The coordinator check, `Paused()` method, interface member, struct field, and test cases are all dead. Add a migration to drop the column and remove all Go references.

4. **Delete gc.Collector interface** — Defined in `atc/gc/collector.go` with zero references anywhere. All GC collectors implement `component.Runnable` directly. Pure deletion.

## Technical Approach

- Work phase-by-phase from highest severity (runtime breakage) to lowest (pure deletion)
- TDD: update/remove tests first, then modify production code
- Regenerate counterfeiter fakes when interfaces change
- Each phase is independently committable — existing tests must pass after each

## Acceptance Criteria

- [ ] `component_factory.go` no longer references `interval` column; tests pass against migrated schema
- [ ] `conditionNotifier`, `Notifier` interface, and `FakeNotifier` are deleted; build_event_source uses `ListenSignal` directly; AbortNotifier replaced with inline signal+condition in engine
- [ ] `paused` column dropped via migration; `Paused()` removed from Component interface; coordinator paused check removed
- [ ] `atc/gc/collector.go` deleted
- [ ] All unit tests pass (`go test ./atc/...`)
- [ ] No `go vet` warnings

## Out of Scope

- Worker cache `refreshInterval` removal (safety net for dropped notifications — intentionally kept)
- Drainable pattern refactoring (requires deeper analysis of context propagation)
- Anything covered by the "Deprecate Old Code Paths" track (`deprecate_old_code_paths_20260305`)
- DB schema changes beyond `paused` column (schema is otherwise clean)
