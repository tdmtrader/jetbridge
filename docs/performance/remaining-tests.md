# Performance of the remaining test suites

## Publication status — 2026-10-04

The measured performance implementation and the completed
[self-contained timing report](test-timings.html) are published together on
`test-performance`. The [completion audit](test-timings-audit.json) covers the
report: 20 tiers, 13 result styles and 45,132 observations, including repeated
runs, failures and historical incomplete observations. It does not certify
that every test passed or that the whole-estate 20% speed target was achieved.

The final measured unit comparison took 1,133.190 → 949.937 seconds (16.17%
less). Brine native took 200.387 → 108.383 seconds; full Brine local/live
coverage took 1,837.161 → 1,684.833 seconds with all 1,324 scenarios and 7,593
steps passing on both sides. The final behavioral comparison took
2,666.489 → 2,471.427 seconds with the same 304 original outcomes. The report
identifies each comparison's source, fixture and timing scope; separate
profiles must not be combined into an estate-wide speedup claim.

The notes below preserve the historical measurement chronology. Statements
about unpublished work, outstanding execution or authorization describe those
earlier stages. Subsequent approved profiles, failures and completed coverage
are recorded in the report. Historical inventories and intermediate timings
are not the final inventory or a current blocker list.

## Historical notes

Status: **local changes verified; full remaining-estate measurements resumed and the goal is not yet achieved.**
The runner, source/dependency transfer, future cluster testing, and image pulls
are approved. The real-subscription model/auth-file configuration is still
pending. Automatic approval review separately rejected exporting the prior-release
source needed by upgrade/downgrade tests; specific payload approval is pending.
All locally runnable comparisons below are complete.
The additional target is a 10% reduction under the original constraints: serial
execution, no replaced dependencies, removed tests, reduced coverage, weakened
assertions, or shorter failure deadlines.

The baseline is `core` at `d01185fbc`, plus the completed PostgreSQL/migration
optimizations documented in [serial-go-tests.md](serial-go-tests.md). The gains
below are additional to that starting state, not a recount of the first phase.

## Changes

- Brine's real daemon launcher probes readiness sooner after a failed initial
  probe, starting at 1 ms and doubling to the original 100 ms interval. It still
  requires the same successful HTTP/TLS readiness check, retains the 20-second
  startup deadline and cleanup, and observes process exit while waiting.
- Vocabulary guards memoize the SDK registry's immutable match results by exact
  sentence within each test. Every feature, expanded scenario, step occurrence,
  definition, ambiguity check, and diagnostic remains checked. Feature files are
  still read freshly; no cross-process cache or stale compiled guard is used.
- The real Tempo/Loki connectivity tests query immediately after an acknowledged
  write. Subsequent queries retain the original one-second interval and complete
  30-second visibility window. No exporter, service, transport, or response is
  substituted.

Behavior-dependent waits, including cancellation backoff, notification recovery,
negative observations, resource cleanup, and production deadlines, are unchanged.

## First complete comparison

Linux amd64; Go 1.25.6; GOMAXPROCS=1; one Ginkgo process. The nested Brine module
uses Ginkgo 2.27.4. The Rust Brine CLI and engine were release builds at the module's
pinned revision `1e9345da594d`. Both sides use the same real PostgreSQL, envtest
API server, BusyBox, artifact/output daemons, and telemetry services.

| Selection | Baseline | Candidate | Reduction |
|---|---:|---:|---:|
| All 1,182 local Brine scenarios, command elapsed | 648.563 s | 544.789 s | 16.00% |
| Sum of all local scenario durations | 562.826 s | 462.434 s | 17.84% |
| Four native Brine packages, precompiled binaries | 201.894 s | 199.577 s | 1.15% |
| Guard execution alone, median of three | 2.617 s | 2.068 s | 20.96% |
| Real Tempo/Loki tests, two matched pairs | 2.105 / 2.057 s | 0.023 / 0.025 s | about 98.8% |

The guard row is a diagnostic subset of the native tests, not an additional tier
or a gain to add to their total. Native outcomes include subtests: 255 passing
records, identical between baseline and candidate. There were no native Brine
skips. All 1,182 behavioral scenarios and 6,870 steps passed on both sides.

Adapter/daemon binaries and native test binaries were built outside the timed
commands. Existing lazy builds owned by individual fixtures remain included on
both sides; those build paths were not changed. Scenario duration excludes CLI
startup and setup outside scenarios. A second complete local comparison ran candidate before baseline to check
order and cache effects:

| Reverse-order comparison | Baseline | Candidate | Reduction |
|---|---:|---:|---:|
| Full local Brine command elapsed | 643.998 s | 558.228 s | 13.32% |
| Sum of all local scenario durations | 562.305 s | 472.873 s | 15.90% |

