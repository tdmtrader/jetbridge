#!/usr/bin/env bash
# Docker-on-theborg: run a Docker daemon as a pod on the theborg k3s cluster and
# expose it to this machine as a remote DOCKER_HOST.
#
# theborg has NO Docker daemon of its own — it runs k3s/containerd. This script
# does not install anything on the host; it runs the upstream docker:dind image
# as a privileged pod in a dedicated namespace, so the host's iptables and
# k3s/flannel networking are untouched.
#
# Usage:
#   ./hack/borg-docker.sh up        # create the pod + local port-forward
#   eval "$(./hack/borg-docker.sh env)"
#   docker info
#   ./hack/borg-docker.sh status
#   ./hack/borg-docker.sh down      # stop forward + delete namespace
#
# See docs/docker-on-theborg.md for what this can and cannot run.

set -euo pipefail

NS="${BORG_DOCKER_NAMESPACE:-borg-docker}"
CTX="${BORG_DOCKER_CONTEXT:-theborg}"
PORT="${BORG_DOCKER_PORT:-12375}"
DIND_IMAGE="${BORG_DOCKER_IMAGE:-docker:28-dind}"
CPU_LIMIT="${BORG_DOCKER_CPU:-6}"
MEM_LIMIT="${BORG_DOCKER_MEMORY:-12Gi}"
DISK_SIZE="${BORG_DOCKER_DISK:-60Gi}"

RUNDIR="${TMPDIR:-/tmp}/borg-docker"
PIDFILE="$RUNDIR/portforward.pid"
LOGFILE="$RUNDIR/portforward.log"

k() { kubectl --context "$CTX" "$@"; }

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "ERROR: $1 is required" >&2; exit 1; }
}

pod_manifest() {
  cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: dind
  namespace: ${NS}
  labels: {app: borg-docker}
spec:
  # No hostNetwork on purpose: Docker's iptables rules must stay inside the
  # pod's own netns, or they can break k3s/flannel forwarding on the host.
  containers:
  - name: dind
    image: ${DIND_IMAGE}
    args: ["--host=tcp://0.0.0.0:2375"]
    env:
    - name: DOCKER_TLS_CERTDIR
      value: ""
    securityContext:
      privileged: true
    ports:
    - containerPort: 2375
    resources:
      requests: {cpu: "500m", memory: "1Gi"}
      limits: {cpu: "${CPU_LIMIT}", memory: "${MEM_LIMIT}"}
    readinessProbe:
      tcpSocket: {port: 2375}
      initialDelaySeconds: 5
      periodSeconds: 3
      failureThreshold: 60
    volumeMounts:
    - name: varlibdocker
      mountPath: /var/lib/docker
  volumes:
  - name: varlibdocker
    emptyDir:
      sizeLimit: ${DISK_SIZE}
YAML
}

# kubectl port-forward drops its connection whenever the API server hiccups or
# a long build stalls the stream, so supervise it rather than run it once.
supervise_forward() {
  while true; do
    kubectl --context "$CTX" -n "$NS" port-forward pod/dind "${PORT}:2375" >>"$LOGFILE" 2>&1 || true
    k -n "$NS" get pod dind >/dev/null 2>&1 || exit 0
    sleep 2
  done
}

forward_running() {
  [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null
}

cmd_up() {
  require kubectl
  require docker
  k version --request-timeout=10s >/dev/null 2>&1 || {
    echo "ERROR: cannot reach kube-context '$CTX' (is the home network up?)" >&2
    exit 1
  }

  k get ns "$NS" >/dev/null 2>&1 || k create ns "$NS" >/dev/null
  pod_manifest | k apply -f - >/dev/null

  echo "waiting for the dind pod to become ready..." >&2
  k -n "$NS" wait --for=condition=Ready pod/dind --timeout=300s >/dev/null

  mkdir -p "$RUNDIR"
  if forward_running; then
    echo "port-forward already running on :${PORT}" >&2
  else
    : >"$LOGFILE"
    # Detach every stream: a background child that inherits the caller's stdout
    # keeps pipes such as `borg-docker.sh up | tail` open forever.
    nohup "$0" __supervise </dev/null >>"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"
    disown 2>/dev/null || true
  fi

  for _ in $(seq 1 30); do
    if DOCKER_HOST="tcp://127.0.0.1:${PORT}" docker version >/dev/null 2>&1; then
      echo "docker is up on tcp://127.0.0.1:${PORT}" >&2
      DOCKER_HOST="tcp://127.0.0.1:${PORT}" docker version \
        --format 'server {{.Server.Version}} ({{.Server.Os}}/{{.Server.Arch}})' >&2
      echo >&2
      echo "  eval \"\$(./hack/borg-docker.sh env)\"" >&2
      return 0
    fi
    sleep 1
  done

  echo "ERROR: pod is Ready but the daemon never answered on :${PORT}" >&2
  echo "  forward log: $LOGFILE" >&2
  exit 1
}

cmd_env() {
  echo "export DOCKER_HOST=tcp://127.0.0.1:${PORT}"
  # testcontainers-go does not read the docker CLI context and otherwise
  # panics with "rootless Docker not found".
  echo "export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock"
  echo "export TESTCONTAINERS_RYUK_DISABLED=true"
}

cmd_status() {
  k -n "$NS" get pod dind -o wide 2>&1 || true
  if forward_running; then
    echo "port-forward: running (pid $(cat "$PIDFILE")) on 127.0.0.1:${PORT}"
  else
    echo "port-forward: not running"
  fi
  DOCKER_HOST="tcp://127.0.0.1:${PORT}" docker version \
    --format 'daemon: {{.Server.Version}} {{.Server.Os}}/{{.Server.Arch}}' 2>&1 || true
}

cmd_down() {
  if forward_running; then
    local pid
    pid="$(cat "$PIDFILE")"
    # Kill the supervisor BEFORE its kubectl child: the other order lets the
    # loop spawn a fresh forward in the gap and leaks it.
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.2; done
    pkill -P "$pid" 2>/dev/null || true
  fi
  rm -f "$PIDFILE"
  # Catch forwards leaked by an earlier crash or a killed shell.
  pkill -f "port-forward pod/dind ${PORT}:2375" 2>/dev/null || true
  k delete ns "$NS" --wait=false >/dev/null 2>&1 || true
  echo "namespace ${NS} deleting; local forward stopped" >&2
}

case "${1:-}" in
  up)     cmd_up ;;
  __supervise) supervise_forward ;;
  env)    cmd_env ;;
  status) cmd_status ;;
  down)   cmd_down ;;
  *)
    echo "usage: $0 {up|env|status|down}" >&2
    exit 2
    ;;
esac
