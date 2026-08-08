# Final Non-API PostgreSQL Success-State Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. The two code tasks own disjoint files and may
> run concurrently, but shared-worktree staging and commits are serialized.

**Goal:** Remove the final six ordinary non-API generated database fakes found
by the repository-wide survivor audit: all three from `CheckFactory` resource
checks and three of four from the Engine typed-step wiring harness. Preserve
only the Engine workflow-run-factory identity sentinel and the independently
reviewed algorithmic, runtime, policy, adapter, and selective-fault survivors.

**Architecture:** `atc/db` reuses its existing clone-per-spec suite graph and
builds every resource check from real pipeline/resource/type/scope rows. A
single embedded-resource decorator injects only the otherwise unreachable
in-memory-build error. Engine moves the two database-dependent standard tests
into its existing Ginkgo suite, calls the existing `UseEngineDB` once per spec,
and selects a persisted general worker through a delegating observation wrapper
over the real `WorkerFactory`; its runtime worker/containers remain deliberate
runtime adapters.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL template clones, Concourse
`atc/db`, `atc/engine`, `dbtest`, production factories and build events.

## Fixed scope and census

- Use only the machine-wide PostgreSQL service at `127.0.0.1:15432`. Do not
  start, stop, recreate, or alter PostgreSQL, Docker, Colima, or theborg.
- Task 1 may modify only `atc/db/check_factory_test.go`; Task 2 may modify only
  `atc/engine/step_factory_test.go`; closure may modify only this plan. Do not
  touch production code, generated fakes, benchmarks, corpus, or concurrent API
  work. Do not push.
- Every database-backed Ginkgo spec owns one unique clone. Reuse the existing
  package runners and cleanup; do not add another runner or `TestMain`.
- Preserve runtime, snapshot, credential, sequence, policy, and exact
  dependency-identity seams. A fake count is not itself a reason to turn a
  non-database port into PostgreSQL.
- Capture RED, GREEN, and sensitivity evidence for each task. Derive all row
  IDs, build IDs, scope IDs, event identities, and worker ownership from real
  objects.

At `e039d52a8f`, scanning only `*_test.go` files and excluding `atc/api` and
benchmark/corpus paths, there are 75 lexical `new(dbfakes.Fake...)` sites. The
reviewed survivor ledger classifies exactly six as ordinary PostgreSQL
candidates:

| File | Baseline | Final | Generated survivors |
|---|---:|---:|---|
| `atc/db/check_factory_test.go` | 3 | 0 | none |
| `atc/engine/step_factory_test.go` | 4 | 1 | `FakeAgentWorkflowRunsFactory` identity sentinel |
| **Phase total** | **7** | **1** | **1 justified sentinel** |

The non-API test-file lexical census must therefore move 75 to 69. The four
composite-literal seams and four non-test runtime/validation utilities are
outside this ledger and remain justified.

## Task 1: Persist `CheckFactory` resource checks — 3 to 0

- [x] Verify `pg_isready -h 127.0.0.1 -p 15432 -U postgres` reports accepting
  connections before runtime tests. Record the exact baseline: 24
  `CheckFactory TryCreateCheck` specs pass, and the file has one
  `FakeResource`, one `FakeResourceType`, and one `FakeBuild` constructor plus
  one `dbfakes` import. If readiness fails, report it without altering service
  lifecycle and do not claim a runtime result.
- [x] Capture a persisted RED in the base resource database-success path:
  assert that the returned check reloads as an unfinished row owned by the
  persisted resource while the call still uses the fake resource/build. The
  assertion must fail because no row was written.
- [x] Add a helper that saves a dedicated pipeline on `defaultTeam` from a
  resource config and optional parent resource types, then reloads the real
  resource and `ResourceTypes` from that same pipeline. Base fixtures use
  `some-name`, `base-type`, tags `tag-a/tag-b`, and source `{some: source}`.
  The parent fixture defines `custom-type` over `some-base-type`, tags
  `some-tag`, source `{some: type-source}`, and defaults `{sdk: sdk}`.
