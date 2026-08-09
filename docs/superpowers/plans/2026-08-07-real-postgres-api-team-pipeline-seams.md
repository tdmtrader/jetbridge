# Teams and Pipelines Final PostgreSQL Seam Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. The parent serializes staging and commits.

**Goal:** Ensure no generated broad database fake participates in a healthy
Teams or Pipelines API request, replace their three remaining generated
constructors with real PostgreSQL rows plus narrow method-specific fault
decorators, and retire the now-unneeded primary default-suite team factory.

**Architecture:** Both files already use endpoint-local `useRealDB()` clones
for almost all ordinary behavior. Complete the conversion by wrapping freshly
loaded real `db.Team` and `db.Pipeline` values in mutex-protected decorators
that override only the exact late method a fault context needs. All healthy
methods delegate to PostgreSQL. Persist the sole-admin, build-pagination, and
debug-versions graphs, deriving wire expectations from their real identities.
After Builds and agent cleanup remove all remaining package-global consumers,
replace the default suite's primary team factory with a deterministic
unavailable adapter; retain the distinct worker-team factory/team only for the
documented worker authorization seam.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL per-spec template clones,
Concourse `db.Team`/`db.Pipeline`, `dbtest.Builder`, API handlers.

## Fixed scope, ordering, names, and census

- Execute only after the Builds plan, agent/suite cleanup plan, and Accessor
  plan are complete. Modify only `atc/api/teams_test.go`,
  `atc/api/pipelines_test.go`, `atc/api/api_suite_test.go`, and this plan. Do
  not modify production code, generated fakes, the shared PostgreSQL lifecycle,
  Docker, or Colima. If the Task 3 unavailable adapter exposes a consumer in
  another file, stop and amend/re-review this fixed scope before editing that
  file. Do not push.
- Preserve all exact target-file names: 33 specs in `teams_test.go` and 112 in
  `pipelines_test.go`, for 145 target-file specs. The broader
  `--focus='Pipelines API'` regression intentionally includes 10 additional
  specs from `info_test.go`, for 122 Pipelines-focus specs and 155 combined
  Teams/Pipelines-focus specs. Compare sorted Ginkgo JSON reports by
  `LeafNodeLocation.FileName` before/after for the exact target-file counts.
- Remove the one `FakeTeam` constructor/import from `teams_test.go` and the
  `FakeTeam` + `FakePipeline` constructors/import from `pipelines_test.go`.
  Remove the primary suite `FakeTeamFactory` constructor only after a zero-
  reference check and full-suite proof. Do not move a generated fake.
- Historical prerequisite checkpoint is 93 constructors across 35 non-benchmark
  `*_test.go` files importing `atc/db/dbfakes`. The two files remove three
  constructors and two imports; suite
  cleanup later removed the suite-bootstrap worker constructors as part of the
  final review wave. The final requested-pattern census is 86 constructors
  across 32 non-benchmark `*_test.go` files importing `atc/db/dbfakes`,
  using the repository-wide requested-pattern scope.
- Historical plan assumption: retain the separate `FakeWorkerFactory`, worker
  `FakeTeamFactory`, and worker `FakeTeam` as worker lookup/authorization
  seams. The final review removed those suite-bootstrap constructors, made
  defaults fail closed, and retained only two context-local late-method fault
  seams in Workers API; do not treat them as ordinary Teams/Pipelines state.

## Shared decorator rules

- [x] Add test-owned embedded decorators with one mutex-protected state object
  per spec. A Teams factory/team decorator may override only `CreateTeam`,
  `NotifyCacher`, `UpdateProviderAuth`, `Delete`, `Rename`, and `Builds`, plus
  record delegated `FindTeam`/page arguments where an existing spec observes
  routing or parsing.
- [x] A Pipelines team/pipeline decorator may override only `Pipelines`,
  `Pipeline`, `OrderPipelines`, `OrderPipelinesWithinGroup`, `RenamePipeline`,
  `Destroy`, `Pause`, `Archive`, `Unpause`, `Expose`, `Hide`,
  `LoadDebugVersionsDB`, `Builds`, and `CreateStartedBuild` for their existing
  selective error/semantic-conflict contexts. Every unset method delegates to
  the freshly loaded real object.
- [x] Decorator factories first delegate real lookup, then wrap only the
  intended dynamic team/pipeline ID. Defensively copy page/order arguments and
  never return a synthetic healthy entity or healthy data graph. Error flags,
  recorders, and overrides must be safe under `go test -race` style concurrent
  handler access.

## Task 1: Finish Teams API — one constructor to zero

