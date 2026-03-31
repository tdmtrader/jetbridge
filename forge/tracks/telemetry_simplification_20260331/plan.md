# Plan: Telemetry Simplification

## Phase 1: Remove Scheduler & Algorithm Spans

### Tasks

- [ ] Remove algorithm resolver spans from `atc/scheduler/algorithm/`
  - Delete spans: `Algorithm.Compute`, `individualResolver.Resolve`, `pinnedResolver.Resolve`, all `groupResolver.*` spans
  - Files: `compute.go`, `individual_resolver.go`, `pinned_resolver.go`, `group_resolver.go`
  - Update any tests that assert on these spans

- [ ] Remove scheduler sub-phase spans from `atc/scheduler/buildstarter.go`
  - Delete spans: `scheduler.try-start-pending-build`, `build.schedule`, `build.determine-inputs`, `build.create-plan`, `build.start`
  - Update any tests that assert on these spans

- [ ] Remove periodic scheduler span from `atc/scheduler/runner.go`
  - Delete span: `scheduler.Run` (keep `schedule-job`)

## Phase 2: Remove Check/Lidar Internal Spans

### Tasks

- [ ] Remove check delegate wait spans from `atc/engine/check_delegate.go`
  - Delete spans: `check.wait-to-run`, `check.wait-for-rate-limit`, `check.wait-for-lock`
  - Update any tests that assert on these spans

- [ ] Remove scanner resolution spans from `atc/lidar/scanner.go`
  - Delete spans: `scanner.resolveResourceType`, `scanner.resolveResource`, `scanner.Run`
  - Keep: `scanner.check`

## Phase 3: Remove K8s Worker Internal Spans

### Tasks

- [ ] Remove K8s container/volume/process spans
  - Delete from `container.go`: `k8s.container.attach`
  - Delete from `volume.go`: `k8s.volume.stream-in`, `k8s.volume.stream-out`
  - Delete from `process.go`: `k8s.process.wait`, `k8s.exec-process.wait`, `k8s.exec-process.wait-for-running`, `k8s.exec-process.stream-inputs`, `k8s.exec-process.exec`
  - Keep: `k8s.container.run`, `k8s.spdy.exec`

- [ ] Remove K8s periodic spans
  - Delete from `reaper.go`: `k8s.reaper.run`
  - Delete from `registrar.go`: `k8s.registrar.register`

## Phase 4: Remove DB Internal Spans

### Tasks

- [ ] Remove DB internal spans
  - Delete from `versions_db.go`: `VersionsDB.migrateSingle`, `PaginatedBuilds.migrateLimit`
  - Delete from `lock/lock.go`: `db.lock.acquire`
  - Keep: `db.build.create`, `db.versions.save`, `check-factory.try-create`

## Phase 5: OTel Collector Tail-Sampling Config

### Tasks

- [ ] Create/document OTel Collector tail-sampling configuration
  - Tail-sampling processor: 1% default, always keep > 1s duration
  - Pass-through for non-HTTP spans
  - Provide as a reference config file (e.g., `hack/otel-collector-sampling.yaml`)

## Phase 6: Verify & Clean Up

### Tasks

- [ ] Run full jetbridge test suite
- [ ] Run scheduler tests
- [ ] Run engine/exec tests
- [ ] Remove unused tracing imports from modified files
- [ ] Document span reduction in a summary
