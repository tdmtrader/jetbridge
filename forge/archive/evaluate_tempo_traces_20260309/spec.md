# Spec: Close Build Lifecycle Tracing Gaps

**Track ID:** `evaluate_tempo_traces_20260309`
**Type:** feature

## Overview

Analysis of Tempo traces from recent k8s-integration test runs revealed that **5-11 seconds per build** is unaccounted for in existing spans. The gap lies between `fly trigger-job` and the engine's `build` span starting. This track adds server-side spans to the build scheduling pipeline and resource check flow so that every phase of a build's lifecycle — from API request through scheduling, input resolution, and execution — is visible as a connected trace.

### Evidence (from trace analysis)

- A simple `echo` task takes ~1.1s server-side (`build` span) but 6.7s from the test's perspective (`waitForBuildAndWatch`).
- A 3-task pipeline takes ~5.4s server-side but 16.5s+ test-side.
- The 5-11s gap is invisible — no spans cover it today.
- Pod startup (`k8s.exec-process.wait-for-running`) is well-traced at 0.6-1.9s/pod, but the scheduling/input-resolution phases before execution are not.

### What's already traced

| Component | Span | Status |
|-----------|------|--------|
| `db.build.create` (pending) | `job.CreateBuild()` | Traced |
| `scheduler.Run` / `schedule-job` | Scheduler loop | Traced |
| `job.EnsurePendingBuildExists` | Linked to input span | Traced |
| `build` (engine execution) | `engineBuild.Run()` | Traced |
| `task`, `k8s.container.run`, `wait-for-running`, `exec` | K8s runtime | Traced |

### What's NOT traced (the gaps)

| Gap | Component | File | Impact |
|-----|-----------|------|--------|
| **Build start API** | `CreateJobBuild` handler | `atc/api/jobserver/create_build.go` | No span for the trigger-job request itself |
| **Build starter** | `tryStartNextPendingBuild()` | `atc/scheduler/buildstarter.go` | The entire pending→started transition is dark |
| **Input resolution** | `BuildInputs(context.TODO())` | `atc/scheduler/buildstarter.go:177` | Uses `context.TODO()` — no tracing context |
| **Build tracker** | `Tracker.Run()` / `trackBuild()` | `atc/builds/tracker.go` | Discovery of started builds has no span |
| **Check creation** | `TryCreateCheck()` | `atc/db/check_factory.go` | Resource checks created with no tracing |
| **Check delegate** | `WaitToRun()` | `atc/engine/check_delegate.go` | Rate-limiting/lock waits invisible |

## Requirements

1. **API handler span** — Wrap `CreateJobBuild` in a span that captures team, pipeline, job, and the created build ID. This is the root span for the "manual trigger" flow.

2. **Build starter spans** — Add spans to `tryStartNextPendingBuild()` covering:
   - `scheduler.try-start-pending-build` — overall span for the attempt
   - `build.determine-inputs` — wrapping `BuildInputs()` (replacing `context.TODO()`)
   - `build.create-plan` — wrapping `planner.Create()`
   - `build.start` — wrapping the `Start(plan)` call that transitions pending→started

3. **Build tracker span** — Add a span in `trackBuild()` that links to the build's stored span context, covering the started→engine handoff.

4. **Check factory span** — Add a span to `TryCreateCheck()` covering check creation (both DB and in-memory paths).

5. **Check delegate spans** — Add spans to `WaitToRun()` covering lock acquisition and rate-limit waits.

6. **Propagate context** — Replace `context.TODO()` in `buildstarter.go:177` with proper tracing context from the scheduler span.

## Acceptance Criteria

- [ ] After deploying, a single manually-triggered build produces a connected trace from API request through scheduling, input resolution, build start, and engine execution.
- [ ] The previously invisible 5-11s gap is decomposed into named spans.
- [ ] Resource check lifecycle is visible as spans (creation, wait-to-run, execution).
- [ ] No existing spans are broken or duplicated.
- [ ] Integration tests pass with tracing enabled; new spans appear in Tempo.
- [ ] All new spans follow existing conventions: `tracing.StartSpan()`, kebab-case names, `tracing.Attrs{}` for metadata.

## Out of Scope

- Changing pod startup time or scheduler behavior (this track is observability only).
- Adding spans to the `fly` CLI client.
- Parallelizing test helpers (e.g., sequential `newMockVersion` calls).
- Metrics or dashboards — just trace spans.
- Modifying the web UI or Grafana configuration.
