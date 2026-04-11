# CGX — Discovery Notes

## Key Architectural Insights

### Two Parallel Check Paths: In-Memory vs DB
The check subsystem has two fundamentally different creation paths:
- **DB builds** (`toDB=true`): Used by API manual checks and webhooks. Persisted immediately, deduplication via `CreateBuild` (DB-level).
- **In-memory builds** (`toDB=false`): Used by scanner for periodic checks. Deduplication via `sync.Map`, sent to tracker via channel. Lazy DB initialization in `OnCheckBuildStart()`.

This split exists because the scanner creates thousands of checks per cycle — writing each to DB would be expensive. The webhook and API paths need DB builds because the response includes the build JSON.

### Native Registry-Image Resolution Is the Happy Path
The scanner has a dedicated code path (`resolveResource`, `resolveResourceType`) that bypasses check containers entirely for `registry-image` resources/types. It calls the registry API directly, saves the digest as a version, and updates LastCheckEndTime. This is the preferred path — check pods are only needed for custom resource types.

### Rate Limiting Is Two-Tiered
1. **Dynamic rate limiter** (`ResourceCheckRateLimiter`): Throttles check creation based on active resource count. Refreshes periodically from DB.
2. **Checking lock** (`WaitToRun`): Prevents concurrent execution of the same scope across multiple ATCs. Only one ATC runs the actual check; others reuse the result.

The delegate enforces rate limiting BEFORE lock acquisition — this prevents lock contention from overwhelming the system when many checks fire simultaneously.

### Webhook Token Validation Uses Credential Manager
Webhook tokens are stored as credential references (e.g., `((webhook-token))`) and evaluated through the pipeline's variable sources. This means webhook tokens can be stored in external secret managers. The validation path is: evaluate token → compare with query param → 401 if mismatch.

### Resource Types Skip Lock But Not Interval
A subtle difference: resource type checks (`DL-09`) don't acquire a distributed lock (they return a no-op lock), but they DO enforce interval checking (`DL-10`). This is because resource type checks are lightweight and don't benefit from deduplication across ATCs — each ATC independently checks its types.

## Coverage Observations

- **Sections 1-6, 9 are at 100%** — core check lifecycle is thoroughly tested
- **Section 7 (Webhooks) at 83%** — missing empty-token 400 test
- **Section 8 (API Manual Checks) at 67%** — missing malformed body and explicit deep-check tests
- **Section 10 (Metrics) at 20%** — same pattern as scheduler: metric code exists but no test assertions
- **Overall 90% Full** — strong baseline; gap-filling targets metrics and API edge cases

## Decisions

- Chose Check Runner over Build Tracker because it has highest user-impact ("why aren't my resources being checked?") with well-bounded scope (~3K LOC prod)
- Requirement IDs use two-letter prefixes: SD (Scanner Discovery), CC (Check Creation), RL (Rate Limiting), CE (Check Execution), DL (Delegation & Locking), VS (Version Storage), WH (Webhook), MC (Manual Check), CL (Check Lifecycle), MO (Metrics/Observability)
- Metrics tests will follow the same approach as scheduler: assert on global metric struct fields since check_step_test.go already uses real metric infrastructure
