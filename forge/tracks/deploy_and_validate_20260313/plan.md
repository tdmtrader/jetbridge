# Implementation Plan: Deploy and Validate Home Cluster

## Phase 1: Emergency Cleanup

- [x] Task: SSH to theborg, verify k3s is running and cluster is Ready
- [x] Task: Delete 26,788 orphaned check pods in cicd namespace
- [x] Task: Destroy stale testflight pipeline `tf-pipeline-1-b1cca092-...`
- [x] Task: Restart ArgoCD repo-server (was in Unknown state)
- [x] Task: Delete 204 stale weather-collector pods in monitoring namespace
- [x] Task: Prune retiring legacy worker `concourse-worker-0`

## Phase 2: Pipeline Fix and Rebuild

- [x] Task: Remove `custom-time` resource type and `custom-trigger` resource from jetbridge pipeline (root cause of pod explosion + nil pointer panic)
- [x] Task: Unpause jetbridge and jetbridge-agents pipelines
- [x] Task: Trigger build-and-vet job and verify it succeeds
- [x] Task: Verify unit-tests job succeeds
- [x] Task: Verify k8s-runtime-tests job succeeds
- [x] Task: Verify k8s-live-tests job succeeds
- [x] Task: Verify build-image job succeeds
- [x] Task: Verify deploy job succeeds (updated home-infra to imagePullPolicy=Always, ArgoCD synced new pod)

## Phase 3: Post-Deploy Validation

- [x] Task: Verify deployed concourse-web image matches latest jetbridge commit (JetBridge 0.1.0 / Concourse 8.0.1)
- [x] Task: Monitor for 10+ minutes — confirm no check pod accumulation (steady at 2 check pods over 10 min)
- [x] Task: Verify fly CLI can connect and list pipelines/workers (2 pipelines, k8s-cicd worker running)
- [ ] Task: Phase 3 Manual Verification

---
