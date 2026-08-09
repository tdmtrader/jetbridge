#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_ROOT="${CONCOURSE_TEST_SOURCE_ROOT:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
DEFAULT_ADMIN_DSN="host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable"
ADMIN_DSN="${CONCOURSE_TEST_POSTGRES_DSN:-${DEFAULT_ADMIN_DSN}}"
LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/concourse-postgres-concurrency.XXXXXX")"
TOKEN="$(date +%s)_${$}"
PIPELINE_APPLICATION_NAME="cc_accept_pipelineserver_${TOKEN}"
AUTH_APPLICATION_NAME="cc_accept_auth_${TOKEN}"
PIPELINE_LOG="${LOG_DIR}/pipelineserver.log"
AUTH_LOG="${LOG_DIR}/auth.log"
CATALOG_SNAPSHOT="${LOG_DIR}/pg_stat_activity.log"
PIPELINE_PID=""
AUTH_PID=""
PIPELINE_PGID=""
AUTH_PGID=""
SUITE_PID=""
SUITE_PGID=""
OBSERVED=0
CLEANUP_STARTED=0
LAUNCH_IN_PROGRESS=0
PENDING_SIGNAL_STATUS=""

cleanup() {
	local status=$1
	if (( CLEANUP_STARTED++ )); then
		return
	fi
	trap - EXIT
	trap '' HUP INT TERM

	local pid
	local pgid
	for pid_and_pgid in "${PIPELINE_PID}:${PIPELINE_PGID}" "${AUTH_PID}:${AUTH_PGID}" "${SUITE_PID}:${SUITE_PGID}"; do
		pid="${pid_and_pgid%%:*}"
		pgid="${pid_and_pgid#*:}"
		if [[ -n "${pid}" && -n "${pgid}" ]] && process_group_alive "${pgid}"; then
			kill -TERM -- "-${pgid}" 2>/dev/null || true
		fi
	done

	local grace_deadline=$((SECONDS + 5))
	while (( SECONDS < grace_deadline )); do
		if ! process_group_alive "${PIPELINE_PGID}" && ! process_group_alive "${AUTH_PGID}"; then
			break
		fi
		sleep 0.05
	done
	for pid_and_pgid in "${PIPELINE_PID}:${PIPELINE_PGID}" "${AUTH_PID}:${AUTH_PGID}" "${SUITE_PID}:${SUITE_PGID}"; do
		pid="${pid_and_pgid%%:*}"
		pgid="${pid_and_pgid#*:}"
		if [[ -n "${pid}" && -n "${pgid}" ]] && process_group_alive "${pgid}"; then
			kill -KILL -- "-${pgid}" 2>/dev/null || true
		fi
	done
	for pid in "${PIPELINE_PID}" "${AUTH_PID}" "${SUITE_PID}"; do
		if [[ -n "${pid}" ]]; then
			wait "${pid}" 2>/dev/null || true
		fi
	done

	if [[ "${status}" -eq 0 ]]; then
		rm -rf "${LOG_DIR}"
	else
		echo "shared PostgreSQL concurrency: FAIL (logs and catalog snapshot preserved in ${LOG_DIR})" >&2
	fi
}

on_exit() {
	local status=$?
	cleanup "${status}"
}

on_signal() {
	local status=$1
	if [[ "${CLEANUP_STARTED}" -ne 0 ]]; then
		return
	fi
	if [[ "${LAUNCH_IN_PROGRESS}" -eq 1 ]]; then
		if [[ -z "${PENDING_SIGNAL_STATUS}" ]]; then
			PENDING_SIGNAL_STATUS="${status}"
		fi
		return
	fi
	cleanup "${status}"
	exit "${status}"
}

trap on_exit EXIT
trap 'on_signal 129' HUP
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

require_source_root() {
	[[ -d "${SOURCE_ROOT}/atc/api/pipelineserver" ]] || {
		echo "ERROR: CONCOURSE_TEST_SOURCE_ROOT is not a Concourse source tree: ${SOURCE_ROOT}" >&2
		exit 2
	}
	command -v ginkgo >/dev/null 2>&1 || {
		echo "ERROR: ginkgo is required" >&2
		exit 2
	}
	command -v pg_isready >/dev/null 2>&1 || {
		echo "ERROR: pg_isready is required" >&2
		exit 2
	}
	command -v psql >/dev/null 2>&1 || {
		echo "ERROR: psql is required" >&2
		exit 2
	}
}

require_shared_postgres() {
	if ! pg_isready -q -d "${ADMIN_DSN}"; then
		echo "ERROR: shared PostgreSQL must already be running; set CONCOURSE_TEST_POSTGRES_DSN for a non-default service" >&2
		exit 1
	fi
}

