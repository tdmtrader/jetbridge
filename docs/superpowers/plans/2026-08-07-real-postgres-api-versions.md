# Real PostgreSQL API Versions Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Use one owner for `versions_test.go`, preserve concurrent edits, and stage
> only the task's assigned files.

**Goal:** Replace every generated database fake in the Versions API tests with
persisted resource versions, metadata, enabled/pinned state, build input/output
relationships, visibility, and clear-version outcomes from a unique PostgreSQL
template clone.

**Exact baseline and target:** At `c033d05492`,
`atc/api/versions_test.go` has 13 explicit generated database-fake constructor
sites: `FakePipeline` x1, `FakeResource` x7, `FakeBuild` x4, and
`FakeResourceType` x1. This phase targets 13 to 0, removes `dbfakes`, and
removes every reference to suite-level `dbTeamFactory` and `dbTeam` from the
file. The fake-backed `Versions API` baseline contains exactly 95 specs.

**Architecture:** Opt each converted Versions API Describe into the existing
`useRealDB` harness. Persist fixtures with `dbtest.Builder` and production DB
methods, build each handler from the clone's real dependencies, and derive all
IDs/timestamps/pagination from the saved graph. Direct SQL failures use a
preloaded real pipeline/resource/type whose secondary connection is closed
exactly once. Later-call failures use one-method embedding decorators around
healthy real objects. A tiny `Versions` or `PinVersion` protocol decorator may
represent only a defensive `found=false` result that production SQL cannot
naturally return; it must never manufacture a successful row or count.

**Files:** Modify only `atc/api/versions_test.go` and this plan. Do not modify
production code, generated fakes, shared API fixtures, Docker/service
lifecycle, benchmarks, corpus files, or another agent's work. Do not push.

## Global constraints

- [ ] Call `useRealDB()` exactly once per converted spec. Register every custom
  server and non-nil response body for cleanup, and close every secondary
  connection exactly once before clone drop.
- [ ] Persist all successful objects and outcomes. Decorators may route a real
  object, record arguments, or inject one exact failure/protocol result only;
  they may not fabricate successful versions, builds, pagination, mutations,
  or deletion counts.
- [ ] Keep access/authentication seams in memory. Persist pipeline visibility
  with `Expose`/`Hide` and resource metadata visibility with the real resource
  config rather than stubbing `Public`.
- [ ] Do not hard-code row IDs, check order, build names, timestamps, SQL
  ordering, or instance-var query encoding. Derive expectations from real
  objects and use `ConsistOf` when order is not contractual.
- [ ] Keep every incremental commit green under the entire `Versions API`
  focus. Capture real RED/GREEN and a restored sensitivity failure for each
  task; compile errors and deliberately injected failures do not count.
- [ ] Stage only exact owned paths and inspect the cached name list before each
  commit so concurrent Resources/Jobs work cannot be included.

## Task 1: Persist resource-version listing and pagination

**Payoff:** remove the list Describe's `FakeResource`; 13 to 12.

- [ ] Add a file-local fixture that creates one real team and pipeline at the
  requested `PipelineRef`, initializes `dbtest.NewBuilder`, shadows the server,
  and exposes helpers for a real resource, doomed pipeline/resource routing,
  requests, JSON decoding, and pipeline updates at the current config version.
- [ ] Seed versions with `dbtest.Builder.WithResourceVersions`, attach metadata
  through `WithVersionMetadata`, disable one version through the real resource,
  reload, and compare the JSON to dynamic real version IDs and enabled state.
  Retain the successful response's `Content-Type: application/json` assertion.
- [ ] Exercise metadata visibility with a real public pipeline and real
  resource `Public` configuration: unauthorized viewers receive metadata only
  when the resource is public; authorized viewers receive it for private
  resources. Preserve the existing 401/403 access matrix.
- [ ] Replace fake argument assertions with behavioral filter and pagination
  proofs. Seed enough ordered versions to exercise default limit, explicit
  `from`/`to`/`limit`, multiple filters, spaces, percent signs, and colon
  splitting. Derive cursor IDs from the persisted versions.
- [ ] Persist an instanced pipeline and build RFC5988 links using
  `PipelineRef.QueryParams()`. Do not assume query parameter order beyond the
  endpoint's actual header contract.
