# CI history: elapsed time, retries and suite attribution

This analysis covers the downloaded snapshot in
`output/ci-performance-20261102`: 20 build records for each of 16 jobs (320
records), with complete event streams and input revisions for 68 builds.
It uses no new CI execution. The offline parser is
[history-analysis.py](../../output/ci-performance-20261102/history-analysis.py).
Machine-readable results include [all build records](../../output/ci-performance-20261102/history-builds.json),
[cohort statistics](../../output/ci-performance-20261102/history-summary.json),
[event markers](../../output/ci-performance-20261102/history-markers.json),
[unit summaries](../../output/ci-performance-20261102/history-unit-summaries.json),
and [behavioral specs](../../output/ci-performance-20261102/history-behavioral-specs.json).

Build duration means API `end_time - start_time`. It excludes any queueing
before the build started. Task boundaries are reported at one-second precision.
Shell phase intervals below use the timestamp of the log event carrying a
command or marker, so they are approximate and can include output delivery
latency. Buffered Ginkgo output is interpreted using its own reported durations,
not its eventual log-delivery time. None of these durations is CPU time.

## Latest core: the automatic chain took 51m37s

All jobs in this main-pipeline chain consumed exactly
`1d0e377594898bff615f1c222f9977e0ea094acc`. The automatic path from build-and-vet
start to live-tests completion took **3,097 seconds**, on 2026-11-02 from
16:28:07 to 17:19:44 UTC. Brine occupied **2,078 seconds, 67.1%** of that path.
Worker-image added an otherwise easy-to-miss **164-second tail**: build-image
was ready, but self-upgrade could not start until worker-image released its
serial group. [Exact input revisions and timestamps](../../output/ci-performance-20261102/history-latest-core-chain.json)

| Job / build | Build ID | Start UTC | Finish UTC | Elapsed | Contribution to this path |
|---|---:|---|---|---:|---:|
| build-and-vet / 2235 | 1140176 | 16:28:07 | 16:29:27 | 80s | 80s |
| unit-tests / 1105 | 1140183 | 16:29:27 | 16:33:06 | 219s | 219s |
| brine / 121 | 1140196 | 16:33:06 | 17:07:44 | 2,078s | 2,078s |
| tag-rc / 341 | 1140315 | 17:07:44 | 17:08:18 | 34s | 34s |
| build-image / 617 | 1140319 | 17:08:18 | 17:11:20 | 182s | 182s |
| worker-image / 52 | 1140197 | 17:07:44 | 17:14:04 | 380s | 164s after tag/image |
| self-upgrade / 405 | 1140331 | 17:14:04 | 17:14:18 | 14s | 14s |
| verify-upgrade / 379 | 1140342 | 17:14:18 | 17:14:56 | 38s | 38s |
| k8s-live-tests / 805 | 1140345 | 17:14:56 | 17:19:44 | 288s | 288s |

The sum of the last column is 3,097s. Adding every job's full duration would
incorrectly double-count the 216s of worker-image that overlap tag/image.
Build-image itself consists of a 33s frontend task and a 149s image task.
Release / 175 started at 17:20:35, 51s after live tests finished, and lasted 60s.
Release is manually triggered; the 51s is not attributed to test execution.
Commit-to-first-build latency is not established by this dataset.

### Latest build-and-vet phases

The captured commands account for approximately 4s of repo acquisition, 5s
between task start and first build command, 47s building concourse, 3s building
artifact-daemon, 9s ordinary vet, 5s `live` vet and 7s `hangar_live` vet/final
completion. These add to the 80s build. No CPU profile exists in this historical
run, so the 47s cannot be separated into compiler work, scheduling and I/O.
[Event stream](../../output/ci-performance-20261102/build-1140176-events.txt)

### Latest Brine phases

| Approximate interval UTC | Phase | Seconds |
|---|---|---:|
| 16:33:06–16:33:12 | Task startup and revision receipt | 6 |
| 16:33:12–16:34:16 | Private-network launcher, adapter and artifact-daemon builds | 64 |
| 16:34:16–16:34:22 | Nested-module vet | 6 |
| 16:34:22–16:35:30 | Native suites, including build/check overhead | 68 |
| 16:35:30–16:35:43 | Static live artifact-daemon build | 13 |
| 16:35:43–16:35:48 | Coverage guards and repeated private-network build | 5 |
| 16:35:48–17:06:41 | Instrumented adapter build + mandatory local/live run + verification | 1,853 |
| 17:06:41–17:07:36 | Cross-node run + verification and coverage calculation | 55 |
| 17:07:36–17:07:44 | Ordinary-adapter rebuild and final completion | 8 |

