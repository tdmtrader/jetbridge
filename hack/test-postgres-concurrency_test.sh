#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/test-postgres-concurrency.sh"
FIXTURES="${SCRIPT_DIR}/test-postgres-concurrency-testdata"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-postgres-concurrency-test.XXXXXX")"
SOURCE_ROOT="${TMP_DIR}/source"
FAKE_BIN="${TMP_DIR}/bin"
PARENT_PID=""
TARGET_STATUS=0
PID_FILES=()

cleanup() {
	local pid_file
	local pid
	local signal
	trap - EXIT
	if [[ -n "${PARENT_PID}" ]] && kill -0 "${PARENT_PID}" 2>/dev/null; then
		kill -TERM "${PARENT_PID}" 2>/dev/null || true
		wait "${PARENT_PID}" 2>/dev/null || true
	fi
	for signal in TERM KILL; do
		for pid_file in "${PID_FILES[@]}"; do
			[[ -f "${pid_file}" ]] || continue
			while IFS= read -r pid; do
				if kill -0 "${pid}" 2>/dev/null; then
					kill "-${signal}" "${pid}" 2>/dev/null || true
				fi
			done <"${pid_file}"
		done
	done
	rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

wait_for_line_count() {
	local file=$1
	local expected=$2
	local description=$3
	local deadline=$((SECONDS + 10))
	local count=0
	while (( SECONDS < deadline )); do
		if [[ -f "${file}" ]]; then
			count="$(wc -l <"${file}")"
			if [[ "${count}" -ge "${expected}" ]]; then
				return 0
			fi
		fi
		sleep 0.02
	done
	fail "timed out waiting for ${description}: got ${count}, want at least ${expected}"
}

wait_for_file() {
	local file=$1
	local description=$2
	local deadline=$((SECONDS + 10))
	while [[ ! -e "${file}" ]] && (( SECONDS < deadline )); do
		sleep 0.02
	done
	[[ -e "${file}" ]] || fail "timed out waiting for ${description}"
}

new_case() {
	CASE_DIR="${TMP_DIR}/$1"
	CHILD_PIDS="${CASE_DIR}/child-pids"
	GRANDCHILD_PIDS="${CASE_DIR}/grandchild-pids"
	TERMINATIONS="${CASE_DIR}/terminations"
	READY_LOG="${CASE_DIR}/pg-isready.log"
	PSQL_LOG="${CASE_DIR}/psql.log"
	DSN_LOG="${CASE_DIR}/dsn.log"
	PARENT_LOG="${CASE_DIR}/parent.log"
	LAUNCH_HOOK_FIRED="${CASE_DIR}/launch-hook-fired"
	CLEANUP_HOOK_READY="${CASE_DIR}/cleanup-hook-ready"
	CLEANUP_HOOK_RELEASE="${CASE_DIR}/cleanup-hook-release"
	CASE_TARGET_TMP="${CASE_DIR}/target-tmp"
	CASE_OBSERVE_TIMEOUT_SECONDS=120
	mkdir -p "${CASE_DIR}" "${CASE_TARGET_TMP}"
	PID_FILES+=("${CHILD_PIDS}" "${GRANDCHILD_PIDS}")
}

start_case() {
	local dsn=$1
	local signal_scenario=$2
	local stubborn_child=$3
	local stubborn_grandchild=$4
	PATH="${FAKE_BIN}:${PATH}" \
	BASH_ENV="${FIXTURES}/signal-hooks" \
	FAKE_GINKGO_SIGNAL_SCENARIO="${signal_scenario}" \
	FAKE_GINKGO_CHILD_PIDS="${CHILD_PIDS}" \
	FAKE_GINKGO_GRANDCHILD_PIDS="${GRANDCHILD_PIDS}" \
	FAKE_GINKGO_TERMINATIONS="${TERMINATIONS}" \
	FAKE_GINKGO_DSN_LOG="${DSN_LOG}" \
	FAKE_GINKGO_STUBBORN="${stubborn_child}" \
	FAKE_GINKGO_GRANDCHILD_STUBBORN="${stubborn_grandchild}" \
	FAKE_GINKGO_LAUNCH_HOOK_FIRED="${LAUNCH_HOOK_FIRED}" \
	FAKE_GINKGO_CLEANUP_HOOK_READY="${CLEANUP_HOOK_READY}" \
	FAKE_GINKGO_CLEANUP_HOOK_RELEASE="${CLEANUP_HOOK_RELEASE}" \
	FAKE_PG_ISREADY_LOG="${READY_LOG}" \
	FAKE_PSQL_LOG="${PSQL_LOG}" \
	CONCOURSE_TEST_POSTGRES_DSN="${dsn}" \
	CONCOURSE_TEST_POSTGRES_OBSERVE_TIMEOUT_SECONDS="${CASE_OBSERVE_TIMEOUT_SECONDS}" \
	CONCOURSE_TEST_SOURCE_ROOT="${SOURCE_ROOT}" \
	TMPDIR="${CASE_TARGET_TMP}" \
	bash "${TARGET}" >"${PARENT_LOG}" 2>&1 &
	PARENT_PID=$!
}

wait_for_target() {
	local pid="${PARENT_PID}"
	set +e
	wait "${pid}"
	TARGET_STATUS=$?
	set -e
	PARENT_PID=""
}

terminate_target() {
	kill -TERM "${PARENT_PID}" 2>/dev/null || fail "target exited before TERM"
	wait_for_target
	[[ "${TARGET_STATUS}" -eq 143 ]] || fail "parent exit status = ${TARGET_STATUS}, want 143"
}

assert_recorded_processes_gone() {
	local pid_file
	local description
	local pid
	local deadline
	for pid_file in "${CHILD_PIDS}" "${GRANDCHILD_PIDS}"; do
		if [[ "${pid_file}" == "${CHILD_PIDS}" ]]; then
			description="fake Ginkgo child"
		else
			description="fake compiled-test grandchild"
		fi
		[[ -f "${pid_file}" ]] || fail "${description} PID file was not written"
		while IFS= read -r pid; do
			deadline=$((SECONDS + 5))
			while kill -0 "${pid}" 2>/dev/null && (( SECONDS < deadline )); do
				sleep 0.02
			done
			kill -0 "${pid}" 2>/dev/null && fail "${description} ${pid} survived parent cleanup"
		done <"${pid_file}"
	done
	return 0
}

assert_termination_count() {
	local kind=$1
	local expected=$2
	local count=0
	if [[ -f "${TERMINATIONS}" ]]; then
		count="$(grep -c "^${kind}$" "${TERMINATIONS}" || true)"
	fi
	[[ "${count}" -eq "${expected}" ]] || fail "expected ${expected} ${kind} terminations, got ${count}"
}

assert_dsn_records() {
	local prefix=$1
	local suffix=$2
	local application_name
	local dsn
	local pipeline_records=0
	local auth_records=0
	while IFS='|' read -r application_name dsn; do
		[[ "${application_name}" =~ ^cc_accept_(pipelineserver|auth)_[0-9]+_[0-9]+$ ]] || fail "invalid generated PGAPPNAME"
		[[ "${dsn}" == "${prefix}${application_name}${suffix}" ]] || fail "DSN application_name did not exactly match PGAPPNAME"
		case "${application_name}" in
			cc_accept_pipelineserver_*) ((pipeline_records += 1)) ;;
			cc_accept_auth_*) ((auth_records += 1)) ;;
		esac
	done <"${DSN_LOG}"
	[[ "${pipeline_records}" -eq 1 ]] || fail "expected one pipelineserver DSN record"
	[[ "${auth_records}" -eq 1 ]] || fail "expected one auth DSN record"
}

