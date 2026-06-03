# Implementation Plan: Release Versioning for Fly and JetBridge Image

## Phase 1: Version Source of Truth [checkpoint: 09758d35d]

- [x] Task: Create VERSION file and clean up versions.go a8d8fab67
- [x] Task: Update Helm chart appVersion 12c306513
- [x] Task: Phase 1 Manual Verification 09758d35d

## Phase 2: CI Pipeline Version Injection [checkpoint: 09758d35d]

- [x] Task: Add ldflags version injection to build-and-push-image job 166b7bf6a
- [x] Task: Add git tag and post-release version bump steps 09758d35d
- [x] Task: Phase 2 Manual Verification 09758d35d

---