- [ ] Exercise missing resource naturally. Use a doomed real pipeline for
  `Pipeline.Resource` failure and a doomed real resource routed through a
  one-method pipeline decorator for `Resource.Versions` failure.
- [ ] The handler's `Versions(... found=false, err=nil)` 404 branch is not
  produced by the real `resource.Versions` implementation once a resource row
  is found: an empty scope returns `found=true` with an empty slice. Preserve
  that defensive branch only through a named real-backed decorator overriding
  `Versions` with `(nil, db.Pagination{}, false, nil)`, and comment the exact
  boundary.
- [ ] Capture RED/GREEN on a persisted version ID/metadata assertion and
  sensitivity by temporarily expecting the wrong persisted `Enabled` value,
  require that exact assertion to fail, then restore it. Run the list focus and
  complete Versions API focus serially and across 9 processes, format, compile,
  vet, diff-check, verify 13 to 12, obtain independent review, and commit as
  `test(api): persist version listing state`.

  ```bash
  gofmt -w atc/api/versions_test.go
  go test ./atc/api -run '^$'
  go test -tags=live ./atc/api -run '^$'
  ginkgo --procs=1 --focus='GET /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions( |$)' ./atc/api
  ginkgo --procs=1 --focus='Versions API' ./atc/api
  ginkgo --procs=9 --focus='Versions API' ./atc/api
  go vet ./atc/api
  go vet -tags=live ./atc/api
  git diff --check -- atc/api/versions_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/versions_test.go
  ```

  The focused persisted assertion must fail only during the deliberate wrong
  `Enabled` expectation, every command must otherwise pass, and the final
  search must report exactly 12 constructor sites.

## Task 2: Persist enable, disable, and pin mutations

**Payoff:** remove three `FakeResource` sites; 12 to 9.

- [ ] For disable success, seed a real version, issue the endpoint, reload, and
  assert it is disabled in `Resource.Versions`. For enable success, disable it
  first, issue the endpoint, reload, and assert it is enabled. Assert the exact
  dynamic version ID targeted by the URL.
- [ ] For pin success, seed a real version, issue the endpoint, reload, and
  assert `CurrentPinnedVersion` equals it. Use a wrong/nonexistent ID for the
  natural failure behavior where production supports it.
- [ ] Verify whether `PinVersion` can naturally return `found=false` for a
  nonexistent ID under current PostgreSQL semantics. If it instead returns a
  constraint/error result, retain the handler's defensive 404 branch only with
  a named decorator overriding `PinVersion` to `(false, nil)` and document why.
- [ ] Exercise resource-not-found naturally and `Pipeline.Resource` failure
  with a doomed real pipeline. Exercise each mutation's generic SQL failure by
  routing a doomed real resource through a healthy pipeline decorator.
- [ ] Exercise enable, disable, and pin immutability conflicts with real
  source-selection ownership. Insert a valid workflow definition and call
  `db.NewWorkflowResourceSourcePipelinesFactory(...).Activate(...)` using the
  real team/pipeline/config version, valid hashes, and a matching configured
  resource declaration. Seed/disable versions before activation because the
  mutations themselves must be the rejected operations.
- [ ] Preserve the complete status matrix separately for enable, disable, and
  pin: unauthenticated `401`, authenticated-but-unauthorized `403`, authorized
  success `200`, missing configured resource `404`, pipeline resource lookup
  failure `500`, mutation SQL failure `500`, and immutable ownership `409`.
  Pin must additionally retain its defensive `(found=false, err=nil)` `404`.
- [ ] Capture RED/GREEN on a persisted enabled/pinned outcome and sensitivity
  by temporarily expecting the wrong dynamic target version/pin, require the
  persisted assertion to fail, then restore it. Run all mutation focuses and the complete
  Versions API focus serially and in parallel, format/compile/vet/diff-check,
  verify 12 to 9, independently review the immutable graph and defensive
  decorator, and commit as `test(api): persist version mutations`.

  ```bash
  gofmt -w atc/api/versions_test.go
  go test ./atc/api -run '^$'
  go test -tags=live ./atc/api -run '^$'
  ginkgo --procs=1 --focus='versions/:resource_version_id/(enable|disable|pin)' ./atc/api
  ginkgo --procs=1 --focus='Versions API' ./atc/api
  ginkgo --procs=9 --focus='Versions API' ./atc/api
  go vet ./atc/api
  go vet -tags=live ./atc/api
  git diff --check -- atc/api/versions_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/versions_test.go
  ```

  The deliberate wrong target assertion must fail before restoration; all
  restored commands must pass and the search must report exactly 9 sites.