run_valid_dsn_case() {
	local name=$1
	local input_dsn=$2
	local expected_prefix=$3
	local expected_suffix=$4
	new_case "${name}"
	start_case "${input_dsn}" "" 0 0
	wait_for_line_count "${CHILD_PIDS}" 2 "${name} suite leaders"
	wait_for_line_count "${GRANDCHILD_PIDS}" 2 "${name} compiled-test processes"
	wait_for_line_count "${DSN_LOG}" 2 "${name} DSN records"
	assert_dsn_records "${expected_prefix}" "${expected_suffix}"
	terminate_target
	assert_recorded_processes_gone
	assert_termination_count child 2
	assert_termination_count grandchild 2
}

run_malformed_case() {
	local name=$1
	local input_dsn=$2
	new_case "${name}"
	CASE_OBSERVE_TIMEOUT_SECONDS=1
	start_case "${input_dsn}" "" 0 0
	wait_for_target
	[[ "${TARGET_STATUS}" -ne 0 ]] || fail "${name} malformed URI unexpectedly succeeded"
	grep -F "ERROR: malformed percent encoding in PostgreSQL URI" "${PARENT_LOG}" >/dev/null || fail "${name} malformed URI did not report a clear error"
	[[ ! -s "${READY_LOG}" ]] || fail "${name} malformed URI reached PostgreSQL readiness before validation"
	[[ ! -s "${DSN_LOG}" ]] || fail "${name} malformed URI reached Ginkgo"
	[[ ! -s "${CHILD_PIDS}" ]] || fail "${name} malformed URI launched a suite process"
}

