# JetBridge 0.4.6 (running notes)

JetBridge 0.4.6 follows jb-0.4.5 (e610ff1acb). These are running notes, kept as changes land; they become the release notes when 0.4.6 is declared.

## Changes

### Step runtime

- **The reaper collects step Jobs under either step runtime, and one pod it cannot delete no longer stops a pass.** A deployment switched from `job` back to `exec` keeps the step Jobs left behind, and their pods reach the reaper's destroying path. Deleting their Jobs was forbidden under `exec`, and the reaper returned at the first pod it could not delete. Every pass then stopped there, so no other destroyed container was reaped and no artifacts were dropped. Now a pod that cannot be deleted is logged and left for the next pass. The rest of the pass goes on, and the pass ends in an error. Under `exec`, a Role that cannot list or delete step Jobs (a chart older than the web) is reported once per process at info (`step-jobs-not-collected`), not as an error on every pass. Under `exec` this also stops the `failed-to-list-step-jobs` error 0.4.5 logged every 10 s. Under `job` a refusal stays an error.

## Chart

- **The pod-manager Role grants `batch/jobs` `list` and `delete` under `exec` too,** so the reaper can collect step Jobs a `job` deployment left behind. `get`, `watch`, `create`, `update` and `patch` stay `job`-only. No new value.
