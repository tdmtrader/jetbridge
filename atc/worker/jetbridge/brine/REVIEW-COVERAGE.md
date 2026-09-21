# Review behavior coverage

The review CLI is for humans and agents. Its eventual public MCP must expose the
same operations to any compatible client. The input-reader MCP tested here is an
internal worker tool, not the still-pending public submission interface.

The executable scenarios are `features/review-artifacts.feature` and
`features/review-worker.feature`. Their steps run the compiled `jb` and
`jb-review-worker` commands, actual Git repositories and OS pipes. Linux workers
use real tmpfs and signals. The only substituted process is Codex: the shared
`agent/review/testdata/provider` executable emits deterministic model output and
refreshes synthetic auth. It never calls a model. No review step substitutes a
database, HTTP server or Kubernetes client.

| Behavior | Brine coverage | Previous Go coverage |
| --- | --- | --- |
| Exact base/head trees, deletion context, optional plan, export attributes and repeatable digest | Artifact capture scenario | `TestCaptureCommittedTrees` |
| Dirty/untracked/submodule/LFS rejection without publishing input | Unsupported-state outline | `TestCaptureRejectsUnsupportedInputs` |
| CLI capture followed by a fresh stdio reader; credential path denied | Reader scenarios | `TestCaptureCommandAndWorkerInputTools` |
| Valid/partial output, provider exit, missing/malformed output, forbidden tools, timeout, cleanup and no API-key inheritance | Worker outlines | `TestWorkerSubprocessBoundary` |
| Invalid subscription credentials and existing report preservation | Credential outline and overwrite scenario | `TestWorkerRejectsAPIAuthAndDirtyOutput` |
| Abandoned credential stream expires | Real CLI pipe scenario | `TestWorkerTimeoutDuringCredentialHandoff` |
| Located/deletion findings, invalid line/path, unknown fields and incomplete declared coverage | Worker result outlines | End-to-end additions; focused schema cases remain in Go |
| No reuse across invocations; actual SIGTERM after refresh | Worker lifecycle scenarios | End-to-end additions |

The replaced behavioral tests were removed after the Brine cases passed. Go
retains focused schema/provenance/location tables, input containment and MCP
framing checks, and the memory-runtime gate/early-failure cleanup checks.

The migration found a real regression: the CLI's inherited blocking stdin could
not be interrupted by closing it from another goroutine. The previous `io.Pipe`
test passed while the executable hung. On Linux the CLI now registers a
nonblocking duplicate with Go's poller so credential timeout interrupts the read.
The original and refreshed credentials are removed before the process exits.

Build the adapter before each source-changing run. Invoke the real Brine CLI,
with an engine at the adapter's pinned revision, and verify nonzero scenario
counts. Run the artifact feature on macOS; run both on Linux. The existing CI
Brine job discovers these files through its feature glob.

Validation on 2026-09-12: eight artifact scenarios passed on macOS; all eight
artifact and twenty worker scenarios passed in a local Linux arm64 container.
The container had no external network access; its real API-server fixture used
an explicit loopback advertise address. Brine CLI/engine source revision was
`1e9345da594d`, matching the owner's contract-5 migration. Adapter vocabulary
guards and vet passed. CI attempt `828655` could not pull the migration's absent
`registry.home/concourse-test-runner:v10`, so this is not a passing CI claim.

This coverage does not establish live subscription eligibility or behavior,
detached Run admission, remote credential handoff, Kubernetes orphan cleanup,
durable result retention or public MCP acceptance. Those remain open in the
approved review track.
