# Spec: GCP Artifact Registry Authentication for Scanner Resolver

**Track ID:** `gcp_artifact_auth_20260319`
**Type:** bugfix

## Overview

The scanner resolver (lidar + on-demand image resolution) fails to authenticate to GCP Artifact Registry when running on GKE with Workload Identity. The root cause is that `imageresolver.NewResolver(nil)` defaults to `authn.DefaultKeychain`, which only reads `~/.docker/config.json`. It does not support GCP Workload Identity, Application Default Credentials, or the GCP metadata server.

The fix is to use `authn.NewMultiKeychain(google.Keychain, authn.DefaultKeychain)` so GCP credentials are tried first, with Docker config as fallback.

## Requirements

1. The image resolver must authenticate to GCP Artifact Registry using the pod's service account credentials (Workload Identity).
2. Existing Docker config-based authentication must continue to work as a fallback.
3. No new CLI flags or configuration required — GCP auth should work automatically when running on GKE.

## Technical Approach

- Import `github.com/google/go-containerregistry/pkg/v1/google` in the resolver.
- Change the nil-keychain default from `authn.DefaultKeychain` to `authn.NewMultiKeychain(google.Keychain, authn.DefaultKeychain)`.
- This gives automatic GCP credential resolution (Workload Identity, ADC, gcloud) before falling back to Docker config.

## Acceptance Criteria

- [x] `NewResolver(nil)` uses a multi-keychain that includes `google.Keychain`
- [x] Existing basic auth path (username/password from source) is unchanged
- [x] Unit tests verify the keychain wiring
- [x] `make test-unit` passes

## Out of Scope

- AWS ECR or Azure ACR keychain support (separate tracks if needed)
- Changes to how Kubelet pulls images (that uses imagePullSecrets, unrelated)
- New CLI flags for GCP credentials
