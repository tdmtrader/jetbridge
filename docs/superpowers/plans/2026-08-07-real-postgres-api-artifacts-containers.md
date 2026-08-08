# Artifact and Container API PostgreSQL Conversion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. The two code tasks own disjoint files and may
> run concurrently, but the parent serializes staging and commits.

**Goal:** Remove all four remaining generated database constructors and both
`dbfakes` imports from the Artifact Repository and Containers API tests while
preserving their exact 18 and 47 spec names and using one template-cloned
PostgreSQL database per spec.

**Architecture:** Reuse `useRealDB()` and build each endpoint's server/request
after nested fixtures are finalized. Persist real route teams, workers,
volumes, worker artifacts, one-off builds, ordinary build-step containers, and
resource-config check-session containers. Keep runtime worker-pool,
volume/process, access, clock, and timeout collaborators as intentional
non-database seams. Use embedded real-team decorators only for selective SQL
errors and query-argument observation that a healthy database cannot express.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL template clones, Concourse
`atc/db`, `dbtest`, runtime test adapters, HTTP/WebSocket tests.

## Fixed scope and census

- Task 1 may modify only `atc/api/artifacts_test.go`; Task 2 may modify only
  `atc/api/containers_test.go`; closure may modify only this plan. Do not touch
  production code, generated fakes, migrations, suite helpers, Docker, Colima,
  or PostgreSQL lifecycle. Do not push.
- Every converted spec calls `useRealDB()` exactly once and owns a unique clone
  on `127.0.0.1:15432`. Use endpoint-local deps/server/request state and
  existing cleanup; introduce no package-global mutable state.
- Preserve every `Describe`, `Context`, and `It` text. Capture sorted dry-run
  names before/after. Artifact Repository remains exactly 18 specs (POST 7,
  GET 11); Containers remains exactly 47 (list 17, get 8, hijack 22).
- Exact constructors: Artifact `FakeWorkerArtifact` and `FakeCreatedVolume`
  2→0; Containers two `FakeContainer` 2→0. Both file imports and every shared
  `dbTeam` reference move to zero. None of the four is a justified survivor.
- Retain `fakeWorkerPool`, `runtimetest.Volume`/`Container`, `fakeAccess`,
  `fakeClock`, and intercept timeout seams. They model runtime storage,
  process/I/O, authorization, clock, and cancellation rather than DB state.

## Shared implementation rules

- [x] Each endpoint Describe owns local `realdb`, copied `deps`, decorated
  route team/factory, and shadowed `server *httptest.Server`. Nested setup
  selects persisted state/errors/URL values first. `JustBeforeEach` assigns
  `realdb.Deps = deps`, starts `server = realdb.Serve()`, constructs the final
  HTTP/WebSocket request, sends it, and registers response/connection cleanup
  in safe LIFO order (`Serve` already registers server cleanup). No request may
  retain the package fake server URL.
- [x] Embedded decorators delegate all healthy methods to PostgreSQL, guard
  mutable errors/call logs with a mutex, and return defensive snapshots.
  Found/not-found, ownership, filter results, state, IDs, handles, and
  timestamps must come from real rows.
- [x] Replace literal suite team ID 734 everywhere with the persisted route
  team ID. Derive every worker, build, artifact, volume, and container identity
  from the returned real object.
- [x] Real container states are `creating` or `created`; do not preserve the
  fabricated `container-state` or omitted state. Do not assume query order;
  decode and compare by identity or `ConsistOf`.

## Task 1: Persist Artifact Repository state — 2 to 0

- [x] Create real route team `some-team`, persist a worker with
  `deps.workerFactory.SaveWorker(atc.Worker{Name: ...}, 0)`, and persist a real
  one-off build with `team.CreateOneOffBuild()` because worker-artifact
  `build_id` has a real foreign key. Then build an artifact fixture only
  through public APIs:

  ```go
  creating, err := deps.volumeRepository.CreateVolumeWithHandle(
      handle, team.ID(), worker.Name(), db.VolumeTypeArtifact,
  )
  created, err := creating.Created()
  artifact, err := created.InitializeArtifact(name, build.ID())
  ```

  Use a runtime volume with the same handle. Return the real artifact from the
  retained runtime pool stub.
