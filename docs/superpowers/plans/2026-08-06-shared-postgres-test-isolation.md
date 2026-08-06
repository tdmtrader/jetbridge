# Shared PostgreSQL Test Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-Ginkgo-process PostgreSQL postmasters with one Colima-hosted machine-wide PostgreSQL service that clones a unique database from a suite-owned template for every spec.

**Architecture:** A shell helper explicitly targets the `colima` Docker context and owns one named PostgreSQL 14 container. `atc/postgresrunner` connects to that external server, creates one uniquely named template in Ginkgo process 1, broadcasts its configuration with synchronized suite hooks, and gives every node/spec a uniquely named clone. Core lifecycle methods return errors and contain no Ginkgo assertions; the compatibility methods and Ginkgo adapter keep existing suite call sites terse.

**Tech Stack:** Bash, Docker/Colima, PostgreSQL 14, Go 1.25, pgx v5/stdlib, Ginkgo v2.27.3, Gomega.

## Global Constraints

- One PostgreSQL server is shared machine-wide; isolation is a database boundary, never a schema boundary.
- On macOS, PostgreSQL runs in the existing Colima Docker runtime as the named container `concourse-test-postgres`; the Go test library must never start, stop, or reconfigure Colima or shell out to Docker.
- Every Docker command in `hack/test-postgres.sh` explicitly uses the non-overridable `--context colima` and ignores inherited `DOCKER_HOST`.
- The container binds only `127.0.0.1:15432`, uses PostgreSQL 14 with trust authentication, and sets `fsync=off`, `synchronous_commit=off`, `full_page_writes=off`, and `max_connections=500`.
- The default admin DSN is exactly `host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable`; `CONCOURSE_TEST_POSTGRES_DSN` may override it.
- Each suite invocation owns one non-`IS_TEMPLATE` database named `cc_tpl_<run-id>` and each spec owns `cc_db_<run-id>_n<node>_s<serial>`; generated identifiers contain only `[a-z0-9_]` and remain below PostgreSQL's 63-byte limit.
- Migrations run once per suite invocation in Ginkgo process 1. Every node clones from that same closed template.
- `DropTestDB` terminates all sessions for the exact owned database, including active and idle-in-transaction sessions; it must never act outside its run namespace.
- Process cleanup sweeps its tracked databases. On normal suite termination, process-1 cleanup discovers residual databases by the exact run ID, drops clones before the template, and tolerates an unconfigured runner after failed setup. A killed Ginkgo node may leave its namespace; after 24 hours it becomes eligible for reaping on a subsequent suite setup. Cleanup after a crash has no wall-clock deadline when no later suite runs.
- A fixed PostgreSQL advisory lock guards stale cleanup. Only `cc_tpl_`/`cc_db_` namespaces older than 24 hours are eligible.
- Existing one-connection limits and join-limit validation remain unchanged.
- Preserve `CreateEmptyTestDB`, `CreateTestDBFromTemplate`, `DropTestDB`, `OpenConn`, `OpenSingleton`, `OpenDB`, `OpenDBAtVersion`, `TryOpenDBAtVersion`, `MigrateToVersion`, `DataSourceName`, and `Truncate` call patterns.
- Do not push this branch.

---

### Task 1: Provision the shared Colima PostgreSQL service

**Files:**
- Create: `hack/test-postgres.sh`
- Create: `hack/test-postgres_test.sh`
- Modify: `Makefile:1-40`
- Modify: `CLAUDE.md:1-40`
- Modify: `TESTING.md:1-110`

**Interfaces:**
- Produces: executable `hack/test-postgres.sh {up|status|env|down}`.
- Produces: `CONCOURSE_TEST_POSTGRES_DSN='host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable'`.
- Consumes: an already-running Docker context named `colima`; it never invokes `colima start`.

- [ ] **Step 1: Write the failing shell tests with a stateful fake Docker CLI**

Create `hack/test-postgres_test.sh` as an executable Bash test. Follow the repository's `docs/migration/migrate-preflight_test.sh` pattern: use `mktemp -d`, clean it with `trap`, prepend a fake `docker` to `PATH`, invoke the helper in a subprocess, and fail with the captured output.

The fake must record one argument vector per line in `${FAKE_DOCKER_LOG}` and implement these states through `${FAKE_DOCKER_STATE}`. Also install a fake `sleep` that returns immediately so the readiness-timeout case is deterministic and does not take 60 seconds:

