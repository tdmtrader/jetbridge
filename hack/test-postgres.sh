#!/usr/bin/env bash
set -euo pipefail

DOCKER_CONTEXT="colima"
CONTAINER="concourse-test-postgres"
IMAGE="postgres:14"
HOST_PORT="15432"
LABEL="com.concourse.test-postgres=true"
DSN="host=127.0.0.1 port=${HOST_PORT} user=postgres dbname=postgres sslmode=disable"

d() { docker --context "${DOCKER_CONTEXT}" "$@"; }

require_colima() {
  command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required" >&2; exit 1; }
  d context inspect "${DOCKER_CONTEXT}" >/dev/null 2>&1 || {
    echo "ERROR: Docker context '${DOCKER_CONTEXT}' does not exist" >&2
    exit 1
  }
  d info >/dev/null 2>&1 || {
    echo "ERROR: Colima is not running; start the existing runtime before running tests" >&2
    exit 1
  }
}

inspect_container() {
	local target="${1:-${CONTAINER}}"
	d container inspect --format '{{ .Id }}|{{ .Name }}|{{ index .Config.Labels "com.concourse.test-postgres" }}|{{ .State.Status }}|{{ .Config.Image }}|{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostIp }}|{{ (index (index .HostConfig.PortBindings "5432/tcp") 0).HostPort }}|{{ json .Config.Cmd }}|{{ json .Config.Env }}|{{ json .HostConfig.PortBindings }}' "${target}" 2>/dev/null
}

wait_ready() {
	local container_id="$1"
	for _ in $(seq 1 60); do
		d exec "${container_id}" pg_isready -U postgres -d postgres >/dev/null 2>&1 && return 0
		sleep 1
  done
  echo "ERROR: PostgreSQL did not become ready within 60 seconds" >&2
  return 1
}

parse_inspection() {
	IFS='|' read -r INSPECT_ID INSPECT_NAME INSPECT_OWNED INSPECT_STATE INSPECT_IMAGE INSPECT_HOST_IP INSPECT_HOST_PORT INSPECT_CMD INSPECT_ENV INSPECT_PORT_BINDINGS <<<"$1"
}

validate_ownership() {
  if [[ "${INSPECT_OWNED}" != "true" ]]; then
    echo "ERROR: ${CONTAINER} exists but is not owned by ${LABEL}; refusing to modify it" >&2
    return 1
  fi
}

validate_contract() {
	if [[ "${INSPECT_NAME}" != "/${CONTAINER}" ]]; then
		echo "ERROR: ${CONTAINER} name differs (got ${INSPECT_NAME}, want /${CONTAINER})" >&2
		return 1
	fi
	if [[ "${INSPECT_IMAGE}" != "${IMAGE}" ]]; then
    echo "ERROR: ${CONTAINER} image differs (got ${INSPECT_IMAGE}, want ${IMAGE})" >&2
    return 1
  fi
  if [[ "${INSPECT_HOST_IP}" != "127.0.0.1" ]]; then
    echo "ERROR: ${CONTAINER} loopback binding differs (got ${INSPECT_HOST_IP}, want 127.0.0.1)" >&2
    return 1
  fi
  if [[ "${INSPECT_HOST_PORT}" != "${HOST_PORT}" ]]; then
    echo "ERROR: ${CONTAINER} host port differs (got ${INSPECT_HOST_PORT}, want ${HOST_PORT})" >&2
    return 1
  fi
  if [[ "${INSPECT_PORT_BINDINGS}" != '{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15432"}]}' ]]; then
    echo "ERROR: ${CONTAINER} port bindings differ; exactly 127.0.0.1:${HOST_PORT}:5432 is required" >&2
    return 1
  fi
  if [[ "${INSPECT_CMD}" != '["-c","fsync=off","-c","synchronous_commit=off","-c","full_page_writes=off","-c","max_connections=500"]' ]]; then
    echo "ERROR: ${CONTAINER} PostgreSQL command differs" >&2
    return 1
  fi
  if [[ "${INSPECT_ENV}" != *'"POSTGRES_HOST_AUTH_METHOD=trust"'* ]]; then
    echo "ERROR: ${CONTAINER} trust environment differs" >&2
    return 1
  fi
}

inspect_owned_contract() {
	local inspection
	local target="${1:-${CONTAINER}}"
	if ! inspection="$(inspect_container "${target}")"; then
		return 1
  fi
  parse_inspection "${inspection}"
  validate_ownership
	validate_contract
}