- [x] Add a narrow `artifactAPITeam` embedding the real team. Record and
  delegate `FindVolumeForWorkerArtifact(int)`; only its exact error field may
  short-circuit for the existing post-team-lookup 500. Add a factory decorator
  that delegates lookup and decorates only found `some-team` rows.
- [x] POST success must require the pool's `worker.Spec.TeamID` equals the real
  team ID, the runtime stream contains the request archive, status/header are
  unchanged, and decoded `atc.WorkerArtifact` exactly matches the real row's
  dynamic ID/name/build ID/`CreatedAt().Unix()`. Remove fake ID 0/time 42.
- [x] GET uses the real `artifact.ID()` in the late-bound URL, proves the exact
  recorded lookup ID, then requires the pool to receive the real team ID and
  persisted volume handle. Runtime error/not-found/streaming behavior remains
  on the pool/volume seams.
- [x] Express DB not-found with an absent dynamic artifact ID. Use only the
  narrow method error for DB 500. No fabricated `CreatedVolume` may remain.
- [ ] Persisted RED: create the real artifact and expect its dynamic response
  while the route still uses the suite fake server without a matching return;
  require the found path to fail 404, then bind the real server and pass.
  No final-closeout evidence was recorded; left open.
- [ ] Sensitivity: temporarily request a missing artifact ID or associate the
  volume with a different team; require the found 200/body path to fail 404,
  restore, and rerun. Not run during final closeout; left open.
- [ ] Run compile, exact 18/18 serial and nine-process focus, full API
  regression when feasible, vet, dry-run-name diff, diff/census searches, and
  independent review with no unresolved findings. Commit only the file as:
  `test(api): persist artifact repository state`. The code commit and all
  runtime/static gates are recorded below; this remains open only until the
  final independent branch review is recorded.

## Task 2: Persist Containers API state — 2 to 0

- [x] Add `containersAPITeam` and factory decorators over real objects. Record
  and delegate `FindContainersByMetadata(db.ContainerMetadata)` and
  `FindCheckContainers(lager.Logger, atc.PipelineRef, string)`. Permit only
  selective `Containers()` and `FindContainerByHandle(string)` errors for the
  existing 500 paths. Do not fake healthy query results.
- [x] Add a helper for an ordinary real build-step container: persist route
  team, worker, and `team.CreateOneOffBuild()`, then call
  `worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), planID,
  team.ID()), metadata)` and optionally `Created()`. Return every dynamic
  identity. Use a distinct second real team for outside-team cases.
- [x] List-all success persists two containers with the existing distinct
  metadata and workers, using valid real states (one may remain creating and
  one become created). Decode `[]atc.Container`, compare without assuming row
  order, and require all dynamic handles, worker names, states, and metadata.
  Empty-list state must use no rows; list 500 uses only the narrow decorator.
- [x] Each ordinary filter spec persists a full-metadata team-owned container,
  sends its one query value, requires the decorator's exact metadata argument,
  and where stable requires the returned dynamic handle. Invalid build ID must
  prove no metadata/check query was attempted.
- [x] A real check container must use a
  `resource_config_check_session_id` owner; `Metadata.Type=check` alone is not
  sufficient. Save pipeline ref `some-pipeline` with instance vars
  `{branch: master}`, a resource using `dbtest.BaseResourceType`, a compatible
  worker, resource config/scope/version graph through
  `dbtest.NewBuilder(realdb.Conn, realdb.LockFactory)`, and
  `db.NewResourceConfigCheckSessionContainerOwner(...)`. Persist/transition
  the container, then require `FindCheckContainers` receives the exact ref and
  resource name and returns that row.
