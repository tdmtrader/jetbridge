# Config API PostgreSQL Conversion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. Complete the GET and PUT commits separately;
> shared-worktree staging and commits are serialized by the parent agent.

**Goal:** Remove all four generated database/domain fake constructors and the
`dbfakes` import from `atc/api/config_test.go`, while preserving its exact 105
spec names and observing Config API state through one template-cloned
PostgreSQL database per spec.

**Architecture:** Reuse the API suite's `useRealDB` harness, which creates and
drops a unique migrated clone for every calling spec on the single shared
PostgreSQL server. Build each spec's handler and request only after nested
fixtures are finalized. Persist real teams, pipelines, configs, instance vars,
config versions, archive state, and workflow-run templates. Keep only narrow
embedded decorators for selective database errors and call/order observation;
continue using the existing access and credential-manager seams because they
are not database state.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL template clones, Concourse
`atc/db`, `rata`, PostgreSQL notification bus.

## Fixed scope and census

- Modify only `atc/api/config_test.go` in the two code commits and this plan in
  the planning/closure commits. Do not modify `api_suite_test.go`,
  `real_db_test.go`, production code, generated fakes, Docker, Colima, or the
  PostgreSQL service lifecycle. Do not push.
- Use `127.0.0.1:15432` and the existing `postgresrunner.GinkgoRunner` through
  `useRealDB()` exactly once per spec. Every Config API spec must own its own
  clone and cleanup.
- Preserve all `Describe`, `Context`, and `It` text. Capture dry-run names
  before and after; both lists must contain exactly 105 specs with an empty
  diff: GET 15 and PUT 90.
- Exact file census is four `new(dbfakes.Fake...)` sites to zero, one
  `dbfakes` import to zero, six qualified `dbfakes.*` references to zero,
  `dbTeamFactory` references nine to zero, and `dbTeam` references 29 to zero.
- `fakeAccess`, `fakeSecretManager`, and `fakeVarSourcePool` remain intentional
  non-database seams.

## Shared fixture and narrow seam design

- [x] Add mutex-protected file-local value/call types:
  `configAPISaveCall`, `configAPITeam`, `configAPIPipeline`, and
  `configAPITeamFactory`. Embed the real `db.Team`, `db.Pipeline`, and
  `db.TeamFactory`; delegate every healthy operation to PostgreSQL.
- [x] Record defensive copies of `Pipeline` refs, `SavePipeline` arguments,
  `FindTeam` names, and scanner-notify calls. Override only these selective
  fault ports, which healthy PostgreSQL cannot express at the required point:
  team lookup error, pipeline lookup error, pipeline config error, and save
  error. Preserve the existing exact sentinel messages.
- [x] Make the factory's healthy `FindTeam` delegate first and decorate only a
  found team. Real deletion must therefore produce real not-found behavior.
  Make healthy `NotifyResourceScanner` record and delegate so positive tests
  cover the production PostgreSQL notification too.
- [x] Shadow `realdb`, copied `deps`, and `server` separately inside each
  endpoint Describe, not in the shared Config API Describe. Each converted
  endpoint's setup calls `useRealDB()` once and stores route params, headers,
  query values, and body bytes only. Its `JustBeforeEach` applies finalized
  deps by assigning `realdb.Deps = deps`, starts `server = realdb.Serve()`,
  builds a request generator against that final server, creates the request,
  applies its state, sends it, and registers response-body cleanup. `Serve()`
  reads `realdb.Deps`, so mutating only the local copy would silently leave the
  decorators unwired. No nested context may mutate a handler or URL captured
  earlier. This separation is required so the GET-only first commit leaves the
  still-fake PUT endpoint wired to the package suite server and remains green.
- [x] Capture the current 105 dry-run names before behavior changes. Capture a
  persisted RED by wiring the real update path with the old literal version
  42: the real CAS must reject it rather than silently accepting a fake call.

## Task 1: Persist the 15 GET config specs — two constructors to zero

- [x] Create real team `a-team` and real pipeline `something-else` from
  `pipelineConfig`; route the handler through the recording decorators over
  those real objects. Assert the response config and dynamic persisted config
  version header.
- [x] For valid instance vars, save a real sibling pipeline whose ref contains
  `{branch: feature}` with a config distinguishable from the non-instanced
  seed. Require the recorded exact lookup ref plus the sibling's exact real
  config and dynamic config-version header, so a decorator returning the wrong
  pipeline cannot pass.
- [x] For malformed instance vars, replace the existing vacuous assertion on
  the unwired shared `dbTeam` with an empty local pipeline-lookup record and,
  where stable, an empty team-lookup record. This must prove parsing precedes
  database lookup.
