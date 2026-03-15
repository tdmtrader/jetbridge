# Implementation Plan: Convert Polling Components to Notification-Driven

## Phase 1: LidarScanner — Notification-Only [checkpoint: 0ba5f06d4]

The LidarScanner polls every 10s to find resources/types needing checks. `NotifyResourceScanner()` already exists but the scanner still polls. Convert to notification-only and add NOTIFY calls at all resource/type state-change points.

- [x] Write tests for LidarScanner notification-only mode (verify scanner wakes on NOTIFY, verify it does NOT poll on a timer) 0ba5f06d4
- [x] Write tests for NOTIFY trigger points in `resource.go` — verify `Bus().Notify(atc.ComponentLidarScanner)` fires after `SetResourceConfigScope()`, `PinVersion()`, `UnpinVersion()`, `toggleVersion()` commit; verify no notify on rollback 0ba5f06d4
- [x] Write tests for NOTIFY trigger points in `resource_type.go` — verify notify fires after `SetResourceConfigScope()` commit 0ba5f06d4
- [x] Write tests for NOTIFY trigger points in `resource_config_scope.go` — verify notify fires after `SaveVersions()` and `UpdateLastCheckEndTime()` commit 0ba5f06d4
- [x] Implement: Set `NotifyOnly: true` on ComponentLidarScanner in `atc/atccmd/command.go`; remove `LidarScannerInterval` config usage for this component 0ba5f06d4
- [x] Implement: Add `Bus().Notify(atc.ComponentLidarScanner)` in `atc/db/resource.go` — after `SetResourceConfigScope()`, `PinVersion()`, `UnpinVersion()`, `toggleVersion()` 0ba5f06d4
- [x] Implement: Add `Bus().Notify(atc.ComponentLidarScanner)` in `atc/db/resource_type.go` — after `SetResourceConfigScope()` 0ba5f06d4
- [x] Implement: Add `Bus().Notify(atc.ComponentLidarScanner)` in `atc/db/resource_config_scope.go` — after `SaveVersions()` and `UpdateLastCheckEndTime()` 0ba5f06d4
- [x] Verify existing `NotifyResourceScanner()` calls in `configserver/save.go` and `pipelineserver/unpause.go` still work 0ba5f06d4
- [x] Run full scanner test suite (`go test ./atc/lidar/...`) and related DB tests (`go test ./atc/db/...`) — all green 0ba5f06d4
- [x] Phase 1 Manual Verification 0ba5f06d4

## Phase 2: SyslogDrainer & BuildReaper — Build Completion Notifications [checkpoint: a9bd46b9f]

Both components react to build completion. `build.Finish()` is the single trigger point. Add NOTIFY calls there and convert both components.

- [x] Write tests for SyslogDrainer notification-only mode (verify wakes on NOTIFY, verify no polling timer) a9bd46b9f
- [x] Write tests for BuildReaper notification-only mode (verify wakes on NOTIFY, verify no polling timer) a9bd46b9f
- [x] Write tests for NOTIFY trigger points in `build.go` — verify `Bus().Notify(atc.ComponentSyslogDrainer)` and `Bus().Notify(atc.ComponentBuildReaper)` fire after `Finish()` commits; verify no notify on rollback a9bd46b9f
- [x] Implement: Add `Bus().Notify(atc.ComponentSyslogDrainer)` and `Bus().Notify(atc.ComponentBuildReaper)` in `atc/db/build.go` after `Finish()` commits a9bd46b9f
- [x] Implement: Set `NotifyOnly: true` on ComponentSyslogDrainer in `atc/atccmd/command.go`; remove `Syslog.DrainInterval` config usage for this component a9bd46b9f
- [x] Implement: Set `NotifyOnly: true` on ComponentBuildReaper in `atc/atccmd/command.go`; remove hardcoded `30 * time.Second` interval a9bd46b9f
- [x] Run syslog drainer tests (`go test ./atc/syslog/...`), build reaper tests (`go test ./atc/gc/...`), and build DB tests (`go test ./atc/db/...`) — all green a9bd46b9f
- [x] Phase 2 Manual Verification a9bd46b9f

## Phase 3: GC Collectors — Build Lifecycle Collectors [checkpoint: fb46ec7c7]

Convert GC collectors triggered by build lifecycle events. These share the `build.Finish()` trigger point with Phase 2.

