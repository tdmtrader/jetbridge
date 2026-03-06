# Spec: scheduler-notify-on-build-request

**Track ID:** `scheduler_notify_on_build_request_20260226`
**Type:** feature

## Overview

When a build is triggered (manually or via resource check), the ATC scheduler currently learns about it only through polling (`requestSchedule()` sets a DB flag, and the scheduler polls every 10s). This adds up to 10 seconds of unnecessary latency before the build starts executing.

The scheduler's `component.Runner` already listens on a `pg_notify('scheduler')` channel and calls `RunImmediately()` when a notification arrives. The notification path is fully wired but never triggered — `requestSchedule()` only updates a timestamp, it never fires `NOTIFY scheduler`.

Adding `NOTIFY scheduler` inside `requestSchedule()` (which runs inside a transaction) would make the scheduler respond to new builds instantly. Postgres defers `NOTIFY` until the transaction commits, so there's no race condition.

## Requirements

1. Add `NOTIFY scheduler` to `requestSchedule()` in `atc/db/job.go` so the scheduler wakes immediately when a build is created or a job requests scheduling
2. The polling fallback must remain — the notification is an optimization, not a replacement
3. Multi-ATC safety: the existing lock-based coordination in `component.Coordinator` already handles concurrent wakeups correctly

## Acceptance Criteria

- [ ] `requestSchedule()` sends `NOTIFY scheduler` within its transaction
- [ ] Existing scheduler unit tests pass
- [ ] Integration tests demonstrate reduced build scheduling latency (measured before/after)
- [ ] Multi-ATC scenario: only one ATC runs the scheduler per wakeup (existing behavior, verify not broken)

## Out of Scope

- Changing the scheduler's polling interval (the 10s hardcoded default remains as a fallback)
- Adding notifications to other components (tracker, scanner, etc.) — can be done in follow-up tracks
- Removing the DB-tuning workaround in integration tests (can be removed after this lands)
