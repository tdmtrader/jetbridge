# Spec: notify-driven-build-tracker-and-scheduler-job-payload

**Track ID:** `notify_driven_build_tracker_and_scheduler_job_payload_20260227`
**Type:** feature

## Overview

Two related optimizations that extend the NOTIFY-driven pattern established in the scheduler track:

1. **BuildTracker notification-only mode** — `build.Start()` already fires `NOTIFY build_tracker` (`atc/db/build.go:555`), and the component Runner already listens on that channel. But the tracker still polls every 10s via `GetAllStartedBuilds()`. Converting it to notification-only mode (like the scheduler) eliminates the polling overhead. The tracker's `checkBuildsChan` channel handles in-memory check builds; NOTIFY handles DB-persisted builds transitioning to "started".

2. **Scheduler NOTIFY payload with job IDs** — The scheduler currently wakes on `NOTIFY scheduler` (no payload), then scans all jobs with `WHERE schedule_requested > last_scheduled`. Since `requestSchedule()` already has the job ID, we can include it in the NOTIFY payload (`NOTIFY scheduler, '42'`). The `db.Notification` struct already has a `Payload string` field wired through `PgxListener` → `NotificationsBus`. The scheduler can then query only the specific job(s) instead of scanning all jobs.

## Requirements

1. Set `NotifyOnly: true` on the BuildTracker component in `atc/atccmd/command.go`
2. Verify that all paths creating "started" builds fire `NOTIFY build_tracker`
3. Add job ID payload to `NOTIFY scheduler` in `requestSchedule()` and comma-separated job IDs in bulk variants
4. Pass notification payload through `component.Runner` → `Schedulable.RunImmediately()` via context
5. Update scheduler runner to parse job IDs from context and query only those jobs when available; fall back to full `JobsToSchedule()` scan when payload is empty

## Acceptance Criteria

- [ ] BuildTracker runs in notification-only mode (no polling timer)
- [ ] BuildTracker test verifies notification-only wakeup
- [ ] `NOTIFY scheduler` includes job ID payload in all requestSchedule* functions
- [ ] Scheduler parses payload and queries specific jobs when available
- [ ] Scheduler falls back to full scan when payload is absent
- [ ] Existing scheduler and build tracker tests pass
- [ ] Unit test verifies NOTIFY payload is delivered with correct job IDs

## Out of Scope

- LidarScanner changes (CheckEvery is the primary path there)
- Changing the `schedule_requested / last_scheduled` DB mechanism (still needed for multi-ATC dedup)
- Adding NOTIFY to BuildReaper, PipelinePauser, or K8s components
