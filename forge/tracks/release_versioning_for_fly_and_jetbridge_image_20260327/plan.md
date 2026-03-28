# Implementation Plan: Release Versioning for Fly and JetBridge Image

## Phase 1: Version Source of Truth

- [ ] Task: Create VERSION file at repo root containing `0.2.0`
- [ ] Task: Clean up versions.go — remove `init()` override, keep `var Version = "0.0.0-dev"` as ldflags target, keep `JetBridgeVersion` and `ConcourseVersion` as informational constants
- [ ] Task: Update Helm chart `appVersion` in `deploy/chart/Chart.yaml` to `0.2.0`
- [ ] Task: Phase 1 Manual Verification — local build with and without ldflags, confirm `concourse --version` and `fly --version` output

## Phase 2: CI Pipeline Version Injection

- [ ] Task: Update `build-and-push-image` job in `deploy/concourse-pipeline.yml` — read VERSION file, add `-ldflags` to `go build` for concourse and fly binaries, pass as build arg to Dockerfile
- [ ] Task: Add git tag step — after successful image push, tag commit on main as `v<version>` and push tag
- [ ] Task: Add post-release version bump step — increment patch in VERSION, update `JetBridgeVersion` in versions.go, update `appVersion` in Chart.yaml, commit and push to jetbridge
- [ ] Task: Phase 2 Manual Verification — trigger CI build, verify image reports correct version, verify git tag created, verify bump commit on jetbridge

---
