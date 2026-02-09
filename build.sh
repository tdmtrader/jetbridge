#!/usr/bin/env bash
set -euo pipefail

# Build the Concourse Docker image with embedded frontend assets.
#
# Usage:
#   ./build.sh                          # Build as concourse-local:latest
#   ./build.sh my-registry/concourse    # Build with custom tag
#   ./build.sh my-registry/concourse --push  # Build and push

IMAGE="${1:-concourse-local:latest}"
PUSH="${2:-}"
VERSION="${CONCOURSE_VERSION:-0.0.0-dev}"

echo "==> Building Concourse image: ${IMAGE} (version: ${VERSION})"

docker build \
  -f Dockerfile.build \
  --build-arg "CONCOURSE_VERSION=${VERSION}" \
  -t "${IMAGE}" \
  .

echo "==> Build complete: ${IMAGE}"

if [ "${PUSH}" = "--push" ]; then
  echo "==> Pushing ${IMAGE}"
  docker push "${IMAGE}"
  echo "==> Push complete"
fi
