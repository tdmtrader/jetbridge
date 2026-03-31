# Spec: Telemetry Simplification — Reduce Span Volume

## Overview

Concourse emits 50+ distinct span types across 10 subsystems, but ~90% of the span volume is low-value internal instrumentation (scheduler algorithm internals, DB lock acquisition, K8s process wait states, periodic polling cycles). This drives excessive cost in GCP Cloud Trace and clutters self-hosted Tempo, making it harder to find the signal (build/step execution) in the noise.

## Goals

1. **Reduce span volume by ~80-90%** by removing low-value spans from code
2. **Keep build/step execution spans** — the high-value "what ran and how long" view
3. **Add tail-based sampling for HTTP spans** — sample 1% normally, always keep spans > 1s duration
4. **Reduce GCP Cloud Trace cost** and improve clarity in Grafana/Tempo

## Requirements

### R1: Remove low-value spans from code

Delete `tracing.StartSpan` calls for these categories:

**Scheduler algorithm internals** (delete all):
- `Algorithm.Compute` — `atc/scheduler/algorithm/compute.go`
- `individualResolver.Resolve` — `atc/scheduler/algorithm/individual_resolver.go`
- `pinnedResolver.Resolve` — `atc/scheduler/algorithm/pinned_resolver.go`
- `groupResolver.Resolve` — `atc/scheduler/algorithm/group_resolver.go`
- `groupResolver.tryResolve` — `atc/scheduler/algorithm/group_resolver.go`
- `groupResolver.trySatisfyPassedConstraintsForInput` — `atc/scheduler/algorithm/group_resolver.go`
- `groupResolver.tryJobBuilds` — `atc/scheduler/algorithm/group_resolver.go`
- `groupResolver.tryBuildOutputs` — `atc/scheduler/algorithm/group_resolver.go`

**Scheduler sub-phases** (delete all):
- `scheduler.try-start-pending-build` — `atc/scheduler/buildstarter.go`
- `build.schedule` — `atc/scheduler/buildstarter.go`
- `build.determine-inputs` — `atc/scheduler/buildstarter.go`
- `build.create-plan` — `atc/scheduler/buildstarter.go`
- `build.start` — `atc/scheduler/buildstarter.go`

**Check/Lidar internals** (delete all):
- `check.wait-to-run` — `atc/engine/check_delegate.go`
- `check.wait-for-rate-limit` — `atc/engine/check_delegate.go`
- `check.wait-for-lock` — `atc/engine/check_delegate.go`
- `scanner.resolveResourceType` — `atc/lidar/scanner.go`
- `scanner.resolveResource` — `atc/lidar/scanner.go`

**K8s worker internals** (delete all):
- `k8s.container.attach` — `atc/worker/jetbridge/container.go`
- `k8s.volume.stream-in` — `atc/worker/jetbridge/volume.go`
- `k8s.volume.stream-out` — `atc/worker/jetbridge/volume.go`
- `k8s.process.wait` — `atc/worker/jetbridge/process.go`
- `k8s.exec-process.wait` — `atc/worker/jetbridge/process.go`
- `k8s.exec-process.wait-for-running` — `atc/worker/jetbridge/process.go`
- `k8s.exec-process.stream-inputs` — `atc/worker/jetbridge/process.go`
- `k8s.exec-process.exec` — `atc/worker/jetbridge/process.go`

**DB internals** (delete all):
- `VersionsDB.migrateSingle` — `atc/db/versions_db.go`
- `PaginatedBuilds.migrateLimit` — `atc/db/versions_db.go`
- `db.lock.acquire` — `atc/db/lock/lock.go`

**Periodic/polling spans** (delete all):
- `scheduler.Run` — `atc/scheduler/runner.go`
- `scanner.Run` — `atc/lidar/scanner.go`
- `k8s.reaper.run` — `atc/worker/jetbridge/reaper.go`
- `k8s.registrar.register` — `atc/worker/jetbridge/registrar.go`

### R2: Keep high-value spans

These spans MUST remain:
- `build` (engine.go)
- `build-tracker.track` (tracker.go)
- `task`, `get`, `put`, `check`, `set_pipeline`, `load_var` (exec steps)
- `hook.on_success`, `hook.on_failure`, `hook.on_error`, `hook.ensure` (step hooks)
- `schedule-job` (scheduler/runner.go — keep this one, it's per-job-per-cycle, useful)
- `job.EnsurePendingBuildExists` (scheduler.go)
- `scanner.check` (lidar/scanner.go — check creation, not internal resolution)
- `check-factory.try-create` (db/check_factory.go)
- `k8s.container.run` (jetbridge/container.go — pod lifecycle)
- `k8s.spdy.exec` (jetbridge/executor.go — hijack debugging)
- `creds.lookup` (creds/cached_secrets.go)
- `db.build.create` (db/job.go)
- `db.versions.save` (db/resource_config_scope.go)
- `api.create-job-build` (api handler)
- HTTP auto-instrumentation (otelhttp — handled by sampling, not removal)
- CI-Agent spans (`gen_ai.invoke`, `phase.run`)

### R3: Tail-based sampling for HTTP spans

Configure an OTel Collector pipeline (or provide a reference config) that:
- Samples 1% of HTTP spans by default
- Always keeps spans with duration > 1 second
- Passes through all non-HTTP spans unmodified

## Acceptance Criteria

- [ ] All spans listed in R1 are removed from code
- [ ] All spans listed in R2 still function correctly
- [ ] Tests pass after span removal (update test assertions as needed)
- [ ] OTel Collector tail-sampling config is documented/provided
- [ ] Estimated span volume reduction documented

## Out of Scope

- Metrics reduction (only spans)
- Changes to CI-Agent tracing
- Changes to trace context propagation
- New span creation
