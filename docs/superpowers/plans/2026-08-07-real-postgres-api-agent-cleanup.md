# Agent API PostgreSQL and Suite-Fake Cleanup Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. The parent serializes staging and commits.

**Goal:** Move the remaining agent feedback/transcript success paths onto one
unique template-cloned PostgreSQL database per spec, then remove generated
database constructors that survive only as unused default API-suite wiring.

**Architecture:** Make feedback and transcript stores explicit members of
`apiDBDeps`, so every real-DB server reads the same clone its spec seeds. Keep
only narrow error adapters for states PostgreSQL cannot naturally produce.
Default fake-suite servers receive nil for genuinely unreachable dependencies,
the existing unavailable workflow-run backend for the ticket journal, and a
small non-nil unavailable experiment store because that handler validates its
configuration at construction time. The final conditional cleanup occurs only
after `builds_test.go` has no shared build/pipeline references.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL template clones, Concourse
agent review/feedback/workflow-run/transcript persistence, API handlers.

## Fixed scope, names, and census

- Modify only `atc/api/agent_feedback_test.go`,
  `atc/api/agent_workflow_transcripts_test.go`, `atc/api/api_suite_test.go`,
  `atc/api/real_db_test.go`, and this plan. Do not modify production code,
  generated fakes, Docker/Colima, or PostgreSQL lifecycle. Do not push.
- Preserve all nine exact spec names: three feedback and six transcript specs.
- Every converted spec calls `useRealDB()` once and starts an endpoint-local
  server only after its fixtures/dependency overrides are finalized.
- Historical pre-conversion product-test census was 115 generated constructors
  across 37 non-benchmark `*_test.go` files importing `atc/db/dbfakes`.
  Completing the final three local
  Builds constructors removes those sites and that file's import, producing a
  post-Builds checkpoint of 112 / 36. This plan then removes one transcript
  constructor plus 15 default-suite constructors, yielding 96 / 36.
  Feedback removes success-state fake behavior but no constructor by itself.
- Historical plan assumption: retain the two default `FakeTeamFactory`
  constructors, `FakeTeam`, and `FakeWorkerFactory` until a later full-suite
  audit. The final review instead removed the suite bootstrap constructors,
  made worker defaults fail closed, and retained only two context-local
  late-method fault seams in the Workers API. Do not rewrite the earlier 96 / 36
  projection as if it anticipated that later cleanup.

## Task 1: Persist the three feedback specs

- [x] Add `feedbackStore feedback.Store` to `apiDBDeps`. Default suite deps use
  an explicit fail-closed `unavailableFeedbackStore`; `useRealDB()` uses
  `db.NewAgentFeedbackFactory(conn)`. Pass `deps.feedbackStore` to
  `api.NewHandler` and remove the package-global `apiFeedbackStore`. The
  fail-closed default supersedes the plan's original temporary memory-store
  proposal after whole-branch review found it could synthesize HTTP 201.
- [x] In each feedback spec, call `useRealDB()`, create real team `research`,
  assign finalized deps, and serve locally. Seed the production review
  projection required by feedback's foreign-key/authorization semantics,
  retaining snapshot ID `9007199254740993` to cover values above 2^53.
- [x] Seed only clone-local valid rows: snapshot and upload production identity,
  then `db.NewAgentReviewsFactory(conn).UpsertReviewProjection(...)`. Read back
  with `db.NewAgentFeedbackFactory(conn).GetByReviewSnapshot`.
- [x] Success requires status 201, real `research` distinct from `main`, durable
  team-scoped feedback, exact finding/verdict, and canonical
  `DisplayUserId` replacing the forged request reviewer. Blank identity leaves
  the table empty. A genuinely absent team returns 404 and leaves it empty.
- [ ] Persisted RED: bind the real team/feedback dependencies before inserting
  the review projection; the success request must fail closed rather than pass
  through the memory store. Sensitivity: query the main team or wrong snapshot
  and require the durable-scope assertion to fail, then restore.