All four complete runs passed the same 1,182 scenarios and 6,870 steps. Each
recorded resource-drain completion for every scenario. An independent check
after the final run found no remaining adapter roots or adapter/daemon
processes in the measurement workspace.

## Preservation checks

The audit compares every feature/scenario/step identity and native passing
outcome, and verifies identical feature and manifest bytes. The definitions and
scenario inventory were not reduced. Disposable bad-corpus checks confirm that
baseline and candidate both reject repeated undefined sentences, an empty
corpus, and unused definitions with identical diagnostics. Existing tests of
real daemon startup refusal, process cleanup, and resource disposal pass.

The telemetry comparison ran baseline/candidate and then candidate/baseline
against the actual configured Tempo and Loki services, using the existing unique
test markers. Both tests passed in all four runs.

## Additional candidates on the disposable runner

The protocol optimization is now applied to the working tree. The protocol
suite builds its guards once from the current source
and executes that binary at every launcher invocation. Feature reads and guard
results are not cached. The normal launcher still uses `go test` when the
protocol suite has not supplied its fresh binary. Direct execution explicitly
retains the original ten-minute Go guard timeout. A second native comparison
with that deadline explicit passed the same 255 records: **203.424 → 108.114 s
(46.85% reduction)**. Both changed-feature rejection checks passed again.

All four native Brine packages passed the same 255 records: **222.439 → 113.501 s
(48.97% reduction)** on the runner. Both launchers also rejected an undefined
feature sentence and a subsequent edit to that sentence with identical
first-error diagnostics, proving that the compiled guards still read changes.

A separate candidate uses gzip level 1 for transporting the real artifact-daemon
executable. Across three samples, median compression time fell from 2.101 to
0.634 s, while the payload grew from 22.045 MB to 25.503 MB. Every decompressed
hash matched. This is a preparation microbenchmark, not a live-suite speed claim;
the unchanged live checksum, pod identity, readiness and cleanup checks passed
in the full comparison. This gzip change remains isolated because the full live
wall-time comparison did not establish a speedup.

The runner uses the same pinned toolchain and serial execution on both sides.
Its outer pod has a four-CPU limit; measured Go processes use GOMAXPROCS=1.
The real application image was built from the frozen baseline source with Go
1.25.6 and all frontend assets, runtime executables and Fly downloads. Both
sides use that image because their production application source is identical.

## Estate still outside the improvement claim

| Selection | Current evidence |
|---|---|
| Fly integration | Matched: 599 passed and the same existing root skip; 47.651 → 47.376 s suite time; unchanged suite |
| Elm | Matched: 3,102 passed with one test worker; 1.816 → 1.901 s reported execution; unchanged suite |
| Brine full local + live gate | Both passed 1,316 scenarios and 7,548 steps; 2,007.348 → 2,044.725 s (1.86% slower); not an improvement claim |
| Brine kubelet: eight scenarios | Not measured |
| Brine real subscription: one scenario | Not measured; needs approved model/auth-file path |
| Tagged live Go tests: 24 root functions and one nested Brine function | Both passed 35 matching records including subtests; 154.711 → 155.335 s; both namespaces removed; no speedup claim |
| Topgun Kubernetes integration | Candidate: 129 passed, six existing pending; baseline: 128 passed, one failed, same six pending. 1,807.570 → 1,643.621 s; invalid performance comparison because outcomes differ |
| Docker Compose integration, testflight, topgun Kubernetes behavioral | Not measured |
| WATS browser tests: five files, 13 cases | Baseline executed: five passed, eight failed in 130.622 s; candidate withheld after failures; no performance claim |

Fly and Elm used the same test identities, seed, and fuzz settings on both sides.
Their short-run timing differences do not establish a material improvement or
regression; their source and assertions are unchanged.

As a file-inventory measure only, completed matched selections now represent
902 of 1,025 Go test files (88%). This is not statement coverage, assertion
coverage, or a runtime-weighted percentage; the long deployment suites account
for much of the outstanding runtime. Brine scenarios and browser/Elm tests
are separate inventories.

The browser baseline exposed six failures involving task images from the mock
resource resolving to `docker.io/library/image:latest`, plus a cluster-name
fixture mismatch (`disposable-performance` versus expected `dev`) and a browser
login assertion failure. All 13 cases ran; none were removed or substituted.
The image fallback supplies an artifact and resource name `image`, while the
Kubernetes container resolves that name as an OCI image. A matching passing
browser comparison is not available.

The whole remaining-estate percentage cannot be calculated from these partial
measurements. The completed full `scripts/coverage` comparison passed its
unchanged dynamic manifest counts, cleanup checks and 50% production runtime
statement coverage threshold on both sides. It did not meet the speed target.

The completed gate represents 1,316 of 1,325 Brine scenarios (99.3% by
scenario count): all 1,182 local and all 134 standard live scenarios. The eight
kubelet scenarios and one real-subscription scenario remain outstanding. This
is an inventory ratio, not statement coverage or a runtime-weighted measure.

