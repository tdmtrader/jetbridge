# Spec: Fix native check self-notification feedback loop

**Track ID:** `fix_native_check_self_notification_feedback_loop_20260413`
**Type:** bugfix

## Overview

Native registry-image checks (web-node-side digest resolution via OCI registry API) fire more frequently than the configured `check_every` interval. Each native check cycle emits 2-3 scanner notifications (`SaveVersions`, `UpdateLastCheckEndTime`, `SetResourceConfigScope`), triggering unnecessary extra scanner runs. While signal coalescing and coordinator locking prevent a full storm, the system still performs ~2x the registry API calls needed — risking container registry rate limits.

The root cause is that `SaveVersions`, `UpdateLastCheckEndTime`, and `SetResourceConfigScope` unconditionally notify `ComponentLidarScanner`, even when no meaningful state has changed. Native checks complete synchronously (milliseconds), so these self-notifications trigger an immediate re-scan that re-queries all resources from the database and re-resolves any whose intervals are borderline elapsed.

## Requirements

1. `SaveVersions` must only notify the scanner when new versions are actually inserted (not when re-saving existing versions with the same digest).
2. `UpdateLastCheckEndTime` must not notify the scanner — updating bookkeeping timestamps does not create new work.
3. `SetResourceConfigScope` (on both `Resource` and `ResourceType`) must only notify the scanner when the scope actually changes (not when re-setting the same scope).
4. External callers that genuinely change state (pin/unpin version, manual trigger, new scope assignment) must continue to trigger scanner notifications as before.
5. No change to the pod-based check path behavior.

## Technical Approach

- **`SaveVersions`**: The inner `saveVersions` function already computes `containsNewVersion`. Return this boolean and gate the `Notify` call on it.
- **`UpdateLastCheckEndTime`**: Remove the `Bus().Notify(atc.ComponentLidarScanner)` call. The scanner runs on its own tick interval and is woken by other meaningful events.
- **`SetResourceConfigScope`**: Compare the incoming scope ID against the current scope ID before notifying. Only notify if the scope actually changed.

## Acceptance Criteria

- [x] Native registry-image checks respect `check_every` intervals without extra scanner cycles
- [x] `SaveVersions` does not notify when re-saving an existing version (same digest)
- [x] `SaveVersions` still notifies when a genuinely new version is saved
- [x] `UpdateLastCheckEndTime` does not notify the scanner
- [x] `SetResourceConfigScope` does not notify when scope is unchanged
- [x] `SetResourceConfigScope` still notifies when scope changes
- [x] Pod-based check paths are unaffected
- [x] Existing unit tests pass; new tests cover the conditional notification behavior
- [x] Scanner still responds to external events (pin, unpin, manual trigger)

> Verified 2026-06-03 against the live JetBridge web on theborg/`cicd` (build `f6a6a8833d`, contains fix `c3e9f6d48b`). See the *Cluster Verification* section of `plan.md` for evidence.

## Out of Scope

- Rearchitecting the scanner polling/notification model
- Adding rate limiting or debouncing to the scanner runner
- Changes to the `NotifySignal` coalescing mechanism
- Native check resolution logic (registry API calls, digest handling)
