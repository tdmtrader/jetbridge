# CGX — Discovery Notes

## Key Architectural Insights

### Notification Overflow Is the Scheduler's Defining Constraint
The comment in `runner.go:100-107` is the single most important design decision: notifications are non-blocking sends on capacity-1 channels, so they WILL be dropped. This is why `jobsToSchedule()` always does a full scan via `JobFactory.JobsToSchedule()` rather than targeting specific job IDs from the NOTIFY payload. Any spec or test that assumes targeted scheduling would be wrong.

### Three Build Types, Three Input Strategies
The scheduler has three fundamentally different input resolution paths:
- **schedulerBuild**: Uses pre-computed inputs from `SaveNextInputMapping` — just adopts them
- **manualTriggerBuild**: Must recompute algorithm because the user expects latest versions, not whatever was cached
- **rerunBuild**: Uses the original build's inputs — completely separate adoption path (`AdoptRerunInputsAndPipes`)

This three-way split explains many of the branching decisions in `buildstarter.go` and `build.go`.

### The Group Resolver Is the Algorithm's Heart
The `group_resolver.go` (508 lines) is by far the most complex piece. It implements recursive backtracking with:
- Vouch-based candidate tracking (each passed job "vouches for" a version)
- Mismatch detection and candidate restoration
- Doom detection to prevent infinite recursion
- Every-version incremental resolution via build pipes

The doom detection (PC-08) is subtle: if recursive resolution fails and the candidates are identical to the previously doomed set, skip recursion entirely. This was likely added to fix a real infinite loop bug.

### Metrics Are Code-Complete but Test-Light
All 6 metric requirements (MO-01 through MO-06) have correct production code but zero dedicated test assertions. The existing tests use fakes that don't capture metric side effects. Testing metrics requires either:
1. Asserting on the global `metric.Metrics` struct fields (counters are `prometheus.Gauge`/`prometheus.Counter`)
2. Using a test metric registry

This is the weakest area (0% Full) and the primary gap-filling target.

### Tracing Is Extensive but Largely Untested
The scheduler creates ~10 distinct spans across the scheduling pipeline. Only one test (scheduler_test.go:413-468) verifies tracing behavior — and it tests the linked span for trigger detection, not the structural spans. This is a common pattern in the codebase (tracing added for production observability but not regression-tested).

## Coverage Observations

- **Sections 1, 2, 4, 5 are at 100%** — the core scheduling logic has excellent test coverage
- **Section 3 (Passed Constraints) is at 78%** — the two partial items (PC-02, PC-08) are edge cases unlikely to regress but worth pinning
- **Section 6 (Metrics) is at 0% Full** — all 6 items are Partial because the code is correct but assertions are missing
- **Overall 84% Full** — strong baseline, gap-filling is targeted at metrics/observability

## Decisions

- Chose ATC Scheduler over Checker/Build Tracker because it has the highest user-impact ("when does my build start?") with tractable scope (~1500 LOC prod)
- Requirement IDs use two-letter prefixes: SL (Scheduling Lifecycle), IR (Input Resolution), PC (Passed Constraints), TD (Trigger Detection), BS (Build Startup), MO (Metrics/Observability)
- Algorithm tests (5,858 lines) already use real PostgreSQL — no need for additional integration tests; unit-level gap-filling is sufficient