- [x] Replace the outer `FakeTeam` with real rows and the narrow Teams
  decorators. Keep real lookup/create/update/delete/rename/build-list behavior
  for every healthy path; use the decorator only for the current late
  `CreateTeam`, `UpdateProviderAuth`, `Delete`, `Rename`, and `Builds` failures.
- [x] Persist the sole admin team by setting only the clone-owned main team's
  `admin` bit through its dynamic ID, require one affected row, refetch it, and
  prove the DELETE returns 403 while the row remains. This replaces the fake
  `Name/Admin/GetTeams/DeleteCallCount` graph.
- [x] Persist at least four distinguishable team builds. The page-observer
  decorator delegates `Team.Builds` while recording the exact parsed page for
  explicit and default limits. Use real dynamic pagination to assert RFC 5988
  previous/next links; do not synthesize `db.Pagination` on a healthy path.
- [x] Replace call-count-only healthy claims with durable assertions (row still
  present, exact build IDs/order/links). Keep recorder assertions only where
  the behavior under test is argument translation itself.
- [ ] Persisted RED: require the real sole-admin row and real page IDs while
  the old fake graph is bound. Sensitivities: clear the admin bit and shift one
  dynamic page boundary, require failure, then restore.
- [x] Run exact 33-spec serial/nine-process focuses, compile/vet/name/census/
  diff checks, and independent review. Commit only `teams_test.go` as
  `test(api): persist remaining team API state`. The plan records 33/33 in both
  modes, static checks, and review PASS; `20f7b7c4bb` is source-only for
  `atc/api/teams_test.go`.

## Task 2: Finish Pipelines API — two constructors to zero

- [x] Replace the outer generated team/pipeline fakes with real rows and the
  narrow Pipelines decorators. Each late fault begins with a successful real
  team and pipeline lookup; only the named method fails. Preserve exact
  `ErrWorkflowRunTemplateImmutable` versus generic-error response behavior.
- [x] Convert all four healthy `versions-db` specs to a real `a-team` /
  `a-pipeline` graph. Persist jobs/resources and resource versions, one
  succeeded explicit output, one adopted input/implicit output, and a rerun
  mapping using production DB APIs/`dbtest.Builder`. Persist a distinguishable
  decoy pipeline so scope cannot pass accidentally.
- [x] Build the expected `atc.DebugVersionsDB` independently from the persisted
  objects' dynamic IDs, scope IDs, check orders, input names, build/job IDs,
  and rerun identity. Because the production queries have no `ORDER BY`, decode
  the raw body, assert exact slice cardinalities, sort every expected and
  actual slice by explicit composite keys, and compare the complete normalized
  graph. Do not call `LoadDebugVersionsDB` to manufacture the HTTP oracle. The
  error context uses only the decorator's `LoadDebugVersionsDB` error.
- [x] Replace fake `FindTeam` call assertions with the delegating factory's
  defensive argument record plus behavioral scope: `a-team` succeeds and the
  decoy team/pipeline graph is absent from the response.
- [ ] Persisted RED: request the real dynamic graph while the fake values are
  still bound. Sensitivities: substitute a decoy resource/build ID and remove
  one rerun mapping from the expected body, require failure, then restore.
- [x] Run the exact 112 target-file specs and the broader 122-spec Pipelines
  focus serially and across nine processes, then the 145 target-file / 155
  focus combined regressions, compile/vet/name/census/diff checks, and
  independent review. Commit only
  `pipelines_test.go` as `test(api): persist remaining pipeline API state`. The
  plan records 112/112, 122/122, 145/145, and 155/155 in both modes, static
  checks, and review PASS; `d3b5617b1b` is source-only for
  `atc/api/pipelines_test.go`.

## Task 3: Retire the primary default-suite TeamFactory

- [x] Require repository-wide zero references to `dbTeamFactory` outside its
  suite declaration/allocation/default-deps assignment. Replace that default
  dependency with a small non-nil `unavailableTeamFactory` implementing the
  exact `db.TeamFactory` contract and returning one deterministic unavailable
  error (or nil for `GetByID`) rather than a generated success fake.
- [x] Keep the worker-specific factory/team wired only to worker dependencies.
  Rename the suite variable to `dbWorkerTeam` if useful for clarity, but do not
  delete that justified constructor in this task.
- [ ] Run focused authorization/team/pipeline/worker coverage first, then full
  API serially and with nine processes. Any healthy route reaching the
  unavailable primary factory is an unconverted success path: convert that
  route to its clone before removing the constructor; do not weaken the
  adapter to make it pass.
- [ ] Independently review zero references and the full-suite outcome. Commit
  only `api_suite_test.go` as `test(api): retire default team database fake`.
  The final re-review and full API evidence exist, but the prescribed focused
  coverage and source-only Task 3 commit evidence were not recorded.