```bash
#!/usr/bin/env bash
set -euo pipefail

printf '%q ' "$@" >>"${FAKE_DOCKER_LOG}"
printf '\n' >>"${FAKE_DOCKER_LOG}"

[[ "${1:-}" == "--context" && "${2:-}" == "colima" ]] || {
  echo "docker call did not select the colima context" >&2
  exit 90
}
shift 2

state="$(cat "${FAKE_DOCKER_STATE}")"
case "${1:-} ${2:-}" in
  "context inspect"|"info ") exit 0 ;;
  "container inspect")
    case "${state}" in
      missing) exit 1 ;;
      foreign) printf 'false|running\n' ;;
      drifted) printf 'true|running|postgres:13|0.0.0.0|15432|["-c","max_connections=100"]|[]\n' ;;
      stopped|start-race) printf 'true|exited|postgres:14|127.0.0.1|15432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
      *)       printf 'true|running|postgres:14|127.0.0.1|15432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
    esac
    ;;
  "run --detach")
    [[ "${state}" == "race" ]] && { printf 'running' >"${FAKE_DOCKER_STATE}"; exit 125; }
    printf 'running' >"${FAKE_DOCKER_STATE}"
    printf 'container-id\n'
    ;;
  "start concourse-test-postgres")
    if [[ "${state}" == "start-race" ]]; then
      printf 'running' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
    printf 'running' >"${FAKE_DOCKER_STATE}"
    ;;
  "exec concourse-test-postgres") [[ "${FAKE_DOCKER_READY:-1}" == "1" ]] ;;
  "rm --force") printf 'missing' >"${FAKE_DOCKER_STATE}" ;;
  *) echo "unexpected docker arguments: $*" >&2; exit 91 ;;
esac
```

Add exact cases that prove:

```bash
expect_log_contains "--context colima run --detach"
expect_log_contains "--name concourse-test-postgres"
expect_log_contains "--publish 127.0.0.1:15432:5432"
expect_log_contains "--env POSTGRES_HOST_AUTH_METHOD=trust"
expect_log_contains "--label com.concourse.test-postgres=true"
expect_log_contains "postgres:14"
expect_log_contains "-c fsync=off"
expect_log_contains "-c synchronous_commit=off"
expect_log_contains "-c full_page_writes=off"
expect_log_contains "-c max_connections=500"
```

Also assert: a second `up` does not issue another `run`; a stopped owned container is started; simulated `run` and `start` races are recovered by bounded re-inspection; disappearance during readiness retries returns to inspection; a foreign same-name container is rejected by `up` and `down`; a drifted owned container with the wrong image, binding, command, or trust environment is rejected by `up`/`status` but recoverably removable by `down`; readiness timeout says `PostgreSQL did not become ready`; `status` never mutates state and prints exactly `concourse-test-postgres: running (ready)` for a healthy service; `down` is idempotent; an intentionally concurrent `down` has last-mutation-wins semantics and is documented as unsafe while tests are running; hostile `DOCKER_HOST=tcp://example.invalid:2375` does not alter the explicit context; and `env` emits exactly:

```bash
export CONCOURSE_TEST_POSTGRES_DSN='host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable'
```

- [ ] **Step 2: Run the shell test to verify it fails**

Run:

```bash
bash hack/test-postgres_test.sh
```

Expected: FAIL because `hack/test-postgres.sh` does not exist.

- [ ] **Step 3: Implement the Colima-targeted helper**

Create `hack/test-postgres.sh` with strict Bash and these exact constants:

```bash
#!/usr/bin/env bash
set -euo pipefail

DOCKER_CONTEXT="colima"
CONTAINER="concourse-test-postgres"
IMAGE="postgres:14"
HOST_PORT="15432"
LABEL="com.concourse.test-postgres=true"
DSN="host=127.0.0.1 port=${HOST_PORT} user=postgres dbname=postgres sslmode=disable"

d() { docker --context "${DOCKER_CONTEXT}" "$@"; }
```

Implement these functions and behavior:

```bash
require_colima() {
  command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required" >&2; exit 1; }
  d context inspect "${DOCKER_CONTEXT}" >/dev/null 2>&1 || {
    echo "ERROR: Docker context '${DOCKER_CONTEXT}' does not exist" >&2
    exit 1
  }
  d info >/dev/null 2>&1 || {
    echo "ERROR: Colima is not running; start the existing runtime before running tests" >&2
    exit 1
  }
}

inspect_container() {
  d container inspect --format '{{ index .Config.Labels "com.concourse.test-postgres" }}|{{ .State.Status }}|{{ .Config.Image }}|{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostIp }}|{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostPort }}|{{ json .Config.Cmd }}|{{ json .Config.Env }}' "${CONTAINER}" 2>/dev/null
}

wait_ready() {
  for _ in $(seq 1 60); do
    d exec "${CONTAINER}" pg_isready -U postgres -d postgres >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "ERROR: PostgreSQL did not become ready within 60 seconds" >&2
  return 1
}
```

