#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFLIGHT="${SCRIPT_DIR}/migrate-preflight.sh"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMP_ROOT}"' EXIT

cat >"${TMP_ROOT}/psql" <<'FAKE_PSQL'
#!/usr/bin/env bash
set -euo pipefail

query=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "-c" ]]; then
    query="$2"
    break
  fi
  shift
done

case "${query}" in
  "SELECT 1") echo "1" ;;
  "SHOW server_version;") echo "15.4" ;;
  *"table_name = 'migrations_history'"*) echo "t" ;;
  *"table_name = 'schema_migrations'"*) echo "f" ;;
  *"table_name = 'migration_version'"*) echo "f" ;;
  *"SELECT version, direction FROM migrations_history"*)
    echo "${FAKE_HEAD_VERSION}|${FAKE_HEAD_DIRECTION}"
    ;;
  *"SELECT max(version)"*"migrations_history"*)
    echo "${FAKE_PREVIOUS_VERSION}"
    ;;
  *"SELECT dirty FROM migrations_history"*) ;;
  *"pg_extension"*"pgcrypto"*) echo "t" ;;
  *"SELECT count("*|*"SELECT count(*)"*) echo "0" ;;
  *"pg_size_pretty"*) echo "1 MB" ;;
  *) echo "0" ;;
esac
FAKE_PSQL
chmod +x "${TMP_ROOT}/psql"

run_case() {
  local name="$1"
  local head="$2"
  local direction="$3"
  local previous="$4"
  local expected_status="$5"
  local expected="$6"
  local forbidden="$7"
  local output
  local status

  set +e
  output="$(
    PATH="${TMP_ROOT}:${PATH}" \
      FAKE_HEAD_VERSION="${head}" \
      FAKE_HEAD_DIRECTION="${direction}" \
      FAKE_PREVIOUS_VERSION="${previous}" \
      bash "${PREFLIGHT}" 2>&1
  )"
  status=$?
  set -e

  if [[ "${status}" -ne "${expected_status}" ]]; then
    echo "${name}: exit ${status}, want ${expected_status}"
    echo "${output}"
    return 1
  fi
  if [[ "${output}" != *"${expected}"* ]]; then
    echo "${name}: missing ${expected}"
    echo "${output}"
    return 1
  fi
  if [[ -n "${forbidden}" && "${output}" == *"${forbidden}"* ]]; then
    echo "${name}: unexpectedly contained ${forbidden}"
    echo "${output}"
    return 1
  fi
}

# These cases are relative to the script's own target, not to hard-coded
# numbers: two of them only mean what they say when the "previous" version IS
# the JetBridge target, so pinning literals here silently breaks the suite on
# every migration bump.
JETBRIDGE_TARGET="$(sed -n 's/^JETBRIDGE_VERSION=\([0-9]*\)$/\1/p' "${PREFLIGHT}")"
if [[ -z "${JETBRIDGE_TARGET}" ]]; then
  echo "could not read JETBRIDGE_VERSION from ${PREFLIGHT}"
  exit 1
fi
BELOW_TARGET="$((JETBRIDGE_TARGET - 1))"
ABOVE_TARGET="$((JETBRIDGE_TARGET + 1))"

run_case \
  "rolled back JetBridge head" \
  "${JETBRIDGE_TARGET}" "down" "${BELOW_TARGET}" \
  0 \
  "Current migration version: ${BELOW_TARGET}" \
  "already at JetBridge version"

run_case \
  "rolled back newer head to JetBridge" \
  "${ABOVE_TARGET}" "down" "${JETBRIDGE_TARGET}" \
  0 \
  "already at JetBridge version" \
  "downgrade not supported"

run_case \
  "applied newer head" \
  "${ABOVE_TARGET}" "up" "${JETBRIDGE_TARGET}" \
  1 \
  "downgrade not supported" \
  ""

echo "migrate-preflight direction tests passed"
