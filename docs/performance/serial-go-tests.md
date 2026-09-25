# Serial Go test performance

Benchmark base: `core` at `d01185fbc1c6a5a140ede4b34bcdf1761dbb6caa`.
Measured on Linux amd64 with Go 1.25.6, PostgreSQL 15.19, and Ginkgo 2.27.3.
These historical measurements precede the integration of this change onto
`core` at `08e8be2aff81a34a09e6583b666885bc7efa486b`. They are not timings of
that newer revision.

The target is the complete package selection used by `make test-unit`, run
serially. The original and candidate builds are warmed before timing.

## Changes

- Send fixture SQL directly to the real PostgreSQL server through pgx instead
  of starting a `psql` process for every command. Each operation closes its
  connection. Existing clone/drop and truncate/reset lifecycles are unchanged.
- On PostgreSQL 15 or newer, clone the migrated template with `FILE_COPY`.
  Earlier servers keep their existing creation command. This changes copying
  strategy, not database contents or isolation. PostgreSQL documents the
  strategy and its checkpoint tradeoff in
  [CREATE DATABASE](https://www.postgresql.org/docs/15/sql-createdatabase.html).
- Parse the immutable embedded migration definitions once per process and
  return independent slices to callers. Database state checks, migration
  execution, encryption handling, and locks still run normally. Explicitly
  supplied filesystems are reread on every call.

## Preservation of coverage and reliability

Within this first phase, all 1,023 original Go test files were byte-for-byte unchanged, as were the
Makefile and pipeline test selections. No tests, assertions, timeouts, or
retries were removed or weakened. PostgreSQL remains a real fixture; no mocks
or fakes were introduced. Tests run with one Ginkgo process and Go's parallel-test default
limited to one by `GOMAXPROCS=1`.

Seven additional regression specs verify complete migration discovery, caller
mutation isolation, rereading real mutable filesystems, SQL errors and rollback,
connection cleanup, refusal of a missing database, and pristine data/schema/
sequence state across database clones. The complete migration suite passed
269 specs, and the PostgreSQL helper passed all seven specs in focused checks.
Both versions passed all 113 packages in both comparisons. The baseline reported 5,975 Ginkgo
specs and the candidate reported 5,982. All baseline spec identities and
outcomes are present in the candidate. Logged native Go outcomes also match:
2,127 passing records and four preexisting permission-dependent skips under
root (these counts include subtests and exclude unlogged tests in mixed suites).

## Preliminary measurements

These focused measurements explain the optimization; they do not establish the
whole-tier target on their own.

| Measurement | Original | Optimized |
|---|---:|---:|
| Embedded migration definitions, three samples | 1.680–1.721 ms/op | 7.591–10.618 µs/op |
| Allocations per definitions call | 7,700 | 4 |
| Isolated database create/open/close/drop cycle, 40 iterations | 88.852 ms | 53.291 ms |

## Full comparison

The first comparison ran the baseline before the candidate with seed 20260924.
Both used the full package inventory selected by `make test-unit`, overriding
its parallel default with one test process. This includes `atc/integration`.

| Measurement | Baseline | Candidate | Reduction |
|---|---:|---:|---:|
| Full serial command, warmed builds | 1,272.284 s | 1,052.312 s | 17.29% |
| Ginkgo-reported execution time (78 suites only) | 879.450 s | 714.085 s | 18.80% |

The first row still includes Ginkgo's build/link checks. To measure raw execution
across **all** 113 packages, a second comparison builds both inventories first,
then runs their precompiled test binaries serially, candidate before baseline,
with seed 20260925. It includes the seven new candidate specs. Both complete
inventories passed.

| Execution-only measurement | Baseline | Candidate | Reduction |
|---|---:|---:|---:|
| Sum of elapsed package commands, all 113 packages | 1,297.464 s | 990.099 s | **23.69%** |

This saves 307.365 seconds of test execution. The raw audit matched
all original Ginkgo spec identities and all logged native test outcomes across
the two versions, and matched each version's Ginkgo inventory against its first
full run. All Ginkgo specs passed without retries. The same four existing native
permission tests skipped under root in both comparisons. These are measured
results on the environment above, not a hardware-independent timing guarantee.

The measured scope is the Go unit tier; it does not establish a percentage
improvement for Elm, Brine, or live Kubernetes tiers. Those test selections and
sources are unchanged.

## Reproduction

Use the same source revision, toolchain and PostgreSQL version on both sides.
Run from each checkout:

```sh
GOMAXPROCS=1 GOTOOLCHAIN=go1.25.6 ginkgo -r \
  --procs=1 --compilers=2 --keep-going --flake-attempts=1 \
  --seed=20260924 --no-color \
  --output-dir=/path/to/results --json-report=run.json \
  --skip-package=./integration,testflight,topgun,fly/integration,testhelpers/otel,atc/worker/jetbridge/brine
```

`--compilers=2` only applies to compilation; the test processes execute serially.
Do not substitute `-test.parallel=1`: Ginkgo rejects that flag in its own suites
and drops forwarded arguments for ordinary Go suites.

This container required three common setup corrections on both sides: a child
subreaper because PID 1 is `sleep infinity`; a temporary directory with its
sticky bit set; and a private minimal kubeconfig so Helm warnings do not pollute
rendered YAML. These are environment corrections, not optimization gains.
The first run on installed Go 1.25.12 also reproduced an `os.Root.MkdirAll`
trailing-slash failure independently of JetBridge. Both measured versions use
the Go 1.25.6 version declared in this repository. Existing permission-dependent
skips under root were identical in both pairs. No skip was introduced for
these measurements.

Raw logs, commands, source hashes, and result auditing are under
`/tmp/jetbridge-test-performance/`; `compare.py` performs sequential runs and
`audit.py` checks preserved original test sources and reported outcomes.

### Execution-only method

Build each complete inventory outside the timed portion, with the same linker
flags as Ginkgo's normal run:

```sh
GOMAXPROCS=1 GOTOOLCHAIN=go1.25.6 ginkgo build -r --compilers=2 \
  --ldflags='-w -s' \
  --skip-package=./integration,testflight,topgun,fly/integration,testhelpers/otel,atc/worker/jetbridge/brine
```

Discover the precompiled inventory from every `Compiled ...test` line; require
nonempty and identical package lists. Run each binary exactly once in package
order, never concurrently. Ginkgo suite binaries run through the Ginkgo CLI:

```sh
GOMAXPROCS=1 GOTOOLCHAIN=go1.25.6 ginkgo --procs=1 \
  --flake-attempts=1 --seed=20260925 --no-color \
  --output-dir=/path/to/package-results --json-report=report.json \
  /absolute/path/to/package/package.test
```

Ordinary Go test binaries that import Ginkgo transitively (including database
fixtures) also run through Ginkgo, with `-- -test.v=true` and without a Ginkgo
JSON report. Pure Go test binaries without Ginkgo imports run directly with
`-test.v=true` from their package directories. No test name filters are applied.
The harness sums per-package elapsed command time, excluding compilation and
Python wrapper startup. The same Linux subreaper surrounds every package on
both sides. Ginkgo handles the package working directory for its binaries.

`raw_audit.py` requires every package once on each side, zero failing commands,
identical native result records, exact Ginkgo spec identities matching the
first full run, one Ginkgo process, no retries, and unchanged candidate source
hashes. Per-package times remain available in `raw-results.json`.

## Measured source hashes

SHA-256 of the four source files used for both candidate runs:

```text
55baa930ad880df5f14e8471a4f3dcf9db4ca770d265be1d9430a3d9fb26f60f  atc/db/migration/migration.go
30d447ec15da0153663763025dee96598bb4dbad3bbaeafc46307caed9ad4976  atc/postgresrunner/postgresrunner.go
ef784fe3f7186edb475d102f2a3336c01055748f0bfda98d1bdfc1057a43781d  atc/db/migration/embedded_migrations_test.go
6fce85ae287058c90acaa196506d5d06ee7c61644ef8a7560ed72749c322e6e8  atc/postgresrunner/sql_test.go
```

## Validation after updating the branch

On top of `core` at `08e8be2af`, a fresh serial run passed all 274 migration
specs, seven PostgreSQL helper specs, and 1,384 database specs. This includes
the migrations and database changes added since the benchmark base. All three
suites passed without retries. This targeted integration check is not a repeat
of the historical full 113-package timing comparison.

```sh
GOMAXPROCS=1 GOTOOLCHAIN=go1.25.6 ginkgo \
  --procs=1 --compilers=2 --flake-attempts=1 --seed=20260925 \
  ./atc/db/migration ./atc/postgresrunner ./atc/db
```

The same temporary-directory, kubeconfig, and child-reaper environment
corrections described above were used. Fresh reports are under
`/tmp/jetbridge-test-performance-push-checks/`.
