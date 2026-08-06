#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
	status) echo "concourse-test-postgres: running (ready)" ;;
	env) echo "export CONCOURSE_TEST_POSTGRES_DSN='host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable'" ;;
	*) exit 2 ;;
esac
