# Builds API PostgreSQL Conversion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. Complete the five endpoint batches as
> separately green commits; the parent serializes staging and commits.

**Goal:** Remove all seven generated database constructors, the `dbfakes`
import, and all reliance on the four shared suite database fakes from
`atc/api/builds_test.go`, while preserving its exact 95 spec names and using a
unique template-cloned PostgreSQL database per spec.

**Architecture:** Convert endpoints incrementally with endpoint-local
`useRealDB()` fixtures and late-bound servers so unconverted endpoints remain
on the package fake server until their commit. Persist real teams, pipelines,
jobs, one-off/job/check builds, visibility, plans, resources,
preparation state, pagination, and abort state. Event-route build lookup,
authorization, and delegation use real state while the existing event-handler
seam remains responsible for streaming. Retain only narrow embedded
decorators for selective post-lookup database failures and the existing
non-database access/event-handler seams.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL template clones, Concourse
`atc/db`, `dbtest`, API presentation/authorization handlers.

## Fixed scope and census

- Modify only `atc/api/builds_test.go` in code commits and this plan in
  planning/closure commits. Do not touch production code, generated fakes,
  suite helpers, Docker, Colima, or PostgreSQL lifecycle. Do not push.
- Every converted spec calls `useRealDB()` exactly once and owns a clone on
  `127.0.0.1:15432`. Keep deps/server/request local to each endpoint until all
  eight groups are converted; do not hoist real DB setup while any endpoint
  still relies on the suite fake graph.
- Preserve all `Describe`, `Context`, and `It` text and capture sorted dry-run
  names before/after. Exact total remains 95:

  | Endpoint | Specs |
  |---|---:|
  | POST builds | 8 |
  | GET builds list | 17 |
  | GET build | 11 |
  | GET build resources | 11 |
  | GET build events | 12 |
  | PUT abort | 6 |
  | GET preparation | 15 |
  | GET plan | 15 |

- Exact local constructors: one `FakeBuild`, three `FakeBuildForAPI`, and
  three `FakeJob`, 7→0; `dbfakes` import 1→0. Remove every reference in this
  file to shared `dbTeam`, `dbBuildFactory`, `build`, and `fakePipeline`; do not
  move a generated fake elsewhere.
- Retain `fakeAccess` and `constructedEventHandler`. Access identity is not DB
  state; the event handler is the intentional route/delegation seam while SSE
  streaming remains covered by its dedicated tests.

## Shared fixture and decorator design

- [x] Each endpoint's `BeforeEach` calls `database = useRealDB()` and copies
  `deps = database.Deps`. Nested contexts finalize persisted fixtures and
  narrow error fields. Its `JustBeforeEach` assigns `database.Deps = deps`,
  starts `server = database.Serve()`, creates the request from that final URL
  and dynamic IDs/query/body, sends it, and registers response cleanup. No
  request may point at the package fake server after conversion.
- [x] Add mutex-protected embedded decorators that delegate every healthy
  operation:
  - `buildsAPIBuildFactory` over `db.BuildFactory`, overriding only
    `BuildForAPI`, `VisibleBuilds`, and `AllBuilds` for exact error/page
    observation.
  - `buildsAPITeamFactory`/`buildsAPITeam` over real factory/team, overriding
    only `CreateStartedBuild` for its selective POST error.
  - `buildsAPIBuild` over `db.BuildForAPI`, overriding only `Pipeline`,
    `Resources`, `Preparation`, or `MarkAsAborted` when the matching existing
    failure context requires it.
  - `buildsAPIPipeline` over a real pipeline, overriding only `Job(string)` for
    deliberate job lookup error/not-found after a valid build/pipeline lookup.
- [x] Decorators return fresh wrappers over the actual delegated object,
  share only mutex-protected recorder/error state, and defensively copy page or
  argument values. Real found/not-found, visibility, privacy, ID, timestamps,
  plans, resources, preparation, and abort outcomes must remain real;
  event-route build lookup, authorization, and delegation must use that real
  graph.
- [x] Derive every ID, time, team/pipeline/job name, created-by value, plan
  origin, pagination bound, and response field from persisted objects. Avoid
  cross-domain ID coincidences by persisting decoys where an assertion could
  otherwise pass with two unrelated sequence values both equal to one.

## Task 1: POST build — local constructors 7 to 6

- [x] Persist real `some-team`. The normal handler calls
  `Team.CreateStartedBuild(plan)`; decode its dynamic response and retrieve the
  row through the real build factory. Require team identity, started/running
  status, nonzero start time, schema, exact private plan, and public plan.
