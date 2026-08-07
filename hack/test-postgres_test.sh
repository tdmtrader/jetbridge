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
GOOD_COMMAND='["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]'
GOOD_ENV='["POSTGRES_HOST_AUTH_METHOD=trust"]'
GOOD_BINDINGS='{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15432"}]}'
EXTRA_BINDINGS='{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15432"},{"HostIp":"0.0.0.0","HostPort":"25432"}]}'
OWNED_ID='owned-container-id'
FOREIGN_ID='foreign-container-id'
GOOD_NAME='/concourse-test-postgres'

owned() {
	printf '%s|%s|true|%s|%s|%s|%s|%s|%s|%s|%s\n' "${OWNED_ID}" "${GOOD_NAME}" "$@"
}

case "${1:-} ${2:-}" in
  "context inspect"|"info ") exit 0 ;;
	"container inspect")
		case "${state}" in
			missing|race) exit 1 ;;
			foreign) printf '%s|%s|false|running\n' "${FOREIGN_ID}" "${GOOD_NAME}" ;;
			drift-name) printf '%s|/renamed-test-postgres|true|running|postgres:14|127.0.0.1|15432|%s|%s|%s\n' "${OWNED_ID}" "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
			drifted) owned running postgres:13 0.0.0.0 15432 '["-c","max_connections=100"]' '[]' "${GOOD_BINDINGS}" ;;
      drift-image) owned running postgres:13 127.0.0.1 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
      drift-binding) owned running postgres:14 0.0.0.0 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
      drift-port) owned running postgres:14 127.0.0.1 25432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
      drift-command) owned running postgres:14 127.0.0.1 15432 '["-c","max_connections=100"]' "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
      drift-env) owned running postgres:14 127.0.0.1 15432 "${GOOD_COMMAND}" '[]' "${GOOD_BINDINGS}" ;;
      drift-extra-binding) owned running postgres:14 127.0.0.1 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${EXTRA_BINDINGS}" ;;
			stopped|start-race|swap-before-start) owned exited postgres:14 127.0.0.1 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
			swap-before-exec|swap-after-ready|swap-before-rm) owned running postgres:14 127.0.0.1 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
			*)       owned running postgres:14 127.0.0.1 15432 "${GOOD_COMMAND}" "${GOOD_ENV}" "${GOOD_BINDINGS}" ;;
		esac
    ;;
  "run --detach")
    [[ "${state}" == "race" ]] && { printf 'running' >"${FAKE_DOCKER_STATE}"; exit 125; }
    printf 'running' >"${FAKE_DOCKER_STATE}"
		printf '%s\n' "${OWNED_ID}"
		;;
	"start owned-container-id")
		if [[ "${state}" == "swap-before-start" ]]; then
			printf 'foreign' >"${FAKE_DOCKER_STATE}"
			exit 1
		fi
		if [[ "${state}" == "start-race" ]]; then
			printf 'running' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
    printf 'running' >"${FAKE_DOCKER_STATE}"
    ;;
	"exec owned-container-id")
		if [[ "${state}" == "swap-before-exec" ]]; then
			printf 'foreign' >"${FAKE_DOCKER_STATE}"
			exit 1
		fi
		if [[ "${state}" == "swap-after-ready" ]]; then
			printf 'foreign' >"${FAKE_DOCKER_STATE}"
			exit 0
		fi
		if [[ "${state}" == "disappearing" ]]; then
      printf 'missing' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
    [[ "${state}" != "missing" ]] || exit 1
    [[ "${FAKE_DOCKER_READY:-1}" == "1" ]]
    ;;
	"rm --force")
    if [[ "${state}" == "down-race" ]]; then
      printf 'missing' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
		if [[ "${state}" == "down-race-foreign" || "${state}" == "swap-before-rm" ]]; then
			printf 'foreign' >"${FAKE_DOCKER_STATE}"
      exit 1
    fi
    printf 'missing' >"${FAKE_DOCKER_STATE}"
    ;;
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
expect_log_contains "--context colima start owned-container-id"
reset_fake race
run_helper up 0
expect_log_count "--context colima run --detach" 1
expect_log_count_at_least "container inspect" 2
reset_fake start-race
run_helper up 0
expect_log_contains "--context colima start owned-container-id"
expect_log_count_at_least "container inspect" 2

