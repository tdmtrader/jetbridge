# Real PostgreSQL API Resources Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Use one owner for `resources_test.go`, preserve concurrent edits, and stage
> only the task's assigned files.

**Goal:** Replace every generated database fake in the Resources API tests
with real resource, type, prototype, build, pin, cache, pipeline, team, and
shared-scope state from a unique PostgreSQL template clone.

**Exact baseline and target:** At `213f772751`,
`atc/api/resources_test.go` has 27 explicit generated database-fake
constructors: `FakeResource` x17, `FakeBuild` x4, `FakeResourceType` x4,
`FakePipeline` x1, and `FakePrototype` x1. It also consumes suite-level
`dbTeamFactory`, `dbTeam`, `dbResourceFactory`, and `dbCheckFactory` fakes.
This phase targets 27 to 0, removes the `dbfakes` import, and removes every
reference to those four suite DB fakes from this file.

**Architecture:** Opt the Resources API Describe into existing `useRealDB`.
Each spec gets one clone and a locally shadowed server built from copied real
dependencies. Persist ordinary graphs with `dbtest.Builder` and production
factories. Direct SQL failures use preloaded real objects whose secondary
connection is then closed exactly once. Selective handler-protocol outcomes use
small embedding/delegating decorators around real interfaces; they may record
arguments or override only the result PostgreSQL cannot produce, but they must
not fabricate successful persisted state.

**Files:** Modify only `atc/api/resources_test.go` and this plan. Do not modify
production code, generated fakes, the shared API fixture, Docker/service
lifecycle, benchmarks, or corpus files. Do not push.

## Task 1: Persist read, list, detail, and shared-resource endpoints

**Payoff:** remove 15 constructors.

- [ ] Capture the focused fake-backed baseline for all Resources API specs.
- [ ] Call `useRealDB` only in each converted context during Tasks 1 and 2,
  create the route's real team/pipeline/config, shadow
  `server *httptest.Server`, and register every non-nil response body and custom
  server for cleanup before connection/drop cleanup. Keep unconverted contexts
  on their existing fake-backed package server until Task 3 removes the broad
  root setup; every incremental commit must remain green under the entire
  Resources API focus.
- [ ] Create resource versions, resource-type versions, and prototype versions
  by running `dbtest.Builder` setup functions against a `dbtest.Scenario` that
  holds the already-saved team/pipeline. Reload resources/types after attaching
  scopes or changing pins.
- [ ] Convert the global resource list, pipeline resource list, resource-type
  list, and resource detail responses to decoded objects derived from real
  rows. Replace fake-inconsistent cross-team/pipeline summaries, arbitrary IDs,
  and impossible started-with-end-time builds with the real owning graph and
  dynamic timestamps/IDs.
- [ ] Update an existing pipeline only through
  `team.SavePipeline(ref, config, pipeline.ConfigVersion(), false)`; the
  `realDB.SavePipeline` zero config version is for initial creation only.
  Persist visibility with `Expose`/`Hide`, not config fields.
- [ ] For shared resources/types, set `atc.EnableGlobalResources=true` and
  restore it with `DeferCleanup`. Save identical type/source configurations in
  multiple pipelines and attach real scopes by saving versions. Include public,
  authorized-private, and unauthorized-private pipelines/resources so the
  response preserves authorization semantics. Compare unordered SQL results
  with `ConsistOf`.
- [ ] Produce top-level `ResourceFactory` SQL failures with a production factory
  over an already-closed secondary clone connection. For nested pipeline
  lookups, preload the real pipeline through a secondary connection, close it
  exactly once, and return it through a narrow embedded Team/TeamFactory
  decorator so access middleware succeeds before the intended method fails.
- [ ] Exercise shared-lookup missing-scope errors with real resources/types
  that naturally have no scope.
- [ ] Add one persisted response assertion before rewiring and capture RED,
  then GREEN. Sensitivity-check a wrong persisted resource/version/team value,
  observe failure, restore, and pass.
- [ ] Run the read/list/detail/shared focus and the entire Resources API focus
  serially, format, vet, diff-check, recount 27 to 12, obtain independent
  review, and commit as
  `test(api): persist resource read state`.

## Task 2: Persist unpin, pin-comment, and cache mutation endpoints

**Payoff:** remove 3 constructors; 12 to 9.

- [ ] Seed resource versions and call `Resource.PinVersion` before testing
  `SetPinComment`; a comment update without an existing `resource_pins` row is
  not a valid production state. Reload and assert the exact persisted pin and
  comment after the response.
- [ ] Use a normally pinned real resource for successful unpin and a real
  unpinned resource for the natural `NonOneRowAffectedError` failure.
