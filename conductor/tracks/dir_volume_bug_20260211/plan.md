# Implementation Plan: Dir Volume Bug

## Phase 1: Fix buildVolumeMounts to include Dir emptyDir

- [x] Write test: Pod spec includes emptyDir volume for `spec.Dir` when Dir is set
- [x] Write test: Pod spec has no Dir volume when `spec.Dir` is empty
- [x] Implement: Add Dir emptyDir creation to `buildVolumeMounts()` in `container.go`
- [x] Verify existing input/output/cache volume tests still pass (266/266 pass)
- [ ] Phase 1 Manual Verification

---
