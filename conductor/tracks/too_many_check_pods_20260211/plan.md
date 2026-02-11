# Implementation Plan: Too Many Check Pods

## Phase 1: In-Flight Check Dedup

Prevent the scanner from creating duplicate in-memory check builds for the same resource.

**Key files:** `atc/db/check_factory.go`, `atc/db/check_factory_test.go`

- [x] Task 1.1: Write tests for in-flight dedup
  - Add test cases to `check_factory_test.go`:
    - (a) second in-memory check for same resource is skipped when one is in-flight
    - (b) check is allowed after in-flight build completes
    - (c) check is allowed after in-flight build fails
    - (d) manual trigger bypasses dedup

- [x] Task 1.2: Implement in-flight tracking
  - Add a `sync.Map` (keyed by resource ID or equivalent) to `checkFactory`
  - In `TryCreateCheck`, before the in-memory branch: if not `manuallyTriggered` and the resource is already tracked, return `nil, false, nil`
  - After creating the build, add the resource to the map
  - Provide a completion callback (or wrap the build) so the entry is removed when the build finishes

- [x] Task 1.3: Wire completion cleanup
  - Ensure the in-memory build execution path calls back into the check factory to remove the in-flight entry when the build completes or errors
  - Investigate `checkBuildChan` consumer to find the right hook point

- [ ] Task 1.4: Phase 1 Manual Verification

---

## Phase 2: Failed Check Container Cap

Limit the number of retained failed check containers per resource to 2, exempting hijacked containers.

**Key files:** `atc/gc/container_collector.go`, `atc/gc/container_collector_test.go`, `atc/db/container_repository.go`

- [ ] Task 2.1: Write tests for failed container cap
  - Add test cases:
    - (a) 3+ failed containers for same resource → oldest marked destroying
    - (b) only 2 retained
    - (c) hijacked container is exempt even if oldest
    - (d) failed containers from different resources are independent

- [ ] Task 2.2: Implement failed container cap query
  - Add a method to `ContainerRepository` (e.g. `DestroyExcessFailedCheckContainers(maxPerResource int)`) that finds failed check containers grouped by resource, ordered by creation time desc, and marks all beyond the Nth as destroying — excluding any with a recent `last_hijack`

- [ ] Task 2.3: Wire into container collector
  - Call the new method from `containerCollector.Run()`, using the existing `hijackContainerGracePeriod` for the hijack exemption window

- [ ] Task 2.4: Phase 2 Manual Verification

---