## Required verification and closure

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres
ginkgo --dry-run --json-report=/private/tmp/team-pipeline-after.json ./atc/api
ginkgo --procs=1 --focus='Teams API' ./atc/api
ginkgo --procs=9 --focus='Teams API' ./atc/api
ginkgo --procs=1 --focus-file='pipelines_test.go' ./atc/api
ginkgo --procs=9 --focus-file='pipelines_test.go' ./atc/api
ginkgo --procs=1 --focus='Pipelines API' ./atc/api
ginkgo --procs=9 --focus='Pipelines API' ./atc/api
ginkgo --procs=1 --focus-file='(teams|pipelines)_test.go' ./atc/api
ginkgo --procs=9 --focus-file='(teams|pipelines)_test.go' ./atc/api
ginkgo --procs=1 --fail-fast ./atc/api
ginkgo --procs=9 --fail-fast ./atc/api
go test ./atc/api -count=1
go vet ./atc/api
gofmt -d atc/api/teams_test.go atc/api/pipelines_test.go atc/api/api_suite_test.go
git diff --check
```

- [x] Exact names remain 33 Teams target-file / 112 Pipelines target-file / 122
  Pipelines focus / 155 combined focus / complete API. The two target files
  have zero generated DB constructors/imports, no healthy path uses a generated
  DB fake, and only documented narrow error/observer seams remain.
- [x] Final repository requested-pattern census is 86 constructors across 32
  non-benchmark `*_test.go` files importing `atc/db/dbfakes` after all
  prerequisites and Tasks 1–3. The syntax split is 83 `new(dbfakes.Fake...)`
  sites and three composite/address literals; those literals are metric seams
  in `atc/metric/query_counter_test.go` and `atc/metric/periodic_test.go`.
  All 86 sites are reviewed narrow algorithmic, fault-injection, observation,
  transaction/listener, instrumentation, or timing seams; no healthy
  persistence-state fake remains.
- [ ] Record RED/GREEN/sensitivity evidence, review outcomes, exact census,
  full gates, and commit IDs below. Commit this plan as
  `docs: record final team and pipeline postgres seams`. Do not push. The
  recorded focuses, static checks, source-only Task 1/2 commits, and review are
  sufficient for their individual verification boxes; the plan-specific RED and
  sensitivity evidence keeps this composite open.

## Observed completion evidence

- Completed implementation commits: Teams `20f7b7c4bb`; Pipelines
  `d3b5617b1b`; default-suite/interface retirements `5507ea7258`.
- Focus validation passed serially and with nine processes: Teams 33/33;
  exact Pipelines target file 112/112; broader Pipelines focus 122/122;
  combined exact target files 145/145; and broader Teams-or-Pipelines focus
  155/155.
- Full API passed 825/825 serially and with nine processes. `go test ./atc/api`
  passed, as did `make test-integration` (24/24) and Fly integration (680/680).
- Static validation passed `go vet ./atc/api`, the final dry-run name checks,
  `gofmt -d`, and `git diff --check`.
- The full `make test-unit` run exercised 155 suites in 29m48s and exited 2
  only for the seven predeclared unrelated migration-version failures: the
  expected head is `1773106160`, while embedded migrations/preflight stop at
  `1773106159`. The other 154 suites passed; this gate is not reported green.
- The runtime RED for an empty pipeline rename was reproduced as expected 400
  versus actual 500 when its real team fixture was absent. Hoisting one cloned
  database/team/pipeline/server setup to the invalid-identifier parent fixed
  the fixture; the exact leaf and all listed gates then passed. Independent
  review: PASS.
- Final requested-pattern census is 86 constructors across 32 non-benchmark
  `*_test.go` files importing `atc/db/dbfakes`, down from 606 constructors
  across 134 such importer files. The syntax split is 83 `new(dbfakes.Fake...)`
  sites and three composite/address literals; those literals are metric seams
  in `atc/metric/query_counter_test.go` and `atc/metric/periodic_test.go`.
  All 86 sites are reviewed narrow algorithmic, fault-injection, observation,
  transaction/listener, instrumentation, or timing seams; no healthy
  persistence-state fake remains.
- Mutation-only evidence remains intentionally open. The whole-branch review at
  `cfc7452ca6` found four Important findings and one Minor; the six-commit fix
  wave (`bc232dd968`, `3f74327afd`, `4800ff4ea9`, `b3123e78f8`, `d637e0fad1`,
  and `26dc5a1d9d`) addressed them. The scoped re-review of
  `cfc7452ca6..26dc5a1d9d` found all five addressed, no new
  Critical/Important/Minor issue, and authorized closure-document finalization.
