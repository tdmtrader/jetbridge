# Real PostgreSQL Engine Task Delegate Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Use one implementation owner for `task_delegate_test.go`, preserve concurrent
> edits, and stage only the task's assigned files.

**Goal:** Remove every generated database fake from Engine TaskDelegate tests
by observing build events, image-cache associations, resource-config metadata,
resolver outcomes, and pinned behavior through isolated PostgreSQL clones.

**Exact baseline and target:** At `37bac0beaf`,
`atc/engine/task_delegate_test.go` has 16 generated database-fake constructors
across 40 specs: `FakeBuild` x1, `FakeWorkerFactory` x1, `FakeResourceCache` x9,
and one each of `FakeResourceConfigFactory`, `FakeResourceCacheFactory`,
`FakeResourceConfig`, `FakeResourceConfigScope`, and
`FakeResourceConfigVersion`. This phase targets 16 to 0 and removal of the
`dbfakes` and `lockfakes` imports from that file.

**Architecture:** The existing Engine clone fixture lives in external package
`engine_test`, while TaskDelegate must remain in internal package `engine` to
construct its private delegate fields. Move the fixture and the suite's sole
`postgresrunner.GinkgoRunner` registration into a new exported internal-package
`_test.go` helper. External tests import the test-compiled package and retain
lowercase type/function aliases, avoiding changes to their callers. Never
register a second runner or create more than one clone in a spec.

**Files:** Add one internal Engine test-helper file; modify
`atc/engine/engine_suite_test.go`, `atc/engine/task_delegate_test.go`, and this
plan only. Do not change production code, generated fakes, Docker/service
lifecycle, benchmarks, or corpus files. Do not push.

## Task 1: Bridge the existing Engine fixture across test packages

- [ ] Move `engineDBFixture`, `enginePostgresRunner`, `useEngineDB`,
  `closedEngineCloneConn`, `createEngineJobBuild`, and
  `consumeEngineBuildEvent` into a new `package engine` `_test.go` helper with
  exported names. Move—not duplicate—the one `postgresrunner.GinkgoRunner`
  registration.
- [ ] In external `engine_suite_test.go`, import the Engine package and retain
  lowercase type/function aliases so existing external tests compile without
  edits. Keep `TestEngine`, panic sink, and external-only helpers there.
- [ ] Preserve cleanup order: register clone drop before primary and singleton
  closes so Ginkgo LIFO closes all connections first. Preserve
  `db.CleanupBaseResourceTypesCache` per clone.
- [ ] Compile the full package and run one existing opt-in Engine focus serially
  and with `ginkgo -p`. Independently inspect test-package linking, single
  runner registration, and lifecycle.
- [ ] Commit only the bridge files as
  `test(engine): share isolated postgres fixture`.

## Task 2: Persist TaskDelegate events and plan-path image caches

- [ ] Opt the TaskDelegate Describe into `UseEngineDB` exactly once per spec.
  Persist a real team/pipeline/job build and pass that build plus the fixture's
  real worker and lock factories to `NewTaskDelegate`. Keep fake clock, policy,
  step/stepper, resolver, and runtime collaborators as non-database seams.
- [ ] For the ten lifecycle/sidecar specs, consume the exact persisted events
  at offsets 0/1 in the eight event-emitting cases and marshal the decoded
  event for the existing JSON assertions. Close each event writer/source. The
  SidecarWriter-non-nil and “no sidecars” cases emit no event; query inherited
  `build_events` count for the exact build rather than calling
  `EventSource.Next`, which may block when no event exists.
- [ ] For plan-path `FetchImage` specs, create real resource caches with
  `ResourceCacheFactory.FindOrCreateResourceCache(db.ForBuild(build.ID()), ...)`
  using each exact type/version/source/params. Return them through the fake
  step result, then query `build_image_resource_caches` to assert the exact
  build/cache association and persisted cache identity.
- [ ] Add one persisted event or association assertion before rewiring and
  capture RED, then GREEN. Sensitivity-check a wrong expected event type/JSON
  value or cache ID, observe failure, restore, and pass. Never request a
  nonexistent event offset: `EventSource.Next` can wait indefinitely while the
  build remains incomplete.
