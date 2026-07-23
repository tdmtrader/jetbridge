#!/usr/bin/env bash
set -euo pipefail

# local-dev-up.sh — stand up a fully local Concourse/jetbridge dev environment
# on this Mac (Colima + KinD), with no dependency on theborg/concourse.home.
#
# Verified workflow: see docs/local-dev.md.
#
# Usage:
#   ./hack/local-dev-up.sh            # create/update everything
#   ./hack/local-dev-up.sh --destroy  # tear down the kind cluster
#
# What it does:
#   1. Ensures Colima is running (docker context).
#   2. Cross-compiles concourse + artifact-daemon + fly for linux/arm64.
#   3. Builds concourse-local:latest via Dockerfile.local (fast, no emulation).
#   4. Creates a KinD cluster "jetbridge-local" with an ISOLATED kubeconfig
#      (never touches ~/.kube/config, so theborg contexts stay untouched).
#   5. Loads the image via docker save + kind image-archive
#      (`kind load docker-image` fails with containerd image stores).
#   6. Helm-installs deploy/chart with local-friendly values.
#
# After it finishes:
#   export KUBECONFIG="$(pwd)/.local-cluster/kubeconfig"
#   kubectl -n concourse port-forward svc/concourse-concourse-jetbridge-web 18080:8080 &
#   fly -t local login -c http://localhost:18080 -u test -p test

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="jetbridge-local"
LOCAL_DIR="${REPO_ROOT}/.local-cluster"
KUBECONFIG_PATH="${LOCAL_DIR}/kubeconfig"
IMAGE="concourse-local:latest"
ARTIFACT_HELPER_SOURCE="${ARTIFACT_HELPER_IMAGE:-docker.io/library/busybox:1.37.0}"
NAMESPACE="concourse"

if [[ "${1:-}" == "--destroy" ]]; then
  kind delete cluster --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_PATH}" || true
  rm -rf "${LOCAL_DIR}"
  echo "==> destroyed ${CLUSTER_NAME}"
  exit 0
fi

mkdir -p "${LOCAL_DIR}"

echo "==> 1/6 Colima"
if ! colima status >/dev/null 2>&1; then
  colima start --cpu 4 --memory 8
fi
docker ps >/dev/null

echo "==> 1.5/6 Building web assets (yarn build) — REQUIRED before go:embed"
# web/handler.go embeds web/public into the concourse binary at go build
# time (//go:embed public). Any Elm/CSS/JS change that is not rebuilt into
# web/public BEFORE the cross-compile below silently does not ship — the
# binary keeps the stale bundle. Set LOCAL_DEV_SKIP_WEB=1 to skip when you
# know the committed web/public is already current (backend-only rebuilds).
cd "${REPO_ROOT}"
if [ "${LOCAL_DEV_SKIP_WEB:-0}" = "1" ]; then
  echo "    LOCAL_DEV_SKIP_WEB=1 — using committed web/public as-is"
else
  corepack enable >/dev/null 2>&1 || true
  yarn install --immutable
  yarn build   # regenerates web/public/{main.css,elm.js,elm.min.js,bundle.js,*.bundle.js}
fi

echo "==> 2/6 Cross-compiling linux/arm64 binaries"
cd "${REPO_ROOT}"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -ldflags "-s -w -X github.com/concourse/concourse.Version=0.0.0-local" \
  -o concourse-linux-arm64 ./cmd/concourse
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" \
  -o artifact-daemon-linux-arm64 ./cmd/artifact-daemon
mkdir -p fly-assets
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" \
  -o "${LOCAL_DIR}/fly-linux-arm64" ./fly
tar czf fly-assets/fly-linux-arm64.tgz -C "${LOCAL_DIR}" -s '/fly-linux-arm64/fly/' fly-linux-arm64

echo "==> 3/6 Building ${IMAGE} (Dockerfile.local, native arm64)"
docker build -f Dockerfile.local -t "${IMAGE}" .

echo "==> 4/6 KinD cluster ${CLUSTER_NAME} (isolated kubeconfig: ${KUBECONFIG_PATH})"
if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  kind create cluster --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_PATH}"
else
  kind export kubeconfig --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_PATH}"
