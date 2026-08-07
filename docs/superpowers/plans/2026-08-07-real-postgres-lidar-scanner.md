# Real PostgreSQL Lidar Scanner Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Implement one task at a time, preserve concurrent edits, and stage only the
> files owned by the task.

**Goal:** Replace every generated database fake in the Lidar scanner tests
with persisted resources, resource types, configurations, scopes, versions,
checks, builds, teams, and pipelines from a unique PostgreSQL template clone.

**Exact baseline and target:** At `c033d05492`,
`atc/lidar/scanner_test.go` has 23 explicit generated database-fake
constructors: `FakeCheckFactory` x3, `FakeResourceConfigFactory` x2,
`FakeResource` x7, `FakeResourceType` x4, `FakeResourceConfig` x3,
`FakeResourceConfigScope` x3, and `FakeBuild` x1. This phase targets 23 to 0,
removes the `dbfakes` import, and leaves the image resolver as the deliberate
non-database collaborator under observation.

**Architecture:** Register one package-local `postgresrunner.GinkgoRunner` and
provide an opt-in `useLidarDB` fixture. Each converted spec creates exactly one
database clone from the shared migrated template and closes the primary and all
advisory-lock connections before dropping it. A small recording/delegating
CheckFactory may override only scan enumeration, a configured error/panic, or
an impossible defensive `created=false` result; ordinary check creation must
delegate to the production factory. Likewise, narrow wrappers around a real
resource, resource type, resource config, or scope may inject the exact
post-success FK/non-FK failure under test but may never manufacture successful
state.

**Files:** Modify only `atc/lidar/lidar_suite_test.go`,
`atc/lidar/scanner_test.go`, and this plan. Do not modify production code,
generated fakes, Docker/service lifecycle, benchmarks, or corpus files. Do not
push.

## Task 1: Add and prove the opt-in Lidar database fixture

- [x] Register `postgresrunner.GinkgoRunner` exactly once in
  `lidar_suite_test.go`; do not add a second `BeforeSuite`.
- [x] Define a fixture containing `DbConn`, production lock factory,
  `dbtest.Builder`, Team/Pipeline/Check/ResourceConfig/Build factories, and a
  buffered check-build channel large enough for the concurrency case.
- [x] Register clone drop before ordinary and singleton connection cleanup so
  Ginkgo LIFO order closes every connection first. Call
  `db.CleanupBaseResourceTypesCache()` after opening each clone.
- [x] Add helpers to persist a team/pipeline config, update a pipeline at its
  real config version, attach a resource config scope, read versions/check end
  time through fresh real objects, and drain in-memory check builds.
- [x] Add small embedding decorators only for enumeration recording and the
  exact post-success failure boundaries used in later tasks. Delegation is the
  default and no decorator may fabricate a successful resource/config/scope.
  Any CheckFactory call recorder must protect its observations with a mutex:
  scanner workers call `TryCreateCheck` concurrently.
- [x] Capture baseline coverage, prove a small opt-in persistence spec serially
  and across 9 processes, compile, format, diff-check, obtain independent
  lifecycle review, and commit as `test(lidar): add isolated postgres fixture`.

## Task 2: Persist ordinary resource scan and check scheduling

**Payoff:** remove the first Scanner Describe's 5 constructors; 23 to 18.

- [x] Persist resources through pipeline configs whose jobs actually consume
  them so production `CheckFactory.Resources` selects them. Persist the custom
  parent resource type in the same pipeline and use the real pipeline ID map.
- [x] Exercise fetch failures with a recording CheckFactory that delegates all
  ordinary calls and overrides only `Resources` or `ResourceTypesByPipeline`
  with the configured error. Exercise panic recovery with its one explicit
  `TryCreateCheck` panic mode.
- [x] For base-type and custom-type checks, delegate `TryCreateCheck` to the
  real factory, drain the buffered in-memory check build, and assert its
  checkable identity/private plan rather than only a call count.
- [x] Seed a real pinned version with `dbtest.Builder`, reload the resource,
  and use the recording decorator to assert exact pinned `from`, resource
  types, manual/recursive flags, and `toDB=false` forwarding.
- [x] Persist `check_every: never` and a put-only resource in configuration and
  assert they naturally produce no check build. A fresh put-only row has a NULL
  `last_check_build_id` and is intentionally selected by `CheckFactory.Resources`;
  first attach a real scope and complete a successful check (for example with
  `dbtest.Builder.WithResourceVersions`) before asserting steady-state
  exclusion. Do not synthesize either state in a wrapper.
- [x] Generate 20 uniquely named resources and consuming get steps in one real
  pipeline, attach distinct nonzero scopes, run with max concurrency 5, and
  assert 20 distinct in-memory check builds are emitted without assuming
  goroutine or SQL order. Bypass a recorder or use the mutex-safe recorder from
  Task 1; never append observations from scanner workers without synchronization.
- [x] Capture RED/GREEN on a persisted check-plan/build assertion and
  sensitivity on an incorrect resource ID/pin; restore and pass.
- [x] Run this Describe serially and across 9 processes, format/vet/diff-check,
  verify the exact 5-site reduction, obtain independent review, and commit as
  `test(lidar): persist scanner resource checks`.

## Task 3: Persist native resource-type resolution

**Payoff:** remove the resource-type resolution Describe's 6 constructors; 18
to 12.

- [x] Persist a registry-image-backed custom resource type in a real pipeline.
  Run the real ResourceConfigFactory path, then reload the type and assert its
  attached scope, saved digest version, and updated last-check end time.