- [x] GET found/not-found/error must use real generated handles. Create the
  success container for route team. For the true not-found spec, capture a
  generated handle from a real container, transition the row through
  `Created` → `Destroying` → `Destroy`, then request that now-absent handle;
  using a live second-team row would exercise ownership instead of lookup
  absence. Reserve a live second-team container for the separate outside-team
  context, relying on global `FindContainerByHandle` plus real
  `IsContainerWithinTeam` rather than fabricated ownership.
- [x] For hijack, embed the existing `runtimetest.Container` in a
  `containersAPIRuntimeContainer` whose `DBContainer()` returns the real
  `db.CreatedContainer`. Keep runtime process/I/O/errors on the retained pool
  and runtime adapters. Replace fake `UpdateLastHijack` call assertions with a
  fresh `FindCreatedContainerByHandle` lookup on every `Eventually` poll.
  Capture the initial `LastHijack` only after it becomes nonzero; after the
  fake-clock tick, eventually require a freshly loaded timestamp strictly
  after the captured value. Merely observing the runtime process is not enough
  synchronization because `Run` can publish the process before the handler
  persists its first hijack update.
- [x] Check-container admin authorization must use a real check-session owner.
  Its outside-team context must create that same kind of check-session-owned
  row for the second real team; substituting a build-step owner would change
  the branch under test. Its resource type/source must produce a distinct
  global resource-config ID that no route-team resource references (or the
  route-team check graph must be omitted in that nested context), because check
  ownership is determined by whether the route team references the session's
  resource config, not by a direct container team ID. The separate
  build-container outside-team context uses a second-team real build-step row.
  All runtime pool lookups receive dynamic real team IDs/handles.
- [x] Ensure WebSocket cleanup handles nil connections and cannot leave the
  handler's periodic DB updater alive. Closing the socket alone does not cancel
  the handler. Preserve an idempotent process-exit release for every
  long-running runtime stub, explicitly release it, close a nonnil connection,
  and, only when `ContextOfRun()` is nonnil because `Run` actually occurred,
  wait for its `Done()` before server/DB cleanup. Bad-handshake and early pool
  failure paths must not call `Done()` on a nil run context. Keep fake clock and
  timeout assertions for periodic/idle behavior.
- [ ] Persisted RED: save the two real list rows and expect their response while
  the handler is still suite-fake-backed with no return; require the body spec
  to fail, then bind the real server and pass. No final-closeout evidence was
  recorded; left open.
- [ ] Sensitivities, restored one at a time: mutate persisted `StepName` while
  retaining the expected list/filter result; move a hijack container to the
  second team while retaining success; invert the real `LastHijack` outcome.
  Each affected focus must fail before restoration. These mutation-only checks
  were not run during final closeout; left open.
- [ ] Run compile, exact 47/47 serial and nine-process focus, combined
  Artifact/Containers serial+9, full API regression when feasible, vet,
  dry-run-name diff, diff/census searches, and independent review with no
  unresolved findings. Commit only the file as:
  `test(api): persist container API state`. The code commit and all
  runtime/static gates are recorded below; this remains open only until the
  final independent branch review is recorded.

## Required verification

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres

ginkgo --dry-run --focus='ArtifactRepository API' \
  --json-report=/private/tmp/artifacts-api-after.json ./atc/api
ginkgo --dry-run --focus='Containers API' \
  --json-report=/private/tmp/containers-api-after.json ./atc/api

ginkgo --procs=1 --focus='ArtifactRepository API' ./atc/api
ginkgo --procs=9 --focus='ArtifactRepository API' ./atc/api
ginkgo --procs=1 --focus='Containers API' ./atc/api
ginkgo --procs=9 --focus='Containers API' ./atc/api
ginkgo --procs=1 --focus='(ArtifactRepository API|Containers API)' ./atc/api
ginkgo --procs=9 --focus='(ArtifactRepository API|Containers API)' ./atc/api

go test ./atc/api -run '^$'
go test ./atc/api -count=1
ginkgo --procs=1 ./atc/api
ginkgo --procs=9 ./atc/api
go vet ./atc/api