valid_percent_encoding() {
	local LC_ALL=C
	local text=$1
	while [[ "${text}" == *%* ]]; do
		text="${text#*%}"
		[[ "${text}" =~ ^[[:xdigit:]][[:xdigit:]] ]] || return 1
		text="${text:2}"
	done
}

validate_postgres_uri_percent_encoding() {
	local dsn=$1
	case "${dsn}" in
		postgres://*|postgresql://*)
			if ! valid_percent_encoding "${dsn}"; then
				echo "ERROR: malformed percent encoding in PostgreSQL URI" >&2
				return 1
			fi
			;;
	esac
}

query_key_is_application_name() {
	local LC_ALL=C
	local encoded=$1
	local expected="application_name"
	local byte
	local actual
	while [[ -n "${encoded}" ]]; do
		[[ -n "${expected}" ]] || return 1
		if [[ "${encoded}" == %* ]]; then
			byte="${encoded:1:2}"
			encoded="${encoded:3}"
			[[ "${byte}" != "00" ]] || return 1
			printf -v actual '%b' "\\x${byte}"
		else
			actual="${encoded:0:1}"
			encoded="${encoded:1}"
		fi
		[[ "${actual}" == "${expected:0:1}" ]] || return 1
		expected="${expected:1}"
	done
	[[ -z "${expected}" ]]
}

