# Spec: Too Many Check Pods

**Track ID:** `too_many_check_pods_20260211`
**Type:** bugfix

## Overview

The lidar scanner creates in-memory check builds via `CreateInMemoryBuild`, which has no guard against duplicate in-flight checks for the same resource. The DB-backed path (`CreateBuild` in `resource.go:342-357`) already checks `WHERE resource_id = ? AND completed = false` and skips creation if a build is running — but the in-memory path bypasses this entirely. When a resource check takes longer than the check interval (default 1 min) or the scanner interval (10s), the scanner schedules additional check builds for the same resource, creating duplicate check pods.

Additionally, there is no cap on how many failed check containers exist per resource. If a resource fails repeatedly, each check creates a new container that lives for its full session TTL (5 min–1 hr). Only 1-2 recent failures need to be retained for debugging/hijack; older failures should be cleaned up promptly.

## Requirements

1. **Prevent duplicate in-flight checks per resource** — Before creating an in-memory check build, verify that no in-memory check build is already running for the same resource. If one exists, skip creation (matching `CreateBuild` behavior).
2. **Track in-flight in-memory check builds** — Maintain a concurrent tracking structure (e.g. `sync.Map`) in the check factory to record which resources have running in-memory checks. Remove entries when builds complete.
3. **Cap failed check containers per resource** — For each resource, retain at most 2 recent failed check containers. Mark older failures for immediate destruction, unless the container is actively hijacked.
4. **Preserve existing behavior** — Manual/API-triggered checks (`toDB=true`) and the existing DB-backed dedup are unchanged. Hijacked containers are never reaped early.

## Acceptance Criteria

- [ ] `TryCreateCheck` (in-memory path) skips creation when an in-memory check is already in-flight for the same resource
- [ ] Tracking state is cleaned up when in-memory builds complete (success or failure)
- [ ] Manual checks (`manuallyTriggered=true`) bypass the dedup guard (matching `CreateBuild` behavior)
- [ ] At most 2 failed check containers are retained per resource; older failures are marked destroying
- [ ] Actively hijacked containers are exempt from the failed-container cap
- [ ] Unit tests cover: dedup skip, cleanup on completion, cleanup on failure, manual override, failed cap enforcement, hijack exemption
- [ ] No regression in check correctness (versions still detected on schedule)

## Out of Scope

- Changing the one-pod-per-check model
- Changes to the DB-backed check build path (`CreateBuild`)
- Modifying the session TTL values (5 min / 1 hr)
- Changes to the `--failed-grace-period` flag behavior
