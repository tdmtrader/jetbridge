#!/usr/bin/env bash
set -euo pipefail

DEFAULT_ADMIN_DSN="host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable"
ADMIN_DSN="${CONCOURSE_TEST_POSTGRES_DSN:-${DEFAULT_ADMIN_DSN}}"

status() {
	command -v pg_isready >/dev/null 2>&1 || {
		echo "ERROR: pg_isready is required to check the shared PostgreSQL service" >&2
		return 1
	}
	if ! pg_isready -q -d "${ADMIN_DSN}"; then
		echo "ERROR: shared PostgreSQL must already be running; set CONCOURSE_TEST_POSTGRES_DSN for a non-default service" >&2
		return 1
	fi
	printf 'shared PostgreSQL: ready\n'
}

print_env() {
	printf 'export CONCOURSE_TEST_POSTGRES_DSN=%q\n' "${ADMIN_DSN}"
}

case "${1:-}" in
	status) status ;;
	env) print_env ;;
	*)
		echo "usage: $0 {status|env}" >&2
		exit 2
		;;
esac