- [ ] Run the event/plan-path TaskDelegate focuses serially and in parallel,
  format, vet, diff-check, confirm the first three local cache constructors are
  removed, obtain independent review, and commit as
  `test(engine): persist task delegate events and caches`.

## Task 3: Persist metadata, resolver, fallback, and pinned behavior

- [ ] Replace cached/warm metadata fakes with exact source-specific real
  ResourceConfig + scope rows and `SaveVersions`; every spec seeds the source
  it actually fetches because the old factory fake ignored source.
- [ ] For empty/fallback paths, persist a config/scope without versions plus a
  healthy real fallback cache. In the two-call transition spec explicitly call
  `SaveVersions` between calls; the fake check step's in-memory result does not
  populate PostgreSQL.
- [ ] For on-demand resolution, begin with an empty real scope, keep the
  external registry resolver fake, then reopen the scope/`LatestVersion` and
  prove the returned version was persisted. Resolver failure keeps that
  non-database seam and asserts the real scope remains empty.
- [ ] Produce the direct metadata-lookup SQL fault with
  `db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)`
  while retaining a healthy real build/cache for fallback and keeping
  `imageResolver == nil`; with a resolver present, production correctly makes
  this registry-metadata error fatal. Close the doomed connection exactly once
  through the existing helper.
- [ ] For the four authoritative non-empty-digest pin specs, use a real cache
  factory/build association and the closed-connection ResourceConfigFactory as
  a sentinel proving direct config/scope lookup is bypassed, while the fake
  resolver proves its own bypass/call boundary. The three controls in the same
  pinned Describe—unpinned cached, unpinned on-demand, and non-nil-but-empty
  pin—must instead use a healthy real ResourceConfigFactory and scope so they
  exercise `LatestVersion` or the resolver normally. Assert no new
  scope/version or an unchanged stale latest value and query the actual
  association/cache version; do not assert zero resource configs, because
  cache creation itself creates one.
- [ ] In no-factories mode, leave only the resource factories nil but retain a
  real build and real fallback cache.
- [ ] Remove all remaining generated database fakes/imports. Capture a
  persisted latest-version or association RED/GREEN and a wrong-version/ID
  sensitivity failure, then restore.
- [ ] Run the DelegateFactory TaskDelegate focuses serially and in parallel,
  format, vet, diff-check, recount 16 to 0, inspect each retained non-DB seam,
  obtain independent review, and commit as
  `test(engine): persist task delegate metadata state`.

## Task 4: Full verification and closure

- [ ] Run `gofmt -w` on modified Go files,
  `go test ./atc/engine -run '^$'`, every TaskDelegate-focused command, full
  `ginkgo ./atc/engine`, and uncached `go test ./atc/engine -count=1`.
- [ ] Run focused TaskDelegate and full Engine tests with `ginkgo -p` using at
  least two processes; no Serial label or package mutex may be introduced.
- [ ] Run `go vet ./atc/engine`, live-tag compile/vet if applicable,
  `git diff --check`, `git status --short`, and exact fake/import counts.
- [ ] Inspect one clone per spec, no nested `UseEngineDB`, no cross-clone
  objects, connection-before-drop ordering, event source/writer closure,
  default interval restoration, and persisted outcome rereads.
- [ ] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding. Record exact evidence and commit plan closure as
  `docs: record engine task delegate postgres conversion`.

## Phase acceptance

- [ ] TaskDelegate reaches exactly 16 to 0 generated database-fake constructors
  and drops both generated database and lock fake imports.
- [ ] Events, caches, associations, metadata, versions, fallbacks, and pinned
  state are asserted through real persisted rows.
- [ ] Only clock, step/runtime, policy, and registry resolver seams remain in
  memory, because they are not database persistence substitutes.
- [ ] The existing external Engine tests remain source-compatible through the
  test-only fixture aliases; exactly one suite runner/template is registered.
- [ ] Serial and parallel full Engine suites pass against isolated clones in
  the one machine-wide PostgreSQL service.
- [ ] No production behavior or unrelated file changes, and nothing is pushed.
