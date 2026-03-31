# Spec: Telemetry Simplification — Reduce Span Volume

## Overview

Concourse emits 50+ distinct span types, but ~85% of the trace volume in Tempo is noise:
- `db.lock.acquire` alone is **77.7%** of all root traces (standalone, <1ms, no parent build)
- otelhttp auto-instrumented polling endpoints (`GetInfo`, `GetWall`, `ListAllPipelines`) add another **7.4%**
- Check bookkeeping spans (`scanner.check`, `check-factory.try-create`, `scanner.resolve*`, `db.versions.save`) are always <50ms and add no diagnostic value

Meanwhile, the spans that matter — build/step execution and K8s pod lifecycle nested under builds — are <15% of volume but contain all the diagnostic signal (pod scheduling latency, exec duration, step timing).

### Data from Tempo (48h sample, 1000 traces)

| Category | % of Volume | Avg Duration | Action |
|----------|-------------|-------------|--------|
| `db.lock.acquire` | 77.7% | <1ms | Remove |
| otelhttp polling (`GetInfo`, `GetWall`, `ListAllPipelines`) | 7.4% | 0ms | Remove |
| Periodic roots (`scheduler.Run`, `k8s.reaper.run`, `k8s.registrar.register`) | 9.4% | <10ms | Remove |
| Check bookkeeping (`scanner.check`, `check-factory.try-create`, `scanner.resolve*`, `db.versions.save`, `check.wait-*`) | ~2% (child spans) | <1ms | Remove |
| Build/step/K8s execution (`build`, `check`, `get`, `k8s.exec-process.*`) | ~4% | 100ms-600s | **Keep** |

## Goals

1. **Reduce trace volume by ~85%** by removing noise spans from code
2. **Keep all build/step execution spans** and their K8s pod lifecycle children
3. **Reduce cost** in GCP Cloud Trace and storage in self-hosted Tempo
4. **Increase clarity** — when you open Grafana, you see builds, not lock acquisitions

## Requirements

### R1: Remove standalone noise spans (delete from code)

**`db.lock.acquire`** — `atc/db/lock/lock.go`
- 77.7% of all traces. Always standalone, always <1ms. Delete the span.

**Periodic polling root spans** (fire every cycle regardless of work):
- `scheduler.Run` — `atc/scheduler/runner.go`
- `scanner.Run` — `atc/lidar/scanner.go`
- `k8s.reaper.run` — `atc/worker/jetbridge/reaper.go`
- `k8s.registrar.register` — `atc/worker/jetbridge/registrar.go`

### R2: Remove check bookkeeping spans (delete from code)

These are child spans inside scanner/check traces, always <50ms:
- `scanner.check` — `atc/lidar/scanner.go`
- `check-factory.try-create` — `atc/db/check_factory.go`
- `scanner.resolveResourceType` — `atc/lidar/scanner.go`
- `scanner.resolveResource` — `atc/lidar/scanner.go`
- `check.wait-to-run` — `atc/engine/check_delegate.go`
- `check.wait-for-rate-limit` — `atc/engine/check_delegate.go`
- `check.wait-for-lock` — `atc/engine/check_delegate.go`
- `db.versions.save` — `atc/db/resource_config_scope.go`

### R3: Remove or filter otelhttp noise

The `otelhttp` wrappa auto-instruments every HTTP request, creating spans for UI polling endpoints that are always 0ms. Options:
- Remove the otelhttp wrappa entirely (simplest)
- Or filter specific routes (more surgical)

### R4: Keep all valuable spans (DO NOT touch)

**Build execution tree:**
- `build` (engine.go) — root build span
- `build-tracker.track` (tracker.go) — build lifecycle
- `task`, `get`, `put`, `check`, `set_pipeline`, `load_var` (exec steps)
- `hook.on_success`, `hook.on_failure`, `hook.on_error`, `hook.ensure` (step hooks)

**K8s pod lifecycle (nested under builds):**
- `k8s.container.run` — pod creation timing
- `k8s.container.attach` — container attach
- `k8s.exec-process.wait` — total wait time
- `k8s.exec-process.wait-for-running` — pod scheduling latency (avg 808ms!)
- `k8s.exec-process.stream-inputs` — input streaming
- `k8s.exec-process.exec` — command execution
- `k8s.spdy.exec` — SPDY exec (avg 1107ms)
- `k8s.volume.stream-in`, `k8s.volume.stream-out` — artifact streaming

**Scheduler (nested under builds):**
- `schedule-job` — per-job scheduling
- `job.EnsurePendingBuildExists` — build creation
- `scheduler.try-start-pending-build` — build start attempt
- `build.schedule`, `build.determine-inputs`, `build.create-plan`, `build.start` — scheduler sub-phases
- `Algorithm.Compute`, all `*Resolver.*` spans — input resolution

**Other valuable spans:**
- `db.build.create` — build creation in DB
- `creds.lookup` — secret resolution
- `api.create-job-build` — API entry point
- CI-Agent spans (`gen_ai.invoke`, `phase.run`)

## Acceptance Criteria

- [ ] All spans listed in R1-R3 are removed from code
- [ ] All spans listed in R4 still function correctly
- [ ] Tests pass after span removal (update test assertions as needed)
- [ ] Tempo trace volume drops by ~85% (verify with Grafana query)

## Out of Scope

- Metrics reduction (only spans)
- Changes to CI-Agent tracing
- Tail-based sampling at collector level (can be a follow-up if volume is still too high)
- New span creation
