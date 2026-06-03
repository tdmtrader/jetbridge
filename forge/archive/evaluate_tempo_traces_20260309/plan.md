# Implementation Plan: Close Build Lifecycle Tracing Gaps

## Phase 1: Build Starter Spans (highest-value gap)

This closes the biggest unknown: the pending→started transition in `buildstarter.go`.

- [x] Task: Add span to `tryStartNextPendingBuild()` in `atc/scheduler/buildstarter.go` dcfb8455b
  - Wrap the function body in `tracing.StartSpan(ctx, "scheduler.try-start-pending-build", tracing.Attrs{...})`
  - Add child spans for `build.schedule`, `build.determine-inputs`, `build.create-plan`, `build.start`
  - Replace `context.TODO()` at line 177 with the span context
  - Pass context through from `TryStartPendingBuildsForJob()` (which currently doesn't accept ctx)

- [x] Task: Add context parameter to `TryStartPendingBuildsForJob` and `tryStartNextPendingBuild` dcfb8455b
  - Update the `BuildStarter` interface to accept `context.Context`
  - Thread context from `scheduler.Schedule()` (which already has span context) through to build starter
  - Update all callers

- [x] Task: Phase 1 verification dcfb8455b — run k8s-integration tests with tracing enabled, query Tempo for `scheduler.try-start-pending-build` spans, verify they appear and show meaningful durations

---

## Phase 2: API Handler & Build Tracker Spans

- [x] Task: Add span to `CreateJobBuild` handler in `atc/api/jobserver/create_build.go` 4583119b9
  - Wrap handler body in `tracing.StartSpan(r.Context(), "api.create-job-build", tracing.Attrs{team, pipeline, job})`
  - Capture build ID in span attributes after creation
  - Also wrap the `TryCreateCheck` calls that happen after build creation

- [x] Task: Add span to `Tracker.trackBuild()` in `atc/builds/tracker.go` 4583119b9
  - Add `tracing.StartSpanFollowing(ctx, build, "build-tracker.track", build.TracingAttrs())` to link to build's stored span context
  - This bridges the gap between scheduler starting the build and engine picking it up

- [x] Task: Phase 2 verification 4583119b9 — trigger a build via `fly trigger-job`, verify connected trace in Tempo from API handler through tracker to engine `build` span

---

## Phase 3: Check Factory & Check Delegate Spans

- [x] Task: Add span to `TryCreateCheck()` in `atc/db/check_factory.go` 1d1c46e2f
  - Wrap in `tracing.StartSpan(ctx, "check-factory.try-create", tracing.Attrs{resource_name, resource_type})`
  - Add child spans for the DB-check vs in-memory-check paths
  - Ensure context propagates to `checkable.CreateBuild()` and `checkable.CreateInMemoryBuild()`

- [x] Task: Add spans to `checkDelegate.WaitToRun()` in `atc/engine/check_delegate.go` 1d1c46e2f
  - Add span for lock acquisition wait: `"check.wait-for-lock"`
  - Add span for rate-limit wait: `"check.wait-for-rate-limit"`
  - These reveal whether checks are blocked waiting for locks or throttled

- [x] Task: Phase 3 verification 1d1c46e2f — run a pipeline with resource checks, verify check lifecycle spans in Tempo (creation → wait-to-run → execution)

---

## Phase 4: End-to-End Validation

- [x] Task: Run full k8s-integration suite with tracing enabled, query Tempo for the new spans, produce a before/after comparison showing the previously invisible 5-11s gap is now decomposed 1d1c46e2f
- [x] Task: Phase 4 Manual Verification 1d1c46e2f

---