- [x] Write tests for collector_builds notification-only mode (verify wakes on NOTIFY, no polling) fb46ec7c7
- [x] Write tests for collector_resource_cache_uses notification-only mode fb46ec7c7
- [x] Write tests for collector_checks notification-only mode fb46ec7c7
- [x] Write tests for NOTIFY trigger points — verify `Bus().Notify()` fires for each collector at the correct DB mutation point fb46ec7c7
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorBuilds)` in `atc/db/build.go` after `Finish()` commits fb46ec7c7
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorResourceCacheUses)` in `atc/db/build.go` after `Finish()` commits fb46ec7c7
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorChecks)` in check completion path (where check builds finish) fb46ec7c7
- [x] Implement: Set `NotifyOnly: true` on these collectors in `atc/atccmd/command.go` `gcComponents()`; remove their polling intervals fb46ec7c7
- [x] Run full GC test suite (`go test ./atc/gc/...`) and DB tests — all green fb46ec7c7
- [x] Phase 3 Manual Verification fb46ec7c7

## Phase 4: GC Collectors — Resource & Pipeline Lifecycle Collectors [checkpoint: b05551187]

Convert GC collectors triggered by resource config, cache, and pipeline lifecycle events.

- [x] SKIP: collector_resource_configs stays polling — uses time-based grace period (unreferencedConfigGracePeriod) b05551187
- [x] Write tests for collector_resource_caches notification-only mode b05551187
- [x] Write tests for collector_pipelines notification-only mode b05551187
- [x] SKIP: collector_artifacts stays polling — uses time-based 12h expiry (RemoveExpiredArtifacts) b05551187
- [x] Write tests for collector_task_caches notification-only mode b05551187
- [x] Write tests for NOTIFY trigger points at each DB mutation — verify correct channel, commit-only firing b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorResourceConfigs)` in `atc/db/resource_config_scope.go` after scope creation/orphaning b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorResourceCaches)` in `atc/db/resource_cache.go` after cache invalidation b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorPipelines)` in `atc/db/pipeline.go` after archive/destroy b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorArtifacts)` in artifact lifecycle state changes b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorTaskCaches)` in task cache lifecycle state changes b05551187
- [x] Implement: Set `NotifyOnly: true` on these collectors in `atc/atccmd/command.go` `gcComponents()`; remove their polling intervals b05551187
- [x] Run full GC test suite and DB tests — all green b05551187
- [x] Phase 4 Manual Verification b05551187

## Phase 5: GC Collectors — Container, Volume & Worker Lifecycle Collectors [checkpoint: b05551187]

Convert the remaining GC collectors tied to container/volume/worker state transitions.

- [x] SKIP: collector_containers stays polling — uses MissingGracePeriod and HijackGracePeriod (time-based) b05551187
- [x] SKIP: collector_volumes stays polling — uses MissingGracePeriod (time-based) b05551187
- [x] SKIP: collector_workers stays polling — uses worker expiry (expires < NOW()) b05551187
- [x] SKIP: collector_check_sessions stays polling — uses expires_at < NOW() (time-based) b05551187
- [x] SKIP: collector_access_tokens stays polling — uses leeway-based token expiry (time-based) b05551187
- [x] Write tests for NOTIFY trigger points at each DB mutation — verify correct channel, commit-only firing b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorContainers)` in container state transition points (creating→failed, mark-for-destruction) b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorVolumes)` in volume state transition points b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorWorkers)` in worker state transition points (stall, land, retire) b05551187
- [x] Implement: Add `Bus().Notify(atc.ComponentCollectorCheckSessions)` in check session completion b05551187
- [x] Implement: Set `NotifyOnly: true` on converted collectors in `atc/atccmd/command.go` `gcComponents()`; remove their polling intervals b05551187
- [x] Run full GC test suite and DB tests — all green b05551187
- [x] Phase 5 Manual Verification b05551187

## Phase 6: Deprecation Cleanup & Final Verification [checkpoint: 1feb34e13]

Remove all dead polling infrastructure for converted components and run the full test suite.

- [x] Remove unused polling interval config flags/fields for all converted components (e.g., `LidarScannerInterval`, `Syslog.DrainInterval`, `GC.Interval` for converted collectors) 1feb34e13
- [x] Remove any dead polling-specific code paths in converted components 1feb34e13
- [x] Audit `command.go` — verify every component is either `NotifyOnly: true` or intentionally polling (K8s, PipelinePauser, SigningKey, BeingWatchedBuildMarker) 1feb34e13
- [x] Run full `go test ./atc/...` — all green 1feb34e13
- [x] SKIP: Integration tests (testflight) require running Concourse cluster — verified unit/DB/API/GC tests all pass 1feb34e13
- [x] Run `go vet ./...` — clean 1feb34e13
- [x] Phase 6 Manual Verification 1feb34e13

---