- [x] Unauthorized/forbidden specs compare real team build rows before/after
  and require no insertion. Do not replace fake call counts with another
  recorder-only assertion.
- [x] Use only the decorated team's `CreateStartedBuild` error for the exact
  500 path. Remove the local `FakeBuild`; do not preserve literal ID 42 or fake
  end/reap timestamps for a newly started real build.
- [ ] Persisted RED: expect the decoded response ID to resolve in the real
  factory while the request still hits the fake server; require failure, then
  bind the real endpoint and pass. Sensitivity: expect `persisted.ID()+1`,
  require failure, restore. No final-closeout RED evidence was recorded and the
  mutation was not run; left open.
- [ ] Run exact 8-spec serial+9 focus, compile/vet/diff/name/census and
  independent review. Commit only the file as:
  `test(api): persist one-off build creation`. Final validation ran the full
  95-spec Builds focus instead, and the implementation landed in one combined
  commit; the remaining prescribed gates keep this item open.

## Task 2: Global build list — local constructors 6 to 3

- [x] Persist private/public pipelines with real `Hide`/`Expose`, job builds
  through `Job.CreateBuild` plus `Start`/`Finish`, and a real resource check
  through `Resource.CreateBuild` or the production `dbtest.Builder` resource
  path. Remove the three `FakeBuildForAPI` values.
- [x] Decode real list responses and assert dynamic identities/fields.
  `VisibleBuilds` does not filter on `JobConfig.Public`: prove unauthenticated
  visibility with empty token team names plus a public pipeline only; prove an
  authenticated token sees its same-team private pipeline; and prove admin can
  see a cross-team private pipeline. Do not use job privacy as list evidence.
- [x] Use real ID-descending order. For pagination, persist at least four builds
  `b1 < b2 < b3 < b4`; request `from=b2&limit=2`, require `b3,b2`, and derive
  previous/next link bounds from `b4`/`b1`. Assert full RFC5988 URLs against the
  handler's configured fixed external URL (`https://example.com` in this API
  suite), not the random `httptest` server URL.
- [x] For timestamp mode, set deliberately separated UTC `start_time` values by
  clone-scoped SQL, request inclusive epoch-second from/to bounds, assert the
  exact subset and no pagination links.
- [x] Use only the build-factory list error fields for the two 500 contexts.
  Record pages only where the existing specs assert parsing/defaults.
- [ ] Persisted RED: seed real rows and expect their dynamic IDs/order while the
  request still uses fake list returns. Sensitivities: reverse expected IDs and
  shift a timestamp bound; require failure, restore. No final-closeout RED
  evidence was recorded and the mutation checks were not run; left open.
- [ ] Run cumulative 25-spec serial+9 focus, exact census 3 remaining, review,
  and commit only the file as: `test(api): persist global build listing`.
  Final validation ran the full 95-spec focus and the implementation landed in
  one combined commit, so this composite checkpoint remains open.

## Task 3: Build detail and resources — local constructors remain 3

- [x] GET detail uses a persisted started/finished real job build with dynamic
  ID/timestamps. One-off unauthorized uses `Team.CreateOneOffBuild`; private
  and public pipeline states use real `Hide`/`Expose`. Authenticated but
  unauthorized 200 must still use a genuinely public pipeline under the
  pipeline-level handler tier.
- [x] Express absent build through a missing dynamic ID and lookup error through
  only `buildsAPIBuildFactory.BuildForAPI`. A post-build pipeline failure uses
  only the embedded build's `Pipeline` override.
- [x] Resources success uses `dbtest.Builder.WithResourceVersions`,
  `WithNextInputMapping`, and `WithJobBuild` so real `AdoptInputsAndPipes` and
  `SaveOutput` rows feed `Build.Resources`. Decode and assert only fields the
  endpoint exposes: input name/version/pipeline ID/first-occurrence and output
  name/version. Persist a decoy build with distinct resources so the response
  proves target-build ownership; resource type may be fixture sanity only and
  must not be claimed as response behavior.
- [x] Empty resources use a real build with no adopted/saved versions. Only the
  existing `Resources()` error uses the build decorator. Invalid and absent IDs
  must fail before/at real lookup as their contexts specify.
- [ ] Persisted RED: request a real dynamic ID while the suite fake factory
  cannot resolve it. Sensitivity: mutate one expected persisted version and
  require failure, restore. No final-closeout RED evidence was recorded and the
  mutation was not run; left open.
