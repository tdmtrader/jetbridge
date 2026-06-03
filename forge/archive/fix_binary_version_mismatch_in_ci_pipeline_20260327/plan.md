# Implementation Plan: Fix Binary Version Mismatch in CI Pipeline

## Phase 1: Fix ldflags and add verification

- [x] Task: Add JetBridgeVersion to ldflags in deploy/concourse-pipeline.yml
- [x] Task: Add post-build verification that checks binary version output
- [x] Task: Fix self-upgrade to pin versioned image tag before rollout restart
- [x] Task: Phase 1 Manual Verification

---