Both runs recorded 1,316 completed resource drains and left zero owned Brine
namespaces. The production denominator was identical at 3,707 statements;
coverage was 2,999 statements (80.901%) versus 2,996 (80.820%). The differing
blocks concern peer-probe errors, trailing empty log segments and pod diagnostic
output. Exact statement-hit equality was not achieved and is not claimed.
Local scenario time fell from 585.329 to 492.375 s, while live scenario time
rose from 1,280.633 to 1,420.029 s. All scenario and step identities matched.

The tagged baseline exposed a stale sidecar-test control: production now
streams sidecar logs through stdout when no dedicated writer is supplied, so
both original runs engage the five-second wait. Independent pod startup times
also distort their raw-duration difference. The common correction retains both
real logging paths, adds a no-sidecar control, and timestamps each real main
command's output. It retains the original 25-second total bound and 3–9-second
delta bounds. The focused real-cluster check passed, measuring deltas of
5.002 s and 5.005 s for dedicated and fallback streams. Both complete tagged
selections then passed with the same expanded inventory. This fixture
correction is not counted as a performance gain.

The topgun baseline setup also exposed a nested-network collision: its default
K3s DNS service address matched the outer cluster DNS. Both fixtures now accept
explicit Pod, Service and DNS overrides through `K3S_TEST_CLUSTER_CIDR`,
`K3S_TEST_SERVICE_CIDR` and `K3S_TEST_CLUSTER_DNS`; unset values retain the
original defaults. The comparison uses the same non-overlapping networks on
both sides. The failed setup is retained as diagnostic evidence and excluded
from timing claims. The corrected integration baseline
exposed a missing `across-item=a` log line in a successful four-item across
build. The candidate passed that case and all other enabled cases: 129 passed
with the same six pending cases. Both test clusters were removed. The raw
command reduction was 9.07%, below the target, and the differing outcomes make
this an invalid performance comparison. The assertion remains intact and the
polling experiment remains isolated. A separate live-versus-replay check and
real PostgreSQL event-ordering diagnostic investigated the loss. Ten ordinary
builds retained all four lines in both live output and completed replay. The
deterministic PostgreSQL diagnostic nevertheless reproduced a live subscriber
advancing past an uncommitted lower event ID. The ordinary DB fixture permits
only one connection and hides that transaction overlap; the diagnostic uses
independent real transactions in a single spec. This proves an event-ordering
defect, though it does not conclusively attribute the original integration
failure. No production fix or performance gain is claimed for the diagnostic.

The prepared deployment plan keeps Docker/K3s and Brine hostPath/hostPort fixtures
inside a disposable privileged test runner. The user approved the runner, source/dependency transfer, future cluster testing,
and image pulls. The runner and its inner K3s cluster are ready; the full live
measurements are in progress. The initial native-snapshotter fixture evicted
the Git-timeout and Node application pods. Both unchanged features passed
(19 scenarios) after recreating the inner cluster with overlay storage on a
bind-mounted ext4 directory, with all test resource limits retained and no
owned Brine namespaces left after cleanup. The initial run is excluded from
performance claims; both measured sides use the corrected fixture. The real-subscription case still needs the model
and existing authentication-file path.

## Evidence and reproduction

Working artifacts are in `/tmp/jetbridge-remainder-performance/`:

- `baseline-state.json`, `starting.patch`: exact second-phase starting state.
- `compare-brine.py`, `compare-brine-results.json`, and per-phase logs: serial
  native and behavioral comparisons with prebuilt binaries.
- `comparison-audit.json`, `audit-comparison.py`: complete local inventory,
  outcome, and timing audit.
- `guard-mutation-results.json`: identical rejection of malformed corpora.
- `compare-otel.py`, `otel-results.json`: real-service comparison in both orders.
- `compare-local-rest.py`, `local-rest-audit.json`: matched serial Fly and Elm
  runs with exact test-identity and outcome checks.
- `repeat-brine.py`, `repeat-audit.json`: passing reverse-order full local comparison.
- `completed-drain-audit.json`: matching per-scenario drain completion and no
  remaining owned adapter roots/processes.
- `applied-second-phase-hashes.json`: hashes of the three applied source files.
- `cluster/disposable-runner.yaml`, `cluster/test-plan.md`: approved, running
  deployment fixture and complete outstanding test selections.

Local Brine timing uses an external isolated manifest with the original local
feature glob expanded to an absolute path and the original wrapper as its
runner. This deliberately avoids recursively discovering `live/.brine`; every
local scenario still executes through `brine run --no-engine --mode sync
--format jsonl`. This arrangement must not be described as the full Brine gate.

Both sides use the same task-owned sticky temporary directory, empty kubeconfig
for nonlive runs, and child subreaper because this container's PID 1 does not
reap orphan processes. These are common environment corrections, not measured
optimization gains.