`up` validates ownership and the complete immutable contract (image, loopback binding, host port, trust environment, and PostgreSQL command), starts a stopped owned container, or creates it with this exact command:

```bash
d run --detach \
  --name "${CONTAINER}" \
  --label "${LABEL}" \
  --publish "127.0.0.1:${HOST_PORT}:5432" \
  --env POSTGRES_HOST_AUTH_METHOD=trust \
  "${IMAGE}" \
  -c fsync=off \
  -c synchronous_commit=off \
  -c full_page_writes=off \
  -c max_connections=500
```

Use bounded inspect/mutate/re-inspect loops so concurrent `up` calls recover when either `run` or `start` loses a race and when a container disappears during readiness. Never accept a merely labeled but drifted container for `up` or `status`; report exactly which immutable property differs. `status` returns success only for an owned, contract-matching, running, ready container and prints `concourse-test-postgres: running (ready)`; absent, stopped, foreign, and unready states print an actionable status and fail. `env` performs no Docker call. `down` may remove a drifted container only when its exact ownership label still matches, uses `d rm --force "${CONTAINER}"`, and succeeds when absent; it always refuses an unlabeled/foreign same-name container. `down` is explicit teardown after all tests, not a coordinated operation to run against an active `up` or test. Print actionable errors to stderr and machine-sourceable exports only from `env`.

- [ ] **Step 4: Run the shell tests and verify they pass**

Run:

```bash
bash hack/test-postgres_test.sh
```

Expected: PASS with a final `test-postgres helper: PASS` line.

- [ ] **Step 5: Add Make targets and documentation**

Add these phony targets without making `test-unit` silently start Docker:

```make
.PHONY: test-postgres-up test-postgres-status test-postgres-down test-postgres-helper

test-postgres-up:
	./hack/test-postgres.sh up

test-postgres-status:
	./hack/test-postgres.sh status

test-postgres-down:
	./hack/test-postgres.sh down

test-postgres-helper:
	bash ./hack/test-postgres_test.sh
```

Update `CLAUDE.md` with a narrow exception: the shared test PostgreSQL container uses the existing local Colima context, while every other Docker workflow retains the repository's documented provider rule. Replace the fixed `testdb_template` collision warning with the unique-database model and use `pg_isready -h 127.0.0.1 -p 15432 -U postgres`.

Update `TESTING.md` quick start and prerequisites to use:

```bash
make test-postgres-up
eval "$(./hack/test-postgres.sh env)"
make test-quick
```

Document that the named container stays up for concurrent commands and `make test-postgres-down` is explicit teardown.

- [ ] **Step 6: Verify the helper change**

Run:

```bash
bash -n hack/test-postgres.sh hack/test-postgres_test.sh
bash hack/test-postgres_test.sh
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit**

```bash
git add hack/test-postgres.sh hack/test-postgres_test.sh Makefile CLAUDE.md TESTING.md
git commit -m "test(postgres): provision one shared Colima service"
```

The commit body must state that the helper selects `--context colima`, refuses foreign same-name containers, and deliberately leaves the service running for concurrent test commands.

---

### Task 2: Implement the shared-server runner core

**Files:**
- Create: `atc/postgresrunner/shared_server.go`
- Create: `atc/postgresrunner/shared_server_test.go`
- Modify: `atc/postgresrunner/postgresrunner.go:1-376`
- Modify: `atc/postgresrunner/postgresrunner_suite_test.go:1-13`

**Interfaces:**
- Consumes: `CONCOURSE_TEST_POSTGRES_DSN`, defaulting to the Task 1 DSN.
- Produces:

```go
const DefaultAdminDSN = "host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable"

type SuiteConfig struct {
    AdminDSN     string `json:"admin_dsn"`
    RunID        string `json:"run_id"`
    TemplateName string `json:"template_name"`
    CreatedUnix  int64  `json:"created_unix"`
}

type ConnectionInfo struct {
    Host     string
    Port     uint16
    User     string
    Password string
    Database string
    SSLMode  string
}

func (r *Runner) CreateSuiteTemplate(context.Context) (SuiteConfig, error)
func (r *Runner) AdoptSuiteConfig(SuiteConfig, int) error
func (r *Runner) CleanupProcess(context.Context) error
func (r *Runner) CleanupSuite(context.Context) error
func (r *Runner) ConnectionInfo() ConnectionInfo
func (r *Runner) DatabaseName() string
```

- Preserves the existing no-argument spec lifecycle methods as Ginkgo assertion wrappers.

- [ ] **Step 1: Write failing pure naming/configuration tests**

In `shared_server_test.go`, use `package postgresrunner` and standard `testing`. Add deterministic tests around unexported helpers:

```go
func TestNewRunIDIsSafeBoundedAndCarriesCreationTime(t *testing.T) {
    got, err := newRunID(time.Unix(1_786_000_000, 0), 4242, bytes.NewReader([]byte{0xaa, 0xbb, 0xcc, 0xdd}))
    if err != nil { t.Fatal(err) }
    if got != "t1786000000_p4242_aabbccdd" { t.Fatalf("run ID = %q", got) }
    if !identifierPattern.MatchString("cc_tpl_" + got) { t.Fatalf("unsafe identifier") }
    if len("cc_db_"+got+"_n99_s999999") > 63 { t.Fatalf("identifier too long") }
}