## Task 3: Persist input-to and output-of build relationships

**Payoff:** remove two `FakeResource` and four `FakeBuild` sites; 9 to 3.

- [ ] Save a real pipeline config with jobs and resources. Seed input versions,
  by running `dbtest.Builder.WithResourceVersions` for every version that will
  appear in an input mapping, then use `WithJobBuild` for the next-input
  mapping/adoption and output association. `WithJobBuild` does not itself
  create input `resource_config_versions` rows. Create at least two
  relationally valid builds for each endpoint and obtain the real
  resource-config-version IDs from the scenario.
- [ ] Start/finish builds through public DB methods as needed, reload them, and
  compare decoded presentation to their real IDs, names, statuses, owners, and
  times. Do not preserve fake started builds with completion timestamps. Do not
  assume query order because `GetBuildsWithVersionAsInput/Output` has no
  `ORDER BY`.
- [ ] Assert both a nonexistent numeric version ID and the existing nonnumeric
  literal `hello` return an empty `200` list naturally for each of `input_to`
  and `output_of`; this preserves the handler's `Atoi`-to-zero parse path.
  Exercise resource-not-found with a missing configured name and resource
  lookup failure with a doomed real pipeline.
- [ ] Exercise the later `GetBuildsWithVersionAsInput` and
  `GetBuildsWithVersionAsOutput` failures through two one-method pipeline
  decorators embedding the healthy persisted pipeline; a wholly closed
  pipeline would fail at the earlier resource lookup and would not reach the
  intended branch.
- [ ] Preserve the full visibility/status/header matrix for both endpoints:
  private + unauthenticated is `401`, private + authenticated but unauthorized
  is `403`, public + unauthorized is `200`, and authorized success is `200`
  with `Content-Type: application/json`. Also retain missing resource `404`,
  resource lookup failure `500`, and the later relationship-query `500`.
- [ ] Capture RED/GREEN on a persisted build/version association and
  sensitivity by temporarily expecting `build.ID()+1`, require that dynamic
  association assertion to fail, then restore it. Run both endpoint groups
  and the complete Versions API focus serially/across 9 processes, format,
  compile, vet, diff-check, verify 9 to 3, obtain independent review, and
  commit as `test(api): persist version build relationships`.

  ```bash
  gofmt -w atc/api/versions_test.go
  go test ./atc/api -run '^$'
  go test -tags=live ./atc/api -run '^$'
  ginkgo --procs=1 --focus='versions/:resource_version_id/(input_to|output_of)' ./atc/api
  ginkgo --procs=1 --focus='Versions API' ./atc/api
  ginkgo --procs=9 --focus='Versions API' ./atc/api
  go vet ./atc/api
  go vet -tags=live ./atc/api
  git diff --check -- atc/api/versions_test.go
  rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/versions_test.go
  ```

  The deliberate `build.ID()+1` assertion must fail before restoration; all
  restored commands must pass and the search must report exactly 3 sites.

## Task 4: Persist clear-resource and clear-resource-type outcomes

**Payoff:** remove the final `FakeResource`, `FakeResourceType`, and broad root
`FakePipeline`; 3 to 0.

- [ ] Seed three real resource versions, issue clear as an authenticated admin,
  assert the dynamic deletion count, reload the resource, and prove its version
  list is empty. Seed a decoy resource/scope and prove its versions survive.
- [ ] Seed three real resource-type versions through
  `dbtest.Builder.WithResourceTypeVersions`, issue clear, assert the deletion
  count, reload, and prove a different resource type's versions survive. The
  decoy type must use a distinct underlying type/source configuration, not
  merely a different name: resource-type scopes use `FindOrCreateScope(nil)`
  and identical configs intentionally share one scope.
- [ ] Exercise missing resource/type naturally and lookup failures with a
  doomed real pipeline. Exercise `ClearVersions` SQL failures with doomed real
  resource/type objects routed through one-method healthy pipeline decorators.
- [ ] Exercise resource-clear immutability with the real workflow resource
  source activation graph. Confirm the versions remain after 409. Resource
  types have no corresponding ownership guard; do not invent one.
