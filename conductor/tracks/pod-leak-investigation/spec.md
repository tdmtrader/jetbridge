# Spec: Pod Leak Investigation

## Overview

In-memory check build `Finish()` DELETEs container records from the database, orphaning K8s pods. The Reaper only deletes pods whose DB record is in "destroying" state. Since `Finish()` removes the DB record entirely, orphaned pods accumulate indefinitely. For the `custom-time` resource type (checked every ~10s due to fast fail/retry), this reached 385+ pods, breaching the node's 110-pod limit and cascading failures across the entire pipeline.

## Root Cause Chain

1. Lidar scanner triggers `TryCreateCheck()` -> in-memory check build -> K8s pod created
2. Check completes -> `Finish()` (`atc/db/build_in_memory_check.go:456`) does `DELETE FROM containers WHERE in_memory_build_id = X`
3. Pod still exists in K8s but has **no DB record**
4. Reaper lists pod -> no matching DB handle -> `FindDestroyingContainers` returns nothing -> pod never deleted
5. Next scanner cycle creates new check -> new pod -> repeat
6. Node hits 110-pod limit -> all new pods Unschedulable -> all checks/builds error

## Requirements

1. Change `Finish()` to transition containers to "destroying" state instead of DELETE, so the Reaper can find and delete the actual K8s pods
2. Add orphan pod detection to the Reaper as a safety net -- delete pods that have no matching DB container record (with a grace period to avoid racing with container creation)
3. Preserve the existing `DestroyDirtyInMemoryBuildContainers` 5-min failsafe for builds that never finish
4. All existing tests must continue to pass
5. New tests for both the Finish() lifecycle change and the Reaper orphan detection

## Acceptance Criteria

- After a check build finishes, its K8s pod is deleted within one Reaper cycle (~10s)
- Pods that have no matching DB container record (older than grace period) are detected and deleted by the Reaper
- Pod count remains stable over time (no unbounded accumulation)
- No regression in existing GC behavior for non-check containers

## Out of Scope

- Changing check scheduling frequency or intervals
- Node-level pod limit configuration
- Multi-node scaling