- [x] Add `attachCheckScope`, using the real resource config factory,
  `FindOrCreateScope(&resourceID)`, `SetResourceConfigScope`, and a reload.
  Add a recent-completed-check helper that creates and finishes a real seed
  check, calls the real scope lifecycle's `UpdateLastCheckStartTime` with the
  seed ID/public plan and `UpdateLastCheckEndTime(true)`, and only then uses
  parameterized SQL/PostgreSQL time to backdate `LastCheckEndTime`
  deterministically. It must reload and return the resource and assert the
  reloaded timestamp, because `Resource.LastCheckEndTime()` is cached on the
  scanned object.
- [x] Retain exactly one non-generated fault seam:

  ```go
  type inMemoryBuildErrorResource struct {
      db.Resource
      err error
  }
  ```

  It overrides only `CreateInMemoryBuild` to return its sentinel and delegates
  every successful database method to the embedded real resource. No broad
  call recorder or fabricated successful entity may replace the three fakes.
- [x] Convert the two database-success specs to require a reloaded started,
  incomplete, non-manual check with the dynamic resource/pipeline/team links,
  one unfinished row, nonempty plan ID, and the full real `CheckPlan` fields.
- [x] Replace both fake `created=false` setups with one pre-existing real
  unfinished check. Preserve two assertions: `(nil, false, nil)` and exactly
  one surviving unfinished row.
- [x] Convert the three ordinary in-memory specs to the real resource. Require
  an in-memory identity/run-state derived from the suite sequence generator,
  the returned real private plan, the exact returned build on
  `checkBuildChan`, and no second channel item. Use the narrow decorator only
  for the one error spec and prove nil/false, exact error, empty channel, and
  zero persisted check rows.
- [x] For the five in-flight specs, attach a real nonzero scope and issue the
  first in-memory check. Automatic duplication stays nil/false with only the
  first channel item; manual duplication creates a distinct second run-state;
  finishing the first wrapper as succeeded or errored clears the production
  in-flight tracker and permits a new check. Assert channel cardinality and
  run-state identities rather than fake call counts.
- [x] For the two interval specs, use the recently completed real check.
  Automatic creation stays suppressed with no unfinished row/channel item;
  manual creation persists one started manual check whose plan has the default
  interval and `SkipInterval=true`.
- [x] Persist webhook-token, explicit `CheckEvery{Interval: 42*time.Second}`,
  and `CheckEvery{Never: true}` variants. Assert the production plan intervals,
  from-version, source/tags/defaults, and suppression outcomes.
- [x] Persist the parent custom resource type and assert the complete outer and
  nested type-image plan, including merged defaults, dynamic plan IDs, tags,
  source, interval, identity, and a reloaded started check.
- [x] Leave the already-real two resource-type specs unchanged. Remove all
  three generated constructors and `dbfakes` import.
- [x] Sensitivity: temporarily expect one persisted plan interval/source to be
  wrong and one unfinished-row count to be wrong. Require focused failures,
  restore, and rerun GREEN.
- [x] Run formatting, compile, focused 24/24 serial and exactly nine-process
  tests, full package verification, vet, diff check, and zero searches:

  ```bash
  gofmt -w atc/db/check_factory_test.go
  go test ./atc/db -run '^$'
  ginkgo --procs=1 --focus='CheckFactory TryCreateCheck' ./atc/db
  ginkgo --procs=9 --focus='CheckFactory TryCreateCheck' ./atc/db
  go test ./atc/db -count=1
  go vet ./atc/db
  git diff --check -- atc/db/check_factory_test.go
  ! rg -n 'dbfakes|Fake(Resource|ResourceType|Build)' atc/db/check_factory_test.go
  ```

- [x] Obtain independent review with no unresolved Critical, Important, or
  Minor finding, then commit only `atc/db/check_factory_test.go` as
  `test(db): persist resource check creation`.

## Task 2: Persist Engine typed-step wiring — 4 to 1

