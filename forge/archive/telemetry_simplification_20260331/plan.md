# Plan: Telemetry Simplification

## Phase 1: Remove `db.lock.acquire` Span

The single biggest win — 77.7% of all traces.

### Tasks

- [x] Remove span from `atc/db/lock/lock.go`
  - Delete `tracing.StartSpan` call for `db.lock.acquire`
  - Remove unused tracing imports
  - Update any tests that assert on this span

## Phase 2: Remove Periodic Polling Root Spans

These fire every cycle and are noise when no work happens.

### Tasks

- [x] Remove `scheduler.Run` span from `atc/scheduler/runner.go`
  - Keep `schedule-job` span (that one is per-job, nested, valuable)

- [x] Remove `scanner.Run` span from `atc/lidar/scanner.go`
  - Keep `scanner.check` removal for Phase 3

- [x] Remove `k8s.reaper.run` span from `atc/worker/jetbridge/reaper.go`

- [x] Remove `k8s.registrar.register` span from `atc/worker/jetbridge/registrar.go`

## Phase 3: Remove Check Bookkeeping Spans

Child spans inside scanner/check traces that are always <50ms.

### Tasks

- [x] Remove scanner resolution spans from `atc/lidar/scanner.go`
  - Delete: `scanner.check`, `scanner.resolveResourceType`, `scanner.resolveResource`

- [x] Remove check delegate wait spans from `atc/engine/check_delegate.go`
  - Delete: `check.wait-to-run`, `check.wait-for-rate-limit`, `check.wait-for-lock`

- [x] Remove `check-factory.try-create` span from `atc/db/check_factory.go`

- [x] Remove `db.versions.save` span from `atc/db/resource_config_scope.go`

## Phase 4: Remove otelhttp Noise

### Tasks

- [x] Remove or filter otelhttp auto-instrumentation from `atc/wrappa/otel_http_wrappa.go`
  - Evaluate: remove entirely vs filter specific routes
  - If removing entirely, delete the wrappa and its registration

## Phase 5: Verify & Clean Up

### Tasks

- [x] Run scheduler tests (`ginkgo ./atc/scheduler/...`)
- [x] Run engine/exec tests (`ginkgo ./atc/engine/...`)
- [x] Run jetbridge tests (`ginkgo ./atc/worker/jetbridge/`)
- [x] Run DB tests for lock and check_factory (`ginkgo ./atc/db/lock/ ./atc/db/`)
- [x] Remove unused tracing imports from all modified files
- [x] Verify in Grafana: trace volume dropped, build traces still have full child span trees
