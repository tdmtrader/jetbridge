# Spec: Test and Deploy Pipeline

**Track ID:** `test_and_deploy_pipeline_20260322`
**Type:** feature

## Overview

Replace the timer-based CI pipeline with a git-triggered pipeline that watches the `jetbridge` branch, runs all test tiers, pushes passing commits to `main`, builds and pushes a Docker image to GHCR (`ghcr.io/tdmtrader/concourse`), and self-upgrades the Concourse deployment on `theborg`.

## Requirements

1. Git resource watches `jetbridge` branch for new commits (replaces 5m timer trigger)
2. Test chain: build-and-vet -> unit-tests -> k8s-runtime-tests -> k8s-live-tests
3. On test success, push `jetbridge` to `main` branch
4. Build Docker image and push to GHCR with tags: short commit SHA + `latest`
5. Self-upgrade: restart the `concourse-web` deployment in `cicd` namespace to pick up the new image
6. Pipeline credentials (GitHub PAT) managed via `fly set-pipeline -v` variables
7. Continue pushing to `registry.home` for backward compatibility with the local registry

## Technical Approach

- Evolve the existing live `jetbridge` pipeline (already uses `repo` git resource with inputs)
- Add `push-to-main` job after tests pass using HTTPS push with `((github_token))`
- Modify `build-image` job: DinD builder pushes to both `registry.home` and `ghcr.io`
- GHCR auth via `docker login` using `((github_token))` inside the DinD builder pod
- Image tags: `ghcr.io/tdmtrader/concourse:<short-sha>` + `:latest`
- Deploy job: `kubectl rollout restart` (image is `registry.home/jetbridge:latest` with `Always` pull policy)

## Acceptance Criteria

- [ ] Pipeline triggers on new commits to `jetbridge` branch
- [ ] All 4 test jobs run and gate subsequent jobs
- [ ] Passing commits are pushed to `main`
- [ ] Docker image pushed to GHCR with short SHA and latest tags
- [ ] Concourse self-upgrades after image push
- [ ] Pipeline set successfully via `fly set-pipeline`

## Out of Scope

- Multi-arch image builds (arm64 + amd64)
- Switching the deployment image from `registry.home` to `ghcr.io` (future work)
- Webhook-based triggers (using git polling for now)
- Full Dockerfile.build with frontend (using binary-only build matching current pipeline)
