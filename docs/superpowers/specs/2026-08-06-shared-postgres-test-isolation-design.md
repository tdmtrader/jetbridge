# Shared PostgreSQL Test Isolation

> **Environment update (2026-08-08):** The template/clone design below remains
> authoritative, but its Colima provisioning section is historical. This Mac
> no longer has a Docker daemon. The repository now treats PostgreSQL as an
> externally managed machine-wide service, and `hack/test-postgres.sh` provides
> readiness/DSN output only; it never starts, stops, or recreates the service.

**Date:** 2026-08-06
**Status:** Implemented; provisioning model superseded as noted above

## Context

`atc/postgresrunner` currently starts one PostgreSQL postmaster in every
Ginkgo process. Its port is `5433 + GinkgoParallelProcess()`, while its
template and test databases are always named `testdb_template` and `testdb`.
`GinkgoParallelProcess()` is unique only inside one suite invocation. Two
independent database-backed suites therefore both start at process 1, both
choose port 5434, and one suite can connect to the other's postmaster.

This was reproduced with `atc/api/pipelineserver` and `atc/api/auth` launched
concurrently: one suite passed and the other failed after its postmaster could
not bind 5434 and its subsequent `psql` commands reached the winning suite's
server.

The test-double conversion is adding real-database consumers, so serializing
all package-level verification is the wrong long-term constraint. The desired
model is one machine-wide PostgreSQL instance with an independently cloned
database for every spec.

## Goals

- Permit independent Ginkgo commands, suites, worktrees, and Ginkgo parallel
  nodes to run concurrently against one PostgreSQL server.
- Give every spec a migrated database cloned from a template and invisible to
  every other spec.
- Run migrations once per suite invocation, not once per Ginkgo node or spec.
- Preserve the existing `postgresrunner` call pattern wherever possible:
  create a database, open one or more connections, then drop it.
- Make leaked connections and abandoned databases recoverable and observable.
- Provision the local shared server as a PostgreSQL Docker container using the
  user's existing Colima runtime.

## Non-goals

- A schema-per-spec design. Concourse tests exercise database-level behavior,
  migrations, advisory locks, listeners, and command wiring that assumes a
  real database.
- A globally cached template shared by unrelated suite invocations. Concurrent
  worktrees can contain different migration sets, so each suite owns its own
  template.
- Starting or supervising Colima from the Go test library. Container lifecycle
  belongs in a repository helper, while the runner consumes a connection
  string.
- Changing production database code or weakening the one-connection limits
  used by tests to expose accidental connection fan-out.

## PostgreSQL service

The local service is a named Docker container, `concourse-test-postgres`,
published only on loopback. A new `hack/test-postgres.sh` helper owns its
lifecycle:

```text
hack/test-postgres.sh up       create/start and wait until ready
hack/test-postgres.sh status   show container and readiness
hack/test-postgres.sh env      print CONCOURSE_TEST_POSTGRES_DSN
hack/test-postgres.sh down     remove the dedicated container
```

`up` is idempotent. Concurrent callers either create the named container or
observe the winner and wait for it to become ready; they never create a second
server. The container remains running after a test command so separately
launched commands share it. `down` is explicit.

On macOS the helper targets the `colima` Docker context explicitly rather than
inheriting `DOCKER_HOST`; this prevents a shell previously configured for a
remote daemon from placing the database somewhere its published port cannot be
reached. Colima itself remains an external prerequisite: the helper reports a
stopped runtime clearly but does not start or reconfigure the VM.

The container uses PostgreSQL 14, trust authentication, and a loopback-only
port such as 15432. It is dedicated to tests and is configured for
non-durability and concurrency (`fsync=off`, `synchronous_commit=off`,
`full_page_writes=off`, and at least 500 connections). No production data or
credentials are present.

The runner reads `CONCOURSE_TEST_POSTGRES_DSN`. Its default targets the local
container:

```text
host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable
```

CI may provide the same variable for a service container or another directly
reachable PostgreSQL server. The Go package never shells out to Docker. If the
server is unavailable or the configured user cannot create and drop databases,
suite setup fails immediately with an actionable message.

