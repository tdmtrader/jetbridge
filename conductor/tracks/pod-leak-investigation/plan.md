# Plan: Pod Leak Investigation

## Phase 1: Fix `Finish()` container lifecycle (core fix)

- [x] Write test: `Finish()` transitions containers to "destroying" state instead of deleting them
- [x] Implement: change `build_in_memory_check.go:Finish()` to UPDATE containers SET state='destroying', in_memory_build_id=NULL, in_memory_build_create_time=NULL instead of DELETE

## Phase 2: Reaper orphan pod detection (safety net)

- [x] Write test: Reaper deletes pods that have no matching DB container record
- [x] Implement: add orphan detection to `reaper.go:Run()` -- wire existing `DestroyUnknownContainers` to insert "destroying" records for unknown pod handles

## Phase 3: Verify and deploy

- [x] Run existing unit tests (gc, db, jetbridge packages) -- all pass (270 jetbridge, gc, 154s db suite)
- [x] Cross-compile, build image, deploy, verify pod count stabilizes -- pods dropped from 25 to 7-8, stable over 2+ minutes
