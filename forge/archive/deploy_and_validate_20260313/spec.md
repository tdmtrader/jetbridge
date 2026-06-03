# Spec: Deploy and Validate Home Cluster

**Track ID:** `deploy_and_validate_20260313`
**Type:** feature

## Overview

Validate that the JetBridge Concourse fork deploys correctly to the home cluster (`concourse.home` / `theborg` at 192.168.1.133) running k3s, update the deployed image to the latest jetbridge code, and ensure the cluster behaves correctly — specifically that pipelines run and no check pod explosion occurs.

## Context

The home cluster was found in a critical state:
- **26,788 orphaned check pods** from a stale testflight pipeline (`tf-pipeline-1-*`) with a `custom-type-with-nested-params` resource type
- A nil pointer dereference panic in `atc/exec/log_error_step.go:31` when checking custom resource types
- ArgoCD repo-server was in Unknown state after reboot
- All builds erroring since Feb 15 due to pod exhaustion
- The `custom-time` resource type (registry-image based) was spawning check pods that GC never cleaned up
- Weather collector CronJob in monitoring namespace had 204 stale pods

## Requirements

1. Clean up all orphaned check pods (26,788 in cicd namespace)
2. Destroy the stale testflight pipeline causing the pod explosion
3. Fix ArgoCD repo-server to restore sync capability
4. Remove the `custom-time` resource type from the jetbridge pipeline (root cause of check pod explosion + nil pointer panic)
5. Clean up stale monitoring pods (weather collector CronJobs)
6. Run the full jetbridge pipeline: build-and-vet -> unit-tests -> k8s-runtime-tests -> k8s-live-tests -> build-image -> deploy
7. Verify the deployed image is updated to latest jetbridge code
8. Confirm no new check pod accumulation occurs

## Acceptance Criteria

- [x] All orphaned pods cleaned up (from 26,788 to ~5 healthy pods)
- [x] Stale testflight pipeline destroyed
- [x] ArgoCD back to Synced/Healthy
- [x] `custom-time` resource type removed from jetbridge pipeline
- [x] Monitoring namespace cleaned up
- [x] `build-and-vet` job succeeds
- [x] `unit-tests` job succeeds
- [x] `k8s-runtime-tests` job succeeds
- [x] `k8s-live-tests` job succeeds
- [x] `build-image` job succeeds
- [x] `deploy` job succeeds (new image deployed via ArgoCD sync with imagePullPolicy=Always)
- [x] Verify no check pod accumulation after 10+ minutes (steady at 2 check pods)
- [x] Verify deployed version matches latest jetbridge — JetBridge 0.1.0 (Concourse 8.0.1)

## Out of Scope

- Fixing the nil pointer dereference bug in `atc/exec/log_error_step.go:31` (separate track)
- Fixing GC to properly clean up failed/orphaned check pods (separate track)
- Multi-cluster deployment
- Helm chart changes