- [x] Verify the same PostgreSQL readiness preflight, record the standard-test
  baseline for the two harness tests, and record the exact four constructors.
  Retain only the `FakeAgentWorkflowRunsFactory` used
  by `TestWithSnapshotLoaderKeepsExactCommandScopedDependencies`; it proves
  exact option identity and exercises no persistence behavior.
- [x] Register two internal-package Ginkgo specs under
  `Describe("CoreStepFactory typed output wiring")`, replacing only the two
  harness-dependent standard tests. Invoke the intended real harness before it
  exists and record the compile RED. Leave the other eleven standard tests
  unchanged.
- [x] Convert `newFactoryWiringHarness` into a Ginkgo helper that calls
  `UseEngineDB` exactly once. Create a real team/pipeline/job/build with
  `CreateEngineJobBuild`, start and reload it, and derive all step metadata,
  container metadata, owners, names, and IDs from that graph.
- [x] Persist one general running Linux worker through the fixture's real
  `WorkerFactory`. Make `factoryStaticWorkerFactory` a recording adapter from
  the selected database worker to the preconfigured `runtimetest.Worker`: it
  captures every incoming `db.Worker` before returning the runtime stub. The
  runtime worker, containers, processes, and volumes remain non-database seams.
- [x] Replace the fake worker factory with a mutex-protected observer that
  embeds the real `db.WorkerFactory`, counts and delegates
  `FindWorkersForContainerByOwner` and `Workers`, and overrides nothing else.
  Persist no DB container: the intended production path is owner miss followed
  by fallback to the sole compatible real worker.
- [x] Construct the core factory/delegates with the real build and available
  fixture lock/build/team/resource factories instead of the fake build or nil
  database dependencies. Remove `FakeWorker`, `FakeWorkerFactory`, `FakeBuild`,
  and the now-unused `tracing` import.
- [x] In the enabled spec, preserve task then agent success, two ordered sealer
  calls, and two authorized snapshot metadata loads. Also reload the real build
  as started/incomplete, prove the selected general worker row is real/running,
  and require the adapter to receive that same persisted worker twice, with
  matching dynamic name, running state, Linux platform, and general-team
  identity. Inspect the real build-event stream or rows to prove both task and
  agent selected that worker with their dynamic plan origins. Prefer typed
  event/content assertions over brittle literal event IDs.
- [x] In the disabled spec, preserve both exact `no output sealer` errors and
  require both worker-observer counters to remain zero. Reload the same real
  build as started/incomplete so this path cannot silently fall back to a fake.
- [x] Sensitivity: temporarily expect a wrong selected-worker origin in the
  enabled spec and one owner lookup in the disabled spec. Require each focused
  spec to fail, restore, and rerun GREEN.
- [x] Run formatting, the two-spec focus serially/across exactly nine
  processes, the complete Engine suite serially/across nine processes, the
  remaining standard tests, vet, diff, and the exact one-survivor search:

  ```bash
  gofmt -w atc/engine/step_factory_test.go
  go test ./atc/engine -run '^$'
  ginkgo --procs=1 --focus='CoreStepFactory typed output wiring' ./atc/engine
  ginkgo --procs=9 --focus='CoreStepFactory typed output wiring' ./atc/engine
  go test ./atc/engine -count=1
  ginkgo --procs=1 ./atc/engine
  ginkgo --procs=9 ./atc/engine
  go vet ./atc/engine
  git diff --check -- atc/engine/step_factory_test.go
  test "$(rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/engine/step_factory_test.go | wc -l | tr -d ' ')" = 1
  ! rg -n 'Fake(Worker|WorkerFactory|Build)|concourse/tracing' atc/engine/step_factory_test.go
  ```

- [x] Require the focus at 2/2, the Engine Ginkgo suite at 229/229 serially and
  across exactly nine processes, all eleven remaining standard tests, exact
  4-to-1 file census, and independent review with no unresolved finding. Commit
  only `atc/engine/step_factory_test.go` as
  `test(engine): persist step factory wiring`.

## Final acceptance and closure

