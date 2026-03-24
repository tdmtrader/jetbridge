# Implementation Plan: Sidecar Image Handoff

## Phase 1: Fix docker:/// prefix not stripped for sidecar images

- [~] Task: Write tests for docker:/// prefix stripping in buildSidecarContainers
  - Add unit test in `atc/worker/jetbridge/container_test.go` (or sidecar-specific test file)
  - Test that `buildSidecarContainers` strips `docker:///`, `docker://`, and `raw:///` prefixes
  - Test with image_artifact-style URLs (`docker:///repo@sha256:abc`) and plain refs (`repo:tag`)
- [ ] Task: Strip Concourse URL prefixes from sidecar images in buildSidecarContainers
  - Modify `buildSidecarContainers()` in `atc/worker/jetbridge/container.go`
  - Apply same prefix stripping logic as `resolveImage()` to `sc.Image` before assigning to K8s container spec
- [ ] Task: Phase 1 Verification
  - Run `ginkgo ./atc/worker/jetbridge/` to verify all existing + new tests pass
  - Confirm no regressions in sidecar behavioral tests

## Phase 2: Make sidecar digest pinning best-effort

- [ ] Task: Write tests for best-effort sidecar image resolution
  - Add unit tests in `atc/exec/task_step_test.go`
  - Test that resolver auth failure logs a warning and falls through to tag-based ref
  - Test that successful resolution still pins the digest
  - Test that digest-pinned images still skip resolution
- [ ] Task: Make sidecar image resolution best-effort in task_step.go
  - Modify sidecar resolution loop at `atc/exec/task_step.go:310-325`
  - On `imageResolver.Resolve()` error: log warning with sidecar name, image ref, and error
  - Keep original tag-based `sc.Image` intact (don't fail the step)
  - Successful resolution still pins to digest as before
- [ ] Task: Phase 2 Verification
  - Run `ginkgo ./atc/exec/` to verify all existing + new tests pass
  - Run `make test-unit` for full regression check

---