fi
kubectl --kubeconfig "${KUBECONFIG_PATH}" wait --for=condition=Ready node --all --timeout=180s

echo "==> 5/6 Loading image (docker save + image-archive)"
docker save "${IMAGE}" -o "${LOCAL_DIR}/concourse-local.tar"
kind load image-archive --name "${CLUSTER_NAME}" "${LOCAL_DIR}/concourse-local.tar"

echo "==> Resolving and loading immutable artifact helper"
docker pull --quiet "${ARTIFACT_HELPER_SOURCE}"
if [[ "${ARTIFACT_HELPER_SOURCE}" == *@sha256:* ]]; then
  ARTIFACT_HELPER_IMAGE="${ARTIFACT_HELPER_SOURCE}"
else
  ARTIFACT_HELPER_IMAGE="$(docker image inspect \
    --format '{{index .RepoDigests 0}}' "${ARTIFACT_HELPER_SOURCE}")"
fi
ARTIFACT_HELPER_PATTERN='^[^@[:space:]]+@sha256:[a-f0-9]{64}$'
if [[ ! "${ARTIFACT_HELPER_IMAGE}" =~ ${ARTIFACT_HELPER_PATTERN} ]]; then
  echo "ERROR: artifact helper did not resolve to an exact OCI sha256 digest: ${ARTIFACT_HELPER_IMAGE}" >&2
  exit 1
fi
docker save "${ARTIFACT_HELPER_SOURCE}" -o "${LOCAL_DIR}/artifact-helper.tar"
kind load image-archive --name "${CLUSTER_NAME}" "${LOCAL_DIR}/artifact-helper.tar"

echo "==> 6/6 Helm install"
helm upgrade --install concourse "${REPO_ROOT}/deploy/chart" \
  --kubeconfig "${KUBECONFIG_PATH}" \
  --namespace "${NAMESPACE}" --create-namespace \
  --set image.repository=concourse-local \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set-string kubernetes.artifactHelperImage="${ARTIFACT_HELPER_IMAGE}" \
  --set postgresql.persistence.enabled=false \
  --set cachePvc.enabled=false \
  --set artifactStorePvc.enabled=false \
  --set artifactDaemon.enabled=true \
  --timeout 5m

# The image tag stays "latest" across rebuilds, so `helm upgrade` sees no
# spec change and will NOT roll the web/worker pods to the freshly-loaded
# image — you'd keep running stale code (and stale migrations). Force a
# rollout so every run reflects the current tree.
for d in web worker; do
  dep="concourse-concourse-jetbridge-${d}"
  if kubectl --kubeconfig "${KUBECONFIG_PATH}" -n "${NAMESPACE}" get deploy "${dep}" >/dev/null 2>&1; then
    kubectl --kubeconfig "${KUBECONFIG_PATH}" -n "${NAMESPACE}" rollout restart "deploy/${dep}"
  fi
done

kubectl --kubeconfig "${KUBECONFIG_PATH}" -n "${NAMESPACE}" \
  rollout status deploy/concourse-concourse-jetbridge-web --timeout=300s

kubectl --kubeconfig "${KUBECONFIG_PATH}" -n "${NAMESPACE}" \
  wait --for=condition=Ready pod --all --timeout=300s

cat <<EOF

==> Done. Next steps:
  export KUBECONFIG=${KUBECONFIG_PATH}
  kubectl -n ${NAMESPACE} port-forward svc/concourse-concourse-jetbridge-web 18080:8080 &
  go build -o ${LOCAL_DIR}/fly ./fly   # host fly binary
  ${LOCAL_DIR}/fly -t local login -c http://localhost:18080 -u test -p test

Live jetbridge tests (all 22 pass locally):
  KUBECONFIG=${KUBECONFIG_PATH} \\
  K8S_TEST_NAMESPACE=jb-live-test \\
  ARTIFACT_DAEMON_HOST_PATH=/var/concourse/artifacts \\
  ARTIFACT_DAEMON_SERVICE=concourse-concourse-jetbridge-artifact-daemon \\
  go test -tags live -run '^TestLive' -v -count=1 -timeout 15m ./atc/worker/jetbridge/
  (create the namespace first: kubectl create ns jb-live-test)
EOF
