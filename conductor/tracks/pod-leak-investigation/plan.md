# Plan: Pod Leak Investigation

## Phase 1: Fix `Finish()` container lifecycle (core fix)

- [ ] Write test: `Finish()` transitions containers to "destroying" state instead of deleting them
- [ ] Implement: change `build_in_memory_check.go:Finish()` to UPDATE containers SET state='destroying', in_memory_build_id=NULL, in_memory_build_create_time=NULL instead of DELETE

## Phase 2: Reaper orphan pod detection (safety net)

- [ ] Write test: Reaper deletes pods that have no matching DB container record
- [ ] Implement: add orphan detection to `reaper.go:Run()` -- compare active pod handles against all DB handles for the worker; delete pods whose handle has no DB record and whose creation timestamp is older than a grace period (2 minutes)

## Phase 3: Verify and deploy

- [ ] Run existing unit tests (gc, db, jetbridge packages)
- [ ] Cross-compile, build image, deploy, verify pod count stabilizes