func TestDSNForDatabasePreservesKeywordAndURLConfiguration(t *testing.T) {
    tests := []struct{ input, name, want string }{
        {
            "host=db port=5432 user=u dbname=postgres sslmode=require connect_timeout=5",
            "cc_db_x_n1_s1",
            "host=db port=5432 user=u dbname=cc_db_x_n1_s1 sslmode=require connect_timeout=5",
        },
        {
            "postgres://u:p@db:5432/postgres?sslmode=require&connect_timeout=5",
            "cc_db_x_n1_s1",
            "postgres://u:p@db:5432/cc_db_x_n1_s1?connect_timeout=5&sslmode=require",
        },
    }
    for _, tt := range tests {
        got, err := dsnForDatabase(tt.input, tt.name)
        if err != nil { t.Fatal(err) }
        if got != tt.want { t.Fatalf("dsn = %q, want %q", got, tt.want) }
    }
}
```

Also test invalid `SuiteConfig` identifiers, mismatched `TemplateName`, invalid node numbers, no active database, and a second create request while a current database exists. Keyword DSNs must replace their one existing `dbname` token rather than append a duplicate; quoted values and escaped spaces must remain parseable.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run:

```bash
go test ./atc/postgresrunner -run 'Test(NewRunID|DSNForDatabase|AdoptSuiteConfig)' -count=1
```

Expected: FAIL to compile because the new helpers and types do not exist.

- [ ] **Step 3: Implement configuration, names, and process-local state**

Create `shared_server.go` with:

```go
var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Runner struct {
    Port int

    state *runnerState
}

type runnerState struct {
    mu        sync.Mutex
    suite     SuiteConfig
    node      int
    serial    uint64
    allocating bool
    currentDB string
    ownedDBs  map[string]struct{}
}
```

Move `Runner` out of `postgresrunner.go`. Keep mutable state behind a pointer so the temporarily retained legacy ifrit adapter does not copy a `sync.Mutex`. Implement `newRunID(now, pid, entropy)` with four random bytes, `validateIdentifier`, `dsnForDatabase` for both keyword and `postgres://`/`postgresql://` forms, `activeDSN`, `DatabaseName`, and `ConnectionInfo`. Replace an existing keyword `dbname` in place; never rely on duplicate-key precedence. Parse connection fields with `pgx.ParseConfig`; preserve the requested `sslmode` from either the keyword option or URL query. `AdoptSuiteConfig` validates that `TemplateName == "cc_tpl_" + RunID`, initializes the owned set, stores the Ginkgo node, and sets the retained public `Port` field.

- [ ] **Step 4: Write failing live lifecycle tests against the shared service**

Add tests guarded by an admin readiness helper that fails with the exact Task 1 setup command rather than silently skipping:

```go
func requireSharedPostgres(t *testing.T) string {
    t.Helper()
    dsn := os.Getenv("CONCOURSE_TEST_POSTGRES_DSN")
    if dsn == "" { dsn = DefaultAdminDSN }
    admin, err := sql.Open("pgx", dsn)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { admin.Close() })
    if err := admin.Ping(); err != nil {
        t.Fatalf("shared PostgreSQL unavailable: %v; run make test-postgres-up", err)
    }
    return dsn
}
```

Cover these exact behaviors:

```go
func TestRunnerClonesAnIsolatedDatabaseForEveryCycle(t *testing.T)
func TestTwoNodesAdoptOneTemplateAndCreateDistinctClones(t *testing.T)
func TestRunnerRejectsASecondActiveDatabase(t *testing.T)
func TestRunnerSerializesConcurrentCreateRequestsWithoutLeakingAClone(t *testing.T)
func TestDropTestDBTerminatesActiveAndIdleInTransactionBackends(t *testing.T)
func TestCleanupSuiteDiscoversResidualClonesBeforeDroppingTemplate(t *testing.T)
func TestReapExpiredRunsDropsOnlyOwnedPrefixesOlderThan24Hours(t *testing.T)
func TestCleanupRefusesIdentifiersOutsideTheRunNamespace(t *testing.T)
func TestCleanupDoesNotTreatUnderscoresInRunIDAsWildcards(t *testing.T)
func TestReaperWaitsForTheMachineWideAdvisoryLock(t *testing.T)
```