## Namespaces and database lifecycle

Each suite invocation generates a lowercase, identifier-safe run ID containing
an epoch, the process ID, and random entropy. PostgreSQL identifiers remain
under 63 bytes:

```text
template:  cc_tpl_<run-id>
spec DB:   cc_db_<run-id>_n<ginkgo-node>_s<serial>
```

Names are generated internally and restricted to `[a-z0-9_]`; user-provided
text is never interpolated as an identifier.

The suite's first Ginkgo process creates the template database, opens it once
through the normal Concourse migration path, marks its tables unlogged as the
current runner does, closes every template connection, and shares the template
and run configuration with all nodes through `SynchronizedBeforeSuite`. The
database is not marked `IS_TEMPLATE`, because that flag prevents ordinary
cleanup and is unnecessary when the owner performs the clone.

Every call to `CreateTestDBFromTemplate` allocates a fresh spec database and
records it as that process's current database. `CreateEmptyTestDB` does the
same without cloning, preserving migration-suite behavior. `OpenConn`,
`OpenSingleton`, `OpenDB`, `DataSourceName`, migrations, and truncation all
resolve the current database instead of the literal `testdb`. Creating another
database while one is still active fails loudly, exposing a missing cleanup.

`DropTestDB`:

1. refuses to act on a name outside the runner's owned namespace;
2. terminates every backend attached to that exact database, including active
   and idle-in-transaction sessions;
3. drops the database; and
4. clears the process-local current database.

Normal spec cleanup continues to close its primary connection before dropping
the database. The runner also tracks every database it creates so suite
teardown can retry cleanup after a failed spec. Once all Ginkgo nodes have
finished, process 1 drops the suite template.

`GinkgoRunner` implements this with `SynchronizedBeforeSuite` and
`SynchronizedAfterSuite`. The two suites that currently call
`InitializeRunnerForGinkgo` manually move their existing suite setup into the
synchronized lifecycle rather than retaining a second initialization path.

Existing callers that pass the fixed name `testdb` to an ATC or Dex process
must instead use the runner's current database name. The runner continues to
expose the shared server's host and port for those command-level tests.

## Crash recovery

An interrupted test binary may skip suite teardown while the machine-wide
server remains alive. Run IDs therefore carry their creation epoch. During
suite setup, a PostgreSQL advisory lock serializes a conservative reaper that:

- considers only databases with the `cc_tpl_` or `cc_db_` prefix;
- ignores namespaces newer than 24 hours;
- terminates backends only for an expired namespace; and
- drops expired spec databases before their template.

The 24-hour window is much longer than these PostgreSQL-backed suites and
prevents one active worktree from cleaning another. Cleanup errors are
reported rather than silently ignored.

## Concurrency and capacity

Isolation comes from database identity. Concurrent suites have different run
IDs, concurrent Ginkgo nodes have different node numbers, and sequential specs
on one node have different serials. Fixture names, sequences, advisory locks,
and truncation are therefore private even though every connection reaches the
same server.

Some suites create one primary connection plus six lazy advisory-lock pools per
node. The dedicated server's connection ceiling must accommodate several
parallel suites; the local container therefore uses at least 500 connections.
The existing per-connection limits stay in place.

## Verification

Implementation follows red-green TDD. Coverage includes:

- unique, bounded, identifier-safe names;
- one migrated template shared across Ginkgo nodes;
- a distinct clone for every spec;
- rows written in one clone being absent from another;
- multiple runner connections resolving to the same active spec database;
- cleanup terminating active and idle-in-transaction connections;
- cleanup refusing databases outside its namespace;
- expired namespace reaping without touching a live namespace; and
- actionable failure when the shared server is unavailable.

The acceptance regression launches two previously colliding packages
concurrently against the Colima PostgreSQL container and requires both to pass.
It is followed by a parallel package run and the branch's required verification:

```text
ginkgo --no-color <changed-package>
make test-unit
make test-integration
make test-fly-integration
```

The known pre-existing migration failures are recorded separately rather than
attributed to this change.
