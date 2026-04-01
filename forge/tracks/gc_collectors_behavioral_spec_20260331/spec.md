# GC Collectors Behavioral Specification

**Track:** `gc_collectors_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's garbage collection subsystem (`atc/gc/`). The GC collectors are responsible for operational reliability — cleaning up containers, volumes, caches, configs, sessions, build logs, pipelines, tokens, and workers that are no longer needed. Without correct GC behavior, clusters accumulate stale resources, leak containers, and eventually degrade.

### Scope

- Container collection (failed, orphaned, excess check, missing, in-memory dirty)
- Volume collection (failed, orphaned, missing)
- Build log retention (count-based, date-based, min-success, drain-aware)
- Build log retention calculator (policy logic)
- Resource config collection (unreferenced with grace period)
- Resource cache collection (invalid caches, worker caches)
- Resource cache use collection (build image caches, dirty in-memory, finished builds)
- Resource config check session collection (expired, inactive)
- Task cache collection (invalid caches)
- Build collection (mark non-interceptible)
- Pipeline collection (archive abandoned)
- Worker collection (delete unresponsive ephemeral, state metrics)
- Artifact collection (remove expired)
- Access token collection (remove expired with leeway)
- Check collection (delete completed)
- Destroyer (container/volume destruction for K8s reaper)

### Out of Scope

- Component runner framework (`atc/component/`) — how collectors are scheduled
- Database schema and migration details
- Worker runtime cleanup (K8s pod/volume deletion)
- Check lifecycle cleanup — covered by check_runner spec

---

## Section 1: Container Collection (7 requirements)

### CC-01: Destroy dirty in-memory build containers

When the container collector runs, it MUST call `DestroyDirtyInMemoryBuildContainers()` to clean up containers from failed in-memory builds.

### CC-02: Destroy failed containers

When the container collector runs, it MUST call `DestroyFailedContainers()` to mark containers that failed to transition states as DESTROYING.

### CC-03: Continue cleanup after failed container errors

When destroying failed containers returns an error, the collector MUST continue to process orphaned and excess containers (not short-circuit).

### CC-04: Cap excess check containers

When the container collector runs, it MUST call `DestroyExcessCheckContainers()` with the configured hijack grace period to limit check containers to 1 per resource scope.

### CC-05: Continue cleanup after excess check errors

When destroying excess check containers returns an error, the collector MUST continue to process remaining cleanup stages.

### CC-06: Orphaned container cleanup with hijack grace period

When finding orphaned containers, the collector MUST:
- Mark created containers (not recently hijacked) as DESTROYING
- Mark containers hijacked beyond the grace period as DESTROYING
- Preserve containers that were hijacked within the grace period

### CC-07: Remove missing containers

When the container collector runs, it MUST call `RemoveMissingContainers()` with the configured missing container grace period to remove containers not seen by any worker.

---

## Section 2: Volume Collection (3 requirements)

### VC-01: Cleanup failed volumes

When the volume collector runs, it MUST call `DestroyFailedVolumes()` to mark volumes that failed initialization as DESTROYING.

### VC-02: Mark orphaned volumes as destroying

When the volume collector runs, it MUST call `GetOrphanedVolumes()` and transition each orphaned volume (no associated build/cache/task) to DESTROYING state.

### VC-03: Remove missing volumes

When the volume collector runs, it MUST call `RemoveMissingVolumes()` with the configured grace period to remove volumes not seen by any worker.

---

## Section 3: Build Log Retention (12 requirements)

### BL-01: Remove build events from deleted pipelines

When the build log collector runs, it MUST first remove build events from deleted pipelines before processing active pipelines.

### BL-02: Error on deleted pipeline cleanup failure

When removing build events from deleted pipelines fails, the collector MUST return the error.

### BL-03: Skip paused pipelines

When processing pipelines, the collector MUST skip paused pipelines entirely.

### BL-04: Skip paused jobs

When processing jobs within a pipeline, the collector MUST skip paused jobs.

### BL-05: Skip running builds

When evaluating builds for reaping, the collector MUST skip builds that are still running.

### BL-06: Count-based retention

When a job has a count-based retention policy (Builds > 0), the collector MUST retain only that many builds and reap older ones.

### BL-07: Date-based retention

When a job has a date-based retention policy (Days > 0), the collector MUST retain builds newer than that many days and reap older ones.

### BL-08: Combined count and date retention

When a job has both count and date retention, the collector MUST only reap builds that satisfy BOTH criteria (build is beyond count AND older than days).

### BL-09: Minimum succeeded builds retention

When a job has `MinSuccessBuilds > 0`, the collector MUST retain at least that many succeeded builds regardless of other retention criteria.

### BL-10: Drain-aware reaping

When drain is configured, the collector MUST NOT reap builds that have not yet been drained. It MUST update FirstLoggedBuildID to the earliest non-drained build.

### BL-11: FirstLoggedBuildID tracking

When builds are reaped, the collector MUST update the job's FirstLoggedBuildID to the earliest non-reaped build. When no reaping occurs, it MUST NOT update FirstLoggedBuildID.

### BL-12: Job with zero retention skips reaping

When a job's retention calculator returns 0 for both builds and days, the collector MUST skip reaping for that job entirely.

---

## Section 4: Build Log Retention Calculator (6 requirements)

### RC-01: No settings returns zeros

When no default, max, or job settings are configured, the calculator MUST return zero for all retention fields.

### RC-02: Job settings used when no defaults

When only job-level settings are provided (no default or max), the calculator MUST return the job values.

### RC-03: Default settings applied

When default settings are configured and no job settings, the calculator MUST return the default values.

### RC-04: Job overrides default

When both default and job settings are configured, the calculator MUST use the job values.

### RC-05: Max caps job settings

When max settings are configured, the calculator MUST cap job/default values to the max (take the lower value).

### RC-06: Min success builds preserved

When MinSuccessBuilds is set, the calculator MUST preserve it in the output. When MinSuccessBuilds exceeds the build count, it MUST be capped to the build count.

---

## Section 5: Resource Config Collection (4 requirements)

### CF-01: Preserve configs referenced by check sessions

When a resource config is referenced by active resource config check sessions, the collector MUST NOT delete it.

### CF-02: Preserve configs referenced by resources/types

When a resource config is referenced by active resources or resource types, the collector MUST NOT delete it.

### CF-03: Preserve configs referenced by caches

When a resource config is referenced by active resource caches, the collector MUST NOT delete it.

### CF-04: Grace period before deletion

When a resource config is no longer referenced, the collector MUST NOT delete it until the grace period has elapsed.

---

## Section 6: Resource Cache Collection (6 requirements)

### CA-01: Preserve caches in active use

When a resource cache is still in use by a running build or active resource, the collector MUST NOT delete it.

### CA-02: Remove unused caches from paused pipelines

When a resource cache is a job input and the pipeline is paused, the collector MUST remove the cache.

### CA-03: Preserve job input caches from active pipelines

When a resource cache is a job input and the pipeline is not paused, the collector MUST preserve the cache.

### CA-04: Image cache replacement on build success

When a new build of the same job succeeds with a different image cache, the collector MUST remove the old cache and keep the new one.

### CA-05: Image cache preservation on build failure

When a new build of the same job fails with a different image cache, the collector MUST keep both the old and new caches.

### CA-06: One-off build cache grace period

When a resource cache is from a one-off build, the collector MUST preserve it for a grace period after the build finishes, then remove it.

---

## Section 7: Resource Cache Use Collection (4 requirements)

### CU-01: Preserve cache uses for running builds

When a build is still running, the collector MUST NOT clean up its cache uses.

### CU-02: Clean cache uses for completed builds

When a build has completed (succeeded, failed, or aborted), the collector MUST clean up its cache uses.

### CU-03: Clean cache uses for one-off failed builds

When a one-off build fails, the collector MUST clean up its cache uses.

### CU-04: Clean cache uses when later build succeeds

When a later build of the same job succeeds, the collector MUST clean up cache uses from earlier builds.

---

## Section 8: Resource Config Check Session Collection (4 requirements)

### CS-01: Preserve active resource check sessions

When a resource config check session is associated with an active resource, the collector MUST preserve it.

### CS-02: Remove expired check sessions

When a resource config check session has passed its expiration time, the collector MUST remove it.

### CS-03: Remove sessions on config change

When a resource config changes, the collector MUST remove the old check sessions.

### CS-04: Remove sessions on resource removal

When a resource is removed from the pipeline, the collector MUST remove its check sessions.

---

## Section 9: Simple Collectors (8 requirements)

### SC-01: Build collector marks non-interceptible

When the build collector runs, it MUST call `MarkNonInterceptibleBuilds()` to transition builds to a GC-eligible state.

### SC-02: Pipeline collector archives abandoned

When the pipeline collector runs, it MUST call `ArchiveAbandonedPipelines()` to clean up stale pipelines.

### SC-03: Worker collector deletes unresponsive ephemeral

When the worker collector runs, it MUST call `DeleteUnresponsiveEphemeralWorkers()` to remove workers that have not heartbeat.

### SC-04: Worker collector propagates errors

When deleting unresponsive workers fails, the worker collector MUST return the error.

### SC-05: Artifact collector removes expired

When the artifact collector runs, it MUST call `RemoveExpiredArtifacts()` to clean up old worker artifacts.

### SC-06: Access token collector removes expired with leeway

When the access token collector runs, it MUST call `RemoveExpiredAccessTokens()` with the JWT default leeway.

### SC-07: Check collector deletes completed

When the check collector runs, it MUST call `DeleteCompletedChecks()` to remove finished check builds.

### SC-08: Task cache collector removes invalid

When the task cache collector runs, it MUST call `RemoveInvalidTaskCaches()` to clean up caches for deleted/invalid tasks.

---

## Section 10: Destroyer (5 requirements)

### DS-01: Destroy containers with valid worker

When destroying containers, the destroyer MUST remove containers from the database for the specified worker and handles.

### DS-02: Require worker name for container destruction

When destroying containers without a worker name, the destroyer MUST return an error.

### DS-03: Destroy volumes with valid worker

When destroying volumes, the destroyer MUST remove volumes from the database for the specified worker and handles.

### DS-04: Require worker name for volume destruction

When destroying volumes without a worker name, the destroyer MUST return an error.

### DS-05: Find destroying volumes for GC

When querying for destroying volumes, the destroyer MUST return the handles of all volumes in DESTROYING state for the given worker.