- [ ] Run cumulative 47-spec serial+9 focus, full affected regressions, review,
  and commit only the file as: `test(api): persist build details and resources`.
  Final validation ran the full 95-spec focus and the implementation landed in
  one combined commit, so this composite checkpoint remains open.

## Task 4: Events and abort — local constructors 3 to 2

- [x] Events uses a real job build, pipeline visibility, and job privacy. Keep
  `constructedEventHandler`, but under its mutex require the delivered freshly
  scanned build ID/team/pipeline/job fields equal the persisted graph. Do not
  assert pointer identity. This endpoint task proves DB-backed lookup,
  authorization, and delegation only: the retained fake handler never invokes
  `Build.Events`, so do not claim that saved event rows are exercised here.
- [x] Use real public/private jobs for successful authorization. Use only the
  pipeline `Job` decorator for the deliberate job error/not-found branches and
  the factory decorator for build lookup error; real missing ID covers absence.
- [x] Abort uses a persisted one-off build. Unauthorized/forbidden paths reload
  it and require `IsAborted()==false`; success reloads and requires true.
  Production `MarkAsAborted` updates the flag, not build status. Only the
  selective abort failure uses the build decorator.
- [ ] Persisted REDs: require the event delegate's build ID and the durable
  aborted flag while fake objects still receive the calls. Sensitivities:
  expect the wrong event build ID and invert `IsAborted`, fail, restore. No
  final-closeout RED evidence was recorded and the mutations were not run;
  left open.
- [ ] Run cumulative 65-spec serial+9 focus, exact two remaining job
  constructors, review, and commit only the file as:
  `test(api): persist build events and abort state`. Final validation ran the
  full 95-spec focus and the implementation landed in one combined commit, so
  this composite checkpoint remains open.

## Task 5: Preparation and plan — local constructors 2 to 0

- [x] Preparation persists a real pipeline/job/build graph with
  `RawMaxInFlight: 1`, pending builds, versions/input mappings, and resolve
  errors using `dbtest.Builder`. Require `Job.ScheduleBuild(first)` to return
  true, then call and require `Job.ScheduleBuild(target)` to return false; that
  second call is what persists `jobs.max_in_flight_reached`. Pause pipeline/job
  through real APIs for those reasons. Assert dynamic preparation content from
  the real computation.
- [x] A healthy one-off/non-pending build returns a default found preparation;
  therefore retain narrow `Preparation()` overrides only for the existing
  `found=false` and error paths that cannot arise from a valid consistent row.
- [x] Plan success persists a valid task plan via `Build.Start` or a production
  started-build API and asserts schema `exec.v2` plus exact `plan.Public()`.
  Real `CreateOneOffBuild` supplies natural no-plan state.
- [x] Replace the final two `FakeJob` constructors with real public/private
  job configuration. Use only the pipeline's `Job` override for deliberate
  job error/not-found. Real pipeline visibility controls unauthenticated access.
- [x] Remove the `dbfakes` import and every shared `dbTeam`, `dbBuildFactory`,
  `build`, and `fakePipeline` reference from this file.
- [ ] Persisted RED: request the dynamic build and require computed preparation
  or public plan while fake wiring still answers. Sensitivities: flip
  `RawMaxInFlight`/resolve reason and expect a wrong plan schema/task path;
  require failure and restore. No final-closeout RED evidence was recorded and
  the mutation checks were not run; left open.
- [ ] Run exact 95/95 serial+9, full API serial+9 and `go test` where feasible,
  compile/vet/diff/name/census, and independent review with no unresolved
  findings. Commit only the file as:
  `test(api): persist build preparation and plans`. The 95/95 focus, full API,
  `go test`, vet, name, census, and combined code commit are recorded below;
  the different commit granularity keeps this composite item open.

## Required verification

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres

ginkgo --dry-run --focus='Builds API' \
  --json-report=/private/tmp/builds-api-after.json ./atc/api
ginkgo --procs=1 --fail-fast --focus='Builds API' ./atc/api
ginkgo --procs=9 --fail-fast --focus='Builds API' ./atc/api

go test ./atc/api -run '^$'
go test ./atc/api -count=1
ginkgo --procs=1 --fail-fast ./atc/api
ginkgo --procs=9 --fail-fast ./atc/api
go vet ./atc/api

gofmt -w atc/api/builds_test.go
git diff --check -- atc/api/builds_test.go
! rg -n 'dbfakes|\bdbTeam\b|\bdbBuildFactory\b|\bfakePipeline\b' \
  atc/api/builds_test.go