gofmt -w atc/api/artifacts_test.go atc/api/containers_test.go
git diff --check -- atc/api/artifacts_test.go atc/api/containers_test.go
! rg -n 'atc/db/dbfakes|\bdbTeam\b|DBContainer_' \
  atc/api/artifacts_test.go atc/api/containers_test.go
test "$(rg -o 'new\(dbfakes\.Fake[^)]*\)' \
  atc/api/artifacts_test.go atc/api/containers_test.go | wc -l | tr -d ' ')" = 0
```

## Final acceptance and closure

- [x] Artifact remains 18/18 and Containers 47/47 with exact name snapshots,
  serially and across exactly nine processes. Both files move 4→0 total and no
  generated DB fake moves elsewhere. The combined 65-spec focus also passes in
  both modes, and names/census are verified.
- [x] Every healthy artifact/container success, ownership, lookup, filter,
  state, ID, handle, timestamp, and hijack update comes from PostgreSQL. Only
  documented selective DB errors and non-database runtime/access/time seams
  remain.
- [ ] Record exact RED/GREEN/sensitivity evidence, counts, commits, full gates,
  and independent review outcomes below. Commit only this plan as
  `docs: record artifact and container postgres conversion`. Do not push. The
  code commits and available green gates are recorded below; the prescribed
  persisted RED/sensitivity runs and independent review are not evidenced, so
  this item remains open.

## Observed completion evidence

- Code commits: Artifact Repository `d18d5baae3` (`test(api): persist artifact
  repository state`) and Containers `42d52d7ff3` (`test(api): persist container
  API state`). Each commit changes only its corresponding API test file, and
  the current test sources match those committed versions.
- Census: Artifact Repository removed its `FakeWorkerArtifact` and
  `FakeCreatedVolume`; Containers removed both `FakeContainer` values. Across
  the two files the generated database constructor count is 4 to 0, with no
  `dbfakes` import, shared `dbTeam`, literal suite team ID 734, or
  `DBContainer_` call remaining.
- Name preservation: the sorted before/after dry-run names are identical at
  18 Artifact Repository specs (7 POST, 11 GET) and 47 Containers specs
  (17 list, 8 GET, 22 hijack).
- Persisted implementation: Artifact Repository now creates the route team,
  worker, one-off build, artifact volume, and worker artifact through real DB
  APIs. Containers now persists ordinary build-step and resource-config
  check-session owners, real ownership/filter/list state, and durable hijack
  timestamps; only the documented runtime, access, clock, and selective-error
  seams remain.
- Runtime cleanup correction: the Containers hijack `BeforeEach` resets
  `releaseProcess` for every spec, so a closure installed by one nested spec
  cannot leak into the next spec's `AfterEach`.
- Focus validation: Artifact Repository passed 18/18 serially and with exactly
  nine processes; Containers passed 47/47 in both modes; and their combined
  focus passed 65/65 in both modes.
- Broader branch validation passed the complete API focus 825/825 serially and
  825/825 with exactly nine processes, plus `go test ./atc/api`,
  `make test-integration` (24/24), and `make test-fly-integration` (680/680).
- Static validation passed `go vet ./atc/api`, the final dry-run name checks,
  `gofmt -d`, and `git diff --check`.
- The full `make test-unit` run exercised 155 suites in 29m48s and exited 2
  only for the seven predeclared unrelated migration-version failures: the
  expected head is `1773106160`, while embedded migrations/preflight stop at
  `1773106159`. The other 154 suites passed; this gate is not reported green.
- Final requested-pattern census is 89 constructors across 33 import files,
  down from 606 / 134. All remaining sites reconcile to 86 reviewed non-suite
  seams plus three worker-suite constructors.
- Explicit gap: no final-closeout evidence is recorded for the plan-specific
  persisted REDs, and the sensitivity mutations were not run. Their composite
  task/final-acceptance boxes remain open; final independent branch review is
  pending and is not inferred from the green runtime/static gates.
