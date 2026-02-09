#!/usr/bin/env bash
set -euo pipefail

# Build the Concourse Docker image with embedded frontend assets.
#
# Uses buildx to target linux/amd64 (required because the Elm npm package
# only ships x86_64 binaries, and the target server is amd64).
#
# Usage:
#   ./build.sh                          # Build as concourse-local:latest
#   ./build.sh my-registry/concourse    # Build with custom tag
#   ./build.sh my-registry/concourse --push  # Build and push

IMAGE="${1:-concourse-local:latest}"
PUSH="${2:-}"
VERSION="${CONCOURSE_VERSION:-0.0.0-dev}"
PLATFORM="${PLATFORM:-linux/amd64}"

echo "==> Building Concourse image: ${IMAGE} (version: ${VERSION}, platform: ${PLATFORM})"

PUSH_FLAG=""
LOAD_FLAG="--load"
if [ "${PUSH}" = "--push" ]; then
  PUSH_FLAG="--push"
  LOAD_FLAG=""
fi

docker buildx build \
  --platform "${PLATFORM}" \
  -f Dockerfile.build \
  --build-arg "CONCOURSE_VERSION=${VERSION}" \
  -t "${IMAGE}" \
  ${LOAD_FLAG} ${PUSH_FLAG} \
  .

echo "==> Build complete: ${IMAGE}"