mkdir -p "${SOURCE_ROOT}/atc/api/pipelineserver" "${SOURCE_ROOT}/atc/api/auth" "${SOURCE_ROOT}/hack" "${FAKE_BIN}"
ln -s "${FIXTURES}/ginkgo" "${FAKE_BIN}/ginkgo"
ln -s "${FIXTURES}/pg_isready" "${FAKE_BIN}/pg_isready"
ln -s "${FIXTURES}/psql" "${FAKE_BIN}/psql"

run_valid_dsn_case \
	uri-postgres \
	"postgres://postgres@127.0.0.1:15432/postgres?application_name=raw-first&sslmode=disable&%61pplication%5Fname=encoded-leading&search_path=some%20value&application%5fname=encoded-lower&application_name=raw-last#retained-fragment" \
	"postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable&search_path=some%20value&application_name=" \
	"#retained-fragment"

run_valid_dsn_case \
	uri-postgresql \
	"postgresql://postgres@127.0.0.1:15432/postgres?application%5Fname=encoded-upper&connect_timeout=7&application_name=raw&application%5fname=encoded-lower#keep" \
	"postgresql://postgres@127.0.0.1:15432/postgres?connect_timeout=7&application_name=" \
	"#keep"

KEYWORD_DSN="host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable application_name=wrong"
run_valid_dsn_case \
	keyword \
	"${KEYWORD_DSN}" \
	"${KEYWORD_DSN} application_name=" \
	""

run_malformed_case malformed-hex "postgres://postgres@127.0.0.1:15432/postgres?application%ZZname=wrong"
run_malformed_case malformed-trailing "postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable%"

new_case steady-term
start_case "postgres://postgres@127.0.0.1:15432/postgres?sslmode=disable" "" 1 0
wait_for_line_count "${CHILD_PIDS}" 2 "steady-state suite leaders"
wait_for_line_count "${GRANDCHILD_PIDS}" 2 "steady-state compiled-test processes"
wait_for_line_count "${PSQL_LOG}" 2 "steady-state catalog observations"
terminate_target
assert_recorded_processes_gone
assert_termination_count child 2
assert_termination_count grandchild 2
[[ -s "${READY_LOG}" ]] || fail "shared PostgreSQL readiness was not checked"

new_case launch-window-term
start_case "host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable" launch 1 1
wait_for_target
[[ "${TARGET_STATUS}" -eq 143 ]] || fail "launch-window parent exit status = ${TARGET_STATUS}, want 143"
wait_for_file "${LAUNCH_HOOK_FIRED}" "launch-window signal boundary"
wait_for_line_count "${CHILD_PIDS}" 2 "launch-window suite leaders"
wait_for_line_count "${GRANDCHILD_PIDS}" 2 "launch-window compiled-test processes"
assert_recorded_processes_gone
assert_termination_count child 2
assert_termination_count grandchild 2

new_case repeated-term
start_case "host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable" repeated-term 1 1
wait_for_file "${CLEANUP_HOOK_READY}" "cleanup grace boundary"
kill -TERM "${PARENT_PID}" 2>/dev/null || fail "target exited before repeated TERM"
: >"${CLEANUP_HOOK_RELEASE}"
wait_for_target
[[ "${TARGET_STATUS}" -eq 143 ]] || fail "repeated-TERM parent exit status = ${TARGET_STATUS}, want 143"
wait_for_file "${LAUNCH_HOOK_FIRED}" "repeated-TERM launch boundary"
wait_for_line_count "${CHILD_PIDS}" 2 "repeated-TERM suite leaders"
wait_for_line_count "${GRANDCHILD_PIDS}" 2 "repeated-TERM compiled-test processes"
assert_recorded_processes_gone
assert_termination_count child 2
assert_termination_count grandchild 2

echo "test-postgres concurrency signal cleanup: PASS"
