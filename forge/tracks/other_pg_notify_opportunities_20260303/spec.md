# Spec: Convert Polling Components to Notification-Driven

**Track ID:** `other_pg_notify_opportunities_20260303`
**Type:** refactor

## Overview

Concourse components use a `component.Runner` framework that supports both periodic polling and PostgreSQL NOTIFY-driven execution. Currently, only Scheduler and BuildTracker run in notification-only mode (`NotifyOnly: true`, `Interval: 0`). All other components poll on fixed intervals (10s–30s), causing unnecessary DB load and — critically — latency: components can wait up to a full polling interval before reacting to state changes. This is especially painful in tests, where polling intervals directly translate to wall-clock test duration.

This track audits every polling component and converts viable candidates to notification-only mode, using `Bus().Notify(atc.Component*)` at the DB mutation points where relevant state changes occur.

## Motivation

- **Latency:** Components react up to 10–30s after state changes instead of immediately.
- **Test performance:** Integration tests wait for polling tickers; notification-driven components respond instantly.
- **Consistency:** Scheduler and BuildTracker already demonstrate the pattern. Other components should follow suit where it makes sense.
- **DB load:** Polling components run full-table scans every 30s regardless of whether anything changed.

## Requirements

1. **LidarScanner** — Convert to notification-only. Add `NOTIFY scanner` at resource/resource-type state-change points (config scope assignment, version save, pin/unpin, webhook trigger). Already has partial infrastructure (`NotifyResourceScanner()` called from config save and pipeline unpause).

2. **SyslogDrainer** — Convert to notification-only. Add `NOTIFY drainer` when builds finish (`build.Finish()`), which is the event that makes builds drainable.

3. **BuildReaper (BuildLogCollector)** — Convert to notification-only. Add `NOTIFY reaper` when builds finish, since build completion is the trigger for log retention evaluation.

4. **GC Collectors** (13 collectors) — Convert viable collectors to notification-only. Add NOTIFY calls at the lifecycle state-change points that create work for each collector:
   - `collector_builds` — notify on build completion (interceptibility changes)
   - `collector_containers` — notify on container state transitions (creating→failed, marking for destruction)
   - `collector_volumes` — notify on volume state transitions
   - `collector_checks` — notify on check completion
   - `collector_resource_caches` — notify on cache invalidation
   - `collector_resource_cache_uses` — notify on build completion (cleans finished build uses)
   - `collector_resource_configs` — notify on resource config scope changes
   - `collector_pipelines` — notify on pipeline archival/deletion
   - `collector_artifacts` — notify on artifact expiration
   - `collector_workers` — notify on worker state changes
   - `collector_task_caches` — notify on task cache changes
   - `collector_access_tokens` — notify on token expiration (time-based; may remain polling)
   - `collector_check_sessions` — notify on check session completion

## Technical Approach

### Pattern (established by Scheduler/BuildTracker)

1. At DB mutation points, call `conn.Bus().Notify(atc.ComponentName)` after transaction commit
2. In `command.go`, set `NotifyOnly: true` on the component registration (sets `Interval: 0`)
3. The `component.Runner` switches to `runNotifyOnly()` which blocks until a notification arrives
4. Component's `RunImmediately()` fires, performing its work

### Key Files

- **Component constants:** `atc/component.go` (all constants already defined)
- **Component wiring:** `atc/atccmd/command.go` lines 1175–1330 (backend) and 1421–1477 (GC)
- **Runner framework:** `atc/component/runner.go` (already supports NotifyOnly)
- **Notification bus:** `atc/db/notifications_bus.go`
- **NOTIFY trigger points:** `atc/db/build.go`, `atc/db/resource.go`, `atc/db/resource_type.go`, `atc/db/resource_config_scope.go`, `atc/db/container_repository.go`, `atc/db/volume_repository.go`, `atc/db/pipeline.go`

### Existing Infrastructure

- `NotifyResourceScanner()` already exists in `team_factory.go:169` — calls `Bus().Notify(atc.ComponentLidarScanner)`
- `build.Finish()` already notifies `buildEventsChannel` — just needs additional component notifications
- `build.Start()` already notifies `ComponentBuildTracker` — pattern reference
- All component channel constants already defined in `atc/component.go`

### Deprecation Strategy

The polling path for converted components is **deprecated and removed**, not preserved as a fallback:

1. **Remove polling interval config** — Converted components no longer accept interval configuration flags. The `Interval` field is set to 0 via `NotifyOnly: true`.
2. **Remove `runWithPolling` path** — Converted components exclusively use `runNotifyOnly()`. No hybrid mode.
3. **Clean up dead code** — Any polling-specific logic, timer setup, or interval-related config for converted components is removed.
4. **Document the change** — Component registration in `command.go` clearly reflects the new notification-only mode.

### Testing Strategy

Every phase must be thoroughly tested both during and after migration:

1. **Unit tests per component** — Each converted component gets tests verifying:
   - It wakes immediately when a NOTIFY is sent on its channel
   - It does NOT run on a timer/polling interval
   - It performs its work correctly when triggered by notification
   - All existing behavioral tests continue to pass

2. **NOTIFY trigger point tests** — Each DB mutation point that adds a `Bus().Notify()` call gets tests verifying:
   - The notification is sent after successful transaction commit
   - The notification is NOT sent on transaction rollback
   - The correct channel name is used

3. **Integration tests** — After each phase, run the full relevant test suite to catch regressions
4. **Full suite at end** — `go test ./atc/...` and `testflight/` integration tests pass after all phases complete

## Acceptance Criteria

- [ ] LidarScanner runs with `NotifyOnly: true` and reacts immediately to resource/type changes
- [ ] SyslogDrainer runs with `NotifyOnly: true` and drains logs immediately after build completion
- [ ] BuildReaper runs with `NotifyOnly: true` and evaluates log retention immediately after build completion
- [ ] GC collectors that benefit from notification-driven execution are converted
- [ ] Polling intervals and related config are removed for all converted components
- [ ] All existing tests pass; new tests cover notification trigger points
- [ ] No dual approach: converted components are notification-only, not hybrid

## Out of Scope

- **K8sWorkerRegistrar** — Heartbeat pattern; must run regularly regardless of state changes
- **K8sWorkerReaper** — K8s-native pod cleanup; driven by K8s state, not PG state
- **PipelinePauser** — Time-based policy (pause after N days inactive); polling is correct
- **SigningKeyLifecycler** — Time-based key rotation; no external event triggers
- **BeingWatchedBuildMarker** — Already hybrid (real-time notification for watches, periodic for cleanup); appropriate as-is
- NOTIFY payload optimization (e.g., passing specific IDs to avoid full scans) — can be a follow-up track
- K8s watch API migration for K8s-native components
