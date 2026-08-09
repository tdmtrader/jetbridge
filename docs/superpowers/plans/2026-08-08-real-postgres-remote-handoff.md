# Remote handoff record: real-PostgreSQL test conversion

> **Status (2026-08-09): superseded and closed as a handoff.** The original
> theborg continuation was superseded when that remote host died. Work resumed
> on the Mac's dedicated external PostgreSQL service and reached the reviewed
> implementation HEAD below. This document preserves the useful historical handoff
> context; it is not an active theborg runbook.

## Authoritative closure state

The exact requested constructor pattern includes both `new(dbfakes.Fake...)`
and `&dbfakes.FakeX{}`, excluding `bench`, `benchmark(s)`, and `corpus` paths.

- Baseline `57fae3a5fdba95c1d6507d4766386cd532da7429`: 606 constructors across
  134 non-benchmark `*_test.go` files importing `atc/db/dbfakes`.
- Reviewed implementation HEAD `26dc5a1d9d0b2ac54fd05b474f2f9ad0c5779746`: 86
  constructors across 32 such importer files.
- The reduction is 520 constructors and 102 importer files (approximately 86%
  of constructor callsites).
- The syntax split at HEAD is 83 `new(dbfakes.Fake...)` sites and three
  composite/address literals. The three composite literals are metric seams in
  `atc/metric/query_counter_test.go` and `atc/metric/periodic_test.go`.
- Semantically, all 86 remaining sites are reviewed narrow algorithmic,
  fault-injection, observation, transaction/listener, instrumentation, or
  timing seams. There is no worker-suite bootstrap category and no healthy
  persistence-state fake.

Fresh census commands and their expected output:

```bash
rg -o --no-filename --glob '*_test.go' \
  --glob '!**/bench/**' --glob '!**/benchmark/**' \
  --glob '!**/benchmarks/**' --glob '!**/corpus/**' \
  'new\(dbfakes\.Fake[[:alnum:]_]+\)|&dbfakes\.Fake[[:alnum:]_]+\{' . | wc -l

rg -l --glob '*_test.go' \
  --glob '!**/bench/**' --glob '!**/benchmark/**' \
  --glob '!**/benchmarks/**' --glob '!**/corpus/**' \
  '"github.com/concourse/concourse/atc/db/dbfakes"' . | wc -l
```

Expected output: `86` and `32`.

## Post-rebase publication verification

On 2026-08-09 the completed conversion was rebased onto `origin/jetbridge`.
The rebase retained JetBridge's later checkpoint and pull-request subsystem
deletions without resurrecting their tests, while preserving the PostgreSQL
conversion. Fresh post-rebase verification passed:

- the API suite, 825/825 serially and 825/825 with nine processes;
- `go test ./atc/api`, the complete 229-spec engine suite, `go test
  ./atc/atccmd`, and `go test ./atc/postgresrunner`;
- the complete `atc/db/migration` package at the current `1773106167` head;
- the exact 86/32 constructor/importer census and formatting checks.

The seven migration-head failures recorded below belong to the pre-rebase
`1773106159` implementation. JetBridge has since advanced both the embedded
head and preflight constant to `1773106167`; the formerly failing migration
specs pass. A complete post-rebase `make test-unit` run was not repeated, so
this record does not claim that broader target green.

## Historical remote-handoff context

The original handoff targeted a dedicated worktree on theborg from
`origin/claude/mock-testdouble-audit-b88f3f` in the `tdmtrader/jetbridge` fork.
It correctly required isolation from the live Concourse PostgreSQL data,
superuser-capable test credentials, validated `cc_tpl_*`/`cc_db_*` clone names,
and no Docker installation on theborg. That environment was never the final
closure environment: the remote host died, and the Mac's dedicated external
service supplied the reviewed runtime evidence instead.

At the time of the original handoff, no commits after `db597f2dd0` had been
pushed. That statement is preserved only as historical context and is
superseded by the post-rebase verification described above.

## Key landed commits

- `d18d5baae3` — artifact repository API state
- `42d52d7ff3` — container API state
- `0ebaf2796a` — builds API state
- `f6ced51f7c` — accessor authorization state
- `20f7b7c4bb` — remaining team API state
- `d3b5617b1b` — remaining pipeline API state
- `5507ea7258` — agent API persistence and default-suite fake retirement
- `7f1bce2ff3` — external shared-service PostgreSQL workflow
- `5e4f903aba` — fail-closed default API database adapters
- `d5cf3deacd` — current environment/test prerequisite documentation
- `a8e33aedfa` — earlier final-review harness hardening
- `bc232dd968` — fail-closed worker defaults and final three suite constructors
- `3f74327afd` — semantic URI normalization and signal lifecycle hardening
- `4800ff4ea9` — fixture PID retirement
- `b3123e78f8` — fail-safe hijack teardown
- `d637e0fad1` — Makefile comment alignment
- `26dc5a1d9d` — teardown-unwind regression

## Reviewed runtime and static evidence

At the reviewed implementation HEAD, the following evidence is recorded from
the Mac's dedicated external PostgreSQL service:

- API: 825/825 serial and 825/825 with nine processes; `go test ./atc/api` and
  `go vet ./atc/api ./atc/postgresrunner` are green.
- Workers focus: 21/21 serial and with nine processes.
- Container hijack focus: 22/22 serial and with nine processes.
- The deterministic concurrency harness passed three consecutive runs and Bash
  syntax is green.
- Both live default and encoded/duplicate-URI concurrency runs observed two
  distinct clones and passed.
- The exact 86/32 census, formatting, and `git diff --check` are green.

At that pre-rebase implementation head, `make test-unit` was honestly red at
154/155 suites: seven known migration-head specs expected `1773106160`, while
embedded/preflight migrations stopped at `1773106159`. This predates the
conversion and remains valid historical evidence, but it is not the current
post-rebase migration state described above.

## Final-review history

The initial whole-branch review at `cfc7452ca6` found four Important issues and
one Minor. The single six-commit fix wave listed above addressed all five
findings. The required scoped re-review of `cfc7452ca6..26dc5a1d9d` then found
all five addressed, no new Critical/Important/Minor breakage, and explicitly
authorized closure-document finalization.

## Closure-record status

The five conversion plans have been updated against the reviewed implementation
head.
Completed implementation, static/runtime evidence, and independent review are
recorded as complete where each checkbox's own requirements are satisfied. The
Teams and Pipelines Task 1 and Task 2 verification boxes are checked because
their exact focus/count, static checks, review PASS, and source-only commits
are recorded.

The following historical checkpoints intentionally remain open:

- persisted/mutation-only RED and sensitivity experiments that were never run;
- agent and Builds composite checkpoints lacking prescribed individual
  focus/commit evidence;
- Teams Task 3's prescribed focused-coverage and source-only commit evidence;
- any composite closure box that also requires one of those unrecorded items.

Those open boxes do not negate the completed reviewed-implementation
review/runtime record;
they prevent broader green suites from being misrepresented as historical TDD
or commit-shape evidence.

## Final reporting guidance

Report the 606-to-86 constructor and 134-to-32 importer-file reduction using
the exact non-benchmark `*_test.go` importer scope; explain the 83/three syntax
split and the semantic seam classification. Include the two-stage review
result, the six-commit fix wave, the deliberately open historical checkpoints,
and the post-rebase verification above. If mentioning the old 154/155
`make test-unit` result, identify it as pre-rebase history: the current
JetBridge migration head is `1773106167`, the formerly failing specs pass, and
the complete post-rebase `make test-unit` target was not rerun.
