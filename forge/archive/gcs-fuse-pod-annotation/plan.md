# Plan: GCS Fuse Pod Annotation

## Phase 1: Implementation

- [x] 1.1 Add `ArtifactStoreGCSFuse bool` to `jetbridge.Config`
- [x] 1.2 Add `buildPodAnnotations()` method and wire into `buildPod()`
- [x] 1.3 Add `--kubernetes-artifact-store-gcs-fuse` CLI flag in `atccmd/command.go`
- [x] 1.4 Wire flag to config in both web and worker init paths
- [x] 1.5 Update Helm chart to pass flag when `gcsFuse.enabled=true`
- [x] 1.6 Write 3 unit tests (annotation present, absent without flag, absent without claim)
- [x] 1.7 Verify: 257/257 jetbridge tests pass, full build compiles, helm template correct