- [x] Continue observing repository, tag, and optional basic-auth values only
  through the non-database resolver fake. Assert database outcomes separately.
- [x] Represent direct image, `check_every: never`, missing repository, and two
  pipelines/types as actual configs. For interval suppression, configure a
  nonzero `CheckEvery{Interval: time.Hour}` (or save/set/restore the global
  default with `DeferCleanup`), attach a real scope, update its end time, reload
  the type, and assert the resolver is not called. An updated end time with a
  zero interval does not suppress resolution.
- [x] Keep resolver failure as the existing non-database seam and assert no
  digest version was persisted.
- [x] Inject only the two GC race boundaries with real-backed wrappers:
  `ResourceType.SetResourceConfigScope` returning the existing shaped FK error,
  and `ResourceConfigScope.SaveVersions` returning FK or configured non-FK
  error after real config/scope lookup. Preserve the debug-versus-error log
  assertions and prove that post-save end-time update did not occur.
- [x] Capture RED/GREEN and sensitivity on the wrong persisted digest/scope,
  then run the Describe serially and in parallel, format/vet/diff-check,
  verify the exact 6-site reduction, obtain independent review, and commit as
  `test(lidar): persist resource type resolution`.

## Task 4: Persist native resource resolution and metrics

**Payoff:** remove the final 12 constructors; 12 to 0.

- [x] Persist registry-image and ordinary resources as in-use pipeline inputs.
  Resolve registry-image resources through the real ResourceConfigFactory,
  reload them, and assert attached scope, saved digest version, and check end
  time. Assert ordinary resources emit real in-memory check builds instead.
- [x] Persist mixed resource configurations and assert one native digest plus
  one ordinary check outcome without relying on execution order.
- [x] Represent credentials, `check_every: never`, interval suppression, and
  missing repository as actual persisted config/scope state. Keep the resolver
  failure as a non-database seam and assert no version was saved.
- [x] Inject only `Resource.SetResourceConfigScope` FK failure and
  `ResourceConfigScope.SaveVersions` FK failure through real-backed wrappers;
  preserve exact logging and skipped-follow-up assertions.
- [x] For `ChecksEnqueued`, prove the created case by delegating a real check.
  Prove the already-in-flight case with the exact same production CheckFactory
  instance: attach and reload a resource with a nonzero scope ID, pre-create a
  `toDB=false` check, receive it from the buffered channel without finishing
  it, run the scanner, and assert duplicate suppression. Finish the first build
  before database cleanup. Use a non-delegating `created=false` decorator only
  if this production path cannot represent the exact defensive branch, and
  comment why.
- [x] Remove every generated DB fake, delete `dbfakes`, and assert persisted
  outcomes instead of database interaction counts.
- [x] Capture RED/GREEN and sensitivity on an incorrect digest/metric delta,
  then run the Describe serially and across 9 processes, format/vet/diff-check,
  verify the exact 12-site reduction, obtain independent review, and commit as
  `test(lidar): persist native resource resolution`.

## Task 5: Full verification and closure

- [x] Run `gofmt` on both modified Go files, `go test ./atc/lidar -run '^$'`,
  focused Describes, full `ginkgo ./atc/lidar`, uncached
  `go test ./atc/lidar -count=1`, and full `ginkgo -p ./atc/lidar` across the
  normal 9 processes.
- [x] Run standard/live compile and vet when live-tagged files exist,
  `git diff --check`, `git status --short`, and exact constructor/import
  searches.
- [x] Inspect one clone per opted-in spec, cleanup ordering, buffered channel
  draining, global interval restoration, order-independent concurrency
  assertions, and every decorator's narrow failure-only boundary.
- [x] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding. Record exact evidence and commit plan closure as
  `docs: record lidar scanner postgres conversion`.

## Phase acceptance

- [x] `scanner_test.go` reaches exactly 23 to 0 generated database-fake
  constructors and drops its `dbfakes` import.
- [x] Normal scanner enumeration, check creation, native resolution, scopes,
  versions, intervals, pins, and metrics are persisted and reread.
- [x] Retained in-memory seams are limited to the image resolver and narrow
  real-backed decorators for exact enumeration, panic, or GC-race failures.
- [x] Serial and 9-process full-package suites pass against isolated clones in
  the one machine-wide PostgreSQL service.
- [x] No production behavior or unrelated file changes, and nothing is pushed.

## Observed completion evidence

- Implementation commits: `493ff21df2` (fixture plus ordinary resource
  checks), `2dc90c5b0f` (resource-type resolution), and `1196e37ea1` (native
  resource resolution and metrics). The first commit intentionally
  consolidated Tasks 1 and 2 after their shared review gate.
- The exact generated-database-fake census moved from 23 constructors and one
  `dbfakes` import to zero constructors and zero imports. No production,
  generated, benchmark, corpus, Docker, or service-lifecycle file changed.
- Persisted wrong-ID/digest expectations and the final wrong metric delta all
  failed at the intended assertion, were restored, and returned to GREEN.
- Final verification against the shared PostgreSQL service passed compile-only
  `go test`, uncached full `go test`, all 35 specs serially, and all 35 specs
  across exactly nine Ginkgo processes. `go vet`, `gofmt`, `git diff --check`,
  constructor/import searches, connection cleanup, and build draining also
  passed. Lidar has no live-tagged files requiring a second compile/vet lane.
- Independent Task 3 review's sole minor source-to-config coverage finding was
  fixed and reverified. Independent Task 4 and final whole-phase reviews both
  reported PASS with no unresolved Critical, Important, or Minor findings.