In the isolation test: create one suite template, clone A, create `isolation_probe`, insert `only-a`, drop A, clone B, and assert the row count is zero. In the two-node test, create one suite config, adopt it into node 1 and node 2 runners, create clones concurrently, and assert the names contain the same exact run ID but distinct `_n1_`/`_n2_` components. In the same-runner concurrency test, release two create calls at one barrier, require exactly one success and one active-allocation error, drop the winner, and prove the catalog contains no clone from the losing request. In the termination test, hold one backend in `BEGIN` and another in `SELECT pg_sleep(30)`, invoke drop from a separate admin connection, and require both connections to become unusable. In the wildcard test, create an unrelated but deliberately `LIKE`-matching database and prove suite cleanup leaves it untouched. In the advisory-lock test, hold the fixed lock on one dedicated connection, prove a reaper with a short context cannot enter, release it, and prove the reaper then completes. In every test, register `CleanupSuite` before creating a spec database so failures do not leak names.

- [ ] **Step 5: Run the lifecycle tests to verify they fail**

Run:

```bash
go test ./atc/postgresrunner -run 'TestRunner|TestTwoNodes|TestDropTestDB|TestCleanupSuite|TestReapExpired|TestCleanupRefuses' -count=1
```

Expected: FAIL because the shared-server DDL and lifecycle methods do not exist yet.

- [ ] **Step 6: Implement template, clone, cleanup, and stale-reaper SQL**

Implement error-returning core methods in `shared_server.go`. All DDL identifiers must pass `validateIdentifier` before formatting. Values use parameters.

Use these SQL shapes:

```sql
CREATE DATABASE <safe_name>
CREATE DATABASE <safe_name> TEMPLATE <safe_template>
ALTER DATABASE <safe_name> WITH ALLOW_CONNECTIONS false
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE pid <> pg_backend_pid() AND datname = $1
DROP DATABASE IF EXISTS <safe_name> WITH (FORCE)
SELECT datname FROM pg_database WHERE left(datname, length($1)) = $1 ORDER BY datname
SELECT pg_advisory_lock(18932900154397524)
SELECT pg_advisory_unlock(18932900154397524)
```

`CreateSuiteTemplate` must:

1. choose the configured/default admin DSN;
2. ping it and return an error containing `run make test-postgres-up` on failure;
3. acquire one dedicated admin `*pgx.Conn`, hold advisory lock `18932900154397524` (`0x43435f54455354`, mnemonic `CC_TEST`) on that exact session, perform the complete stale scan/termination/drop operation through that same connection, and defer unlock/close;
4. generate and adopt a run config for node 1;
5. create the template without `IS_TEMPLATE`;
6. open it through `db.Open` so migrations run;
7. close the migration connection;
8. execute the existing `mark_tables_as_unlogged` body through a native pgx connection in simple-protocol mode; and
9. terminate every remaining template backend before returning.

`createTestDBFromTemplate(ctx)` and `createEmptyTestDB(ctx)` allocate `cc_db_<run>_n<node>_s<serial>`. Under the state mutex they must reject either `allocating` or nonempty `currentDB`, reserve one serial/name by setting `allocating`, then release the mutex for DDL. After DDL, one deferred state transition clears the reservation and either tracks the successful database or rolls state back on error. A losing concurrent caller must issue no DDL and leave no untracked clone. Task 2 deliberately does not activate these methods through the existing exported spec wrappers yet; the old Ginkgo lifecycle and exported connection methods remain intact until the atomic activation in Task 3.

The Task 3 activation will replace those wrappers with `ExpectWithOffset` calls around the error-returning core.

`CleanupProcess` snapshots and drops every tracked database. `CleanupSuite` performs a non-pattern prefix query for exact `cc_db_<run-id>_` candidates, then parses and validates every returned name and requires its embedded run ID to equal `Runner.suite.RunID` before any terminate/drop. It drops residual clones before `TemplateName` and is a no-op for an unconfigured runner.

For every drop, first disable new connections, terminate all matching sessions, and issue PostgreSQL 14's `DROP DATABASE ... WITH (FORCE)` outside a transaction; prepared-transaction or replication-slot failures must surface. Reaping parses only exact generated name forms and drops a namespace only when its embedded epoch is older than `now.Add(-24*time.Hour)`. Drop clone names before template names.

- [ ] **Step 7: Preserve the old adapter until the atomic activation task**

Keep `Runner.Run`, the existing `GinkgoRunner`, fixed-path exported wrappers, and their process dependencies compiling and behaving exactly as before. This makes the Task 2 commit green for all existing suites while the new core is still opt-in. Do not introduce a temporary dual-mode wrapper. Task 3 switches the adapter, connection paths, custom callers, and subprocess wiring together before deleting the legacy implementation.