revalidate_same_container() {
	local expected_id="$1"
	if ! inspect_owned_contract "${CONTAINER}"; then
		return 1
	fi
	if [[ "${INSPECT_ID}" != "${expected_id}" ]]; then
		echo "ERROR: ${CONTAINER} was replaced while it was being validated; refusing the replacement" >&2
		return 1
	fi
}

create_container() {
  d run --detach \
    --name "${CONTAINER}" \
    --label "${LABEL}" \
    --publish "127.0.0.1:${HOST_PORT}:5432" \
    --env POSTGRES_HOST_AUTH_METHOD=trust \
    "${IMAGE}" \
    -c fsync=off \
    -c synchronous_commit=off \
    -c full_page_writes=off \
    -c max_connections=500
}

up() {
  local attempt
  local inspection

  require_colima
	for attempt in $(seq 1 5); do
		if inspect_owned_contract; then
			local container_id="${INSPECT_ID}"
			case "${INSPECT_STATE}" in
				running)
					if wait_ready "${container_id}"; then
						if revalidate_same_container "${container_id}"; then
							return 0
						fi
						return 1
					fi
          # A container may have disappeared while readiness was being polled.
          # Re-inspect once before treating an existing but unready service as
          # an error, so a concurrent up can create its replacement.
          if inspect_owned_contract; then
            echo "ERROR: ${CONTAINER} is running but PostgreSQL is not ready" >&2
            return 1
          fi
          ;;
				exited|created)
					d start "${container_id}" >/dev/null 2>&1 || true
          ;;
        *)
          echo "ERROR: ${CONTAINER} is owned but has unsupported state ${INSPECT_STATE}" >&2
          return 1
          ;;
      esac
    else
      # An inspect failure is either absence or a validation failure. Inspect
      # separately so foreign and drifted containers remain explicit errors.
      if inspection="$(inspect_container)"; then
        parse_inspection "${inspection}"
        validate_ownership
        validate_contract
        echo "ERROR: ${CONTAINER} could not be inspected" >&2
        return 1
      fi
      create_container >/dev/null 2>&1 || true
    fi
  done

  echo "ERROR: could not create or start ${CONTAINER} after concurrent updates" >&2
  return 1
}

status() {
  require_colima
  if ! inspect_owned_contract; then
    local inspection
    if inspection="$(inspect_container)"; then
      parse_inspection "${inspection}"
      validate_ownership
      validate_contract
    fi
    echo "ERROR: ${CONTAINER} is absent or does not match the required test PostgreSQL contract" >&2
    return 1
  fi
	if [[ "${INSPECT_STATE}" != "running" ]]; then
    echo "ERROR: ${CONTAINER} is ${INSPECT_STATE}; run $0 up" >&2
    return 1
  fi
	local container_id="${INSPECT_ID}"
	wait_ready "${container_id}"
	revalidate_same_container "${container_id}"
	printf '%s: running (ready)\n' "${CONTAINER}"
}

down() {
  local inspection

  require_colima
  if ! inspection="$(inspect_container)"; then
    return 0
	fi
	parse_inspection "${inspection}"
	validate_ownership
	local container_id="${INSPECT_ID}"
	d rm --force "${container_id}" >/dev/null 2>&1 || {
		# A concurrent teardown may already have removed it. Last mutation wins.
		if ! inspection="$(inspect_container)"; then
			return 0
		fi
		parse_inspection "${inspection}"
		validate_ownership
		if [[ "${INSPECT_ID}" != "${container_id}" ]]; then
			echo "ERROR: ${CONTAINER} was replaced during teardown; refusing the replacement" >&2
			return 1
		fi
		echo "ERROR: failed to remove ${CONTAINER}" >&2
		return 1
	}

	# A successful ID-targeted removal must leave the well-known name absent.
	# If another container claimed it concurrently, never treat that replacement
	# as the object whose ownership was validated above.
	if inspection="$(inspect_container)"; then
		parse_inspection "${inspection}"
		validate_ownership
		if [[ "${INSPECT_ID}" != "${container_id}" ]]; then
			echo "ERROR: ${CONTAINER} was replaced during teardown; refusing the replacement" >&2
			return 1
		fi
		echo "ERROR: ${CONTAINER} still exists after teardown" >&2
		return 1
	fi
}

case "${1:-}" in
  up) up ;;
  status) status ;;
  env) printf "export CONCOURSE_TEST_POSTGRES_DSN='%s'\n" "${DSN}" ;;
  down) down ;;
  *)
    echo "usage: $0 {up|status|env|down}" >&2
    exit 2
    ;;
esac