test "$(rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/builds_test.go | wc -l | tr -d ' ')" = 0
```

## Final acceptance and closure

- [x] Exact names remain 95/95; every endpoint count remains unchanged and the
  full focus passes serially/across exactly nine processes on isolated clones.
- [x] The file moves 7→0 constructors/import and no generated fake moves. Every
  healthy build, list, visibility, pagination, resource, abort, preparation,
  and plan state comes from PostgreSQL; event lookup/auth/delegation uses a real
  build while streaming remains on the documented event-handler seam. Only
  documented selective failure decorators and non-database access/event seams
  remain.
- [ ] Record exact RED/GREEN/sensitivity evidence, commits, full gates, census,
  and reviewer outcomes below. Commit only this plan as
  `docs: record builds api postgres conversion`. Do not push. The actual code
  commit, available green gates, and final scoped re-review are recorded below;
  the prescribed plan-specific RED/sensitivity runs are not, so this item
  remains open.

## Observed completion evidence

- Code commit: `0ebaf2796a` (`test(api): persist builds API state`) changes only
  `atc/api/builds_test.go`, and the current source matches that committed
  version. The implementation landed as one combined commit, rather than the
  five endpoint-batch commits proposed by this plan.
- Census: the file moved from one `FakeBuild`, three `FakeBuildForAPI`, and
  three `FakeJob` constructors to zero. The `dbfakes` import and all shared
  `dbTeam`, `dbBuildFactory`, suite `build`, and `fakePipeline` references are
  also absent; `fakeAccess` and `constructedEventHandler` remain as the
  documented non-database seams.
- Name preservation: the sorted before/after dry-run snapshots are
  byte-identical at 95 specs, retaining the planned endpoint counts of 8, 17,
  11, 11, 12, 6, 15, and 15.
- Persisted implementation: all eight endpoint groups now start their own
  late-bound real-DB server and derive healthy creation, list/visibility,
  pagination, resources, event lookup/authorization, abort, preparation, and
  plan state from PostgreSQL. The embedded decorators are limited to the
  documented selective errors/observations; SSE streaming remains on the
  existing event-handler seam.
- Runtime fixture correction: final validation exposed short declarations in
  the events, preparation, and plan fixtures that shadowed their outer job
  variables. Focused runs reproduced the nil-fixture failure (RED); assigning
  the real jobs to the outer variables at all three sites made the focused
  reruns pass (GREEN).
- Focus and branch validation: Builds passed 95/95 serially and 95/95 with
  exactly nine processes. The complete API focus passed 825/825 serially and
  825/825 with exactly nine processes; `go test ./atc/api` also passed.
  Broader validation passed `make test-integration` (24/24) and
  `make test-fly-integration` (680/680).
- Static validation passed `go vet ./atc/api`, the final dry-run name checks,
  `gofmt -d`, and `git diff --check`.
- The full `make test-unit` run exercised 155 suites in 29m48s and exited 2
  only for the seven predeclared unrelated migration-version failures: the
  expected head is `1773106160`, while embedded migrations/preflight stop at
  `1773106159`. The other 154 suites passed; this gate is not reported green.
- Final requested-pattern census is 86 constructors across 32 non-benchmark
  `*_test.go` files importing `atc/db/dbfakes`, down from 606 constructors
  across 134 such importer files. The syntax split is 83 `new(dbfakes.Fake...)`
  sites and three composite/address literals; those literals are metric seams
  in `atc/metric/query_counter_test.go` and `atc/metric/periodic_test.go`.
  All 86 sites are reviewed narrow algorithmic, fault-injection, observation,
  transaction/listener, instrumentation, or timing seams; no healthy
  persistence-state fake remains.
- Explicit gap: no final-closeout evidence is recorded for the plan-specific
  persisted REDs, and the sensitivity mutations were not run. The task
  checkpoints and final evidence box that additionally require those checks,
  partial focuses, or prescribed per-batch commits remain open rather than
  inferring them from the broader green suites.
- Final review and closure evidence: the whole-branch review at `cfc7452ca6`
  found four Important findings and one Minor. The six-commit fix wave
  (`bc232dd968`, `3f74327afd`, `4800ff4ea9`, `b3123e78f8`, `d637e0fad1`, and
  `26dc5a1d9d`) addressed them. The scoped re-review of
  `cfc7452ca6..26dc5a1d9d` found all five addressed, no new
  Critical/Important/Minor issue, and authorized closure-document finalization.
