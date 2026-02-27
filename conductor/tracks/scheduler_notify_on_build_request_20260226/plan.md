# Implementation Plan: scheduler-notify-on-build-request

## Phase 1: Core Change [checkpoint: 6b4b657]

- [x] Task: Add `NOTIFY scheduler` to `requestSchedule()` in `atc/db/job.go` 6b4b657
- [x] Task: Add unit test verifying the NOTIFY is issued within the transaction 6b4b657
- [x] Task: Run existing scheduler and job DB tests to verify no regressions 6b4b657

## Phase 2: Integration Verification [checkpoint: 03ee365]

- [x] Task: Run K8s integration tests and measure before/after build scheduling latency e3096c4
- [x] Task: Remove the `tuneSchedulerInterval()` DB workaround from integration tests (no longer needed) 03ee365
- [x] Task: Phase 2 Manual Verification 03ee365

---
