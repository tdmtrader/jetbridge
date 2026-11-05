# Behavioral-suite profiles on latest core

Fresh build **1142600 failed**: 293 specs passed, one failed after two attempts,
and two were skipped. Ginkgo took **1,210.486s (20m10.486s)**. The failure is a
fast configuration-validation mismatch, not a timeout: both attempts together
cost **0.414s**. Most elapsed time is build execution watched by the tests;
the clearest removable costs are two 30s SSE reads, readiness sleeps, and
repeated image compression during cluster setup. Both workers finish their
last spec within **1.926s**, so final worker imbalance is small.
[Final Ginkgo report](../../output/ci-performance-20261102/behavioral-profile/specs.json),
[failed verdict](../../output/ci-performance-20261102/behavioral-profile/verdict.txt),
[parsed results](../../output/ci-performance-20261102/behavior-research-summary.json)

## Scope and comparability

The one-off profiling run is build **1142600**, using core
`1d0e377594898bff615f1c222f9977e0ea094acc`, two Ginkgo workers and unchanged
`--flake-attempts=2`. It compiles the application, artifact daemon, fly and
behavioral test binary from the supplied source snapshot. The snapshot has
no `.git`; `JETBRIDGE_REPO_ROOT` explicitly points at that same source tree,
so Helm uses its chart rather than the runner image's baked `/src` checkout.
[Task configuration](../../output/ci-performance-20261102/behavioral-profile-task.json),
[source-root selection](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/cluster_lifecycle_test.go#L683)

The task runs on `k3s-agent-y` in pod
`561d532b-0764-4c66-bc58-783e27ef6203`. Its runner is
`registry.home/concourse-kind-runner:v35`, resolved to digest
`sha256:51b2b3173778055b22dc975ca02c461ef14180ce018ba8e18b04881d439d8e8f`.
The freshly built Concourse image is
`sha256:f3d29b318b2641c14b831574d9691bdc5c2131a968ef7bf5e4b069ad0645ee16`,
with creation time 04:11:19.932 UTC in the captured setup log.
The CPU request is four cores. The profiling harness requested
**18,000,000,000 bytes** of memory, while the normal pipeline's `18GB`
serializes as **19,327,352,832 bytes**. Neither has a memory limit; the
requests are not an exact match. Instrumentation adds profiling and resource
sampling overhead. This run and historical behavioral build 218
(`afc933f94e0c114cf9044a22c1df0d4b11b3da58`, 1,282s) are different revisions
and runs, not an A/B speedup comparison.
[Pod and runner receipt](../../output/ci-performance-20261102/behavioral-fresh-pod.json),
[historical analysis](ci-history-analysis.md)

There is also a scheduling reason a completed E2E result can lag core.
`build-kind-runner` triggers on both the repository and the 24-hour timer;
the downstream contract and behavioral jobs trigger only on the timer input,
with repository versions constrained by `passed`. A new core revision alone
rebuilds the runner but does not independently trigger those downstream jobs
once their timer version has been consumed. The next timer version or a manual
trigger advances that chain. The live configuration matches the source. This
is a configured cadence, not evidence that the historical `afc933` result
validates latest core; assigning a particular skipped revision to it would
require scheduler/build-input history.
[Timer and runner triggers](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/k8s-e2e-pipeline.yml#L7),
[contract inputs](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/k8s-e2e-pipeline.yml#L188),
[behavioral inputs](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/k8s-e2e-pipeline.yml#L717),
[scheduler's first-occurrence trigger condition](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/scheduler/scheduler.go#L87),
[live configuration](../../output/ci-performance-20261102/live-k8s-e2e-pipeline.yml)

## Measured phases and outcomes

The environment receipt records **Go 1.25.14, Linux amd64, 28 visible CPUs**.
All times below are UTC on 2026-11-03. The run precompiles the suite; no Go
test-result cache substitutes for its 296 spec reports. The runner image's
initial build/module caches are uncontrolled, so this is not a cold-cache
benchmark. The report records random seed `1793679087`, two processes, no
focus/skip/label filter and a three-hour suite timeout, despite the CLI's
four-hour flag.
[Environment](../../output/ci-performance-20261102/behavioral-profile/environment.txt),
[phase receipts](../../output/ci-performance-20261102/behavioral-profile/phases.txt)

| Phase | Measured elapsed |
|---|---:|
| Compile Concourse, artifact daemon, fly and behavioral binary | 32.265s |
| Start Docker, including cleanup sleep and readiness | 3.102s |
| Build Concourse Docker image | 12.074s |
| Build OOM-trigger image | 2.730s |
| Pull K3s image | 4.261s |
| Pre-Ginkgo total, including small phase-marker gaps | **54.434s** |
| Ginkgo suite, including cluster setup and teardown | **1,210.486s** |
| Ginkgo CLI, including final profile/report work | **1,210.936s** |

The suite interval is **04:11:27.029–04:31:37.515**. The post-Ginkgo shell
phase marker is absent because `sh -e` exits on the failed command; the CLI
still prints its duration, and the exit trap retains evidence with status 1.
The evidence-retention sleep adds 30s after the verdict and is not test time.
Pre-Ginkgo receipts plus the reported CLI duration total approximately
**1,265.371s (21m05.371s)**; that sum excludes dispatch, artifact handling and
the retention sleep and is not the Concourse build's full lifetime.
[Complete execution log](../../output/ci-performance-20261102/behavioral-execute.log)

The skipped tests are Kubernetes credential-manager integration (not configured
in this deployment, 0.208s) and `fly execute -o` (output-directory/volume
repository support unavailable in this runtime, 4.004s). No other spec is
skipped. The report contains 296 spec records plus four suite-hook records;
the CLI says 294 ran because it excludes the two skips. Historical build 218
also declares 296 specs, with 294 passing and two skipped; the earlier
heading parser's 294 records counted executed specs rather than total
inventory. Equal counts do not establish identical assertions across these
different revisions.
[Exact skip records](../../output/ci-performance-20261102/behavior-research-summary.json)

The sole failure is `6.2: refuses a resource type whose type is another
resource type`. Both `set-pipeline` invocations return zero and print
`pipeline created!`, while the assertion at line 89 expects a nonzero exit
and a message rejecting `level-b`. The current validator checks missing
names/types, conflicting image/type fields and cycles; this acyclic chain
passes those checks. The API save route calls that validator. This is a
source/test contract mismatch that needs resolution in the validation work;
it does not demonstrate a runtime timeout or prove the chained resource will
execute successfully.
[Spec and expectation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/custom_resource_types_test.go#L61),
[current validator](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/configvalidate/validate.go#L452),
[API validation call](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/api/configserver/save.go#L78),
[captured failure output](../../output/ci-performance-20261102/behavior-research-failure-writer.log)

The retry stays inside this spec. The first body takes **46.220ms**, the second
**38.482ms**; their combined common setup is 6.753ms and failure diagnostics/
cleanup is 320.507ms. The complete two-attempt spec takes **413.591ms**.
There are no other retries and no recovered flakes. Rerunning the whole suite
would spend another full setup and execution cycle to investigate this local
validation mismatch.

## Worker timing and startup

| Worker | Spec records | Startup hook wall | Spec window | Spec-body sum | Per-spec setup sum | Per-spec cleanup sum | Last spec finish | Teardown |
|---|---:|---:|---:|---:|---:|---:|---|---:|
| 1 | 163: 162 pass, 1 skip | 115.063s | 1,094.286s | 1,077.018s | 10.613s | 6.396s | 04:31:36.407 | 1.106s |
| 2 | 133: 131 pass, 1 skip, 1 fail | 169.213s | 1,038.235s | 1,021.746s | 10.514s | 5.755s | 04:31:34.482 | 1.048s |

Spec bodies and hooks include both attempts of the failed spec. The sum of
all spec durations is **2,132.238 worker-seconds**, not wall time. Each
worker's gaps between spec reports total only 0.152s and 0.130s respectively.
Worker 1 starts specs 54.125s sooner and takes 30 more specs, compensating for
most startup skew. Rebalancing only the final assignment has little room:
the last-spec finish difference is 1.926s.

Common `BeforeEach` takes just **0.916s across 297 invocations**; it already
copies a per-worker authenticated `.flyrc` instead of logging in for every
spec. Common `AfterEach` takes **11.839s**, including failure diagnostics,
pipeline destruction and pod-delete requests. Most per-spec setup time is
the hijack fixture's two 10s sleeps (**20.211s** together), not authentication
or database teardown. Suite teardown is about one second per cluster.
[Node timing reconstruction](../../output/ci-performance-20261102/behavior-research-summary.json),
[existing authentication reuse](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/behavioral_suite_test.go#L216)

The startup hook contains shared preparation and per-worker setup. Worker 1
spends **18.574s** pulling shared images, then 96.487s in its worker-specific
hook. Worker 2's outer hook includes **18.615s** waiting for that shared phase
before its own 150.598s node begins. Summing only its `Node (End)` durations
would miss that synchronization wait. Timestamped startup logs are rounded to
whole seconds, giving these approximate subphases:

| Startup subphase | Worker 1 | Worker 2 |
|---|---:|---:|
| Wait for cluster-creation lock after shared preparation | 0s | 64s |
| Create K3s cluster | 9s | 9s |
| Load all eight images | 55s | 56s |
| Of loading: application image | 26s | 26s |
| Of loading: PostgreSQL image | 22s | 23s |
| CoreDNS, secrets, Helm, web readiness and API startup after loading | 33s | 22s |

Worker 2's loading overlaps worker 1's deployment and spec execution. These
columns therefore cannot be added to get the suite's setup wall time.
[Captured suite-hook output](../../output/ci-performance-20261102/behavior-research-suite-hook-output.json)

## Longest specs and observed client phases

The ten longest specs total **311.751 worker-seconds**, 14.6% of the summed
spec duration. Each duration includes its hooks; the cleanup/setup sums above
show that most of these particular costs sit inside the body.

| Spec | Duration | Largest identified part |
|---|---:|---|
| Interruptible job superseded | 35.110s | 32.905s from watching build 2 to next marker |
| API SSE events | 34.103s | 30.081s after listing the completed build, inside SSE helper path |
| Build lifecycle SSE events | 34.082s | 30.089s in the same helper path |
| Task container limits | 33.105s | 31.923s watch span; task intentionally sleeps 30s |
| `check_every: never` | 30.927s | Intentional 30s negative observation window |
| Fan-out resource jobs | 30.108s | Source then two consumers executed serially by test |
| Fan-in resource jobs | 30.093s | Two upstream jobs executed serially, then merge |
| Get with passed constraint | 29.080s | Multiple real resource/build operations |
| Serial job without overlap | 28.049s | Watch spans 9.831s and 18.028s |
| Cross-job image build/use | 27.094s | 19.098s watch span in image-building job |

Across the report, 235 `fly watch` start-to-next-marker intervals total
**1,737.564 worker-seconds**; 77 `check-resource` intervals total 93.717s.
These identify the dominant test-side wait paths. They are not subprocess
timings: the interval can include application execution, polling, assertions
or asynchronous work after the command starts. In particular, assigning the
two 30s SSE gaps to the preceding `fly builds` invocation would be wrong;
source inspection places the wait in `getBuildEvents`. Scheduler admission,
pod scheduling, image resolution, command startup and output transfer need
application-side spans to divide the watch time further.
[All specs](../../output/ci-performance-20261102/behavior-research-specs.json),
[all step intervals](../../output/ci-performance-20261102/behavior-research-steps.json)

## CPU, allocations and blocking

All four merged profiles decode successfully. The CPU profile contains
**70.58 sampled CPU-seconds**. Its 2,419.21s duration is the sum of the two
worker process intervals, not a 40-minute suite. Cumulative allocations are
**2,868.35MB**, block delay **66,751.63 aggregate goroutine-seconds**, and
mutex delay **52.02ms**. Long-lived port-forward/SPDY goroutines and the main
suite wait account for much of the block total; it is not 18.5 hours of
recoverable elapsed time. `waitForBuildAndWatch` alone accounts for 1,716.29s
of aggregate block delay, consistent with the marker-based waiting evidence.
[CPU](../../output/ci-performance-20261102/behavior-research-cpu.txt),
[allocation](../../output/ci-performance-20261102/behavior-research-alloc.txt),
[block](../../output/ci-performance-20261102/behavior-research-block.txt),
[mutex](../../output/ci-performance-20261102/behavior-research-mutex.txt)

The main test-process CPU hotspot is **image loading: 55.39 CPU-seconds,
78.48% of samples**, including **53.12s in copying/gzip-compressing image
archives**. It also accounts for **2,372.92MB of allocation, 82.73%**. The
pinned testcontainers 0.41.0 implementation saves each image to a host tar,
wraps that file in another tar plus gzip in a growing `bytes.Buffer`, copies
it to the container and runs `ctr images import`. This is real CPU and
allocation work on the serialized startup path, not evidence that behavioral
assertions themselves are CPU-intensive.
[Focused image CPU](../../output/ci-performance-20261102/behavior-research-image-cpu.txt),
[focused image allocations](../../output/ci-performance-20261102/behavior-research-image-alloc.txt),
[pinned dependency](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/go.mod#L54),
[image loader](https://github.com/testcontainers/testcontainers-go/blob/v0.41.0/modules/k3s/k3s.go#L187),
[in-memory gzip wrapper](https://github.com/testcontainers/testcontainers-go/blob/v0.41.0/file.go#L115)

## Resource-accounting boundaries

The outer task's cgroup contains dockerd, its containerd and both Go test
workers. The complete process receipt reveals **four disjoint hierarchies**:

| Hierarchy | Processes visible at 04:23:08 UTC | Included work |
|---|---:|---|
| Outer `/kubepods.slice/.../cri-containerd-...scope` | 32 | Test workers, fly, Docker daemon, outer containerd and monitoring |
| `/docker/<id>/...`, two K3s containers | 27 | K3s, inner containerd and their shims |
| `/kubepods/.../pod...` | 112 | Nested web, PostgreSQL, artifact daemons, cluster services and workload pods |
| `/k3s` | 2 | Processes recorded as `exe`, resolved to `/bin/k3s` with the two K3s processes as parents |

Thus summing only outer task and Docker counters would still omit the actual
application and task processes. Any aggregate root counter also needs a scope
check before it can be attributed exclusively to this run. The separate
Docker/K3s sampler starts at
04:12:52 UTC. Cumulative counters from surviving cgroups include work before
sampling began; short-lived containers removed before discovery are absent,
and sampled current-memory peaks can miss excursions between samples.
[Process/cgroup receipt](../../output/ci-performance-20261102/behavioral-process-cgroups.txt),
[complete process receipt](../../output/ci-performance-20261102/behavioral-all-process-cgroups.txt),
[parsed hierarchy](../../output/ci-performance-20261102/behavior-research-process-hierarchy.json),
[nested samples](../../output/ci-performance-20261102/behavioral-nested-cgroups.log)

At 04:25:16 UTC, all 26 pod cgroup UIDs under `/kubepods` match the two test
clusters' API pod inventories exactly; the closing 04:31:29 snapshot also
matches all 18 live pod UIDs. These establish membership at two snapshots,
not ownership of the shared root's earlier cumulative counters.
Only the late observation-window counter deltas are attributed here. Likewise,
a zero ancestor `nr_throttled` does not prove CPU-limited descendant pods never
throttled; that requires each constrained leaf's counters.
[Ownership receipt](../../output/ci-performance-20261102/behavioral-inner-cgroup-ownership.txt),
[closing ownership receipt](../../output/ci-performance-20261102/behavioral-inner-cgroup-ownership-final.txt),
[membership comparison](../../output/ci-performance-20261102/behavior-research-inner-ownership.json)

| Accounting domain | Sample coverage | CPU measurement | Memory measurement |
|---|---|---:|---:|
| Outer task | 04:10:32.526–04:31:33.221; 1,260.695s | 389.059s delta; 200.576s in samples inside Ginkgo interval | 9.540GiB cgroup peak counter |
| Two Docker/K3s roots | 04:12:52.715–04:31:31.845; 1,119.130s | 244.273s sampled delta; 286.559s combined last lifetime counters for these new roots | 6.076GiB peak simultaneous sampled current memory |
| Nested pod root `/kubepods` | 04:24:48.451–04:31:33.695; 405.244s | 62.437s late-window delta | 1.233GiB peak sampled current memory in window |
| Extra `/k3s` root | 04:25:14.541–04:31:34.752; 380.212s | 162.910s late-window delta | 0.294GiB peak sampled current memory in window |

The outer sample ends about 4.3s before suite completion; the Docker samples
stop before the clusters are removed. These unequal windows and late root
sampling prevent a precise whole-suite CPU total. Shared-root cumulative CPU
and `memory.peak` are not attributed from zero because they may include prior
runs. Memory peaks in separate domains occur at different times and must not
be added as a simultaneous demand. All sampled ancestor throttling counters
are zero, subject to the leaf-counter limitation above.

Outer-task I/O counters increase by roughly 953.5MB read and 5.102GB written
over the sample window; these are device counters, not a measurement of
network traffic or tmpfs image-copy bytes. The observed current-memory and
copy profiles make streaming image import and archive cleanup useful memory
experiments without assuming a particular OOM or memory-pressure cause.
[Outer resources](../../output/ci-performance-20261102/behavioral-profile/resources.log),
[parsed Docker resources](../../output/ci-performance-20261102/behavior-research-nested-cgroups.json),
[late root resources](../../output/ci-performance-20261102/behavior-research-late-root-cgroups.json)

Go profiles measure the two test processes. They do not profile application
code running inside nested Concourse web, artifact-daemon or PostgreSQL
containers, nor the fly, Helm, kubectl and compiler child processes. A low
test-process CPU count cannot establish that the whole suite is CPU-idle.
Block and mutex profiles sum waits across goroutines; merged profile duration
adds the two process intervals and is not suite wall time.

## Source-verified optimization candidates

These candidates preserve assertions and target measured work. None has a
measured A/B speedup yet; overlap and shifting bottlenecks prevent adding their
nominal durations as an estimated wall-time saving.

### Finish SSE reads when the protocol says they are complete

`getBuildEvents` has a 30s context and reads until error or a size threshold.
It only parses event JSON after that loop and does not recognize the SSE
`event: end` marker. The server deliberately writes `end` and waits for the
client context to close. Consequently two tests of already-completed builds
can sit until their client deadline despite receiving all events. The
existing Go event-stream client already treats that marker as end-of-stream.
[Helper](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/api_helpers_test.go#L101),
[server completion behavior](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/api/buildserver/eventhandler.go#L106),
[existing protocol parser](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/go-concourse/concourse/eventstream/stream.go#L25)

Parse incrementally through `end`, retain authenticated HTTP status and event
content assertions, require the completion marker, and keep a bounded failure
timeout. Keep cancellation/disconnect behavior covered separately. This makes
successful completion explicit rather than reducing the failure timeout or
discarding the streaming test.
[API SSE assertion](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/api_surface_test.go#L25),
[build-lifecycle SSE assertion](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/build_lifecycle_test.go#L275)

The fresh evidence puts the two affected phases at 30.081s and 30.089s,
with one spec on each worker. Their 60.170 aggregate seconds are not a
guaranteed 60.170s wall saving.

### Replace readiness sleeps with observed readiness

The shared hijack fixture sleeps 10s for each of two specs; the containers
spec sleeps 5s. These are 25 aggregate worker-seconds of fixed waiting, not
automatically 25s of suite wall time. Poll for the target pod/step and a
runtime-generated command sentinel, then execute the operation and assert
its result. The hijack specs currently inspect output only if exit status is
zero; a nonzero exit can pass without proving hijack worked. The containers
spec asserts command success without identifying its target container.
Strengthen those result checks while removing the timing proxy.
[Hijack fixture and assertions](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/fly_cli_test.go#L130),
[containers wait](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/fly_cli_test.go#L172)

The measured hijack specs take 11.261s and 11.256s, with 10.112s and 10.105s
in their setup hooks; containers takes 5.183s. This confirms the fixed waits
are paid on a normal run rather than merely being maximum failure budgets.

The resilience tests show the correct sentinel discipline: pod phase
`Running` does not prove an exec command started, and a literal string in
fly's echoed command can match too early. Keep the runtime-assembled sentinel,
pod deletion, nonzero build result and reason assertions in those tests.
Their long sleep keeps the target alive until cancellation; it is not
necessarily time paid in a passing run.
[Pod-resilience readiness and destruction](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/pod_resilience_test.go#L18)

### Reduce image-loading work before changing setup concurrency

Worker setup holds one host file lock across K3s creation and loading the
application plus seven dependency images. Source comments document previous
containerd content-lock failures when these operations overlapped. Host image
pulling already happens once before the workers start creating clusters.
Do not remove that protection based solely on the visible wait. The profile
now identifies an earlier target: avoid re-gzipping each local image archive
into memory before import. Evaluate a streaming, uncompressed local transfer
and reuse immutable exported archives across both clusters. The pinned loader
also removes its host temporary tar but leaves the copied `/tmp` tar inside
K3s; delete it after successful import. Preserve successful image import,
exact image identity and both isolated clusters. Measure export, transfer,
import/unpack and memory separately before changing setup concurrency.
[Setup lock](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/cluster_lifecycle_test.go#L73),
[image loading](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/cluster_lifecycle_test.go#L202),
[suite setup sequence](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/behavioral_suite_test.go#L103)

### Distinguish observation windows from idle task lifetimes

The 30s `check_every: never` assertion intentionally observes absence of
automatic checks before proving manual checks work. Shrinking it without
proving a scheduler/check cycle occurred would weaken the behavioral contract.
By contrast, a task that sleeps 30s merely to keep its pod available for
resource-limit inspection can use an explicit release signal after inspection,
then still finish successfully and emit its expected output.
[Negative observation window](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/resource_checking_test.go#L85),
[resource-limit inspection and successful completion](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/task_step_test.go#L595)

### Overlap independent branches within fan-in/fan-out fixtures

The fan-out test runs consumer A to completion before it triggers consumer B.
The fan-in test likewise finishes upstream A before triggering upstream B.
After establishing the shared source version, trigger both independent
branches before waiting for their results, then retain both successful output
assertions and the downstream passed-constraint check. This preserves every
build and assertion while removing serialization the scenarios do not require.
Measure contention and the overall two-worker schedule; the saving is not the
sum of both branches' durations.
[Fan-out execution sequence](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/custom_resource_types_test.go#L681),
[fan-in execution sequence](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/k8s_behavioral/custom_resource_types_test.go#L770)

## Reproducibility and remaining measurements

The offline [parser](../../output/ci-performance-20261102/behavior-research-analyze.py)
reads the retained Ginkgo report, phase receipts and resource samples. It
writes only `behavior-research-*` outputs. Intervals between command/`By`
markers include command execution, assertions and polling; asynchronous
operations can overlap them. They are client-phase attribution, not exact
subprocess timings or application-side critical spans.

Explicit child-process start/end receipts and application request/build
traces remain useful. `FlyCli.Run` has an optional tracing wrapper, but
`FlyCli.Start` is used directly by most watch operations and the behavioral
suite does not initialize the optional test tracing provider. Exported
application traces and child CPU profiles were not collected in this run.
[Fly wrapper](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/fly.go#L51),
[process-start markers](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/topgun/exec.go#L17),
[optional tracing initialization](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/testhelpers/otel/otel.go#L60)