dsn_with_application_name() {
	local dsn=$1
	local application_name=$2
	if [[ ! "${application_name}" =~ ^[A-Za-z0-9_.-]+$ ]]; then
		echo "ERROR: unsafe PostgreSQL application name" >&2
		return 1
	fi

	case "${dsn}" in
		postgres://*|postgresql://*)
			validate_postgres_uri_percent_encoding "${dsn}" || return 1
			local fragment=""
			local without_fragment="${dsn}"
			if [[ "${without_fragment}" == *#* ]]; then
				fragment="#${without_fragment#*#}"
				without_fragment="${without_fragment%%#*}"
			fi

			local base="${without_fragment%%\?*}"
			local query=""
			if [[ "${without_fragment}" == *\?* ]]; then
				query="${without_fragment#*\?}"
			fi

			local rebuilt=""
			local parameter
			local key
			local parameters=()
			IFS='&' read -r -a parameters <<<"${query}"
			for parameter in "${parameters[@]}"; do
				[[ -n "${parameter}" ]] || continue
				key="${parameter%%=*}"
				query_key_is_application_name "${key}" && continue
				if [[ -n "${rebuilt}" ]]; then
					rebuilt+="&"
				fi
				rebuilt+="${parameter}"
			done
			if [[ -n "${rebuilt}" ]]; then
				rebuilt+="&"
			fi
			rebuilt+="application_name=${application_name}"
			printf '%s?%s%s' "${base}" "${rebuilt}" "${fragment}"
			;;
		*)
			printf '%s application_name=%s' "${dsn}" "${application_name}"
			;;
	esac
}

child_alive() {
	local pid=$1
	[[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null
}

process_group_alive() {
	local pgid=$1
	[[ -n "${pgid}" ]] && kill -0 -- "-${pgid}" 2>/dev/null
}

catalog_rows() {
	psql --dbname="${ADMIN_DSN}" -At -F '|' \
		-v ON_ERROR_STOP=1 \
		-c "SELECT application_name, datname FROM pg_stat_activity WHERE application_name IN ('${PIPELINE_APPLICATION_NAME}', '${AUTH_APPLICATION_NAME}') AND datname LIKE 'cc_db_%' ORDER BY application_name, datname;"
}

database_rows() {
	psql --dbname="${ADMIN_DSN}" -At \
		-v ON_ERROR_STOP=1 \
		-c "SELECT datname FROM pg_database WHERE datname LIKE 'cc_db_%' ORDER BY datname;"
}

run_id_from_database() {
	local database=$1
	if [[ "${database}" =~ ^cc_db_(t[1-9][0-9]*_p[1-9][0-9]*_[0-9a-f]{8})_n[1-9][0-9]*_s[1-9][0-9]*$ ]]; then
		printf '%s' "${BASH_REMATCH[1]}"
		return 0
	fi
	return 1
}

observe_distinct_clones() {
	local snapshot databases application_name database run_id
	local pipeline_db=""
	local auth_db=""
	local pipeline_run_id=""
	local auth_run_id=""
	local timeout_seconds="${CONCOURSE_TEST_POSTGRES_OBSERVE_TIMEOUT_SECONDS:-120}"
	if [[ ! "${timeout_seconds}" =~ ^[1-9][0-9]*$ ]]; then
		echo "ERROR: CONCOURSE_TEST_POSTGRES_OBSERVE_TIMEOUT_SECONDS must be a positive integer" >&2
		return 1
	fi
	local deadline=$((SECONDS + timeout_seconds))
	while (( SECONDS < deadline )); do
		if ! child_alive "${PIPELINE_PID}" || ! child_alive "${AUTH_PID}"; then
			return 1
		fi

		snapshot="$(catalog_rows 2>&1)" || {
			printf '%s\n' "${snapshot}" >>"${CATALOG_SNAPSHOT}"
			return 1
		}
		printf '%s\n%s\n' "--- activity" "${snapshot}" >>"${CATALOG_SNAPSHOT}"
		while IFS='|' read -r application_name database; do
			[[ -n "${application_name}" && -n "${database}" ]] || continue
			run_id="$(run_id_from_database "${database}")" || continue
			case "${application_name}" in
				"${PIPELINE_APPLICATION_NAME}") pipeline_run_id="${run_id}" ;;
				"${AUTH_APPLICATION_NAME}") auth_run_id="${run_id}" ;;
			esac
		done <<<"${snapshot}"

		if [[ -n "${pipeline_run_id}" && -n "${auth_run_id}" && "${pipeline_run_id}" != "${auth_run_id}" ]]; then
			databases="$(database_rows 2>&1)" || {
				printf '%s\n%s\n' "--- databases-error" "${databases}" >>"${CATALOG_SNAPSHOT}"
				return 1
			}
			printf '%s\n%s\n' "--- databases" "${databases}" >>"${CATALOG_SNAPSHOT}"
			pipeline_db="$(printf '%s\n' "${databases}" | awk -v prefix="cc_db_${pipeline_run_id}_" 'index($0, prefix) == 1 { print; exit }')"
			auth_db="$(printf '%s\n' "${databases}" | awk -v prefix="cc_db_${auth_run_id}_" 'index($0, prefix) == 1 { print; exit }')"
		fi
		# The tagged connections identify each suite's generated run ID; one
		# pg_database snapshot then proves clones from those two runs existed at
		# the same time, even when their individual SQL connections are brief.
		if [[ "${pipeline_db}" =~ ^cc_db_ ]] && [[ "${auth_db}" =~ ^cc_db_ ]] && [[ "${pipeline_db}" != "${auth_db}" ]]; then
			printf '%s|%s\n%s|%s\n' "${PIPELINE_APPLICATION_NAME}" "${pipeline_db}" "${AUTH_APPLICATION_NAME}" "${auth_db}" >"${CATALOG_SNAPSHOT}"
			OBSERVED=1
			printf 'observed concurrent clones: %s, %s\n' "${pipeline_db}" "${auth_db}"
			return 0
		fi
		sleep 0.05
	done
	return 1
}

start_suite() {
	local suite=$1
	local application_name=$2
	local package=$3
	local log=$4
	local admin_dsn=$5
	local child_dsn
	child_dsn="$(dsn_with_application_name "${admin_dsn}" "${application_name}")"
	LAUNCH_IN_PROGRESS=1
	set -m
	(
		cd "${SOURCE_ROOT}"
		exec env "CONCOURSE_TEST_POSTGRES_DSN=${child_dsn}" "PGAPPNAME=${application_name}" \
			ginkgo --procs=1 --no-color "${package}"
	) >"${log}" 2>&1 &
	SUITE_PID=$!
	SUITE_PGID="${SUITE_PID}"
	case "${suite}" in
		AUTH)
			AUTH_PID="${SUITE_PID}"
			AUTH_PGID="${SUITE_PGID}"
			;;
		PIPELINE)
			PIPELINE_PID="${SUITE_PID}"
			PIPELINE_PGID="${SUITE_PGID}"
			;;
		*)
			echo "ERROR: unknown suite launch slot: ${suite}" >&2
			return 2
			;;
	esac
	set +m
	SUITE_PID=""
	SUITE_PGID=""
	LAUNCH_IN_PROGRESS=0
	if [[ -n "${PENDING_SIGNAL_STATUS}" ]]; then
		local pending_status="${PENDING_SIGNAL_STATUS}"
		PENDING_SIGNAL_STATUS=""
		on_signal "${pending_status}"
	fi
}

require_source_root
validate_postgres_uri_percent_encoding "${ADMIN_DSN}"
require_shared_postgres

# Auth is the longer suite, so start it first and then start pipelineserver.
start_suite AUTH "${AUTH_APPLICATION_NAME}" ./atc/api/auth "${AUTH_LOG}" "${ADMIN_DSN}"
start_suite PIPELINE "${PIPELINE_APPLICATION_NAME}" ./atc/api/pipelineserver "${PIPELINE_LOG}" "${ADMIN_DSN}"

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