- [x] Archive the real pipeline for the archived 404 and destroy it for the
  not-found 404. Delete the real team for the team-not-found 404. Do not
  fabricate these states.
- [x] Use only the narrow pipeline-lookup, config-read, and team-lookup error
  fields for the three 500 paths. Their embedded successful state remains real.
- [x] Retain access/auth behavior through `fakeAccess`; it is outside the
  database conversion.
- [x] Sensitivity: temporarily make malformed vars valid and require the fixed
  no-lookup assertion to fail; temporarily unarchive or retain the pipeline
  and require the corresponding 404 outcome to fail. Restore each mutation.
- [x] Run gofmt, compile, exact 15-spec focus serially and across nine
  processes, focused name diff, zero GET constructors, an exact two remaining
  PUT constructors plus the still-needed file import, diff check, and an
  independent review with no unresolved findings. Commit only the file as:
  `test(api): persist config GET specs in postgres`.

## Task 2: Persist the 90 PUT config specs — two constructors to zero

- [x] Create real `a-team` and seed real `a-pipeline` with `pipelineConfig` in
  each spec. Capture its actual `ConfigVersion()` as `fromVersion`; use that
  value in the request header and assertions instead of literal 42 so updates
  exercise production compare-and-swap.
- [x] For successful JSON/YAML and credential-validated updates, assert the
  exact recorded ref/config/from/`initiallyPaused=true`, then re-fetch the
  pipeline and require the decoded config, its existing unpaused state to be
  preserved, and a changed config version. `initiallyPaused` applies to inserts;
  a true value does not pause an existing pipeline. Never rely only on recorder
  state.
- [x] For every existing does-not-save assertion, require zero recorded saves
  and, where applicable, re-fetch the original config/version unchanged.
  Continue using the non-database credential seam for credential existence.
- [x] Preserve identifier-warning behavior. For `_team/_pipeline`, create the
  real requested team and leave the pipeline absent so the warning path can
  perform a genuine create. In the existing warning-body `It`, also require
  status 201 and re-fetch
  `realRequestedTeam.Pipeline(atc.PipelineRef{Name: "_pipeline"})` to assert the
  exact persisted config, paused insert state, and dynamic config version.
  Empty identifiers must fail before lookup/save.
- [x] For first creation, destroy the seed before the request and require real
  created status 201, a persisted paused pipeline, and no scanner notification.
  For normal updates, require status 200 and a changed persisted row.
- [x] For valid instance vars, persist/re-fetch the exact
  `PipelineRef{Name: "a-pipeline", InstanceVars: {branch: feature}}` and assert
  recorder arguments and database identity.
- [x] Delete the real team for team-not-found. Use only the narrow team lookup
  and save error fields for exact 500/error-body behavior.
- [x] For the immutable-template 409, destroy the ordinary seed, set
  `templateConfig.Template=true`, and call
  `db.NewWorkflowRunTemplateFactory(realdb.Conn, realdb.LockFactory).SaveWorkflowRunTemplate(context.Background(), realTeam.ID(), atc.PipelineRef{Name: "a-pipeline"}, templateConfig)`.
  Require creation and prove the registry row with
  `IsWorkflowRunTemplate(context.Background(), pipeline.ID())`, then send the
  ordinary PUT and require 409. A plain template-shaped pipeline is not enough.
- [x] In positive notification specs, subscribe with
  `signal, err := realdb.Conn.Bus().ListenSignal(atc.ComponentLidarScanner)` and
  immediately register
  `DeferCleanup(func() { Expect(realdb.Conn.Bus().UnlistenSignal(atc.ComponentLidarScanner, signal)).To(Succeed()) })`.
  Then require one delegated recorder call and an eventually received real
  signal. For create/no-notify specs, require zero calls and consistently no
  signal. Clone isolation prevents cross-spec notification leakage under
  parallel execution.
- [x] Remove all remaining `dbfakes`, `dbTeamFactory`, and `dbTeam` references
  from this file. Remove `gbytes` too if the late-bound body state makes it
  unused.
- [x] Sensitivity, one restored mutation at a time: use `fromVersion+1` and
  require a success spec to fail; suppress scanner delegation and require a
  positive notification spec to fail; create only an ordinary template-shaped
  pipeline and require the 409 spec to fail; clear the save sentinel and
  require its 500/body specs to fail; in a destroyed-seed first-create spec,
  delegate `initiallyPaused=false` and require the reloaded paused assertion to
  fail. Do not use an existing unpaused update for this mutation because its
  persisted pause state intentionally remains unchanged either way.