- [ ] **Step 8: Run core verification**

Run:

```bash
gofmt -w atc/postgresrunner/shared_server.go atc/postgresrunner/shared_server_test.go atc/postgresrunner/postgresrunner.go
go test ./atc/postgresrunner -count=1
git diff --check
```

Expected: all commands exit 0. The new core lifecycle tests use the shared service; the unchanged legacy adapter remains only as a short-lived compatibility path for this commit.

- [ ] **Step 9: Commit**

```bash
git add atc/postgresrunner/shared_server.go atc/postgresrunner/shared_server_test.go atc/postgresrunner/postgresrunner.go atc/postgresrunner/postgresrunner_suite_test.go
git commit -m "test(postgres): isolate every spec in the shared server"
```

The commit body must describe the old fixed port/database collision, exact-run cleanup guard, and all-backend termination.

---

### Task 3: Atomically activate the shared runner across every suite shape

**Files:**
- Modify: `atc/postgresrunner/ginkgo.go:1-40`
- Create: `atc/postgresrunner/ginkgo_test.go`
- Modify: `atc/postgresrunner/postgresrunner.go:1-376`
- Modify: `cmd/concourse/concourse_suite_test.go:1-31`
- Modify: `cmd/concourse/concourse_test.go:1-90`
- Modify: `atc/scheduler/algorithm/suite_test.go:1-84`
- Modify: `atc/integration/integration_suite_test.go:1-100`
- Modify: `skymarshal/dexserver/dexserver_test.go:1-45`
- Modify: `atc/db/migration/legacy_upgrade_test.go:1049-1054`
- Modify: `atc/db/lock/lock_suite_test.go:1-22` only if a focused fixture assertion is needed

**Interfaces:**
- Consumes: Task 2's core lifecycle, `ConnectionInfo`, and `DatabaseName`.
- Produces the composable callbacks `InitializeRunnerForGinkgo`, `SynchronizeRunnerForGinkgo`, `CleanupRunnerForGinkgo`, and `FinalizeRunnerForGinkgo` plus `GinkgoRunner`.
- Produces: no suite or subprocess assumes `testdb`, `testdb_template`, `/tmp`, or `5433 + GinkgoParallelProcess()`.
- This is intentionally one atomic task: switching the adapter, exported wrappers, and every custom direct caller in separate commits would leave an intermediate commit that compiles but runs suites against an unconfigured runner.

- [ ] **Step 1: Write failing adapter and connection-wiring tests**

In `ginkgo_test.go`, add a JSON round trip for this literal config plus malformed JSON and invalid template/run-pair cases:

```go
original := SuiteConfig{
    AdminDSN: DefaultAdminDSN,
    RunID: "t1786000000_p42_aabbccdd",
    TemplateName: "cc_tpl_t1786000000_p42_aabbccdd",
    CreatedUnix: 1786000000,
}
```

For command-level setup, introduce narrow helpers that accept a pointer, so a runner's synchronized state is never copied:

```go
func runnerPostgresConfig(r *postgresrunner.Runner) flag.PostgresConfig {
    info := r.ConnectionInfo()
    return flag.PostgresConfig{
        Host: info.Host, Port: info.Port, User: info.User,
        Password: info.Password, Database: info.Database, SSLMode: info.SSLMode,
    }
}
```

Add assertions in each affected external-process suite that the configured database equals `postgresRunner.DatabaseName()`, starts with `cc_db_`, and carries the shared service host/port. The production change each test catches is a regression to the old fixed database or process-local port.

- [ ] **Step 2: Run the focused tests to verify the red state**

Run:

```bash
go test ./atc/postgresrunner -run 'TestRunnerSuiteConfigRoundTrips' -count=1
ginkgo --no-color ./skymarshal/dexserver/
ginkgo --no-color ./atc/integration/
ginkgo --no-color ./cmd/concourse/
```

Expected: the adapter test fails because serialization helpers do not exist, and at least the external-process assertions fail because configuration still contains `testdb` or the old port.

- [ ] **Step 3: Implement the synchronized adapter and activate every runner connection path**

Implement:

```go
func GinkgoRunner(runner *Runner) none {
    SynchronizedBeforeSuite(
        func() []byte { return InitializeRunnerForGinkgo(runner) },
        func(data []byte) { SynchronizeRunnerForGinkgo(runner, data) },
    )
    SynchronizedAfterSuite(
        func() { CleanupRunnerForGinkgo(runner) },
        func() { FinalizeRunnerForGinkgo(runner) },
    )
    return none{}
}
```

