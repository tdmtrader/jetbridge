# Implementation Plan: notify-driven-build-tracker-and-scheduler-job-payload

## Phase 1: BuildTracker Notification-Only [checkpoint: a1ef0f365]

- [x] Task: Verify all build start paths fire NOTIFY build_tracker (audit `build.go`) 4b74cc2
- [x] Task: Set `NotifyOnly: true` on BuildTracker component in `command.go` 14321b86a
- [x] Task: Add test verifying BuildTracker operates in notification-only mode 895aed4f0
- [x] Task: Run existing build tracker and component tests for regressions 895aed4f0
- [x] Task: Phase 1 Manual Verification a1ef0f365

## Phase 2: Scheduler NOTIFY Payload [checkpoint: dc1622374]

- [x] Task: Add job ID payload to `NOTIFY scheduler` in `requestSchedule()` and comma-separated IDs in bulk variants bd325261f
- [x] Task: Add unit test verifying NOTIFY payload contains correct job IDs ef9ed1615
- [x] Task: Pass notification payload through Runner to Schedulable via context key 5b34e382d
- [x] Task: Update scheduler runner to parse job IDs from context and query specific jobs; fall back to full scan when empty 5533140da
- [x] Task: Add test for targeted job query path and fallback path c31607087
- [x] Task: Run full scheduler and DB test suites for regressions c31607087
- [x] Task: Phase 2 Manual Verification dc1622374

---
