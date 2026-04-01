# CGX — Discovery Notes

## Key Architectural Insights

### ghttp Mock Server Is the Integration Test Foundation
All 67 fly integration test files use the same pattern: a ghttp mock HTTP server that verifies fly makes the correct HTTP requests with correct parameters. Tests never hit a real ATC. Authentication is pre-configured in BeforeEach via a login command against the mock server.

### Five Untested Commands Are All Simple List/Filter/Action Patterns
The 5 missing tests follow the same simple patterns as existing tested commands:
- `rerun-build` mirrors `trigger-job` (POST to create build, optionally watch)
- `schedule-job` mirrors `unpause-job` (POST/PUT action, success message)
- `paused-pipelines` mirrors `pipelines` (GET list, filter paused, render table)
- `active-users` mirrors `teams` (GET list, render table)
- `paused-jobs` mirrors `jobs` (GET list, filter paused, render table)

### Command Complexity Varies Dramatically
- **Simple** (1 API call): pause-pipeline, abort-build, version (30-80 LOC each)
- **Medium** (2-3 API calls with logic): trigger-job, check-resource, pin-resource (80-200 LOC)
- **Complex** (multi-step with I/O): set-pipeline (1438 LOC test), execute (2800+ LOC test), hijack (1164 LOC test), login (1200+ LOC test)

### The `atc.Routes` System Maps Commands to API Endpoints
Commands use `atc.Routes.CreatePathForRoute()` with `rata.Params` to construct API paths. Tests verify the exact route name + params, making them resilient to URL changes.

## Coverage Observations

- **90% Full** — strong baseline for 63 commands
- **Resource management (Section 3) and infrastructure (Section 5) are at 100%** — complete coverage
- **Only 5 gaps** and all are straightforward to fill
- **Test quality is excellent** — set_pipeline_test.go alone has 28 tests covering variable interpolation, instance vars, dry-run, credential checking, and team scoping

## Decisions

- Requirement IDs use two-letter prefixes: PM (Pipeline Management), BE (Build Execution), RM (Resource Management), TA (Team Auth), IC (Infrastructure Commands), UC (Utility Commands), JM (Job Management)
- Scoped to command-level behavioral requirements, not detailed assertion counts per command
- The `completion` command is excluded from requirements — it generates shell completion scripts and is inherently environment-specific
