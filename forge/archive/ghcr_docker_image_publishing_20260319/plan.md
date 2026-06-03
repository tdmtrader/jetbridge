# Implementation Plan: GHCR Docker Image Publishing

## Phase 1: Build, Tag & Push

- [x] Task 1.1: Create GitHub PAT with `write:packages` scope and authenticate with ghcr.io (`docker login ghcr.io`) done
- [x] Task 1.2: Build the Concourse Docker image locally (`Dockerfile.local` or standard build) done
- [x] Task 1.3: Tag image as `ghcr.io/tdmtrader/concourse:latest` and push to ghcr.io done
- [x] Task 1.4: Verify image is pullable (`docker pull ghcr.io/tdmtrader/concourse:latest`) done
- [x] Task 1.5: Set package visibility to public (if desired) done
- [x] Task 1.6: Phase 1 Manual Verification done

## Phase 2: Integration Validation

- [x] Task 2.1: Test KinD cluster with `CONCOURSE_IMAGE=ghcr.io/tdmtrader/concourse:latest` done
- [x] Task 2.2: Document the publish workflow in a Makefile target or script for repeat use done
- [x] Task 2.3: Phase 2 Manual Verification done

---
