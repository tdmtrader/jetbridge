#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_ROOT="${CONCOURSE_TEST_SOURCE_ROOT:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-postgres-concurrency.XXXXXX")"
TOKEN="$(date +%s)_${$}"
PIPELINE_APPLICATION_NAME="cc_accept_pipelineserver_${TOKEN}"
AUTH_APPLICATION_NAME="cc_accept_auth_${TOKEN}"
PIPELINE_LOG="${LOG_DIR}/pipelineserver.log"
AUTH_LOG="${LOG_DIR}/auth.log"
CATALOG_SNAPSHOT="${LOG_DIR}/pg_stat_activity.log"
PIPELINE_PID=""
AUTH_PID=""
OBSERVED=0

cleanup() {
	local status=$?
	trap - EXIT

	for pid in "${PIPELINE_PID}" "${AUTH_PID}"; do
		if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
			kill "${pid}" 2>/dev/null || true
		fi
	done
	for pid in "${PIPELINE_PID}" "${AUTH_PID}"; do
		if [[ -n "${pid}" ]]; then
			wait "${pid}" 2>/dev/null || true
		fi
	done

	if [[ "${status}" -eq 0 ]]; then
		rm -rf "${LOG_DIR}"
	else
		echo "shared PostgreSQL concurrency: FAIL (logs and catalog snapshot preserved in ${LOG_DIR})" >&2
	fi
	exit "${status}"
}
trap cleanup EXIT

require_source_root() {
	[[ -d "${SOURCE_ROOT}/atc/api/pipelineserver" ]] || {
		echo "ERROR: CONCOURSE_TEST_SOURCE_ROOT is not a Concourse source tree: ${SOURCE_ROOT}" >&2
		exit 2
	}
	command -v ginkgo >/dev/null 2>&1 || {
		echo "ERROR: ginkgo is required" >&2
		exit 2
	}
}

default_admin_dsn() {
	local exported
	exported="$("${SOURCE_ROOT}/hack/test-postgres.sh" env)"
	# The helper owns the default admin DSN. Source only its single export, never
	# print it, so a caller-supplied password remains out of this script's output.
	eval "${exported}"
	printf '%s' "${CONCOURSE_TEST_POSTGRES_DSN}"
}

child_alive() {
	local pid=$1
	[[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null
}

catalog_rows() {
	docker --context colima exec concourse-test-postgres psql -U postgres -d postgres -At -F '|' \
		-v ON_ERROR_STOP=1 \
		-c "SELECT application_name, datname FROM pg_stat_activity WHERE application_name IN ('${PIPELINE_APPLICATION_NAME}', '${AUTH_APPLICATION_NAME}') AND datname LIKE 'cc_db_%' ORDER BY application_name, datname;"
}

observe_distinct_clones() {
	local snapshot pipeline_db auth_db attempt
	for attempt in $(seq 1 1200); do
		if ! child_alive "${PIPELINE_PID}" || ! child_alive "${AUTH_PID}"; then
			return 1
		fi

		snapshot="$(catalog_rows 2>&1)" || {
			printf '%s\n' "${snapshot}" >>"${CATALOG_SNAPSHOT}"
			return 1
		}
		printf '%s\n' "${snapshot}" >>"${CATALOG_SNAPSHOT}"
		pipeline_db="$(printf '%s\n' "${snapshot}" | awk -F '|' -v app="${PIPELINE_APPLICATION_NAME}" '$1 == app { print $2; exit }')"
		auth_db="$(printf '%s\n' "${snapshot}" | awk -F '|' -v app="${AUTH_APPLICATION_NAME}" '$1 == app { print $2; exit }')"
		if [[ "${pipeline_db}" =~ ^cc_db_ ]] && [[ "${auth_db}" =~ ^cc_db_ ]] && [[ "${pipeline_db}" != "${auth_db}" ]] && child_alive "${PIPELINE_PID}" && child_alive "${AUTH_PID}"; then
			printf '%s|%s\n%s|%s\n' "${PIPELINE_APPLICATION_NAME}" "${pipeline_db}" "${AUTH_APPLICATION_NAME}" "${auth_db}" >"${CATALOG_SNAPSHOT}"
			OBSERVED=1
			printf 'observed concurrent clones: %s, %s\n' "${pipeline_db}" "${auth_db}"
			return 0
		fi
	done
	return 1
}

start_suite() {
	local application_name=$1
	local package=$2
	local log=$3
	local admin_dsn=$4
	local repeat=$5
	(
		cd "${SOURCE_ROOT}"
		CONCOURSE_TEST_POSTGRES_DSN="${admin_dsn} application_name=${application_name}" \
			ginkgo --procs=1 --no-color --repeat="${repeat}" "${package}"
	) >"${log}" 2>&1 &
	SUITE_PID=$!
}

require_source_root
"${SOURCE_ROOT}/hack/test-postgres.sh" status
ADMIN_DSN="$(default_admin_dsn)"

# Pipelineserver is deliberately repeated: its individual clone windows are
# very short, so repeating the independent package keeps its child alive long
# enough to observe a real overlap with auth rather than a polling race.
start_suite "${PIPELINE_APPLICATION_NAME}" ./atc/api/pipelineserver "${PIPELINE_LOG}" "${ADMIN_DSN}" 3
PIPELINE_PID="${SUITE_PID}"
start_suite "${AUTH_APPLICATION_NAME}" ./atc/api/auth "${AUTH_LOG}" "${ADMIN_DSN}" 0
AUTH_PID="${SUITE_PID}"

if ! observe_distinct_clones; then
	echo "ERROR: did not observe both suite application names simultaneously on distinct cc_db_ clones" >&2
	exit 1
fi

set +e
wait "${PIPELINE_PID}"
PIPELINE_STATUS=$?
wait "${AUTH_PID}"
AUTH_STATUS=$?
set -e

if [[ "${PIPELINE_STATUS}" -ne 0 || "${AUTH_STATUS}" -ne 0 ]]; then
	echo "ERROR: suite command failed (pipelineserver=${PIPELINE_STATUS}, auth=${AUTH_STATUS})" >&2
	exit 1
fi
if [[ "${OBSERVED}" -ne 1 ]]; then
	echo "ERROR: concurrent clone observation was not recorded" >&2
	exit 1
fi

echo "shared PostgreSQL concurrency: PASS"
