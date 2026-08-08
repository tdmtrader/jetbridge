# Accessor Team PostgreSQL Conversion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans`. The parent serializes staging and commits.

**Goal:** Remove the final three ordinary-state generated database fakes from
the Accessor package by loading team name, administrator status, and provider
authorization from a unique template-cloned PostgreSQL database in every
team-sensitive spec.

**Architecture:** Preserve the package's existing synchronized shared-server
harness and add a small fixture wrapper that exposes both its real
`db.TeamFactory` and clone-local connection. Team-insensitive token/claim specs
continue to pass no teams and allocate no clone. Team-sensitive nested specs
declare three mutable fixture values before `JustBeforeEach`; a lazy helper
creates the clone once, persists the rows once, SQL-sets the otherwise
unexposed administrator flag, reloads every row, and passes only real
`db.Team` objects to `accessor.NewAccessor`. The three role tables create the
same fixture inside each entry and reconstruct their subject after persistence.

**Tech Stack:** Go, Ginkgo v2, Gomega, PostgreSQL shared server and per-spec
template clones, Concourse `db.TeamFactory`, Accessor authorization.

## Fixed scope and census

- Modify only `atc/api/accessor/accessor_test.go`,
  `atc/api/accessor/accessor_suite_test.go`, and this plan. Do not modify
  production code, generated fakes, PostgreSQL lifecycle, Docker, or Colima.
  Do not push.
- Preserve all 85 exact specs under the root `Accessor` describe and all 134
  package specs. Capture and compare sorted full names from Ginkgo JSON
  dry-runs.
- `accessor_test.go` moves from three `FakeTeam` constructors and one `dbfakes`
  import to zero. No generated fake may move into a helper or another file.
- Current repository checkpoint is 115 constructors / 37 `dbfakes` test
  imports. After Builds it is 112 / 36; after the reviewed agent/suite cleanup
  it is 96 / 36; this conversion yields 93 / 35. These counts exclude
  `bench/corpus/**` and use the repository-wide requested-pattern census.
- Retain `FakeDisplayUserIdGenerator`: display-ID generation is a non-database
  algorithm seam and is outside this conversion.

## Task 1: Extend the existing real-team fixture without disturbing callers

- [ ] Keep `useRealTeamFactory()` source-compatible for the seven existing
  real-DB test files. Factor its clone, connection, singleton-lock, and cleanup
  setup into a private fixture that also exposes the clone-local `db.DbConn`.
  One invocation must still mean one `CreateTestDBFromTemplate` call.
- [ ] Add a helper that persists an `atc.Team{Name, Auth}` through the real
  factory. When the fixture requests administrator status, update only that
  clone-owned row's `teams.admin` column by dynamic row ID, require exactly one
  affected row, then refetch by name. Always return the freshly scanned real
  object so `Name()`, `Admin()`, and `Auth()` are database reads, not input
  echoes.
- [ ] Keep LIFO cleanup: close all ordinary/singleton connections before the
  unique clone is dropped. Do not start, stop, or reconfigure the shared
  machine-wide PostgreSQL service.

## Task 2: Replace the three Accessor `FakeTeam` values

- [ ] Replace `fakeTeam1/2/3` with declarative fixture values containing only
  name, admin flag, and auth. Default names remain `some-team-1/2/3`; default
  auth is empty and default admin is false.
- [ ] Team-insensitive `HasToken`, `IsAuthenticated`, `IsSystem`, `Claims`,
  and display-only `UserInfo` specs pass an empty team slice and do not call the
  DB helper. Every `IsAuthorized`, role-table, `TeamNames`, `IsAdmin`, and
  `TeamRoles` spec that observes team behavior calls the lazy real fixture
  exactly once and receives three freshly loaded rows.
- [ ] Translate nested `NameReturns`, `AdminReturns`, and `AuthReturns` setup to
  fixture mutations that happen before persistence. For the three
  `DescribeTable` bodies, set the entry-specific fixture, persist once inside
  that entry, reconstruct `access`, then assert the unchanged expectation.
- [ ] Preserve the deliberately arbitrary admin-team names (`some-team` and
  `not-some-team`) so authorization remains based on the persisted admin bit,
  not on the special default-team name. Preserve all exact user/group/provider
  auth maps, including the Cloud Foundry `cf:` normalization cases.
- [ ] Assert fixture sanity at the persistence boundary: three distinct
  positive IDs, exact names/admin flags/auth maps after reload, and no extra
  team row. Assertions about Accessor output remain behavioral and unchanged.

## TDD and sensitivity evidence

- [ ] Record the fake-backed 85-spec baseline. For persisted RED, seed the
  intended rows but temporarily give a representative owner/admin
  authorization spec an empty real-team slice; require its positive result to
  fail, then bind the freshly scanned rows and pass.
- [ ] For sensitivity, temporarily persist the owner auth on the wrong row and
  flip the intended admin row to non-admin; require the exact `TeamNames` and
  `IsAdmin` assertions to fail, then restore the intended fixtures. Do not
  leave deliberate failures in the final tree.
- [ ] Independently review the final diff for one-clone-per-team-sensitive-spec
  isolation, real row reloads, unchanged role semantics, table-entry setup
  order, and absence of generated DB fakes.

## Required verification and closure

```bash
pg_isready -h 127.0.0.1 -p 15432 -U postgres
ginkgo --dry-run --json-report=/private/tmp/accessor-after.json ./atc/api/accessor
ginkgo --procs=1 --focus-file='accessor_test.go' ./atc/api/accessor
ginkgo --procs=9 --focus-file='accessor_test.go' ./atc/api/accessor
ginkgo --procs=1 --fail-fast ./atc/api/accessor
ginkgo --procs=9 --fail-fast ./atc/api/accessor
go test ./atc/api/accessor -count=1
go vet ./atc/api/accessor
gofmt -d atc/api/accessor/accessor_test.go atc/api/accessor/accessor_suite_test.go
git diff --check
```

- [ ] Exact names remain 85/85 for `Accessor` and 134/134 for the package.
  The target file has zero generated database constructors/imports, and the
  repository census is 93 / 35 after all prerequisite active tasks land.
- [ ] Record RED/GREEN/sensitivity evidence, exact counts, review outcome,
  full gates, and commit ID below. Commit the two code files as
  `test(api): persist accessor team authorization`, then commit this plan as
  `docs: record accessor postgres conversion`. Do not push.

## Observed completion evidence

Record evidence only after final acceptance passes.
