#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/test-postgres-concurrency.sh"
FIXTURES="${SCRIPT_DIR}/test-postgres-concurrency-testdata"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-postgres-concurrency-test.XXXXXX")"
SOURCE_ROOT="${TMP_DIR}/source"
FAKE_BIN="${TMP_DIR}/bin"
CHILD_PIDS="${TMP_DIR}/child-pids"
TERMINATIONS="${TMP_DIR}/terminations"
READY_LOG="${TMP_DIR}/pg-isready.log"
PSQL_LOG="${TMP_DIR}/psql.log"
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
	rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

mkdir -p "${SOURCE_ROOT}/atc/api/pipelineserver" "${SOURCE_ROOT}/atc/api/auth" "${SOURCE_ROOT}/hack" "${FAKE_BIN}"
ln -s "${FIXTURES}/ginkgo" "${FAKE_BIN}/ginkgo"
ln -s "${FIXTURES}/pg_isready" "${FAKE_BIN}/pg_isready"
ln -s "${FIXTURES}/psql" "${FAKE_BIN}/psql"

PATH="${FAKE_BIN}:${PATH}" \
FAKE_GINKGO_CHILD_PIDS="${CHILD_PIDS}" \
FAKE_GINKGO_TERMINATIONS="${TERMINATIONS}" \
FAKE_PG_ISREADY_LOG="${READY_LOG}" \
FAKE_PSQL_LOG="${PSQL_LOG}" \
CONCOURSE_TEST_SOURCE_ROOT="${SOURCE_ROOT}" \
bash "${TARGET}" >"${TMP_DIR}/parent.log" 2>&1 &
PARENT_PID=$!

for _ in $(seq 1 100); do
	[[ -f "${CHILD_PIDS}" ]] && [[ "$(wc -l <"${CHILD_PIDS}")" -eq 2 ]] && break
	sleep 0.05
done
[[ -f "${CHILD_PIDS}" ]] || fail "fake Ginkgo children did not start"
[[ "$(wc -l <"${CHILD_PIDS}")" -eq 2 ]] || fail "expected two fake Ginkgo children"

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

[[ -f "${TERMINATIONS}" ]] || fail "fake Ginkgo children were not terminated"
[[ "$(wc -l <"${TERMINATIONS}")" -eq 2 ]] || fail "expected one termination per direct child"
[[ -s "${READY_LOG}" ]] || fail "shared PostgreSQL readiness was not checked"
[[ -s "${PSQL_LOG}" ]] || fail "catalog was not queried through psql"

echo "test-postgres concurrency signal cleanup: PASS"
