# CGX — Discovery Notes

## Key Architectural Insights

### Collectors Are Uniform in Shape, Diverse in Complexity
All 15 collectors implement the same `Run(ctx) error` interface and are driven by the same component runner framework. But complexity varies dramatically:
- **Simple** (1 DB call): artifacts, checks, pipelines, tokens, build, task cache
- **Medium** (2-3 calls with error accumulation): resource cache use, check sessions, resource cache, resource config, worker
- **Complex** (multi-stage with state transitions): container, volume, build log

### Error Accumulation vs Short-Circuit
Two patterns exist:
- **Simple collectors** return the first error immediately
- **Complex collectors** (container, volume, cache use, check sessions) use `multierror.Append` to accumulate errors and continue processing — critical for reliability since a failure in one stage shouldn't block cleanup of unrelated resources

### Grace Periods Are the GC's Safety Net
Three configurable grace periods prevent premature cleanup:
- `missingContainerGracePeriod` / `missingVolumeGracePeriod` (default 5m) — wait before removing resources that disappeared from workers
- `hijackContainerGracePeriod` (default 5m) — preserve containers being debugged via `fly hijack`
- `resourceConfigGracePeriod` (check timeout + 5m) — prevent deleting configs while checks are in progress

### Build Log Collector Is by Far the Most Complex
At 225 LOC prod and 843 LOC tests, the build log collector is the most sophisticated collector. It implements a policy engine (retention calculator) with interacting constraints: count, days, min-success, drain, running-build exclusion. The test suite is excellent (41 tests) and covers the full policy matrix.

### The Destroyer Is Not a Collector
The `destroyer.go` component is a database operations wrapper used by the K8s reaper, not a standalone collector. It provides `DestroyContainers`, `DestroyVolumes`, and `FindDestroyingVolumesForGc` for the external garbage collection loop that runs in the K8s runtime.

## Coverage Observations

- **98% Full** — remarkably high baseline. Only 1 gap: missing task_cache_collector test
- **Build log retention** has the best test suite in the GC subsystem (41 tests covering every policy combination)
- **Container collector** tests are thorough (15 tests) with good error continuation coverage
- **Simple collectors** each have exactly 1 test verifying the primary lifecycle call — this is appropriate for their trivial complexity
- **Destroyer** has excellent coverage (14 tests) including error paths and input validation

## Decisions

- Requirement IDs use two-letter prefixes: CC (Container Collection), VC (Volume Collection), BL (Build Log), RC (Retention Calculator), CF (Resource Config), CA (Resource Cache), CU (Cache Use), CS (Check Sessions), SC (Simple Collectors), DS (Destroyer)
- SC-01 (BuildCollector) is marked Full even without a dedicated test file because it delegates to a single DB call that is integration-tested in atc/db
- The task_cache_collector gap (SC-08) follows the exact same simple-collector pattern as artifacts, checks, and pipelines — one test verifying the lifecycle call
