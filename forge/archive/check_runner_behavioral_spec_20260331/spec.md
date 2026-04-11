# Check Runner Behavioral Specification

**Track:** `check_runner_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's resource check subsystem. The check runner is responsible for the core user-visible behavior of "how does Concourse discover new versions of my resources?" — it schedules periodic checks, executes them in containers (or natively for registry-image), stores discovered versions, and triggers downstream job scheduling.

### Scope

- Scanner discovery and scheduling (resource/type enumeration, concurrency, native resolution)
- Check creation and deduplication (interval enforcement, in-memory vs DB builds, scope-based dedup)
- Rate limiting (dynamic and static check throttling)
- Check execution (CheckStep: container creation, version collection, timeout handling)
- Check delegation and locking (WaitToRun, scope locking, interval enforcement at execution time)
- Version storage and notifications (SaveVersions, check_order, job scheduling triggers)
- Webhook integration (token validation, check triggering via HTTP)
- API manual check triggers (resource, resource type, prototype checks)
- Check lifecycle and garbage collection (completed build cleanup, session cleanup)
- Metrics and observability (ChecksEnqueued, ChecksStarted, ChecksFinished)

### Out of Scope

- Build execution engine (`atc/engine/`) beyond check delegation
- Resource type image resolution internals (`atc/imageresolver/`)
- Container/worker selection (`atc/worker/`)
- Database schema and migrations
- Fly CLI check commands (`fly check-resource`)

---

## Section 1: Scanner Discovery & Scheduling (12 requirements)

### SD-01: Resource enumeration from active pipelines

When the scanner's `Run()` is invoked, the system MUST fetch all resources from active (non-paused) pipelines that are either:
- Used as inputs to jobs (appear in `job_inputs`), OR
- Put-only resources (no job inputs) that have errored (last_check_succeeded=false or no check yet)

### SD-02: Resource type enumeration by pipeline

When the scanner's `Run()` is invoked, the system MUST fetch all resource types from active (non-paused) pipelines, grouped by pipeline ID.

### SD-03: Concurrent resource scanning with bounded parallelism

When scanning resources, the system MUST:
- Process resources concurrently using worker goroutines
- Limit concurrency to `min(maxConcurrency, len(resources))`
- Distribute work via a buffered channel

### SD-04: Concurrent resource type scanning with bounded parallelism

When scanning resource types (for native resolution), the system MUST use the same bounded parallelism pattern as resource scanning.

### SD-05: Context cancellation stops scanning

When the context is cancelled during scanning, the system MUST:
- Stop sending new work to worker goroutines
- Allow in-progress workers to detect cancellation
- Return without error (cancellation is graceful)

### SD-06: check_every=never skips resource

When a resource has `check_every.never=true`, the scanner MUST skip it entirely — no check build is created and no native resolution is attempted.

### SD-07: Pinned version passed to check

When a resource has a `CurrentPinnedVersion()`, the scanner MUST pass that version as the `from` parameter to `TryCreateCheck`. When no pinned version exists, `from` MUST be nil.

### SD-08: Panic recovery per resource

When a panic occurs while scanning an individual resource, the system MUST:
- Recover from the panic using `util.DumpPanic`
- Log the error
- Continue scanning remaining resources (the goroutine does not terminate)

### SD-09: Native registry-image resolution for resource types

When a resolver is available and the resource type uses `registry-image`, the scanner MUST:
- Skip check pod creation entirely
- Resolve the digest via the registry API
- Save the resolved digest as a version `{"digest": "<digest>"}`
- Update LastCheckEndTime with succeeded=true
- Increment ChecksEnqueued metric

### SD-10: Native registry-image resolution for resources

When a resolver is available and a resource's type is `registry-image`, the scanner MUST resolve the resource natively (same flow as SD-09) instead of creating a check pod.

### SD-11: Native resolution respects check interval

When performing native resolution (for both resources and resource types), the system MUST skip resolution if the check interval has not elapsed since LastCheckEndTime.

### SD-12: Native resolution passes credentials

When performing native resolution, the system MUST extract `username` and `password` from the resource source config and pass them to the resolver as BasicAuth credentials.

---

## Section 2: Check Creation & Deduplication (12 requirements)

### CC-01: Default check intervals

When creating a check, the system MUST apply these default intervals:
- Resources: `DefaultCheckInterval` (1 minute)
- Resource types: `DefaultResourceTypeInterval` (1 hour)
- Webhook-enabled resources: `DefaultWebhookInterval` (10 seconds)

### CC-02: Custom check_every overrides default

When a checkable has a non-nil `CheckEvery()`, the system MUST use that interval instead of the default.

### CC-03: Interval enforcement skips check creation

When the check interval has not elapsed since `LastCheckEndTime()`, the system MUST skip check creation and return `(nil, false, nil)`.

### CC-04: Manually triggered checks bypass interval

When `manuallyTriggered=true`, the system MUST skip interval enforcement and always attempt to create the check.

### CC-05: Source defaults from parent type

When creating a check plan, the system MUST merge source defaults from the parent resource type (or base resource type defaults) into the check plan.

### CC-06: DB build creation (toDB=true)

When `toDB=true`, the system MUST call `checkable.CreateBuild()` to persist the check build in the database. If `created=false` (deduplication), return `(nil, false, nil)`.

### CC-07: In-memory build creation (toDB=false)

When `toDB=false`, the system MUST call `checkable.CreateInMemoryBuild()` and send the resulting build to `checkBuildChan` for the tracker to pick up.

### CC-08: In-memory scope-based deduplication

When creating an in-memory build with `manuallyTriggered=false` and `scopeID!=0`, the system MUST use `sync.Map.LoadOrStore()` to prevent duplicate in-flight checks for the same scope. If already in-flight, return `(nil, false, nil)`.

### CC-09: Manual triggers bypass in-memory dedup

When `manuallyTriggered=true`, the system MUST NOT check the inFlightChecks map — always create the build.

### CC-10: In-flight tracking cleanup on finish

When an in-memory build with tracking finishes, the system MUST delete the scope ID from `inFlightChecks` via the `onFinishBuild` wrapper.

### CC-11: In-flight tracking cleanup on creation error

When `CreateInMemoryBuild()` fails and the scope was tracked, the system MUST delete the scope ID from `inFlightChecks` to prevent permanent blocking.

### CC-12: Resource type filter for resource types

When fetching resource types for scanning, the system MUST include only resource types from active (non-paused) pipelines.

---

## Section 3: Rate Limiting (6 requirements)

### RL-01: Dynamic rate calculation from resource count

When `checksPerSecond=0` (dynamic mode), the system MUST calculate the rate limit as `activeResourceCount / checkInterval`, refreshing the count periodically.

### RL-02: Minimum rate floor

When the calculated rate is below `minChecksPerSecond`, the system MUST use `minChecksPerSecond` as the rate limit.

### RL-03: Infinite rate when no checkables

When the active resource count is zero, the system MUST set the rate limit to `rate.Inf` (unlimited).

### RL-04: Static rate override

When `checksPerSecond > 0`, the system MUST use that value directly as the rate limit, ignoring the dynamic calculation.

### RL-05: Negative rate disables limiting

When `checksPerSecond < 0`, the system MUST set the rate limit to `rate.Inf` (unlimited), ignoring the dynamic calculation.

### RL-06: Rate limit wait respects context cancellation

When waiting for a rate limit reservation, the system MUST cancel the reservation and return `ctx.Err()` if the context is cancelled before the delay elapses.

---

## Section 4: Check Execution — CheckStep (14 requirements)

### CE-01: Scope creation and pointing

When a CheckStep runs, the system MUST:
- Find or create a ResourceConfigScope via the delegate
- Point the resource/resource type to the scope (PointToCheckedConfig)

### CE-02: WaitToRun determines execution

When `delegate.WaitToRun()` returns `run=false`, the system MUST NOT execute the check container. It MUST still succeed (no error) and fetch the latest version from the scope as its result.

### CE-03: Lock acquisition before execution

When `WaitToRun()` returns `run=true`, it MUST provide a lock that the step holds during execution and releases after SaveVersions completes.

### CE-04: Custom resource type image fetching

When the check uses a custom resource type, the system MUST fetch the resource type image before creating the check container. The image spec MUST be set on the container spec.

### CE-05: Privileged custom resource types

When a custom resource type has `privileged=true`, the system MUST pass `privileged=true` when fetching the image.

### CE-06: Check timeout enforcement

When a check plan specifies a timeout, the system MUST enforce it on the check execution. When no plan timeout exists, the system MUST apply the default timeout. A timeout MUST cause the step to fail without error.

### CE-07: Version collection and storage

When a check succeeds with versions, the system MUST:
- Call `scope.SaveVersions()` with the collected versions
- Store the latest version as the step result

### CE-08: Empty version result handling

When a check returns zero versions, the system MUST succeed without storing a version. The step result MUST NOT contain a version.

### CE-09: Check start time tracking

Before executing the check container, the system MUST call `UpdateScopeLastCheckStartTime` to record when the check began.

### CE-10: Check end time tracking on success

After saving versions, the system MUST call `UpdateScopeLastCheckEndTime(succeeded=true)`.

### CE-11: Check end time tracking on error

When a check errors (context error, execution failure), the system MUST call `UpdateScopeLastCheckEndTime(succeeded=false)`.

### CE-12: Script failure (non-zero exit) handling

When the check process exits with a non-zero status, the system MUST:
- NOT return an error (the step "succeeds" from the engine's perspective)
- Emit a failed Finished event
- Update the scope's last check end time with succeeded=false

### CE-13: Tracing context propagation

When tracing is enabled, the system MUST:
- Set the TRACEPARENT environment variable on the container
- Emit span events for initializing, starting, and finished states
- Propagate the span context to the scope

### CE-14: Container specification

When creating a check container, the system MUST:
- Include the certs volume mount
- Use the base type for image selection
- NOT set a workdir

---

## Section 5: Check Delegation & Locking (12 requirements)

### DL-01: Resource scope creation

When checking a resource, the delegate MUST find or create a scope for the resource's specific resource config, with the resource ID.

### DL-02: Global scope creation

When no resource or resource type is specified (prototype check), the delegate MUST find or create a global scope (nil resource).

### DL-03: Resource rate limiting before lock

For resource checks, the delegate MUST call the rate limiter before acquiring the checking lock.

### DL-04: SkipInterval bypasses rate limiting

When `skipInterval=true`, the delegate MUST NOT call the rate limiter.

### DL-05: Resource lock acquisition

For resource checks, the delegate MUST acquire a resource checking lock. If the lock cannot be acquired (held by another ATC), it MUST return `run=false`.

### DL-06: Interval re-check after lock acquisition

After acquiring the lock, the delegate MUST re-check whether the interval has elapsed (to handle races where another ATC completed a check while waiting for the lock). If the interval hasn't elapsed, it MUST return `run=false`.

### DL-07: Failed last check always allows retry

When the last check failed (`succeeded=false`), the delegate MUST return `run=true` regardless of whether the interval has elapsed.

### DL-08: Never interval returns false

When the check interval is `never`, the delegate MUST return `run=false` without fetching last check info or acquiring a lock.

### DL-09: Resource types skip lock and rate limit

For resource type checks, the delegate MUST NOT acquire a lock and MUST NOT rate limit. It MUST return a no-op lock.

### DL-10: Resource type interval enforcement

For resource type checks, the delegate MUST enforce the check interval based on LastCheckEndTime. If the interval hasn't elapsed and skipInterval=false, return `run=false`.

### DL-11: PointToCheckedConfig updates scope pointer

After scope creation, the delegate MUST call the appropriate method to point the resource/resource type/prototype to the new scope.

### DL-12: UpdateScopeLastCheckStartTime calls OnCheckBuildStart

For non-nested resource checks, UpdateScopeLastCheckStartTime MUST call `build.OnCheckBuildStart()` to initialize the in-memory build, passing the build ID and public plan to the scope.

---

## Section 6: Version Storage & Notifications (5 requirements)

### VS-01: SaveVersions assigns check_order

When new versions are saved, the system MUST assign incrementing check_order values to new versions.

### VS-02: Existing versions keep check_order

When saving versions that already exist (by SHA256 hash), the system MUST NOT change their check_order.

### VS-03: New versions trigger job scheduling

When new versions are saved for a resource, the system MUST request scheduling for all jobs that use that resource as an input.

### VS-04: Passed-constraint jobs not scheduled directly

When new versions are saved, the system MUST NOT request scheduling for jobs that only reference the resource through passed constraints (not direct inputs).

### VS-05: Empty version list rejected

When SaveVersions is called with an empty version list, the system MUST return an error.

---

## Section 7: Webhook Integration (6 requirements)

### WH-01: Missing webhook token returns 400

When a webhook request arrives without a `webhook_token` query parameter, the system MUST return HTTP 400 Bad Request.

### WH-02: Resource not found returns 404

When the resource specified in the webhook URL does not exist in the pipeline, the system MUST return HTTP 404 Not Found.

### WH-03: Token validation against encrypted resource config

When a webhook request arrives, the system MUST:
- Evaluate the resource's webhook token through the credential manager
- Compare the evaluated token against the request's `webhook_token` parameter
- Return HTTP 401 Unauthorized if they don't match

### WH-04: Valid webhook creates DB check build

When the webhook token is valid, the system MUST call `TryCreateCheck` with `manuallyTriggered=true`, `skipIntervalRecursively=false`, and `toDB=true`.

### WH-05: Successful check creation returns 201

When a check build is successfully created via webhook, the system MUST return HTTP 201 Created with the build JSON.

### WH-06: Check not created returns 500

When `TryCreateCheck` returns `created=false` via webhook (deduplication), the system MUST return HTTP 500 Internal Server Error.

---

## Section 8: API Manual Check Triggers (6 requirements)

### MC-01: Manual check with from version

When a manual check request includes a `from` version in the body, the system MUST pass that version to `TryCreateCheck`.

### MC-02: Manual check without from version

When a manual check request has no `from` version, the system MUST pass `nil` as the from version.

### MC-03: Shallow check does not skip interval recursively

When a manual check request has `shallow=true`, the system MUST pass `skipIntervalRecursively=false` to `TryCreateCheck`.

### MC-04: Deep check skips interval recursively

When a manual check request has `shallow=false`, the system MUST pass `skipIntervalRecursively=true` to `TryCreateCheck`.

### MC-05: Malformed request body returns 400

When the request body cannot be decoded as JSON, the system MUST return HTTP 400 Bad Request.

### MC-06: Manual check always uses toDB=true

All manual check API endpoints (resource, resource type, prototype) MUST create DB-persisted builds (`toDB=true`), not in-memory builds.

---

## Section 9: Check Lifecycle & GC (5 requirements)

### CL-01: Completed check build deletion retains latest

When deleting completed check builds, the system MUST retain the most recent completed check build for each scope (resource, resource type, or prototype).

### CL-02: Incomplete checks preserved

When deleting completed check builds, the system MUST NOT delete check builds that are not yet completed (in-progress builds).

### CL-03: Batch deletion

When deleting completed check builds, the system MUST delete in batches (configurable batch size) to avoid long-running transactions.

### CL-04: Job builds ignored

The check lifecycle cleanup MUST NOT delete job builds (non-check builds) regardless of their status.

### CL-05: Inactive session cleanup

The system MUST clean up resource config check sessions that are no longer associated with active resources, resource types, or prototypes (including those in paused pipelines).

---

## Section 10: Metrics & Observability (5 requirements)

### MO-01: ChecksEnqueued incremented on check creation

When a check build is successfully created (by scanner or native resolution), the system MUST increment `metric.Metrics.ChecksEnqueued`.

### MO-02: ChecksStarted incremented at execution start

When a CheckStep begins execution (after WaitToRun returns run=true), the system MUST increment ChecksStarted.

### MO-03: ChecksFinishedWithSuccess on successful check

When a check completes with versions saved, the system MUST increment ChecksFinishedWithSuccess.

### MO-04: ChecksFinishedWithError on failed check

When a check fails (timeout, non-zero exit), the system MUST increment ChecksFinishedWithError.

### MO-05: Span events emitted during check lifecycle

When tracing is enabled, the CheckStep MUST emit span events at each lifecycle phase: Initializing, Starting, Finished.