`InitializeRunnerForGinkgo` creates the template, marshals the config, and asserts with `ExpectWithOffset`. `SynchronizeRunnerForGinkgo` validates and adopts it with `GinkgoParallelProcess()`. All-process cleanup calls `CleanupProcess`; process-1 cleanup calls `CleanupSuite` after the other nodes exit. Both cleanup callbacks tolerate failed/unconfigured setup. Keep Gomega assertions in the adapter and no-argument spec wrappers; Task 2 core methods continue returning errors.

Replace the exported `CreateEmptyTestDB`, `CreateTestDBFromTemplate`, and `DropTestDB` implementations with assertion wrappers around the error-returning core. Make `MigrateToVersion`, `TryOpenDBAtVersion`, `OpenDBAtVersion`, `OpenDB`, `OpenConn`, `OpenSingleton`, and `DataSourceName` resolve the active clone while preserving pool limits and join-limit validation. Replace `Truncate`'s external `psql` with native pgx simple-protocol execution of the existing SQL body.

Delete `Runner.Run`, `appendToFile`, old initialize/finalize helpers, ifrit/ginkgomon/gexec process startup, `initdb`, `postgres`, `psql`, fixed `/tmp` sockets, and fixed database literals only after every caller in the next two steps is converted in the same working-tree change.

- [ ] **Step 4: Compose PostgreSQL into `cmd/concourse` and scheduler suite lifecycles**

Move `postgresRunner` to `cmd/concourse` suite scope. Replace its raw build-path synchronized payload with:

```go
type synchronizedSuiteConfig struct {
    ConcoursePath string `json:"concourse_path"`
    Postgres      []byte `json:"postgres"`
}
```

Process 1 builds the binary and calls `InitializeRunnerForGinkgo`; every process unmarshals both values and calls `SynchronizeRunnerForGinkgo`. All-process teardown calls `CleanupRunnerForGinkgo`; process-1 teardown calls `FinalizeRunnerForGinkgo` before `gexec.CleanupBuildArtifacts`. Preserve child-process shutdown before `DropTestDB`. Build all PostgreSQL flags from `ConnectionInfo`.

For scheduler, replace ordinary suite setup/teardown with synchronized callbacks. Run Jaeger `Prepare` in every process before adoption. Run per-process exporter shutdown and `CleanupRunnerForGinkgo` in all-process teardown, then `FinalizeRunnerForGinkgo` in process-1 teardown.

- [ ] **Step 5: Replace fixed ATC integration, Dex, and migration wiring**

In `atc/integration`, create the clone before reading `ConnectionInfo`, populate every `cmd.Postgres` field from it, and keep ATC shutdown before database drop. In Dex, build `flag.PostgresConfig` entirely from `ConnectionInfo` and keep `storage.Close()` before suite-level cleanup. Update the migration preflight comment to describe the dynamic TCP DSN and single replaced `dbname`.

- [ ] **Step 6: Verify standard and custom synchronized suites in parallel**

Run:

```bash
gofmt -w atc/postgresrunner/ginkgo.go atc/postgresrunner/ginkgo_test.go atc/postgresrunner/postgresrunner.go cmd/concourse/concourse_suite_test.go cmd/concourse/concourse_test.go atc/scheduler/algorithm/suite_test.go atc/integration/integration_suite_test.go skymarshal/dexserver/dexserver_test.go atc/db/migration/legacy_upgrade_test.go
go test ./atc/postgresrunner -count=1
ginkgo --no-color --procs=2 ./atc/db/lock/
ginkgo --no-color --procs=2 ./atc/scheduler/algorithm/
ginkgo --no-color --procs=2 ./cmd/concourse/
ginkgo --no-color ./skymarshal/dexserver/
ginkgo --no-color ./atc/integration/
git diff --check
```

Expected: all listed packages pass. The Task 2 two-node lifecycle test proves both adopted runners share one run/template while their clone names contain different node components; the real two-process lock, scheduler, and command suites prove the composed Ginkgo lifecycle. Query only the completed run IDs when checking for leaks, so concurrently running suites are not misclassified.

- [ ] **Step 7: Commit the atomic activation**

```bash
git add atc/postgresrunner/ginkgo.go atc/postgresrunner/ginkgo_test.go atc/postgresrunner/postgresrunner.go cmd/concourse/concourse_suite_test.go cmd/concourse/concourse_test.go atc/scheduler/algorithm/suite_test.go atc/integration/integration_suite_test.go skymarshal/dexserver/dexserver_test.go atc/db/migration/legacy_upgrade_test.go atc/db/lock/lock_suite_test.go
git commit -m "test(postgres): route every suite to its isolated database"
```

The commit body must explain synchronized setup/teardown ordering and that fixed `testdb`/5434 wiring could connect a child process to another suite's database.