- [x] Run gofmt, compile, exact 90-spec PUT focus serially and across nine
  processes, the complete 105-spec Config focus serially and across nine
  processes, full API suite serially and across nine processes, `go test
  ./atc/api -count=1`, vet, diff/name/census checks, and independent review with
  no unresolved findings. Commit only the file as:
  `test(api): persist config PUT specs in postgres`.

## Required verification

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres

ginkgo --dry-run --focus='Config API' \
  --json-report=/private/tmp/config-api-before.json ./atc/api
# extract sorted full names to /private/tmp/config-api-before.names

ginkgo --procs=1 \
  --focus='Config API.*GET /api/v1/teams/:team_name/pipelines/:name/config' ./atc/api
ginkgo --procs=9 \
  --focus='Config API.*GET /api/v1/teams/:team_name/pipelines/:name/config' ./atc/api
ginkgo --procs=1 \
  --focus='Config API.*PUT /api/v1/teams/:team_name/pipelines/:name/config' ./atc/api
ginkgo --procs=9 \
  --focus='Config API.*PUT /api/v1/teams/:team_name/pipelines/:name/config' ./atc/api
ginkgo --procs=1 --focus='Config API' ./atc/api
ginkgo --procs=9 --focus='Config API' ./atc/api
go test ./atc/api -count=1
ginkgo --procs=1 ./atc/api
ginkgo --procs=9 ./atc/api
go vet ./atc/api

gofmt -w atc/api/config_test.go
git diff --check -- atc/api/config_test.go
! rg -n 'dbfakes|\bdbTeamFactory\b|\bdbTeam\b' atc/api/config_test.go
test "$(rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/config_test.go | wc -l | tr -d ' ')" = 0
# extract after names, then require empty diff and 105/105 line counts
```

## Final acceptance and closure

- [x] Both code commits pass independently and together. Exact spec names are
  unchanged at 105; GET is 15/15 and PUT is 90/90 serially and across exactly
  nine processes on unique per-spec clones of the one PostgreSQL instance.
- [x] The file moves 4→0 constructors/imports and no generated database fake is
  moved elsewhere. All healthy Config API success state comes from PostgreSQL;
  only documented selective fault decorators and non-database access/secret
  seams remain.
- [x] Record exact RED/GREEN/sensitivity results, counts, full gates, commit
  IDs, and independent reviewer outcomes below. Commit only this plan as
  `docs: record api config postgres conversion`. Do not push.

## Observed completion evidence

- Code commits: GET `2ee418c211` (`test(api): persist config GET specs in
  postgres`) and PUT `b225c3bb6c` (`test(api): persist config PUT specs in
  postgres`). Each commit changed only `atc/api/config_test.go`.
- Census: the file moved from four generated database constructors and one
  `dbfakes` import to zero. `dbTeamFactory`, `dbTeam`, and qualified
  `dbfakes.*` references are also zero; the access and credential seams remain
  intentionally non-database.
- Names: sorted dry-run names were byte-identical before and after, with 105
  total specs: 15 GET and 90 PUT.
- GET verification: the exact 15-spec focus passed serially and with nine
  processes; the still-unconverted 90 PUT specs also passed at the GET commit
  boundary. Persisted archive/destroy/team-delete paths and malformed-vars
  no-lookup sensitivities failed when deliberately inverted, then passed after
  restoration. Independent review reported no findings.
- PUT TDD evidence: wiring the real update path while retaining literal config
  version 42 failed because production compare-and-swap rejected the stale
  version, as required. Separately, the old/fake-server persisted assertion
  failed before the endpoint was late-bound to the real database. Each required
  mutation failed for its intended reason and was restored: `fromVersion+1`,
  suppressed scanner delegation, ordinary template-shaped pipeline in place of
  the workflow-template registry row, cleared save sentinel, and
  `initiallyPaused=false` on first creation.
- PUT verification: 90/90 passed serially and with nine processes. The complete
  Config focus passed 105/105 serially and with nine processes after the final
  review correction. Compile-only, `go vet`, formatting, diff, name, and census
  checks passed. The full API focus passed 825/825 serially and with nine
  processes, and `go test ./atc/api -count=1` passed; these full-suite runs
  preceded an assertion-only review correction, after which the complete
  105-spec Config focus was rerun in both modes.
- Review: an initial independent PUT review found one P2 because six credential
  success cases derived their expected config from the recorder under test.
  The tests now independently decode each exact request payload before checking
  both the recorder and persisted row. Final re-review returned PASS with no
  remaining findings.