- [ ] Exercise immutable-unpin conflict with real state: insert the minimal
  `agent_workflow_definitions` row and register the pipeline through
  `db.NewWorkflowResourceSourcePipelinesFactory(...).Activate(...)`. Create the
  resource version and call `PinVersion` before activating immutability because
  pinning is itself rejected afterward. Use the real pipeline config version,
  valid definition/config/source hashes, and a workflow source declaration
  that matches the activated pipeline/resource relationship.
- [ ] For pin-comment and clear-cache direct SQL failures, preload a real
  resource on a secondary connection, close it exactly once, and return it
  through a narrow pipeline decorator. Do not add a generated fake.
- [ ] Build a real cache deletion graph: scoped resource, `dbtest.BaseWorker`,
  one-off build, ResourceCache, created volume, and
  `CreatedVolume.InitializeResourceCache` worker association. Create both a
  matching-version and nonmatching-version cache/association; assert the API
  deletion count, removal of only the requested association, and survival of
  the decoy through fresh queries.
- [ ] Capture RED/GREEN and a wrong pin/comment/cache association sensitivity,
  then run mutation focuses plus the entire Resources API focus serially,
  format/vet/diff-check, independently review, and commit as
  `test(api): persist resource mutation state`.

## Task 3: Persist resource, resource-type, prototype, and webhook checks

**Payoff:** remove the final 9 constructors and broad root fake setup.

- [ ] Add one narrow CheckFactory decorator that embeds the real
  `db.CheckFactory`, records `TryCreateCheck` arguments, and normally delegates
  to it. It supports exactly two deliberate non-delegating fault/protocol
  results: `(nil, false, nil)`, a defensive handler branch that a manually
  triggered resource/type/prototype check cannot naturally reach because manual
  checks bypass duplicate suppression, and a configured error. Comment both
  exact boundaries.
- [ ] Persist real resources, resource types, and prototypes with their scopes
  and versions. Delegate successful manual checks to the real factory and
  assert the returned build is persisted, started, incomplete, manually
  triggered, dynamically identified, and carries the expected private plan.
- [ ] Use the recording decorator to assert exact `From`, manual, shallow versus
  recursive, and `toDB=true` forwarding without replacing the resulting real
  build assertion with interaction counts.
- [ ] Produce CheckFactory errors only through its narrow result override.
  Preserve access and credential collaborators as non-database seams.
- [ ] Persist the webhook token in a real ResourceConfig and exercise the
  literal-token request through the real resource/check graph. Secret
  interpolation remains covered by the existing credential seam.
- [ ] Remove `FakePipeline`, all remaining file-local DB fakes, the `dbfakes`
  import, and all uses of the four suite-level DB fakes.
- [ ] Capture RED/GREEN on one persisted check-plan/build assertion and
  sensitivity on the wrong manual/from/plan value; restore and pass.
- [ ] Run all check/webhook focuses serially and in parallel, format, vet,
  diff-check, verify the exact 9 to 0 final step, obtain independent review, and
  commit as `test(api): persist resource check state`.

## Task 4: Full verification and closure

- [ ] Run `gofmt -w atc/api/resources_test.go`,
  `go test ./atc/api -run '^$'`, every Resources API focus, full
  `ginkgo ./atc/api`, uncached `go test ./atc/api -count=1`, and full
  `ginkgo -p ./atc/api` if package-wide parallel runtime remains practical.
- [ ] At minimum run all Resources API specs with `ginkgo -p` across the normal
  nine processes to prove clone isolation.
- [ ] Run standard/live compile and vet, exact fake/import/suite-fake reference
  searches, `git diff --check`, and `git status --short`.
- [ ] Inspect one clone per spec, custom server/body cleanup, exact-once doomed
  connection closes, global-feature restoration, no hard-coded IDs/order,
  synchronous manual checks, and every decorator's narrow boundary.
- [ ] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding. Record exact evidence and commit plan closure as
  `docs: record api resources postgres conversion`.

## Phase acceptance

- [ ] `resources_test.go` reaches exactly 27 to 0 generated database-fake
  constructors, drops `dbfakes`, and no longer consumes the four suite DB
  fakes.
- [ ] Normal resource/type/prototype/build/pin/cache/shared/check state is
  persisted and asserted through real rows.
- [ ] Retained in-memory seams are limited to access/credentials and narrow
  decorators for exact SQL-failure delivery or impossible defensive results.
- [ ] Focused serial/parallel and full API verification pass against isolated
  clones in the one machine-wide PostgreSQL service.
- [ ] No production behavior or unrelated file changes, and nothing is pushed.