- [ ] Preserve the clear-endpoint status/header matrices. Resource clear keeps
  authenticated non-admin `403`, admin success `200` with
  `Content-Type: application/json`, missing `404`, lookup `500`, clear failure
  `500`, and immutable `409`. Resource-type clear keeps unauthenticated `401`,
  authenticated non-admin `403`, admin success `200` with JSON Content-Type,
  missing `404`, lookup `500`, and clear failure `500`.
- [ ] Remove the broad fake root setup, `dbfakes`, and all suite-level
  `dbTeamFactory`/`dbTeam` references. Retain only access seams, closed real
  objects, and explicitly documented one-method decorators.
- [ ] Capture RED/GREEN on a persisted deletion/survivor assertion and
  sensitivity by temporarily expecting two removals after seeding three,
  require the exact count assertion to fail, then restore it. Run clear focuses and the entire Versions API
  focus serially/across 9 processes, format/compile/vet/diff-check, prove 3 to
  0, obtain independent review, and commit as
  `test(api): persist version cleanup state`.

  ```bash
  gofmt -w atc/api/versions_test.go
  go test ./atc/api -run '^$'
  go test -tags=live ./atc/api -run '^$'
  ginkgo --procs=1 --focus='DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/(resources/:resource_name|resource-types/:resource_type_name)/versions' ./atc/api
  ginkgo --procs=1 --focus='Versions API' ./atc/api
  ginkgo --procs=9 --focus='Versions API' ./atc/api
  go vet ./atc/api
  go vet -tags=live ./atc/api
  git diff --check -- atc/api/versions_test.go
  ! rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/versions_test.go
  ! rg -n 'atc/db/dbfakes|\b(dbTeamFactory|dbTeam)\b' atc/api/versions_test.go
  ```

  The deliberate `2` versus persisted `3` count assertion must fail before
  restoration; every restored command and both zero-match searches must pass.

## Task 5: Full verification and closure

- [ ] Run the exact final verification below. The converted Versions focus
  must still report all 95 behavior specs with one and nine processes.

  ```bash
  gofmt -w atc/api/versions_test.go
  test -z "$(gofmt -d atc/api/versions_test.go)"
  git diff --check -- atc/api/versions_test.go
  go test ./atc/api -run '^$'
  go test -tags=live ./atc/api -run '^$'
  go vet ./atc/api
  go vet -tags=live ./atc/api
  ginkgo --procs=1 --focus='Versions API' ./atc/api
  ginkgo --procs=9 --focus='Versions API' ./atc/api
  ginkgo --procs=1 ./atc/api
  go test ./atc/api -count=1
  ginkgo --procs=9 ./atc/api
  ! rg -n 'new\(dbfakes\.Fake|dbfakes\.Fake[A-Za-z0-9_]+\{' atc/api/versions_test.go
  ! rg -n 'atc/db/dbfakes|\b(dbTeamFactory|dbTeam)\b' atc/api/versions_test.go
  ```
- [ ] Prove zero generated constructors/imports and zero suite-team-fake
  references with exact `rg` searches. Inspect one clone per spec, dynamic IDs
  and ordering, body/server cleanup, exact-once doomed connection closes,
  decoy survival, ownership setup, and every decorator boundary.
- [ ] Obtain independent final review with no unresolved Critical, Important,
  or Minor finding. Record exact spec counts, commands, and sensitivity
  evidence; mark the plan complete and commit it as
  `docs: record api versions postgres conversion`.

## Phase acceptance

- [ ] `versions_test.go` reaches exactly 13 to 0 generated database-fake
  constructor sites, drops `dbfakes`, and no longer consumes `dbTeamFactory`
  or `dbTeam`.
- [ ] Listing, filters, pagination, metadata visibility, enabled/pinned state,
  build input/output relationships, and clear outcomes are persisted and
  asserted through real rows.
- [ ] Natural missing/immutable states use production DB behavior; retained
  seams are limited to access, closed secondary real objects, and narrow
  real-backed decorators for exact later-call failures or unreachable defensive
  `found=false` branches.
- [ ] Focused serial/9-process and full API verification pass against isolated
  clones in the single shared PostgreSQL service.
- [ ] No production behavior, shared fixture, unrelated file, Docker service,
  benchmark, or corpus change is included, and nothing is pushed.
