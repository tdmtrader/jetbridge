# Real PostgreSQL API Volumes Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Preserve other agents' edits and stage only the file assigned here.

**Goal:** Replace every generated database fake in the Volumes API tests with
state and failures observed through a unique PostgreSQL template clone.

**Exact baseline and target:** At `32a21b1d9a`,
`atc/api/volumes_test.go` imports `dbfakes` and constructs two `FakeWorker`
values plus seven `FakeCreatedVolume` values. This phase targets 9 to 0
constructors and removal of the import.

**Architecture:** Opt the Describe into the existing `useRealDB` fixture. Each
spec gets one unique clone from the shared PostgreSQL service and a server
wired to repositories built from that clone. Persist workers and resource,
resource-type, container parent/child, task-cache, pipeline, and job rows
through production factories. Use a closed clone-local connection for the
repository query failure. Model deletion between repository scan and response
presentation with a small test-only decorator around the real repository; the
returned `CreatedVolume` values remain real.

**Files:** Modify only `atc/api/volumes_test.go` and this plan. Do not modify
production code, generated fakes, Docker/service lifecycle, benchmarks, or
corpus files. Do not push.

## Task 1: Persist the successful volume list

- [x] Capture the focused fake-backed baseline with
  `ginkgo --focus='Volumes API' ./atc/api`.
- [x] Call `useRealDB`, persist team `a-team`, shadow the package server with
  `realdb.Serve`, and save workers `some-worker` and `some-other-worker`.
- [x] Give `some-worker` the base resource type
  `some-base-resource-type@some-base-version` and create nested base/custom
  resource caches through `db.NewResourceCacheFactory`; create a real volume,
  mark it created, and call `InitializeResourceCache`.
- [x] Find the real worker base resource type through
  `db.NewWorkerBaseResourceTypeFactory`, then create and transition its real
  base-resource-type volume.
- [x] Create a fixed-handle real container, its parent container volume, and a
  child through `CreateChildForContainer`. Assert the real invariant that the
  parent and child remain on the same worker instead of preserving the old
  fake's inconsistent cross-worker graph.
- [x] Save pipeline `some-pipeline` with instance var `branch=master`, load job
  `some-job`, create its task cache/worker task cache, and transition the real
  task-cache volume.
- [x] Decode the response as `[]atc.Volume` and compare with `ConsistOf` using
  returned handles and the persisted pipeline ID; `GetTeamVolumes` has no
  ordering guarantee. Persist a team-owned created container or task-cache
  volume for another team and prove it is absent; resource and base-resource-
  type volumes are global and do not prove team filtering.
- [x] Replace the repository-call-count expectation with an externally
  observable team-scoping assertion.
- [x] Register `response.Body.Close` with `DeferCleanup` for every non-nil HTTP
  response, including expected 401 and 500 responses.
- [x] Before rewiring one representative leaf, add its persisted/API outcome
  assertion, run it RED against the fake graph, then rewire and run GREEN.
- [x] Sensitivity-check a returned handle, worker, type, or team scope with a
  deliberately wrong expectation, observe failure, restore, and pass.

## Task 2: Exercise failures with real PostgreSQL objects

- [x] For `GetTeamVolumes` failure, open a secondary clone connection, create
  `db.NewVolumeRepository` from it, close it, install that repository into a
  copied API dependency set, and assert the request returns 500. Close this
  deliberately doomed connection exactly once: `db.DbConn.Close` also closes
  its listener and a second close can panic. Register cleanup for every custom
  `newAPIServer(copiedDeps)` instance.
- [x] Add a narrow test-only repository decorator that embeds
  `db.VolumeRepository`, delegates `GetTeamVolumes`, then synchronously invokes
  an `afterGet` callback before returning the scanned real volumes.
- [x] In the deletion-race spec, persist one resource-cache volume and one
  base-resource-type volume. Before the request, call `build.Delete()` to
  remove `resource_cache_uses`. Define `afterGet func() error`; the decorator
  propagates only a non-nil callback error. Never make Gomega assertions in the
  HTTP server goroutine. Its exact sequence is `Begin`, `defer Rollback`,
  destroy the outer leaf resource cache associated with the resource volume
  (not the nested custom-type cache), then `Commit` and return `nil`. Require
  presentation of the stale real resource volume to fail and be omitted while
  the real resource-type volume remains in the 200 response.
- [x] Keep the decorator limited to the otherwise-unobservable scan/present
  timing midpoint. Do not retain a generated fake for either returned volume.

## Task 3: Verification and closure

- [x] Run `gofmt -w atc/api/volumes_test.go` and
  `go test ./atc/api -run '^$'`.
- [x] Run focused serial and parallel verification:
  `ginkgo --focus='Volumes API' ./atc/api` and
  `ginkgo -p --focus='Volumes API' ./atc/api`.
- [x] Run `go test ./atc/api -count=1`, `go vet ./atc/api`,
  `go test -tags live ./atc/api -run '^$'`, and
  `go vet -tags live ./atc/api`.
- [x] Confirm `rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{'
  atc/api/volumes_test.go` and the import search both return no result.
- [x] Inspect clone lifecycle, secondary-connection cleanup, transaction
  completion, cross-team exclusion, persisted reloads, and response assertions.
- [x] Run `git diff --check`, independently review the implementation with no
  unresolved Critical, Important, or Minor findings, run
  `git status --short` to expose untracked/unrelated files, and record exact
  evidence here.
- [x] Commit the implementation as
  `test(api): persist volume listing state`, then commit plan closure as
  `docs: record api volumes postgres conversion`.

## Phase acceptance

- [x] All nine generated database-fake constructors and the `dbfakes` import
  are removed from `volumes_test.go`.
- [x] Normal list presentation and team filtering use only real persisted rows.
- [x] Repository failure is produced by a real closed connection; the deletion
  race uses scanned real volumes plus only the narrow timing decorator.
- [x] Focused tests pass serially and in parallel against isolated clones in
  the one machine-wide PostgreSQL service.
- [x] No production behavior or unrelated file changes, and nothing is pushed.

## Observed closure evidence

- Assertion-first RED required a fake child volume to share its parent's
  worker; the pre-conversion response failed with `some-other-worker` versus
  `some-worker`. The real parent/child graph then satisfied the invariant.
- Sensitivity changed the expected task-cache pipeline to `wrong-pipeline`;
  `ConsistOf` reported the missing wrong entry and unexpected persisted
  `some-pipeline` entry. Restoring it passed.
- Focused Volumes API specs passed 10/10 sequentially and 10/10 across nine
  Ginkgo processes. Fresh uncached `go test ./atc/api -count=1` passed all 936
  specs in 62.274 seconds.
- Standard and live-tag compile/vet, formatting, fake/import recount, and diff
  checks passed. The exact file census is 9 to 0 generated database fakes.
- Independent implementation review found no Critical, Important, or Minor
  issue and verified the real graph, team filtering, exact-once closed
  connection, HTTP cleanup, deletion transaction, and clone lifecycle.
- Implementation commit: `3e6f3c64ad` (`test(api): persist volume listing
  state`).