- [ ] Run exact 3-spec focus serially and with nine processes, compile/vet,
  names/diff checks, and independent review. Commit the two helper files plus
  feedback test as `test(api): persist agent feedback in postgres`.

## Task 2: Persist the six transcript specs

- [x] Narrow `apiDBDeps.transcripts` from the full generated factory interface
  to `transcriptserver.Store`, the handler's exact one-method read contract.
  Default suite deps use nil (the handler's documented empty/404 behavior);
  `useRealDB()` retains `db.NewAgentRunTranscriptFactory(conn)`.
- [x] Remove the suite `fakeAgentRunTranscriptFactory` global/allocation and
  replace it in six specs with endpoint-local real DB/deps/server setup. Add a
  mutex-safe embedded error decorator that overrides only
  `ListByWorkflowRun` for the existing 500 context.
- [x] Seed a durable workflow definition and explicit run ID
  `9007199254740993` for workflow `review`, valid schema/signature versions and
  hashes, plus real one-off builds and
  `db.NewAgentRunTranscriptFactory(conn).Upsert(...)` transcript rows. Returned
  persisted rows must all carry the non-null workflow-run ID; remove the old
  impossible fake-only expectation that omitted it.
- [x] Prove workflow-name and run-ID scoping behaviorally with distinguishable
  decoy run/transcript state rather than call-history assertions. Empty list
  persists a run with no transcripts; body/unknown-plan specs persist the
  addressed transcript. Malformed ID must perform no store query.
- [ ] Persisted RED: bind the real store before rows exist and require list/body
  success to fail. Sensitivity: swap workflow/run/plan ownership and require
  exact response assertions to fail, then restore.
- [ ] Run exact 6-spec focus serially and with nine processes, combined nine
  agent specs, compile/vet/names/diff/census, and independent review. Commit
  helper plus transcript test as
  `test(api): persist workflow transcripts in postgres`.

## Task 3: Remove 12 definite default-suite constructors

- [x] Delete the globals/allocations/default assignments for these ten
  unreachable pass-through dependencies, leaving their `apiDBDeps` fields nil:
  `FakePipelineFactory`, `FakeJobFactory`, `FakeResourceFactory`,
  `FakeResourceConfigFactory`, `FakeUserFactory`, `FakeCheckFactory`,
  `FakePipelineRunFactory`, `FakeWall`, `FakeSigningKeyFactory`, and
  `FakeVolumeRepository`.
- [x] Narrow `apiDBDeps.workflowRuns` to `ticketjournal.RunStore`; use the
  existing non-nil `unavailableWorkflowRunBackend{}` in default deps and retain
  the real workflow-run factory in `useRealDB()`. This removes
  `FakeAgentWorkflowRunsFactory`.
- [x] Add a non-nil `unavailableExperimentStore` implementing
  `experiment.Store` and `experiment.PagedStore`, returning one deterministic
  unavailable error from every method. Use it only for the default server;
  real experiment specs retain `db.NewAgentExperimentsFactory`. This removes
  `FakeAgentExperimentsFactory` without violating handler validation.
- [ ] First run compile plus focused pipelines/jobs/resources/runs/users/wall/
  JWKS/volumes/experiments/ticket coverage, then the full API suite serially and
  with nine processes. Any nil dereference means the dependency was not truly
  unreachable and must be restored pending endpoint conversion.
- [ ] Independent review must trace handler construction and confirm no removed
  fake supplied observed success state. Commit only `api_suite_test.go` and
  `real_db_test.go` as `test(api): remove stale default database fakes`.

## Task 4: Remove three Builds-conditional suite constructors

- [x] Only after `atc/api/builds_test.go` has zero references to
  `dbBuildFactory`, `fakePipeline`, and package-global `build`, delete the
  corresponding `FakeBuildFactory`, `FakePipeline`, and `FakeBuild` globals and
  allocations, plus `dbTeam.PipelineReturns(fakePipeline, true, nil)`.
- [x] Require a repository-wide zero-reference check for those three suite
  identifiers outside their declarations before deletion. Run the exact 95
  Builds specs and full API serial/nine-process suites to detect implicit
  default-pipeline dependencies.
- [ ] Independently review the deletion and commit only `api_suite_test.go` as
  `test(api): remove retired build suite fakes`.

## Required verification and closure

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres
ginkgo --procs=1 --focus='agent feedback routes' ./atc/api
ginkgo --procs=9 --focus='agent feedback routes' ./atc/api
ginkgo --procs=1 --focus='agent workflow transcript routes' ./atc/api
ginkgo --procs=9 --focus='agent workflow transcript routes' ./atc/api
ginkgo --procs=1 --fail-fast ./atc/api
ginkgo --procs=9 --fail-fast ./atc/api
go test ./atc/api -count=1
go vet ./atc/api
git diff --check
```

- [x] Exact names remain 3 feedback / 6 transcript. All success state comes
  from each spec's clone; only the transcript error decorator and explicit
  unavailable defaults remain as documented non-success seams.
- [x] The cleanup landed atomically with the later default-suite retirements,
  so there is no standalone 96 / 36 commit checkpoint. The final branch is
  86 constructors across 32 non-benchmark `*_test.go` files importing
  `atc/db/dbfakes`; all remaining sites are
  reviewed narrow algorithmic, fault-injection, observation,
  transaction/listener, instrumentation, or timing seams.
- [ ] Record RED/GREEN/sensitivity evidence, commit IDs, full gates, and review
  outcomes below; commit this plan as
  `docs: record agent api postgres cleanup`. Do not push. The implementation
  commit, green gates, and final scoped re-review are recorded below, but the
  prescribed per-task focus/commit composites and mutation-only evidence are
  not, so this composite remains open.

## Observed completion evidence

- Implementation, interface cleanup, fixture wiring, and default-suite
  retirement landed atomically in `5507ea7258` (`test(api): persist agent API
  state and retire default database fakes`). The four planned code files were
  interwoven, so the commit intentionally consolidated the per-task commits.
- Feedback passed 3/3 serially and across nine processes. Its final specs bind
  the clone-backed store before projection, fail closed with no row, then prove
  durable team/snapshot scoping and canonical reviewer persistence after the
  production review projection exists.
- Transcripts passed 6/6 serially and across nine processes. The final specs
  use durable workflow/run/build/transcript rows, a distinguishable decoy run,
  non-null workflow-run IDs, and a mutex-safe one-method error decorator.
- Full API passed 825/825 serially and across nine processes; after the final
  fail-closed-default correction it passed 825/825 again in both modes.
  `go test ./atc/api`, `make test-integration` (24/24), and `make
  test-fly-integration` (680/680) also passed.
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
- Whole-branch review found that the default feedback memory store could still
  accept a healthy write and several nominally unavailable workflow-run reads
  returned nil-success empty state. A focused regression reproduced all ten
  permissive operations, then `5e4f903aba` replaced them with explicit
  unavailable adapters; the regression and both full-API modes passed.
- The same review found stale local-Colima provisioning. `7f1bce2ff3` changed
  the helper to readiness/DSN-only behavior, made the concurrency barrier query
  the external service directly, and aligned current runbooks with theborg's
  no-local-Docker policy. Helper/signal tests, postgresrunner tests, and a live
  distinct-clone concurrency run passed.
- No standalone mutation-only sensitivity log is recorded here. Prescribed
  focused checkpoints and per-task commit composites also remain open.
- Final review and closure evidence: the whole-branch review at `cfc7452ca6`
  found four Important findings and one Minor. The six-commit fix wave
  (`bc232dd968`, `3f74327afd`, `4800ff4ea9`, `b3123e78f8`, `d637e0fad1`, and
  `26dc5a1d9d`) addressed them. The scoped re-review of
  `cfc7452ca6..26dc5a1d9d` found all five addressed, no new
  Critical/Important/Minor issue, and authorized closure-document finalization.
