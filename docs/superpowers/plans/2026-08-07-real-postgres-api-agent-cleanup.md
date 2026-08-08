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
- The live product-test census is 115 generated constructors / 37 `dbfakes`
  import files (excluding `bench/corpus/**`). Completing the final three local
  Builds constructors removes those sites and that file's import, producing a
  post-Builds checkpoint of 112 / 36. This plan then removes one transcript
  constructor plus 15 default-suite constructors, yielding 96 / 36.
  Feedback removes success-state fake behavior but no constructor by itself.
- Retain the two default `FakeTeamFactory` constructors, `FakeTeam`, and
  `FakeWorkerFactory` until a later full-suite-backed audit proves their
  remaining authorization/fault roles can be replaced. Do not claim 92 merely
  by deleting the default `FakeTeam` without behavioral evidence.

## Task 1: Persist the three feedback specs

- [ ] Add `feedbackStore feedback.Store` to `apiDBDeps`. Default suite deps use
  a fresh `feedback.NewMemoryStore()` per spec; `useRealDB()` uses
  `db.NewAgentFeedbackFactory(conn)`. Pass `deps.feedbackStore` to
  `api.NewHandler` and remove the package-global `apiFeedbackStore`.
- [ ] In each feedback spec, call `useRealDB()`, create real team `research`,
  assign finalized deps, and serve locally. Seed the production review
  projection required by feedback's foreign-key/authorization semantics,
  retaining snapshot ID `9007199254740993` to cover values above 2^53.
- [ ] Seed only clone-local valid rows: snapshot and upload production identity,
  then `db.NewAgentReviewsFactory(conn).UpsertReviewProjection(...)`. Read back
  with `db.NewAgentFeedbackFactory(conn).GetByReviewSnapshot`.
- [ ] Success requires status 201, real `research` distinct from `main`, durable
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

- [ ] Narrow `apiDBDeps.transcripts` from the full generated factory interface
  to `transcriptserver.Store`, the handler's exact one-method read contract.
  Default suite deps use nil (the handler's documented empty/404 behavior);
  `useRealDB()` retains `db.NewAgentRunTranscriptFactory(conn)`.
- [ ] Remove the suite `fakeAgentRunTranscriptFactory` global/allocation and
  replace it in six specs with endpoint-local real DB/deps/server setup. Add a
  mutex-safe embedded error decorator that overrides only
  `ListByWorkflowRun` for the existing 500 context.
- [ ] Seed a durable workflow definition and explicit run ID
  `9007199254740993` for workflow `review`, valid schema/signature versions and
  hashes, plus real one-off builds and
  `db.NewAgentRunTranscriptFactory(conn).Upsert(...)` transcript rows. Returned
  persisted rows must all carry the non-null workflow-run ID; remove the old
  impossible fake-only expectation that omitted it.
- [ ] Prove workflow-name and run-ID scoping behaviorally with distinguishable
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

- [ ] Delete the globals/allocations/default assignments for these ten
  unreachable pass-through dependencies, leaving their `apiDBDeps` fields nil:
  `FakePipelineFactory`, `FakeJobFactory`, `FakeResourceFactory`,
  `FakeResourceConfigFactory`, `FakeUserFactory`, `FakeCheckFactory`,
  `FakePipelineRunFactory`, `FakeWall`, `FakeSigningKeyFactory`, and
  `FakeVolumeRepository`.
- [ ] Narrow `apiDBDeps.workflowRuns` to `ticketjournal.RunStore`; use the
  existing non-nil `unavailableWorkflowRunBackend{}` in default deps and retain
  the real workflow-run factory in `useRealDB()`. This removes
  `FakeAgentWorkflowRunsFactory`.
- [ ] Add a non-nil `unavailableExperimentStore` implementing
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

- [ ] Only after `atc/api/builds_test.go` has zero references to
  `dbBuildFactory`, `fakePipeline`, and package-global `build`, delete the
  corresponding `FakeBuildFactory`, `FakePipeline`, and `FakeBuild` globals and
  allocations, plus `dbTeam.PipelineReturns(fakePipeline, true, nil)`.
- [ ] Require a repository-wide zero-reference check for those three suite
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

- [ ] Exact names remain 3 feedback / 6 transcript. All success state comes
  from each spec's clone; only the transcript error decorator and explicit
  unavailable defaults remain as documented non-success seams.
- [ ] Final exact product-test census is 96 constructors / 36 import files after
  Builds and all four tasks, excluding `bench/corpus/**`. Record any later
  `FakeTeam` reduction separately only after full behavioral proof.
- [ ] Record RED/GREEN/sensitivity evidence, commit IDs, full gates, and review
  outcomes below; commit this plan as
  `docs: record agent api postgres cleanup`. Do not push.

## Observed completion evidence

Record evidence only after final acceptance passes.
