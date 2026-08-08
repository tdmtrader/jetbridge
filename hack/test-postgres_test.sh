#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/test-postgres.sh"
FIXTURES="${SCRIPT_DIR}/test-postgres-testdata"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-test-postgres-helper.XXXXXX")"
FAKE_BIN="${TMP_DIR}/bin"
READY_LOG="${TMP_DIR}/pg-isready.log"
PSQL_LOG="${TMP_DIR}/psql.log"
DOCKER_LOG="${TMP_DIR}/docker.log"

cleanup() {
	rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

run_helper() {
	PATH="${FAKE_BIN}:${PATH}" \
		FAKE_PG_ISREADY_LOG="${READY_LOG}" \
		FAKE_PSQL_LOG="${PSQL_LOG}" \
		FAKE_DOCKER_LOG="${DOCKER_LOG}" \
		"${TARGET}" "$@"
}

mkdir -p "${FAKE_BIN}"
ln -s "${FIXTURES}/pg_isready" "${FAKE_BIN}/pg_isready"
ln -s "${FIXTURES}/psql" "${FAKE_BIN}/psql"
ln -s "${FIXTURES}/docker" "${FAKE_BIN}/docker"

DEFAULT_DSN="host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable"

output="$(unset CONCOURSE_TEST_POSTGRES_DSN; run_helper status)" || fail "default status failed"
[[ "${output}" == "shared PostgreSQL: ready" ]] || fail "unexpected status output: ${output}"
grep -F -- "-q -d ${DEFAULT_DSN}" "${READY_LOG}" >/dev/null || fail "status did not check the default DSN"
grep -F -- "--dbname=${DEFAULT_DSN}" "${PSQL_LOG}" >/dev/null || fail "status did not authenticate with the default DSN"
[[ ! -s "${DOCKER_LOG}" ]] || fail "status invoked Docker"

rm -f "${READY_LOG}"
rm -f "${PSQL_LOG}"
OVERRIDE_DSN="postgres://postgres@db.internal:5432/postgres?sslmode=require"
output="$(CONCOURSE_TEST_POSTGRES_DSN="${OVERRIDE_DSN}" run_helper status)" || fail "override status failed"
[[ "${output}" == "shared PostgreSQL: ready" ]] || fail "unexpected override status output: ${output}"
grep -F -- "-q -d ${OVERRIDE_DSN}" "${READY_LOG}" >/dev/null || fail "status did not preserve the override DSN"
grep -F -- "--dbname=${OVERRIDE_DSN}" "${PSQL_LOG}" >/dev/null || fail "status did not authenticate with the override DSN"
[[ ! -s "${DOCKER_LOG}" ]] || fail "override status invoked Docker"

set +e
output="$(unset CONCOURSE_TEST_POSTGRES_DSN; FAKE_PG_ISREADY_STATUS=1 run_helper status 2>&1)"
status=$?
set -e
[[ "${status}" -eq 1 ]] || fail "unready status exited ${status}, want 1"
[[ "${output}" == *"must already be running"* ]] || fail "unready status omitted external-service guidance"
[[ "${output}" != *"password="* ]] || fail "unready status leaked the DSN"

set +e
output="$(unset CONCOURSE_TEST_POSTGRES_DSN; FAKE_PSQL_STATUS=2 run_helper status 2>&1)"
status=$?
set -e
[[ "${status}" -eq 1 ]] || fail "unauthenticated status exited ${status}, want 1"
[[ "${output}" == *"admin connection failed"* ]] || fail "unauthenticated status omitted admin-connection guidance"
[[ "${output}" != *"password="* ]] || fail "unauthenticated status leaked the DSN"

set +e
output="$(unset CONCOURSE_TEST_POSTGRES_DSN; FAKE_PSQL_RESULT=f run_helper status 2>&1)"
status=$?
set -e
[[ "${status}" -eq 1 ]] || fail "unprivileged status exited ${status}, want 1"
[[ "${output}" == *"SUPERUSER"* ]] || fail "unprivileged status omitted SUPERUSER guidance"

exported="$(unset CONCOURSE_TEST_POSTGRES_DSN; run_helper env)" || fail "default env failed"
resolved="$(env -u CONCOURSE_TEST_POSTGRES_DSN bash -c "${exported}; printf '%s' \"\${CONCOURSE_TEST_POSTGRES_DSN}\"")"
[[ "${resolved}" == "${DEFAULT_DSN}" ]] || fail "default env export did not round-trip"

QUOTED_DSN="host=db.internal password=it's-secret dbname=postgres sslmode=require"
exported="$(CONCOURSE_TEST_POSTGRES_DSN="${QUOTED_DSN}" run_helper env)" || fail "override env failed"
resolved="$(env -u CONCOURSE_TEST_POSTGRES_DSN bash -c "${exported}; printf '%s' \"\${CONCOURSE_TEST_POSTGRES_DSN}\"")"
[[ "${resolved}" == "${QUOTED_DSN}" ]] || fail "override env export did not round-trip"

for command in up down unknown; do
	set +e
	output="$(run_helper "${command}" 2>&1)"
	status=$?
	set -e
	[[ "${status}" -eq 2 ]] || fail "${command} exited ${status}, want 2"
	[[ "${output}" == *"usage:"* ]] || fail "${command} omitted usage"
done
[[ ! -s "${DOCKER_LOG}" ]] || fail "unsupported lifecycle command invoked Docker"

bash -n "${TARGET}"
echo "test-postgres helper: PASS"
