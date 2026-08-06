#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPER="${SCRIPT_DIR}/test-postgres.sh"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMP_ROOT}"' EXIT

cat >"${TMP_ROOT}/docker" <<'FAKE_DOCKER'
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
      missing|race) exit 1 ;;
      foreign) printf 'false|running\n' ;;
      drifted) printf 'true|running|postgres:13|0.0.0.0|15432|["-c","max_connections=100"]|[]\n' ;;
      drift-image) printf 'true|running|postgres:13|127.0.0.1|15432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
      drift-binding) printf 'true|running|postgres:14|0.0.0.0|15432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
      drift-port) printf 'true|running|postgres:14|127.0.0.1|25432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
      drift-command) printf 'true|running|postgres:14|127.0.0.1|15432|["-c","max_connections=100"]|["POSTGRES_HOST_AUTH_METHOD=trust"]\n' ;;
      drift-env) printf 'true|running|postgres:14|127.0.0.1|15432|["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]|[]\n' ;;
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
  "exec concourse-test-postgres")
    if [[ "${state}" == "disappearing" ]]; then
      printf 'missing' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
    [[ "${state}" != "missing" ]] || exit 1
    [[ "${FAKE_DOCKER_READY:-1}" == "1" ]]
    ;;
  "rm --force") printf 'missing' >"${FAKE_DOCKER_STATE}" ;;
  *) echo "unexpected docker arguments: $*" >&2; exit 91 ;;
esac
FAKE_DOCKER
chmod +x "${TMP_ROOT}/docker"

cat >"${TMP_ROOT}/sleep" <<'FAKE_SLEEP'
#!/usr/bin/env bash
exit 0
FAKE_SLEEP
chmod +x "${TMP_ROOT}/sleep"

reset_fake() {
  local state="$1"
  printf '%s' "${state}" >"${TMP_ROOT}/state"
  : >"${TMP_ROOT}/docker.log"
}

run_helper() {
  local command="$1"
  local expected_status="$2"
  local output
  local status

  set +e
  output="$(
    PATH="${TMP_ROOT}:${PATH}" \
      FAKE_DOCKER_LOG="${TMP_ROOT}/docker.log" \
      FAKE_DOCKER_STATE="${TMP_ROOT}/state" \
      DOCKER_HOST="${DOCKER_HOST:-tcp://example.invalid:2375}" \
      bash "${HELPER}" "${command}" 2>&1
  )"
  status=$?
  set -e

  if [[ "${status}" -ne "${expected_status}" ]]; then
    echo "${command}: exit ${status}, want ${expected_status}" >&2
    echo "${output}" >&2
    exit 1
  fi
  RUN_OUTPUT="${output}"
}

expect_output_contains() {
  local expected="$1"
  if [[ "${RUN_OUTPUT}" != *"${expected}"* ]]; then
    echo "output missing: ${expected}" >&2
    echo "${RUN_OUTPUT}" >&2
    exit 1
  fi
}

expect_output_equals() {
  local expected="$1"
  if [[ "${RUN_OUTPUT}" != "${expected}" ]]; then
    echo "output differs" >&2
    echo "got: ${RUN_OUTPUT}" >&2
    echo "want: ${expected}" >&2
    exit 1
  fi
}

expect_log_contains() {
  local expected="$1"
  if ! grep -F -- "${expected}" "${TMP_ROOT}/docker.log" >/dev/null; then
    echo "docker log missing: ${expected}" >&2
    cat "${TMP_ROOT}/docker.log" >&2
    exit 1
  fi
}

expect_log_count() {
  local expected="$1"
  local wanted="$2"
  local actual
  actual="$(grep -F -c -- "${expected}" "${TMP_ROOT}/docker.log" || true)"
  if [[ "${actual}" -ne "${wanted}" ]]; then
    echo "docker log count for ${expected}: got ${actual}, want ${wanted}" >&2
    cat "${TMP_ROOT}/docker.log" >&2
    exit 1
  fi
}

expect_log_count_at_least() {
  local expected="$1"
  local minimum="$2"
  local actual
  actual="$(grep -F -c -- "${expected}" "${TMP_ROOT}/docker.log" || true)"
  if [[ "${actual}" -lt "${minimum}" ]]; then
    echo "docker log count for ${expected}: got ${actual}, want at least ${minimum}" >&2
    cat "${TMP_ROOT}/docker.log" >&2
    exit 1
  fi
}

# A missing owned service must be created with the complete immutable contract.
reset_fake missing
run_helper up 0
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

# A second caller observes the shared service instead of starting another one.
run_helper up 0
expect_log_count "--context colima run --detach" 1

# A stopped service is started, and racing start/run calls converge on the winner.
reset_fake stopped
run_helper up 0
expect_log_contains "--context colima start concourse-test-postgres"
reset_fake race
run_helper up 0
expect_log_count "--context colima run --detach" 1
expect_log_count_at_least "container inspect" 2
reset_fake start-race
run_helper up 0
expect_log_contains "--context colima start concourse-test-postgres"
expect_log_count_at_least "container inspect" 2

# If the winner vanishes while readiness is checked, re-inspection creates it again.
reset_fake disappearing
run_helper up 0
expect_log_count_at_least "container inspect" 2
expect_log_contains "--context colima run --detach"

# Same-name containers not owned by this helper are never touched.
reset_fake foreign
run_helper up 1
expect_output_contains "not owned"
run_helper status 1
expect_output_contains "not owned"
run_helper down 1
expect_output_contains "not owned"
expect_log_count "rm --force" 0

# A labeled service with any immutable contract drift is unsafe to use.
for drift in drift-image drift-binding drift-port drift-command drift-env; do
  reset_fake "${drift}"
  run_helper up 1
  case "${drift}" in
    drift-image) expect_output_contains "image" ;;
    drift-binding) expect_output_contains "loopback binding" ;;
    drift-port) expect_output_contains "host port" ;;
    drift-command) expect_output_contains "PostgreSQL command" ;;
    drift-env) expect_output_contains "trust environment" ;;
  esac
  run_helper status 1
done

# A drifted but owned service is recoverably removable.
reset_fake drifted
run_helper down 0
expect_log_contains "--context colima rm --force concourse-test-postgres"

# Status is read-only and succeeds only after the service is ready.
reset_fake running
run_helper status 0
expect_output_equals "concourse-test-postgres: running (ready)"
expect_log_count "run --detach" 0
expect_log_count "start concourse-test-postgres" 0
expect_log_count "rm --force" 0

# Missing and stopped services have actionable, non-mutating status failures.
reset_fake missing
run_helper status 1
expect_output_contains "absent"
reset_fake stopped
run_helper status 1
expect_output_contains "exited"
expect_log_count "start concourse-test-postgres" 0

# An unready service reports a bounded readiness failure without changing it.
reset_fake running
FAKE_DOCKER_READY=0 run_helper status 1
expect_output_contains "PostgreSQL did not become ready"
expect_log_count "run --detach" 0
expect_log_count "start concourse-test-postgres" 0

# Teardown is idempotent. It is intentionally last-mutation-wins and unsafe while tests run.
reset_fake running
run_helper down 0
run_helper down 0
expect_log_count "--context colima rm --force concourse-test-postgres" 1

# env is machine-sourceable and does not require Docker.
reset_fake missing
run_helper env 0
expect_output_equals "export CONCOURSE_TEST_POSTGRES_DSN='host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable'"
if [[ -s "${TMP_ROOT}/docker.log" ]]; then
  echo "env unexpectedly invoked docker" >&2
  cat "${TMP_ROOT}/docker.log" >&2
  exit 1
fi

echo "test-postgres helper: PASS"
