# Task 8 report: end-to-end regression and CI contract

## RED

The daemon-level publish/materialize test initially timed out in
`GCSStore.EnsureTree`. A focused regression showed that calling the cleanup
returned by `closeReadCloserOnCancel` twice could block forever: the first call
successfully stopped `context.AfterFunc`, while the second interpreted the
single-use stop function's `false` result as an in-flight callback and waited
on a channel that could never close.

The live-tag compile probe also exposed two pre-existing stale harness symbols
in `atc/worker/jetbridge/live_test.go`: missing
`postgresrunner.StandardTestRunner` and missing
`Config.ArtifactDaemonNamespace`.

## GREEN

- Made the shared cancellation stopper idempotent and concurrency-safe with
  `sync.Once`; focused tests cover prompt double stop and concurrent
  cancellation/stop with at most one reader close.
- Added a daemon behavioral test through actual HTTP handler/service,
  materializer, official GCS client, and strict fake GCS. It proves exact
  generation publication/retrieval/materialization, canonical and sealed tree
  semantics, exact receipt, producer-node independence, and fail-closed absent,
  corrupt-byte, corrupt-metadata, and replacement-generation behavior without
  partial destinations.
- Added a CI-only `live` K3s contract through JetBridge's strict generated-Pod
  path. It asserts all mounts resolve, required affinities and read-only input,
  then executes the generated BusyBox init/main flow on Linux.
- Restored the daemon-namespace TLS SAN override omitted by the historical live
  test port. Added the narrower `hangar_live` tag so this contract can compile
  and run independently of that port's unrelated PostgreSQL-runner gap.

## Commit

Planned commit subject: `test(hangar): prove strict tree flow`.

## Verification

- Focused cancellation regression: exit 0, `hangar` 0.320s.
- Focused daemon full flow: exit 0, `artifact-daemon` 0.629s.
- Required Hangar/daemon/durable/chart command: exit 0, real 63.66s.
- Root import guard: exit 0, real 1.18s.
- Focused JetBridge behavioral tests: exit 0, real 8.68s.
- `make test-unit`: exit 0; 84 Ginkgo suites passed in
  11m13.010266625s; real 673.89s.
- Final focused rerun: cancellation regression 0.311s and daemon full flow
  0.582s, both exit 0.
- Dedicated Hangar live compile/run probe: exit 0; the test compiled and
  truthfully skipped on macOS before contacting Kubernetes.
- `git diff --check`: exit 0 after final formatting and report creation.

Exact commands, package timings, and environment notes are recorded in
`docs/superpowers/reports/2026-08-19-hangar-core-foundation-verification.md`.

## CI-only gap

The new live test was not run on macOS and no local K3s was started. The
repository-wide external live suite still cannot compile with `-tags live`
because its historical real-DB port references the absent
`postgresrunner.StandardTestRunner`. The missing daemon-namespace companion
change is fixed here, and the new contract independently compiles with
`-tags hangar_live`; it does not inherit the unrelated database fixture. The
existing harness offers no reusable backend failure injector, so the strict
daemon test carries those failure cases. Final whole-branch review and
re-review remain with the controller.
