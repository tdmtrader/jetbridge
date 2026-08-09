#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/test-postgres-concurrency.sh"
FIXTURES="${SCRIPT_DIR}/test-postgres-concurrency-testdata"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-postgres-concurrency-test.XXXXXX")"
SOURCE_ROOT="${TMP_DIR}/source"
FAKE_BIN="${TMP_DIR}/bin"
CHILD_PIDS="${TMP_DIR}/child-pids"
GRANDCHILD_PIDS="${TMP_DIR}/grandchild-pids"
TERMINATIONS="${TMP_DIR}/terminations"
READY_LOG="${TMP_DIR}/pg-isready.log"
PSQL_LOG="${TMP_DIR}/psql.log"
DSN_LOG="${TMP_DIR}/dsn.log"
PARENT_PID=""

cleanup() {
	local pid
	trap - EXIT
	if [[ -n "${PARENT_PID}" ]] && kill -0 "${PARENT_PID}" 2>/dev/null; then
		kill -TERM "${PARENT_PID}" 2>/dev/null || true
		wait "${PARENT_PID}" 2>/dev/null || true
	fi
	if [[ -f "${CHILD_PIDS}" ]]; then
		while IFS= read -r pid; do
			if kill -0 "${pid}" 2>/dev/null; then
				kill -TERM "${pid}" 2>/dev/null || true
			fi
		done <"${CHILD_PIDS}"
	fi
	if [[ -f "${GRANDCHILD_PIDS}" ]]; then
		while IFS= read -r pid; do
			if kill -0 "${pid}" 2>/dev/null; then
				kill -TERM "${pid}" 2>/dev/null || true
			fi
		done <"${GRANDCHILD_PIDS}"
	fi
	rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

dsn_matches_application_name() {
	local application_name=$1
	local dsn=$2
	[[ "${application_name}" =~ ^cc_accept_(pipelineserver|auth)_[0-9]+_[0-9]+$ ]] || return 1
	[[ "${dsn}" == "postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable&application_name=${application_name}" ]]
}

mkdir -p "${SOURCE_ROOT}/atc/api/pipelineserver" "${SOURCE_ROOT}/atc/api/auth" "${SOURCE_ROOT}/hack" "${FAKE_BIN}"
ln -s "${FIXTURES}/ginkgo" "${FAKE_BIN}/ginkgo"
ln -s "${FIXTURES}/pg_isready" "${FAKE_BIN}/pg_isready"
ln -s "${FIXTURES}/psql" "${FAKE_BIN}/psql"

PATH="${FAKE_BIN}:${PATH}" \
FAKE_GINKGO_CHILD_PIDS="${CHILD_PIDS}" \
FAKE_GINKGO_GRANDCHILD_PIDS="${GRANDCHILD_PIDS}" \
FAKE_GINKGO_TERMINATIONS="${TERMINATIONS}" \
FAKE_GINKGO_DSN_LOG="${DSN_LOG}" \
FAKE_PG_ISREADY_LOG="${READY_LOG}" \
FAKE_PSQL_LOG="${PSQL_LOG}" \
CONCOURSE_TEST_POSTGRES_DSN="postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable&application_name=wrong" \
CONCOURSE_TEST_SOURCE_ROOT="${SOURCE_ROOT}" \
bash "${TARGET}" >"${TMP_DIR}/parent.log" 2>&1 &
PARENT_PID=$!

for _ in $(seq 1 100); do
	[[ -f "${CHILD_PIDS}" ]] && [[ "$(wc -l <"${CHILD_PIDS}")" -eq 2 ]] && break
	sleep 0.05
done
[[ -f "${CHILD_PIDS}" ]] || fail "fake Ginkgo children did not start"
[[ "$(wc -l <"${CHILD_PIDS}")" -eq 2 ]] || fail "expected two fake Ginkgo children"
for _ in $(seq 1 100); do
	[[ -f "${GRANDCHILD_PIDS}" ]] && [[ "$(wc -l <"${GRANDCHILD_PIDS}")" -eq 2 ]] && break
	sleep 0.05
done
[[ -f "${GRANDCHILD_PIDS}" ]] || fail "fake compiled-test grandchildren did not start"
[[ "$(wc -l <"${GRANDCHILD_PIDS}")" -eq 2 ]] || fail "expected two fake compiled-test grandchildren"
[[ -f "${DSN_LOG}" ]] || fail "fake Ginkgo DSNs were not recorded"
[[ "$(wc -l <"${DSN_LOG}")" -eq 2 ]] || fail "expected one DSN record per child"
MISMATCHED_APPLICATION_NAME="cc_accept_pipelineserver_101_202"
MISMATCHED_DSN="postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable&application_name=cc_accept_pipelineserver_303_404"
! dsn_matches_application_name "${MISMATCHED_APPLICATION_NAME}" "${MISMATCHED_DSN}" || fail "DSN assertion accepted a mismatched application name"

PIPELINE_DSN_RECORDS=0
AUTH_DSN_RECORDS=0
while IFS='|' read -r application_name dsn; do
	dsn_matches_application_name "${application_name}" "${dsn}" || fail "DSN application_name did not exactly match PGAPPNAME"
	case "${application_name}" in
		cc_accept_pipelineserver_*) ((PIPELINE_DSN_RECORDS += 1)) ;;
		cc_accept_auth_*) ((AUTH_DSN_RECORDS += 1)) ;;
	esac
done <"${DSN_LOG}"
[[ "${PIPELINE_DSN_RECORDS}" -eq 1 ]] || fail "expected one pipelineserver DSN record"
[[ "${AUTH_DSN_RECORDS}" -eq 1 ]] || fail "expected one auth DSN record"
for _ in $(seq 1 100); do
	[[ -f "${PSQL_LOG}" ]] && [[ "$(wc -l <"${PSQL_LOG}")" -ge 2 ]] && break
	sleep 0.05
done
[[ -f "${PSQL_LOG}" ]] && [[ "$(wc -l <"${PSQL_LOG}")" -ge 2 ]] || fail "catalog observation did not complete"

kill -TERM "${PARENT_PID}"
set +e
wait "${PARENT_PID}"
PARENT_STATUS=$?
set -e
PARENT_PID=""
[[ "${PARENT_STATUS}" -eq 143 ]] || fail "parent exit status = ${PARENT_STATUS}, want 143"

while IFS= read -r pid; do
	if kill -0 "${pid}" 2>/dev/null; then
		fail "fake Ginkgo child ${pid} survived parent TERM"
	fi
done <"${CHILD_PIDS}"
while IFS= read -r pid; do
	if kill -0 "${pid}" 2>/dev/null; then
		fail "fake compiled-test grandchild ${pid} survived parent TERM"
	fi
done <"${GRANDCHILD_PIDS}"

[[ -f "${TERMINATIONS}" ]] || fail "fake Ginkgo children were not terminated"
[[ "$(grep -c '^child$' "${TERMINATIONS}")" -eq 2 ]] || fail "expected one termination per Ginkgo child"
[[ "$(grep -c '^grandchild$' "${TERMINATIONS}")" -eq 2 ]] || fail "expected one termination per compiled-test grandchild"
[[ -s "${READY_LOG}" ]] || fail "shared PostgreSQL readiness was not checked"
[[ -s "${PSQL_LOG}" ]] || fail "catalog was not queried through psql"

echo "test-postgres concurrency signal cleanup: PASS"