# Every operation after validation targets the immutable ID. A foreign
# same-name replacement is never started, accepted as ready, or removed.
reset_fake swap-before-start
run_helper up 1
expect_output_contains "not owned"
expect_log_contains "--context colima start owned-container-id"
expect_log_count "start concourse-test-postgres" 0

reset_fake swap-before-exec
run_helper up 1
expect_output_contains "not owned"
expect_log_contains "--context colima exec owned-container-id"
expect_log_count "exec concourse-test-postgres" 0

reset_fake swap-after-ready
run_helper up 1
expect_output_contains "not owned"
expect_log_contains "--context colima exec owned-container-id"
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
for drift in drift-name drift-image drift-binding drift-port drift-command drift-env drift-extra-binding; do
  reset_fake "${drift}"
  run_helper up 1
	case "${drift}" in
		drift-name) expect_output_contains "name" ;;
    drift-image) expect_output_contains "image" ;;
    drift-binding) expect_output_contains "loopback binding" ;;
    drift-port) expect_output_contains "host port" ;;
    drift-command) expect_output_contains "PostgreSQL command" ;;
    drift-env) expect_output_contains "trust environment" ;;
    drift-extra-binding) expect_output_contains "port bindings" ;;
  esac
  run_helper status 1
done

# A drifted but owned service is recoverably removable.
reset_fake drifted
run_helper down 0
expect_log_contains "--context colima rm --force owned-container-id"

# Status is read-only and succeeds only after the service is ready.
reset_fake running
run_helper status 0
expect_output_equals "concourse-test-postgres: running (ready)"
expect_log_count "run --detach" 0
expect_log_count "start owned-container-id" 0
expect_log_count "rm --force" 0
expect_log_contains "--context colima exec owned-container-id"

# Missing and stopped services have actionable, non-mutating status failures.
reset_fake missing
run_helper status 1
expect_output_contains "absent"
reset_fake stopped
run_helper status 1
expect_output_contains "exited"
expect_log_count "start owned-container-id" 0

# An unready service reports a bounded readiness failure without changing it.
reset_fake running
FAKE_DOCKER_READY=0 run_helper status 1
expect_output_contains "PostgreSQL did not become ready"
expect_log_count "run --detach" 0
expect_log_count "start owned-container-id" 0

# Teardown is idempotent. It is intentionally last-mutation-wins and unsafe while tests run.
reset_fake running
run_helper down 0
run_helper down 0
expect_log_count "--context colima rm --force owned-container-id" 1

# If another down wins the remove race, re-inspection observes absence and
# succeeds without trying to remove a potentially foreign replacement.
reset_fake down-race
run_helper down 0
expect_log_count "--context colima rm --force owned-container-id" 1
expect_log_count_at_least "container inspect" 2

# A foreign replacement after a losing race is never removed.
reset_fake down-race-foreign
run_helper down 1
expect_log_count "--context colima rm --force owned-container-id" 1
expect_log_count_at_least "container inspect" 2

# A foreign replacement racing teardown survives because rm targets only the
# previously validated immutable ID, and teardown refuses the replacement.
reset_fake swap-before-rm
run_helper down 1
expect_output_contains "not owned"
expect_log_contains "--context colima rm --force owned-container-id"
expect_log_count "rm --force concourse-test-postgres" 0
if [[ "$(cat "${TMP_ROOT}/state")" != "foreign" ]]; then
	echo "foreign replacement did not survive teardown race" >&2
	exit 1
fi

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