---

### Task 4: Lock in overlapping independent suite commands

**Files:**
- Create: `hack/test-postgres-concurrency.sh`
- Modify: `Makefile:1-45`
- Modify: `TESTING.md:1-115`
- Modify: `CLAUDE.md:20-45`

**Interfaces:**
- Consumes: Task 1's default Colima service and Task 3's active runner.
- Produces: `make test-postgres-concurrency`, a repeatable regression for the original fixed-port/database collision.

- [ ] **Step 1: Write the acceptance script and prove it is red on the old runner**

Create an executable strict-Bash script. It accepts `CONCOURSE_TEST_SOURCE_ROOT` for the code under test, defaults to the repository root, requires `./hack/test-postgres.sh status`, and launches `atc/api/pipelineserver` and `atc/api/auth` as separate background Ginkgo commands with separate logs.

Give each child an exact, safe application name by appending a distinct `application_name=cc_accept_<side>_<token>` to the default admin DSN through its `CONCOURSE_TEST_POSTGRES_DSN`. While both child PIDs are alive, poll `pg_stat_activity` through `docker --context colima exec concourse-test-postgres psql` and require simultaneous rows for both exact application names whose `datname` values are distinct `cc_db_...` clones. This is the barrier: zero exit codes alone are insufficient. Preserve logs and the observed catalog snapshot on failure; clean the temporary log directory on success.

Run the new script against a temporary worktree at the pre-Task-2 commit by setting `CONCOURSE_TEST_SOURCE_ROOT` to that worktree. Expected: FAIL because the old runner ignores the shared DSN/application names and one suite reproduces the 5434 collision. Remove only that exact temporary worktree afterwards; do not modify production code to induce failure.

- [ ] **Step 2: Run the acceptance script against the current runner**

Run:

```bash
bash -n hack/test-postgres-concurrency.sh
./hack/test-postgres.sh status
bash hack/test-postgres-concurrency.sh
```

Expected: both application names are observed concurrently on two distinct `cc_db_` databases, both suite commands exit 0, and the final line is `shared PostgreSQL concurrency: PASS`.

- [ ] **Step 3: Add the Make target and final documentation**

Add `test-postgres-concurrency` to `Makefile`. Document that independent PostgreSQL-backed package commands may run concurrently, every spec owns a clone, and the named service deliberately stays running. Remove advice to wait for or kill another `testdb_template` process. Note that identical integration suites may still contend on application HTTP ports; this regression guarantees PostgreSQL isolation.

- [ ] **Step 4: Commit the acceptance regression**

```bash
git add hack/test-postgres-concurrency.sh Makefile TESTING.md CLAUDE.md
git commit -m "test(postgres): prove database suites overlap safely"
```

The commit body must include the old-run failure, the two observed distinct clone names, and both current command results without printing a password-bearing DSN.

---

### Task 5: Run branch verification and whole-change review

**Files:**
- Modify only files required to resolve verified regressions or review findings.

- [ ] **Step 1: Run focused package verification with a ready-service preflight**

Run:

```bash
./hack/test-postgres.sh status
make test-postgres-helper
make test-postgres-concurrency
ginkgo --no-color --procs=2 ./atc/db/lock/
ginkgo --no-color ./atc/api/
ginkgo --no-color ./atc/gc/
ginkgo --no-color ./atc/db/
```

Expected: helper, concurrency, lock, API, and GC pass. For `atc/db`, record the exact seven known pre-existing `Legacy Database Upgrade` failures and verify no additional failure; also note whether the known 1ms checkpoint-tolerance flake appears.

- [ ] **Step 2: Run the full required verification with fresh output**

Run in this order:

```bash
./hack/test-postgres.sh status
make test-unit
make test-integration
make test-fly-integration
git diff --check
git status --short
```

Expected: zero new failures. Do not describe a command as passing unless its fresh exit status is zero. Classify only the handoff's documented migration failures and two latent timing flakes as pre-existing, with exact spec names and output. Leave the machine-wide PostgreSQL container running for subsequent work; teardown remains the explicit `make test-postgres-down` command.

- [ ] **Step 3: Review and resolve the whole PostgreSQL change**

Review the range from the design commit through Task 4 for remaining fixed database/socket/port assumptions, unvalidated DDL identifiers, namespace-escaping cleanup, DSN secret output, duplicate Ginkgo suite nodes, copied locks, and tests that pass only serially. Resolve every Critical or Important finding through the subagent review loop and re-run directly affected tests.

- [ ] **Step 4: Commit verification fixes, if any**

Create focused test-oriented commits for any fixes. If review and verification require no source change, record the exact results in the SDD ledger and do not create an empty commit.
