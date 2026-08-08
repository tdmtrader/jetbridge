# Real PostgreSQL CC API Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. Preserve concurrent edits, stage only the
> active task's files, and do not push.

**Goal:** Replace all eight generated database-fake constructors in
`atc/api/cc_test.go` with CC dashboard state read from a unique PostgreSQL
template clone per spec, while preserving the three genuine database-failure
branches through closed clone-local production objects.

**Architecture:** The complete `cc.xml` Describe opts into `useRealDB`, builds
its API server only after nested contexts select dependencies and seed rows,
and persists teams, ordinary and instanced pipelines, jobs, finished/next
builds, and public visibility through production factories. A tiny real-backed
factory result decorator routes a closed real team for the selective
`Team.Pipelines` failure; a real-backed team decorator routes a closed real
pipeline for the selective `Pipeline.Dashboard` failure. Direct `FindTeam`
failure uses a production `TeamFactory` over a closed secondary connection.

**Tech Stack:** Go, Ginkgo v2, Gomega, `atc/api`'s `useRealDB`, Concourse
`atc/db`, PostgreSQL template clones, in-memory HTTP server.

## Constraints and exact census

- Use only the running machine-wide PostgreSQL service at
  `127.0.0.1:15432`. Every spec creates one unique clone through `useRealDB`.
  Do not change any service, Docker, Colima, theborg, production file,
  benchmark, or corpus.
- Modify only `atc/api/cc_test.go` and this plan. Do not change the broad
  `api_suite_test.go` fake harness; unrelated API specs still need it.
- Keep JWT/access policy fakes. They are not database state.
- Every successful team, pipeline, job, visibility, build status, build name,
  and instance-var result must originate in PostgreSQL. Use dynamic identities
  and the real owning team name.
- A controlled UTC end time may be written with parameterized SQL after
  `Build.Finish`; reload the build before deriving expected XML.
- Close every response body and secondary connection exactly once. Register
  the real server for cleanup without overwriting the package-level server.
- Capture a consumer-visible persisted RED, a restored GREEN, and a sensitivity
  failure before final review and commit.

At `d2e441a012`, `cc_test.go` has exactly:

| Generated fake | Baseline | Final |
|---|---:|---:|
| `FakeTeam` | 3 | 0 |
| `FakePipeline` | 5 | 0 |
| **Total** | **8** | **0** |

The top-level `atc/api/*_test.go` constructor census is 49 and the recursive
`atc/api` census is 64. This phase must leave them at 41 and 56 respectively,
without moving a generated fake elsewhere.

## Task 1: Capture baseline and persisted RED

- [x] Verify PostgreSQL readiness, compile the API package, run the 22-spec
  `cc.xml` focus serially, and record the exact 8-site/type census.
- [x] In the successful-finished-build path, persist a real team, pipeline,
  job, and succeeded build while the request still targets the old fake
  server. Require the returned build label to equal the dynamic real build
  name. Run only that spec and require failure because the fake still returns
  literal build `42`. Do not commit the temporary mixed wiring.

## Task 2: Add lifecycle-correct persisted helpers and error seams

- [x] Add one helper that creates a real team, saves a named pipeline with a
  configured job and optional instance vars, and returns the real job.
- [x] Add one helper that creates, starts, finishes, timestamp-normalizes, and
  reloads a job build. It accepts the terminal status and controlled UTC end
  time and returns the reloaded build; no identity is literal.
- [x] Define only these selective real-backed seams, with comments explaining
  why healthy PostgreSQL cannot fail the later call while preserving earlier
  lookup success:

  1. a `db.TeamFactory` decorator embedding the healthy real factory and
     returning one supplied real team;
  2. a `db.Team` decorator embedding a healthy real team and returning one
     supplied real pipeline slice from `Pipelines`.

- [x] Load a real team or pipeline through a secondary clone connection, close
  that connection exactly once, and route the closed object only for the
  corresponding selective failure. For direct `FindTeam` failure, install
  `db.NewTeamFactory` over a closed secondary connection with no decorator.

## Task 3: Convert every authorized CC outcome

- [x] At the outer Describe, call `useRealDB` once per spec and copy its
  dependencies. In the outer `JustBeforeEach`, after all nested fixture and
  dependency changes, build a locally shadowed API server, construct the
  request generator from that server, issue the request, and register a
  checked response-body close.
- [x] Persist and assert the successful, aborted, errored, and failed finished
  build mappings. Generate XML with the reloaded build's dynamic name and UTC
  end time, preserving status, header, project name, and URL coverage.
- [x] Persist a finished build plus a real pending/started next build and
  require `activity="Building"`. Persist a configured job with no finished
  build and require it to be omitted.
- [x] Use a real empty-job pipeline and an absent pipeline list for the two
  empty outcomes. Use the closed real pipeline and closed real team for the
  two selective 500 outcomes.
- [x] Save an instanced pipeline with
  `atc.InstanceVars{"branch": "feature/foo"}` in its `PipelineRef`, finish a
  real job build, and preserve the escaped project name and query-string URL.
- [x] Use ordinary real absence for authorized team-not-found and the closed
  production factory for the direct 500.

