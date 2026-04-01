# CGX — Discovery Notes

## Key Architectural Insights

### Visitor Pattern Is the Planner's Core Abstraction
The planner uses Go's visitor pattern (interface method dispatch) to walk the step configuration tree and build a Plan tree. Each step type implements `Visit(StepVisitor)` which dispatches to the corresponding `Visit*` method. This is compile-time safe — adding a new step type forces updating both interfaces. The 33 parameterized test cases in planner_test.go cover all 14 step types plus control flow.

### Two Build Paths: DB and In-Memory
Like the check runner, the build tracker handles two paths:
- **DB builds**: loaded from `GetAllStartedBuilds()` on tracker startup
- **In-memory builds**: received via `checkBuildsChan` from the scanner

The tracker uses the same sync.Map pattern as check_factory for deduplication, but with different keys: `"build-{ID}"` for DB builds, `"resource-{ResourceID}"` for in-memory checks (which start with ID=0).

### Engine Is the Build State Machine
The engine's `Run()` is a carefully sequenced state machine:
1. Lock → 2. Validate → 3. Create stepper → 4. Resolve vars → 5. Monitor abort → 6. Execute → 7. Finish

Each phase has specific error handling. The `select` on release vs done is the drain mechanism — if the release channel closes, the build returns without finishing, preserving it for restart on another ATC.

### Retriable Errors Are Build-Type Dependent
The engine's retriable error handling has an important nuance: normal builds retry on retriable errors (e.g., worker disappeared), but check builds NEVER retry. This prevents infinite check retry loops when a check consistently fails with a worker error.

### Image Fetching Has Three Resolution Paths
1. **Metadata-only** (preferred for registry-image): DB lookup or on-demand resolver, no pods
2. **Plan-based** (fallback): Run check+get plans in a child scope, spawns containers
3. **Hybrid**: Try metadata-only first, fall back to plan-based on failure

The task_delegate_test.go has excellent integration tests (667-1032) covering all three paths including cache lifecycle transitions.

## Coverage Observations

- **Engine execution (Section 3) is at 100%** — all 12 lifecycle states thoroughly tested
- **Delegate events (Section 5) and image fetch (Section 6) are at 100%** — excellent coverage
- **Plan generation (Section 2) at 93%** — only missing UnknownPrototypeError test
- **Tracker metrics (BT-05) are the biggest gap** — same pattern seen in scheduler and check runner
- **Overall 94% Full** — very strong baseline; only 4 items need filling

## Decisions

- Chose Build Tracker after Check Runner because it completes the build lifecycle chain: scheduler (when builds start) → check runner (version discovery) → build tracker (execution and plan generation)
- Requirement IDs use two-letter prefixes: BT (Build Tracking), PG (Plan Generation), EX (Engine Execution), SF (Step Factory), DE (Delegate Events), IF (Image Fetching), PE (Planner Errors)
- Scope explicitly excludes check delegation (already covered in check_runner spec) to avoid duplication
- The planner tests use testify suites (not Ginkgo) — gap-filling tests will follow that pattern