The native CLI reports 67.197s. The mandatory verdict reports 1,049 passing
scenarios across two manifests, followed by one passing cross-node scenario.
Coverage is 2,846 / 3,593 statements (79.210%). This history cannot split the
1,853s into local, live, coverage compilation, scenario work and resource
cleanup: successful detailed JSONL was redirected into the task's temporary
file and never published. These are exactly the missing measurements the
fresh profiling run must supply. [Events](../../output/ci-performance-20261102/build-1140196-events.txt),
[decoded log](../../output/ci-performance-20261102/history-build-1140196.log)

### What the Brine JSONL timers actually include

The adapter pins the Go runner to private Brine commit `1e9345da594d`.
The implementation was inspected in the local Go module cache; these links
identify that exact source revision rather than current runner documentation.
[Adapter dependency pin](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/go.mod#L247)

| Field | Included | Outside this timer |
|---|---|---|
| `step_end.duration_ms` | Registry lookup, `step_start` emission, dispatch, lazy fixture acquisition inside the step, context bookkeeping | Eager scope acquisition, its own `step_end` emission, recorder drain, scope disposal |
| `scenario_end.duration_ms` | Scenario setup inside the runner, all steps and their event overhead, recorder disposer drain and its events | Eager scenario-scope acquisition before the scenario call, scenario-scope disposal after it, its own `scenario_end` emission |
| `recorder_drain_completed.elapsed_ms` | `recorder_drain_started` emission and registered disposer calls | The completed event's emission, resource-scope disposal |
| `run_end.duration_ms` | `RunPlan` execution, including suite/feature/scenario scope acquisition and disposal, recorder drains and intervening events | Adapter preparation before `RunPlan`, its own `run_end` emission, adapter exit checks after `RunPlan` |

The scenario timer ends **before** `DisposeScope(ScopeScenario)`, although the
`scenario_end` event is emitted **after** that disposal. Its timestamp interval
and its `duration_ms` consequently need not measure the same work.
[Scope acquisition and scenario call](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/pipeline.go#L444),
[scope disposal and scenario event](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/pipeline.go#L473),
[step timer](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/pipeline.go#L577),
[recorder drain and scenario timer](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/pipeline.go#L680),
[suite disposal and run timer](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/pipeline.go#L508)

Local envtest startup is performed by `prepareRun` before `RunPlan`, so it is
outside the run timer. Adapter exit then performs any remaining resource
disposal, namespace checks and daemon temporary-root leak checks after
`RunPlan`; those final checks are outside it too. Normal suite-scoped disposal,
including the registered namespace sweep, runs inside `RunPlan`.
[Preparation and run order](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/cmd/brine-adapter-jetbridge/main.go#L152),
[local preparation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/cmd/brine-adapter-jetbridge/run_lifecycle.go#L28),
[suite namespace sweep](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/live_namespace_sweep.go#L98),
[exit checks](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/cmd/brine-adapter-jetbridge/lifecycle.go#L274)

Use the numeric duration fields for subsecond attribution: event timestamps
have whole-second RFC3339 precision; duration fields use monotonic elapsed
time truncated to integer milliseconds, while recorder drain uses fractional
milliseconds. For a normal serial run, **do not add recorder drain to scenario
duration**. `run − sum(scenarios)` mixes eager scope work, resource disposal,
event overhead and rounding; it is not pure teardown.
`scenario − sum(steps)` includes recorder drain plus runner bookkeeping and
rounding; it excludes scope setup/disposal outside the scenario call. The
resource registry emits no per-resource timing, so those residuals need
instrumentation before being assigned to a specific fixture.
[Event timestamp precision](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/events.go#L38),
[resource scope operations](https://github.com/MarkDucommun/brine-private/blob/1e9345da594d/runners/implementations/go/pkg/brine/resources.go#L444)

## Last-20-build medians, with their date windows

These are observed sample medians, not predictions or long-term failure rates.
A successful build may contain a failed attempt. The windows differ across
jobs because each file contains the latest 20 builds, not the same calendar
period. Older release and E2E histories span major changes; do not use their
pooled median as a current baseline.

| Pipeline / job | UTC date range | Success n / median | Failed n / median | Other |
|---|---|---:|---:|---|
| jetbridge-brine | 2026-10-30–2026-11-02 | 18 / 2115.5s | 2 / 3882.0s | — |
| jetbridge-build-and-vet | 2026-10-31–2026-11-02 | 20 / 80.5s | 0 / —s | — |
| jetbridge-build-image | 2026-10-30–2026-11-02 | 20 / 162.0s | 0 / —s | — |
| jetbridge-k8s-live-tests | 2026-10-30–2026-11-02 | 12 / 289.0s | 8 / 587.0s | — |
| jetbridge-release | 2026-08-03–2026-11-02 | 17 / 417s | 2 / 545.5s | aborted: 1, median 109s |
| jetbridge-self-upgrade | 2026-10-30–2026-11-02 | 20 / 15.5s | 0 / —s | — |
| jetbridge-tag-rc | 2026-10-30–2026-11-02 | 20 / 22.0s | 0 / —s | — |
| jetbridge-unit-tests | 2026-10-31–2026-11-02 | 19 / 213s | 1 / 385s | — |
| jetbridge-verify-upgrade | 2026-10-30–2026-11-02 | 20 / 29.0s | 0 / —s | — |
| jetbridge-worker-image | 2026-10-31–2026-11-02 | 20 / 341.0s | 0 / —s | — |
| k8s-e2e-build-kind-runner | 2026-10-31–2026-11-02 | 20 / 284.0s | 0 / —s | — |
| k8s-e2e-hangar-cluster-contract | 2026-10-15–2026-11-02 | 19 / 319s | 1 / 1661s | — |
| k8s-e2e-hangar-disk-store-contract | 2026-10-14–2026-11-02 | 20 / 83.0s | 0 / —s | — |
| k8s-e2e-hangar-generated-pod-contract | 2026-10-14–2026-11-02 | 3 / 57s | 17 / 69s | — |
| k8s-e2e-k8s-behavioral-tests | 2026-09-27–2026-11-02 | 16 / 2289.5s | 4 / 932.5s | — |
| k8s-e2e-run-cancellation-kubelet | 2026-10-14–2026-11-02 | 3 / 304s | 17 / 688s | — |

The complete per-day counts, medians, minima, maxima, known revisions and count
of unknown revisions are in `history-summary.json`. Revisions are known only
where a resources response was downloaded; the parser marks the other 252
builds unknown instead of inferring a SHA from a nearby run.

### Behavioral cohorts must remain separate

| Cohort | UTC dates | Success count / median / range | Failed count / median |
|---|---|---|---|
| Builds 199–210 | Sep 27–Oct 5 21:10 | 9 / 2,332s / 2,271–2,695s | 3 / 479s |
| Builds 211–218 | Oct 5 23:01–Nov 2 | 7 / 1,276s / 1,262–1,332s | 1 / 1,386s |
| Latest builds 216–218 | Oct 31–Nov 2 | 2 / 1,272s / 1,262–1,282s | 1 / 1,386s |

The latest-20 success median of 2,289.5s is misleading for current planning.
The post-211 cohort is already about 21 minutes. Its known revisions are
`7aa0377c77`, `5d28ed32f8`, `adb6db34b6`, `70a73eb747`, `fc411ec1e3` and
`afc933f94e`. This is an observed historical shift, not a speedup from the
current review. Builds 211–215 reported 299 executed specs; latest passing
builds 217–218 report 294 of 296 with two skips, so even these cohorts do not
have identical inventories.

The most recent completed behavioral pipeline build in this historical sample,
218, tests `afc933f94e`, **not**
latest core `1d0e377594`. Only the toolchain-build job has a downloaded run
against latest core in that sample. The source's root-module and live results
do not establish latest-core behavioral completion. The subsequent
[current-core profiling run](ci-behavioral-profiles.md) supplies that execution
and reports one configuration-validation expectation failure.

For generated-pod and kubelet contracts, the last-20 view also hides a sharp
recency difference: each latest three runs, Oct 31–Nov 2 at `70a73eb747`,
`fc411ec1e3`, and `afc933f94e`, passed; the preceding 17 failed. Their latest-three
success medians are 57s and 304s respectively. Calling those tiers currently
85% flaky would collapse older failures and current passes into one label.
Release's latest three Nov 2 runs take 60s, 63s and 60s, while its pooled history
reaches back to August. [Cohorts and dates](../../output/ci-performance-20261102/history-summary.json)

## Retry cost is paid even when the build ends green

Brine / 119, build 1137837, consumed `afc933f94e` and finished successfully after
**4,104s**. Attempt one ran 05:24:13–05:57:57 UTC for **2,024s**, exited 1, and
attempt two immediately ran for **2,080s** and passed. The retry added 33m44s;
it was 49.3% of this green build's elapsed time.

The failed attempt's diagnostics expose timings absent from successful builds:
918 local scenarios passed in **447.518s**; the live tier took **1,380.944s**,
with 130 of 131 scenarios passing. The failing 12.368s scenario was
“A get step's resource is read back from the directory its own pod wrote it to”;
its error says producing step `get-42` had zero pods when exactly one was
expected. This identifies the observed failure, not its root cause. Local and
live timings are for that failed attempt and cannot be presented as exact
breakdown of latest-core Brine / 121.
[Retry events](../../output/ci-performance-20261102/build-1137837-events.txt),
[scenario and run-end diagnostics](../../output/ci-performance-20261102/history-build-1137837.log)

Hangar cluster contract / 32, build 1134172 at `fc411ec1e3`, consumed 834s and
827s in two failed attempts, totaling 1,661s. Later build 33 at `d43050315b`
passed in 319s. Historical records without event streams do not expose
attempt counts, so retry waste calculated from these 68 streams is a lower
bound for the 320-build sample. Multiple tasks in build-image or release are
ordinary sequential tasks, not automatically retries.

## Unit time: package execution is not the whole command

Latest unit / 1105 ran for 219s. Event markers show approximately:

- 4s until script execution, then 1s prerequisite/listing work;
- 9s building the Ginkgo CLI;
- 194s overlapping foreground `go test` and the eight heavy Ginkgo commands;
- 11s for fly integration and completion.

Every heavy suite starts around 16:29:41. At 16:32:55 the foreground command
returns and all eight waits succeed immediately; their logs are then printed
in the script's fixed order. The longest heavy CLI is DB at 179.645s, about 14s
shorter than this 194s overlap interval. The foreground package set therefore
appears to be this run's final bottleneck, not DB's 94.098s Ginkgo spec interval.
This is an inference from command timing and completed background jobs; exact
per-process spans require new instrumentation.
[Events](../../output/ci-performance-20261102/build-1140183-events.txt)

| Suite | Specs run / declared | Ginkgo reported seconds | CLI elapsed seconds | CLI minus reported |
|---|---:|---:|---:|---:|
| DB Suite | 1315 / 1315 | 94.098 | 179.645 | 85.547 |
| API Suite | 752 / 752 | 42.171 | 124.141 | 81.970 |
| Migration Suite | 213 / 213 | 28.911 | 98.602 | 69.691 |
| Exec Suite | 617 / 617 | 25.460 | 104.248 | 78.788 |
| Algorithm Suite | 98 / 98 | 41.672 | 111.167 | 69.495 |
| Engine Suite | 191 / 191 | 11.725 | 89.561 | 77.836 |
| Runs Suite | 117 / 117 | 12.014 | 81.620 | 69.606 |
| Jetbridge Suite | 185 / 185 | 22.919 | 140.123 | 117.204 |
| Integration Suite | 593 / 594 | 6.224 | 9.035 | 2.811 |

“CLI minus reported” is deliberately not called compilation time. It can
include build/link work, ordinary Go tests in mixed packages, process startup,
waiting and other runner overhead outside Ginkgo's reported interval. Ordinary
`TestXxx` functions in a parallel Ginkgo binary execute in each worker and are
not included in the Ginkgo spec inventory. The logs do not separately time
all of them. The very large runtime difference, 117.204s, needs that distinction.
These heavy CLI durations overlap; summing them is not a 219s job breakdown.

The latest eight downloaded unit builds (1098–1105, different core revisions)
span 200–236s. DB's reported spec time spans 94.098–112.611s while its CLI spans
175.789–206.002s; runtime spec time spans 22.919–25.398s while CLI spans
131.604–151.934s. The pattern is repeated, but source and concurrent load vary,
so this is not a controlled performance comparison.

Among foreground Go package execution summaries, the latest largest are
`atc/integration` 42.016s, `atc/gc` 35.084s, chart tests 32.373s,
`atc/hangaroutput` 30.011s, `atc/scheduler` 26.101s and artifact-daemon 25.621s.
These exclude package compilation and overlap each other. Summing them does
not explain foreground elapsed time. Fly integration uses **27 processes**,
reports 593 of 594 specs with one skip, and contributes 6.224s Ginkgo time /
9.035s CLI time. [Parsed unit results](../../output/ci-performance-20261102/history-unit-summaries.json)

## Behavioral / 218: setup, expensive specs and process balance

The latest completed run takes 1,282s; its precompiled Ginkgo invocation takes
1,213.036s and reports 294 of 296 specs in 1,213.000s. Outside that invocation
are approximately 6s before its first script output, 34s source compilation,
3s Docker startup, 17s concourse image build, 3s OOM image build, 4s K3s pull,
and 2s completion. The 1,213s includes Ginkgo suite setup and teardown; it must
not be interpreted as pure test bodies. [Decoded log](../../output/ci-performance-20261102/history-build-1139207.log)

Setup runs for 103.385s on process 1 and 165.508s on process 2. Process 2 waits
**63s on the cluster setup lock**. The phase logs show:

| Process | Cluster create | Image load | API ready |
|---|---|---|---|
| 1 | 11:52:46–11:52:55 | Begins 11:52:55; next process gets lock 11:53:49 | 11:54:11 |
| 2 | 11:53:49–11:53:57 | Begins 11:53:57 | 11:55:13 |

The first specs therefore start roughly 62s apart. Isolated endpoint ports in
each spec's captured output map every one of the 294 specs to its worker:

| Worker | Specs | Sum of reported spec elapsed time | Earliest command | Latest command |
|---|---:|---:|---|---|
| 1 | 152 | 1,100.880s | 11:54:11.365 | 12:12:36.443 |
| 2 | 142 | 1,046.131s | 11:55:13.453 | 12:12:39.916 |

Despite different start times, their last recorded commands finish only 3.47s
apart. There is no large end-of-run worker imbalance here: earlier readiness
lets process 1 take more work. The **2,147.011s** sum of spec durations is
aggregate elapsed work across workers, not wall time and not CPU-seconds.
Teardown hooks are 0.986s and 4.468s. Resource stamps show Docker tmpfs growing
from about 71% to 76%; final diagnostics report 9.1G of 12G used. Host available
memory is not task RSS. The historical events identify the synthetic worker
`k8s-cicd`, not its physical Kubernetes node, so node attribution is unknown.
[Per-worker reconstruction](../../output/ci-performance-20261102/history-behavioral-specs.json)

The ten most expensive individual spec reports are:

| Seconds | Spec | Source |
|---:|---|---|
| 41.669 | Job Scheduling and Concurrency marks interruptible jobs as abortable when superseded | `topgun/k8s_behavioral/job_scheduling_test.go:115` |
| 34.108 | Custom Resource Types and Image Resolution image passing between jobs 6.22: fan-out from single resource to multiple jobs | `topgun/k8s_behavioral/custom_resource_types_test.go:625` |
| 34.095 | Job Scheduling and Concurrency enforces serial max one concurrent pod set in K8s | `topgun/k8s_behavioral/job_scheduling_test.go:295` |
| 34.087 | Build Lifecycle exposes build events via SSE endpoint | `topgun/k8s_behavioral/build_lifecycle_test.go:275` |
| 33.849 | API Surface Validation streams build events via SSE | `topgun/k8s_behavioral/api_surface_test.go:25` |
| 33.076 | Task Steps and Container Targeting configuration 5.18: container_limits set pod resource limits | `topgun/k8s_behavioral/task_step_test.go:595` |
| 30.682 | Resource Checking and Version Management Resource Checking disables automatic checking with check_every: never | `topgun/k8s_behavioral/resource_checking_test.go:85` |
| 30.095 | Custom Resource Types and Image Resolution image passing between jobs 6.23: fan-in from multiple jobs to single downstream | `topgun/k8s_behavioral/custom_resource_types_test.go:700` |
| 30.080 | Custom Resource Types and Image Resolution image passing between jobs 6.21: three-stage pipeline with passed constraints | `topgun/k8s_behavioral/custom_resource_types_test.go:550` |
| 29.123 | Get Steps constrains versions with passed constraint | `topgun/k8s_behavioral/get_step_test.go:152` |

These ten account for 330.864s, 15.4% of aggregate spec elapsed time, spread
across both workers. Their full durations include fixture hooks, CLI/API waits
and cleanup. By source file, custom resource types total 287.460s across 17
specs; task steps 241.563s across 27; get steps 180.537s across 14; scheduling
160.980s across nine. These are useful investigation priorities, not additive
critical-path savings. Full rankings, source locations, decoded log line
numbers and endpoint-based process attribution are retained in the JSON.

## What this evidence supports next

The largest verified path target is Brine, followed by its release-chain
interaction with worker-image. The largest current knowledge gap is local/live
resource and step attribution inside successful Brine runs. Unit optimization
needs compilation/process accounting and foreground package scheduling, not
only the Ginkgo DB summary. Behavioral already has a balanced finish; further
speedups should examine serialized cluster/image setup and the slow repeated
workflow families before increasing worker count.

Archived logs cannot supply historical CPU flame graphs, heap pressure,
PostgreSQL wait attribution, exact pod scheduling/image-pull spans, physical
node identity or complete skipped/ordinary-test inventories. Those remain
explicit unknowns for the fresh profiles. This report makes no claim that a
particular code change has already delivered a speedup.
