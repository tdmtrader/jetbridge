# Spec: GHCR Docker Image Publishing

**Track ID:** `ghcr_docker_image_publishing_20260319`
**Type:** chore

## Overview

Publish the Concourse Docker image to GitHub Container Registry (ghcr.io) under the user's GitHub account (`ghcr.io/tdmtrader/concourse`). This enables pulling the image from any environment (KinD clusters, CI, remote k8s) without needing a local build.

## Requirements

1. Authenticate with ghcr.io using a GitHub PAT with `write:packages` scope
2. Build the Concourse Docker image (using existing `Dockerfile.local` or standard Dockerfile)
3. Tag the image as `ghcr.io/tdmtrader/concourse:<tag>` (support `latest` + version tags)
4. Push the image to ghcr.io
5. Verify the image is pullable from ghcr.io
6. Make the package public (optional, based on user preference)

## Acceptance Criteria

- [ ] Image is pushed to `ghcr.io/tdmtrader/concourse:latest`
- [ ] Image is pullable without local build (`docker pull ghcr.io/tdmtrader/concourse:latest`)
- [ ] KinD/k8s tests can reference the ghcr.io image via `CONCOURSE_IMAGE` env var

## Out of Scope

- CI/CD automation for publishing on every commit (future track)
- Multi-arch manifest lists (arm64 + amd64) — just the local architecture for now
- Helm chart changes to default to the ghcr.io image
