#!/usr/bin/env bash
#
# kind-integration.sh — One-shot CI script for running K8s integration tests
# against a KIND (Kubernetes in Docker) cluster.
#
# Usage:
#   ./hack/kind-integration.sh
#
# Prerequisites:
#   - kind: https://kind.sigs.k8s.io/
#   - helm: https://helm.sh/
#   - kubectl
#   - ginkgo (or go test)
#   - fly binary (built or downloaded)
#
# The script will:
#   1. Create a KIND cluster named "concourse-integration"
#   2. Deploy Concourse via the local Helm chart (deploy/chart/)
#   3. Wait for web + worker pods to be ready
#   4. Run the integration test suite
#   5. Tear down the cluster on exit
#
# Environment variables (override defaults):
#   KIND_CLUSTER_NAME  — cluster name (default: concourse-integration)
#   K8S_NAMESPACE      — namespace for Concourse (default: concourse)
#   ATC_USERNAME       — Concourse admin user (default: admin)
#   ATC_PASSWORD       — Concourse admin password (default: admin)
#   FLY_PATH           — path to fly binary (default: builds from source)
#   SKIP_TEARDOWN      — set to "1" to keep cluster after tests
#

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-concourse-integration}"
K8S_NAMESPACE="${K8S_NAMESPACE:-concourse}"
ATC_USERNAME="${ATC_USERNAME:-admin}"
ATC_PASSWORD="${ATC_PASSWORD:-admin}"
SKIP_TEARDOWN="${SKIP_TEARDOWN:-0}"

# --------------------------------------------------------------------------
# Cleanup on exit
# --------------------------------------------------------------------------
cleanup() {
    local exit_code=$?
    if [ "${SKIP_TEARDOWN}" = "1" ]; then
        echo "SKIP_TEARDOWN=1 — leaving cluster ${KIND_CLUSTER_NAME} running"
    else
        echo "Tearing down KIND cluster ${KIND_CLUSTER_NAME}..."
        kind delete cluster --name "${KIND_CLUSTER_NAME}" 2>/dev/null || true
    fi
    exit $exit_code
}
trap cleanup EXIT

# --------------------------------------------------------------------------
# 1. Create KIND cluster
# --------------------------------------------------------------------------
echo "==> Creating KIND cluster: ${KIND_CLUSTER_NAME}"
if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
    echo "    Cluster already exists, reusing."
else
    kind create cluster --name "${KIND_CLUSTER_NAME}" --wait 120s
fi

export KUBECONFIG="$(kind get kubeconfig-path --name "${KIND_CLUSTER_NAME}" 2>/dev/null || echo "${HOME}/.kube/config")"
kubectl config use-context "kind-${KIND_CLUSTER_NAME}"

# --------------------------------------------------------------------------
# 2. Deploy Concourse via Helm
# --------------------------------------------------------------------------
echo "==> Deploying Concourse to namespace ${K8S_NAMESPACE}"
kubectl create namespace "${K8S_NAMESPACE}" 2>/dev/null || true

helm upgrade --install concourse "${REPO_ROOT}/deploy/chart/" \
    --namespace "${K8S_NAMESPACE}" \
    --set "concourse.web.auth.mainTeam.localUser=${ATC_USERNAME}" \
    --set "concourse.web.localAuth.enabled=true" \
    --set "secrets.localUsers=${ATC_USERNAME}:${ATC_PASSWORD}" \
    --set "worker.replicas=1" \
    --wait \
    --timeout 5m

# --------------------------------------------------------------------------
# 3. Wait for pods to be ready
# --------------------------------------------------------------------------
echo "==> Waiting for Concourse pods to be ready..."
kubectl -n "${K8S_NAMESPACE}" wait --for=condition=ready pod \
    -l "app=concourse-web" --timeout=180s

echo "==> Concourse is running."

# --------------------------------------------------------------------------
# 4. Port-forward to access ATC
# --------------------------------------------------------------------------
ATC_PORT=8080
echo "==> Setting up port-forward to ATC on localhost:${ATC_PORT}"
kubectl -n "${K8S_NAMESPACE}" port-forward svc/concourse-web "${ATC_PORT}:8080" &
PF_PID=$!
sleep 3

export ATC_URL="http://localhost:${ATC_PORT}"

# --------------------------------------------------------------------------
# 5. Build or locate fly binary
# --------------------------------------------------------------------------
if [ -z "${FLY_PATH:-}" ]; then
    echo "==> Building fly binary..."
    FLY_PATH="$(mktemp -d)/fly"
    go build -o "${FLY_PATH}" "${REPO_ROOT}/fly"
fi
export FLY_PATH

# --------------------------------------------------------------------------
# 6. Run integration tests
# --------------------------------------------------------------------------
echo "==> Running K8s integration tests..."
export ATC_URL ATC_USERNAME ATC_PASSWORD K8S_NAMESPACE KUBECONFIG FLY_PATH

cd "${REPO_ROOT}"
if command -v ginkgo &>/dev/null; then
    ginkgo -v ./topgun/k8s/integration/
else
    go test -v ./topgun/k8s/integration/ -count=1
fi

echo "==> Integration tests complete."