- [x] Both code commits are green independently and together. This exact
  non-API test-file census is 75 to 69; no generated fake moved to another
  file:

  ```bash
  rg -o 'new\(dbfakes\.Fake[^)]*\)' --glob '*_test.go' \
    --glob '!atc/api/**' --glob '!**/benchmark/**' \
    --glob '!**/benchmarks/**' --glob '!**/corpus/**' | wc -l
  ```
- [x] `CheckFactory` successful resource state and Engine worker/build state are
  produced and observed through PostgreSQL; only the documented in-memory
  error decorator, worker observer, runtime adapter, and workflow-store identity
  sentinel remain.
- [x] The reviewed 69 lexical survivors remain classified as algorithmic,
  runtime/ordering, policy, adapter, exact-call contract, or selective fault
  seams. No additional ordinary non-API success-state constructor remains.
- [x] Record exact commits, RED/GREEN/sensitivity results, spec counts, full
  gates, census, and reviewer results below. Commit only this plan as
  `docs: record final core postgres conversion`. Do not push.

## Observed completion evidence

Completed 2026-08-07 against the shared native PostgreSQL 14 service at
`127.0.0.1:15432`; readiness reported accepting connections. No service,
Docker, Colima, or theborg lifecycle was changed.

### Task 1 — CheckFactory

- Commit: `2f609e7c26 test(db): persist resource check creation`.
- Census: three constructors/import (`FakeResource`, `FakeResourceType`, and
  `FakeBuild`) to zero; no generated DB fake remains in the file.
- Persisted RED: the fake success path failed the new unfinished-row lookup
  with `sql: no rows in result set` before the real resource was wired.
- Sensitivity: a wrong persisted source failed full-plan equality; a wrong
  unfinished-row count failed `1` versus `2`. Both mutations were restored.
- Final focus: 24/24 serial and 24/24 across exactly nine processes. Compile,
  vet, diff, zero-search, and independent review passed. Review's sole Minor
  fixture-self-assertion was removed before the fresh focus runs.
- The listed `go test ./atc/db -count=1` command timed out at Go's default
  10-minute limit, with no assertion failure reported before timeout. An
  otherwise identical rerun with `-timeout=30m` passed the full DB package in
  896.966 seconds.

### Task 2 — Engine typed-step wiring

- Commit: `f90c248ac7 test(engine): persist step factory wiring`.
- Census: four constructors to exactly one retained
  `FakeAgentWorkflowRunsFactory`; `FakeWorker`, `FakeWorkerFactory`,
  `FakeBuild`, and `tracing` are absent.
- Compile RED: the new Ginkgo harness was referenced before its real DB helper
  existed. Sensitivity mutations for the wrong selected-worker origin and a
  disabled-path owner lookup each failed, then were restored.
- Independent review found and corrected a coincidentally equal BuildID/TeamID
  authorization assertion, the non-production hard-coded attempt, and excess
  `CreatedBy` exposure. A persisted decoy now makes TeamID differ from BuildID;
  exact snapshot IDs, production attempt `0`, and `SnapshotCreatedBy` are
  asserted. Re-review passed with no remaining finding.
- Final focus after corrections: 2/2 serial and 2/2 across nine processes.
  The complete package passed `go test ./atc/engine -count=1 -timeout=10m` in
  98.074 seconds; the final complete nine-process Ginkgo run passed 229/229 in
  46.966 seconds (55.492 seconds suite time). Eleven remaining standard tests,
  vet, diff, and survivor searches passed.

### Final census and review

- Exact non-API `*_test.go` lexical census, excluding API and
  benchmark/corpus paths: 75 to 69. The six ordinary success-state
  constructors were removed rather than moved.
- PostgreSQL now produces and verifies the complete successful resource-check
  graph and Engine worker/build graph. The only new non-generated seams are the
  single-method in-memory error decorator, the delegating worker observer, and
  the runtime worker adapter.
- Independent survivor audit remains unchanged: the 69 generated constructors
  are algorithmic, runtime/ordering, policy, adapter, exact-call, or selective
  fault seams; no additional ordinary non-API success-state constructor was
  found.