## Task 4: Convert unauthenticated visibility outcomes

- [x] Request the actual persisted team name rather than relying on the old
  fake factory's name mismatch.
- [x] For the public case, persist both a hidden decoy and an exposed pipeline,
  each with its own configured job. Finish a real build on both pipelines so
  either one would be rendered if returned, then prove the XML includes the
  exposed dynamic project and excludes the hidden decoy.
- [x] For the empty-public case, persist a private pipeline and prove the
  project list is empty. Use real absence for the unauthenticated missing-team
  response.

## Task 5: Sensitivity, verification, review, and commits

- [x] Temporarily expect one returned build label to equal
  `reloadedBuild.Name()+"-wrong"` and require the focused assertion to fail.
  Temporarily expect the hidden public-visibility decoy to appear and require
  that assertion to fail. Restore both and rerun their specs to GREEN.
- [x] Format and run:

  ```bash
  pg_isready -h 127.0.0.1 -p 15432 -U postgres
  gofmt -w atc/api/cc_test.go
  go test ./atc/api -run '^$'
  ginkgo --procs=1 --focus='cc\.xml' ./atc/api
  ginkgo --procs=9 --focus='cc\.xml' ./atc/api
  ginkgo --procs=1 ./atc/api
  go test ./atc/api -count=1
  ginkgo --procs=9 ./atc/api
  go vet ./atc/api
  git diff --check -- atc/api/cc_test.go
  ! rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/cc_test.go
  ! rg -n 'atc/db/dbfakes' atc/api/cc_test.go
  ```

- [x] Require 22/22 focused specs with one and nine processes, the complete API
  suite serially and across exactly nine processes, compile/vet/diff success,
  exact 8-to-0 file census, and exact 49-to-41 / 64-to-56 API census.
- [x] Obtain independent review with no unresolved Critical, Important, or
  Minor finding. Inspect dynamic identity/timestamp assertions, hidden-decoy
  exclusion, request-generator timing, clone and response cleanup, and the
  exact three closed-object fault boundaries.
- [x] Commit only `atc/api/cc_test.go` as
  `test(api): persist cc dashboard state`. Then add exact commits, RED/GREEN,
  sensitivity, counts, and reviewer evidence below and commit only this plan as
  `docs: record api cc postgres conversion`. Do not push.

## Final acceptance

- [x] `cc_test.go` contains zero generated database fakes/imports and every
  successful dashboard result is backed by PostgreSQL.
- [x] The three database-error branches use closed production objects plus only
  the two narrow real-backed routing decorators; no successful state is
  fabricated.
- [x] Status mapping, next-build activity, empty results, instance vars,
  visibility filtering, authentication behavior, headers, XML, and URLs retain
  coverage with dynamic persisted identities.
- [x] One clone is used per spec, all servers/responses/connections are closed
  before clone drop, and 9-process execution passes against the shared service.
- [x] No unrelated file, production behavior, shared fake harness, service
  lifecycle, benchmark, corpus, or remote branch changes.

## Observed completion evidence

- The independently reviewed plan landed as `e039d52a8f`; the conversion landed
  as `f7da80324f` and modified only `atc/api/cc_test.go`. No production,
  shared-suite, service-lifecycle, benchmark, corpus, or remote change was
  included.
- Baseline readiness reported PostgreSQL accepting connections. The existing
  `cc.xml` focus passed 22/22 before conversion, and the exact file census was
  `FakeTeam` x3 plus `FakePipeline` x5.
- Persisted RED: with a real succeeded build seeded but the request still wired
  to the old fake server, the XML returned fake label `42` while the reloaded
  PostgreSQL build's dynamic label was `1`. The single spec failed on that
  mismatch before rewiring and passed after conversion.
- Sensitivity: expecting `build.Name()+"-wrong"` failed with actual `1` versus
  expected `1-wrong`; requiring the finished hidden pipeline to appear failed
  because only the finished exposed pipeline was returned. Both mutations were
  restored, and the two targeted specs passed.
- Final verification passed: compile-only; focused 22/22 serially and across
  exactly nine processes; complete API 825/825 serially and 825/825 across nine
  processes; uncached `go test ./atc/api -count=1`; `go vet ./atc/api`;
  formatting and diff checks. The 22 `It` descriptions remained byte-identical.
- Exact constructor searches report zero generated database fakes/imports in
  `cc_test.go`, 41 top-level API constructors, and 56 recursive API
  constructors: the required 8-to-0, 49-to-41, and 64-to-56 transitions.
- Inspection confirmed one opt-in clone per spec; the server is constructed in
  the late outer `JustBeforeEach`; response, server, primary/lock connections,
  and clone clean up in LIFO order; every secondary connection closes exactly
  once. Successful, empty, instance-var, next-build, and visibility outcomes
  are persisted and asserted through dynamic real objects.
- Independent final code review reported PASS with no Critical, Important, or
  Minor finding. It confirmed the three 500 paths fail through closed
  production objects at `FindTeam`, `Team.Pipelines`, and
  `Pipeline.Dashboard`, with only the two narrow real-backed routing decorators
  and no fabricated successful state.
