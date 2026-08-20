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
- Added a CI-only `live || hangar_live` K3s contract through JetBridge's strict
  generated-Pod path. It asserts all mounts resolve, required affinities and
  read-only input, then executes the generated BusyBox init/main flow on Linux.
  The pipeline invokes the focused `hangar_live` contract explicitly before
  the historical broad live suite.
- Restored the daemon-namespace TLS SAN override omitted by the historical live
  test port. Added the narrower `hangar_live` tag so this contract can compile
  and run independently of that port's unrelated PostgreSQL-runner gap.

## Final review fix round

The whole-branch reviewer found no Critical issue and four Important boundary
failures. The fix round:

- made same-key malformed-object publication a conflict-only result and proved
  the public route returns sanitized 409;
- typed every daemon-owned GCS scratch failure as infrastructure while retaining
  the underlying cause and distinguishing compression-sink failures from source
  reads;
- changed artifact-daemon ingress policy to select the labels JetBridge actually
  places on task/check Pods, with a parsed non-vacuous chart test;
- made the focused live contract use in-cluster configuration when no explicit
  kubeconfig is set, moved its fixture off the deployed daemon port, and added
  the exact command to CI;
- built the published archive from the producer directory and proved producer
  mutation changes the upload; and
- joined materializer-owned archive/root/capture cleanup errors into its result,
  retaining an already-published receipt for idempotent retry.

The reviewer also identified coverage guards that now assert their tables are
non-empty and exact mode checks that include setuid, setgid, and sticky bits.

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
- Final Hangar package rerun after the exact-mode cleanup: exit 0, 1.080s.
- `git diff --check`: exit 0 after final formatting and report creation.

Exact commands, package timings, and environment notes are recorded in
`docs/superpowers/reports/2026-08-19-hangar-core-foundation-verification.md`.

## CI-only gap

The new live test was not run on macOS and no local K3s was started. The
repository-wide external live suite still cannot compile with `-tags live`
because its historical real-DB port references the absent
`postgresrunner.StandardTestRunner`. The missing daemon-namespace companion
change is fixed here, and the pipeline now runs the independent
`-tags hangar_live` contract before that legacy command. The existing harness
offers no reusable backend failure injector, so the strict daemon test carries
those failure cases. These are layered proofs: they do not constitute a real
multi-node deployment using GCS after ejecting the producing node; that remains
environment-dependent follow-up coverage.
