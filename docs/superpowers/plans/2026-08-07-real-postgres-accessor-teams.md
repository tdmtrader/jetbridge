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
- Historical pre-conversion repository checkpoint was 115 constructors across
  37 non-benchmark `*_test.go` files importing `atc/db/dbfakes`. After Builds
  it was 112 / 36; after the reviewed agent/suite cleanup it was projected to
  be 96 / 36; this conversion was projected to yield 93 / 35. The work landed
  in a different order, and the accepted final branch census is 86 constructors
  across 32 such importer files. These historical checkpoints use the
  repository-wide requested-pattern census.
- Retain `FakeDisplayUserIdGenerator`: display-ID generation is a non-database
  algorithm seam and is outside this conversion.

## Task 1: Extend the existing real-team fixture without disturbing callers

- [x] Keep `useRealTeamFactory()` source-compatible for the seven existing
  real-DB test files. Factor its clone, connection, singleton-lock, and cleanup
  setup into a private fixture that also exposes the clone-local `db.DbConn`.
  One invocation must still mean one `CreateTestDBFromTemplate` call.
- [x] Add a helper that persists an `atc.Team{Name, Auth}` through the real
  factory. When the fixture requests administrator status, update only that
  clone-owned row's `teams.admin` column by dynamic row ID, require exactly one
  affected row, then refetch by name. Always return the freshly scanned real
  object so `Name()`, `Admin()`, and `Auth()` are database reads, not input
  echoes.
- [x] Keep LIFO cleanup: close all ordinary/singleton connections before the
  unique clone is dropped. Do not start, stop, or reconfigure the shared
  machine-wide PostgreSQL service.

## Task 2: Replace the three Accessor `FakeTeam` values

- [x] Replace `fakeTeam1/2/3` with declarative fixture values containing only
  name, admin flag, and auth. Default names remain `some-team-1/2/3`; default
  auth is empty and default admin is false.
- [x] Team-insensitive `HasToken`, `IsAuthenticated`, `IsSystem`, `Claims`,
  and display-only `UserInfo` specs pass an empty team slice and do not call the
  DB helper. Every `IsAuthorized`, role-table, `TeamNames`, `IsAdmin`, and
  `TeamRoles` spec that observes team behavior calls the lazy real fixture
  exactly once and receives three freshly loaded rows.
- [x] Translate nested `NameReturns`, `AdminReturns`, and `AuthReturns` setup to
  fixture mutations that happen before persistence. For the three
  `DescribeTable` bodies, set the entry-specific fixture, persist once inside
  that entry, reconstruct `access`, then assert the unchanged expectation.
- [x] Preserve the deliberately arbitrary admin-team names (`some-team` and
  `not-some-team`) so authorization remains based on the persisted admin bit,
  not on the special default-team name. Preserve all exact user/group/provider
  auth maps, including the Cloud Foundry `cf:` normalization cases.
- [x] Assert fixture sanity at the persistence boundary: three distinct
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
- [x] Independently review the final diff for one-clone-per-team-sensitive-spec
  isolation, real row reloads, unchanged role semantics, table-entry setup
  order, and absence of generated DB fakes. The final scoped re-review of
  `cfc7452ca6..26dc5a1d9d` passed with no Critical, Important, or Minor finding.

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

- [x] Exact names remain 85/85 for `Accessor` and 134/134 for the package.
  The target file has zero generated database constructors/imports, and the
  repository census is 86 constructors across 32 non-benchmark `*_test.go`
  files importing `atc/db/dbfakes` on the final branch.
- [ ] Record RED/GREEN/sensitivity evidence, exact counts, review outcome,
  full gates, and commit ID below. Commit the two code files as
  `test(api): persist accessor team authorization`, then commit this plan as
  `docs: record accessor postgres conversion`. Do not push. The source-only
  code commit, green gates, count, and independent review are recorded below;
  the persisted RED and sensitivity evidence is not, so this composite remains
  open.

## Observed completion evidence

- Implementation commit: `f6ced51f7c` (`test(api): persist accessor team
  authorization`), changing only `accessor_test.go` and
  `accessor_suite_test.go`.
- The target `Accessor` describe passed 85/85 serially and across nine
  processes; the complete Accessor package passed 134/134 in both modes. The
  target file has no generated database constructor or `dbfakes` import.
- Inspection confirms source-compatible fixture delegation, clone-local admin
  updates by dynamic row ID, fresh row reloads, LIFO connection cleanup, lazy
  allocation for team-sensitive specs, and three-row ID/name/admin/auth sanity
  checks at the persistence boundary.
- Broader acceptance passed the complete API suite 825/825 serially and across
  nine processes, `go test ./atc/api`, `make test-integration` (24/24), and
  `make test-fly-integration` (680/680).
- Static validation passed `go vet ./atc/api/accessor`, `gofmt -d`, and
  `git diff --check`.
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
- No separate mutation-only evidence is recorded here, so the corresponding
  TDD/sensitivity items and the composite closure record remain open even
  though the independent review is now complete.
- Final review and closure evidence: the whole-branch review at `cfc7452ca6`
  found four Important findings and one Minor. The six-commit fix wave
  (`bc232dd968`, `3f74327afd`, `4800ff4ea9`, `b3123e78f8`, `d637e0fad1`, and
  `26dc5a1d9d`) addressed them. The scoped re-review of
  `cfc7452ca6..26dc5a1d9d` found all five addressed, no new
  Critical/Important/Minor issue, and authorized closure-document finalization.
