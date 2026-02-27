# Implementation Plan: notify-driven-build-tracker-and-scheduler-job-payload

## Phase 1: BuildTracker Notification-Only

- [ ] Task: Verify all build start paths fire NOTIFY build_tracker (audit `build.go`)
- [ ] Task: Set `NotifyOnly: true` on BuildTracker component in `command.go`
- [ ] Task: Add test verifying BuildTracker operates in notification-only mode
- [ ] Task: Run existing build tracker and component tests for regressions
- [ ] Task: Phase 1 Manual Verification

## Phase 2: Scheduler NOTIFY Payload

- [ ] Task: Add job ID payload to `NOTIFY scheduler` in `requestSchedule()` and comma-separated IDs in bulk variants
- [ ] Task: Add unit test verifying NOTIFY payload contains correct job IDs
- [ ] Task: Pass notification payload through Runner to Schedulable via context key
- [ ] Task: Update scheduler runner to parse job IDs from context and query specific jobs; fall back to full scan when empty
- [ ] Task: Add test for targeted job query path and fallback path
- [ ] Task: Run full scheduler and DB test suites for regressions
- [ ] Task: Phase 2 Manual Verification

---
