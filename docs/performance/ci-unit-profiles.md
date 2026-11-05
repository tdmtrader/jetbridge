# Unit-suite profiles: measured work and remaining attribution

This report analyzes retained evidence from one-off CI build **1142547**, using
core `1d0e377594898bff615f1c222f9977e0ea094acc` with instrumentation added to the
archived source. The test inventory and concurrency policy were unchanged.
The unit command passed in **204.319s**, followed by fly integration in **9.379s**.
The CI build itself errored during artifact export after the commands passed;
the report uses the archive recovered directly from the task. It does not call
that build green. Initial attempt 1142519 is excluded because its profiling
output crossed filesystems and Ginkgo's binary rename failed.
[Instrumentation](../../output/ci-performance-20261102/unit-instrumentation.diff),
[phase receipts](../../output/ci-performance-20261102/unit-profile/phases.txt),
[verdict](../../output/ci-performance-20261102/unit-profile/verdict.txt),
[export diagnostics](../../output/ci-performance-20261102/unit-export-diagnostics.log)

The strongest follow-up finding comes from successful focused build
**1142575**: `atc/integration` spends **23.67 CPU-seconds generating new 4096-bit
ID-token signing keys** and **6.85 CPU-seconds hashing fixture passwords**.
Its 29 spec bodies take only 5.193s; common teardown takes 18.073s. Reusing valid
test signing keys and prehashed fixture passwords deserves the first controlled
experiment, alongside giving this package an earlier execution opportunity
under a shared compilation budget. It is the full unit run's last package:
it starts **159.612s after the unit command**, runs 44.342s, and finishes 0.365s
before the command ends. The focused run below identifies its work; it does
not replace that original timeline or establish a measured speedup.

## Evidence coverage and test outcomes

The eight heavy suites passed **3,488 Ginkgo specs**. Fly passed **593 of 594**,
with its permission test skipped because root can write the target. The
foreground `go test -json -count=1` stream records 104 passing packages and
41 package skips reporting no test files; it has 2,160 passing Go test/subtest
records and five skipped test/subtest records. Those records include Ginkgo
entry points and nested subtests and therefore are not a count of distinct
behavioral assertions. There are no failed test records and no `(cached)`
package results in this stream.

The five explicit foreground skips are four permission cases that require a
non-root process and the Hangar integration ancestry check, which cannot run
in a source archive without `.git`. The heavy binaries' ordinary Go tests
have no per-test JSON inventory here, so this evidence cannot claim zero
additional skips in those binaries.
[Outcomes and exact skip records](../../output/ci-performance-20261102/unit-research-rest.json),
[fly report](../../output/ci-performance-20261102/unit-profile/fly-specs.json)

The task declares no persistent task caches. `-count=1` disables foreground
test-result reuse, and Ginkgo directly executes compiled binaries. These facts
do **not** establish a completely cold Go build/module cache: the runner image
and earlier work inside this same task can supply cached inputs. The log
actually downloads three module versions. This run must not be described as
either a controlled cold-cache or warm-cache benchmark.
[Task configuration](../../output/ci-performance-20261102/unit-task.yml),
[unit log](../../output/ci-performance-20261102/unit-profile/unit.log)

`SpecReports` contains suite hooks as well as specs. Each heavy report has
eight hook records in addition to its `It` records; fly has 54 hooks for 27
processes. Counting the JSON array length would incorrectly turn DB's 1,315
specs into 1,323. All counts below distinguish hooks from specs.

## Wall time: execution begins long after the command

All timestamps below are UTC on 2026-11-03; the unit command starts at
03:56:01.932 and ends at 03:59:26.251. Commands for the eight heavy suites run
concurrently with the foreground package set. Their durations cannot be added
to make a wall-time breakdown.

| Heavy suite | Passing specs | Ginkgo start | Ginkgo finish | Ginkgo interval | CLI elapsed | CLI minus Ginkgo |
|---|---:|---|---|---:|---:|---:|
| DB | 1,315 | 03:57:37.653 | 03:59:12.040 | 94.387s | 183.540s | 89.153s |
| API | 752 | 03:57:36.032 | 03:58:19.555 | 43.523s | 130.356s | 86.833s |
| Migration | 213 | 03:57:23.050 | 03:57:49.217 | 26.167s | 99.376s | 73.209s |
| Exec | 617 | 03:57:32.595 | 03:57:55.566 | 22.971s | 106.032s | 83.061s |
| Algorithm | 98 | 03:57:23.184 | 03:58:06.904 | 43.721s | 116.960s | 73.239s |
| Engine | 191 | 03:57:32.071 | 03:57:43.296 | 11.225s | 93.472s | 82.247s |
| Runs | 117 | 03:57:23.643 | 03:57:34.549 | 10.906s | 84.712s | 73.806s |
| Runtime | 185 | 03:58:10.228 | 03:58:36.570 | 26.342s | 146.976s | 120.633s |

The Ginkgo interval includes its suite hooks and spec execution. The difference
includes compilation/linking, startup, native Go tests, profile/report handling
and possible scheduling delays. It is not a compilation measurement. Logs are
buffered and printed after the foreground command, so their output order is
not process execution order.
[Suite and worker reconstruction](../../output/ci-performance-20261102/unit-research-suites.json)

The foreground tail is directly timestamped:

| Package | Start relative to unit command | Package execution | Finish UTC |
|---|---:|---:|---|
| `atc/integration` | 159.612s | 44.342s | 03:59:25.886 |
| `deploy/chart/tests` | 166.478s | 29.323s | 03:59:17.732 |
| `cmd/artifact-daemon` | 165.849s | 23.907s | 03:59:11.687 |
| `atc/gc` | 155.284s | 33.642s | 03:59:10.858 |
| `atc/hangaroutput` | 159.212s | 29.020s | 03:59:10.164 |

DB's Ginkgo interval ends **13.846s before integration completes**. This is an
observed interval, not a proven 13.846s saving: DB's CLI still has final profile
work, and changing launch order may change contention. Integration runs 29
Ginkgo specs under the single foreground Go test process. Its fixture starts
an ATC, generates an RSA signing key, clones a template database, waits for
readiness, then stops the ATC and drops the database for each spec. The current
stream times `TestIntegration` as a whole. The separate focused run below
adds hook timing and CPU attribution for those operations.
[Fixture setup](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L35),
[readiness and teardown](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L79)

Fly follows the unit command: 9.379s command, 8.856s Ginkgo CLI, 5.910s Ginkgo
interval. Its 27 workers each execute 19–24 specs; their final spec ends are
within 0.122s. Synchronized startup is about 2.8s per worker, much of it shared
waiting rather than 27 independent setup charges. More workers would not
remove its compile/startup floor.

## Resource evidence: excessive compilation fan-out deserves a controlled trial

The task reports 28 CPUs, no CPU quota and no memory limit, despite requesting
six CPUs. Across 103 resource samples spanning 213.781s, its cgroup consumes
**3,687.923 CPU-seconds**: 1,974.349 user and 1,713.574 system, approximately
17.25 CPUs on average. Peak sampled cgroup memory is **9.997GiB**; sampled
throttled time stays zero. The highest observed counts are **187 compiler
processes** and **11 linkers**. These are counts observed at roughly two-second
intervals, not exact maxima or a CPU profile of the compiler.
[Environment](../../output/ci-performance-20261102/unit-profile/environment.txt),
[resource reconstruction](../../output/ci-performance-20261102/unit-research-resources.json)

Compiler processes appear from +2.006s and continue through the fly phase;
linkers first appear at +8.028s. The monitor identifies command names, not
full arguments/PIDs, so it cannot assign a compiler/linker to a particular
package or separate queued compile time from active work. `/proc/pressure/*`
is host scope; `ps %CPU` is a lifetime average. Neither is a task-level,
instantaneous saturation measurement. RSS sums also double-count shared pages;
the peak above comes from `memory.current` instead.

The runner launches eight independent Ginkgo commands plus `go test`, each
with its own default Go compilation concurrency. A trial should coordinate
their build budget, preserve all package execution, and compare full command
wall time and CPU-seconds. Start with explicit per-command `-p`/Go build
parallelism and prioritize the late critical package. Avoid concluding that
raising Ginkgo's process count will help when startup/build work dominates.
[Current dispatch](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/hack/unit-tests.sh#L110)

## Native tests inside Ginkgo: real repeated waits are invisible in its JSON

Four runtime test processes are already visible at +96.807s, but the runtime
Ginkgo suite starts at +128.296s. Thus at least one runtime process exists
**31.49s before Ginkgo starts**; that interval cannot all be compilation.
Package initialization, ordinary Go tests, and process coordination precede
the Ginkgo entry point. The merged CPU profile starts at 03:57:38, consistent
with that observation. The precise per-native-test elapsed distribution was
not captured.

There is a concrete source explanation for a substantial part of that work.
`DaemonSetVolume.fetchOnce` makes three transport attempts with a two-second
sleep between them. Four ordinary tests deliberately exhaust the producer
transport: `TestVT06_DaemonSetVolume_StreamOut_GivesUpAfter3Failures`,
`TestDaemonSetMode_StreamOut_FallsBackToPeer_AfterProducerDeath`,
`TestDaemonSetVolume_StreamOut_FallsBackToPeer_OnConnectionRefused`, and
`TestDaemonSetVolume_StreamOut_FallsBack_PreservesNotFoundOnProbeMiss`.
That is **16 seconds of prescribed sleeps per binary**, or **64 aggregate
sleep-seconds across four binaries**, before their other work. The successful
retry case exercises the same backoff too. These are source-derived waits,
not a measured per-test attribution, and the four binaries overlap.
[Retry loop](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/volume_daemonset.go#L333),
[give-up test](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/behavioral_volume_test.go#L187),
[producer-death test](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/daemonset_peer_fallback_restored_test.go#L37),
[fallback tests](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/volume_daemonset_restored_test.go#L96)

Introduce a controlled retry sleeper/clock in these tests, retaining assertions
for exact attempts, payload, refusal behavior, peer discovery and fallback.
Separately, inventory ordinary Go tests and run them once, while limiting the
parallel Ginkgo binary to its actual Go entry-point test. Require nonempty,
disjoint inventories so nothing silently disappears. Keep the process-level
temporary-root guard in `TestMain`; it is required for native-only execution.
These changes preserve coverage while avoiding four copies of native work.
[Current duplication](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/hack/unit-tests.sh#L90),
[process cleanup guard](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/tempdir_main_test.go#L28)

## Named Ginkgo costs and worker balance

| Spec | Measured elapsed | Interpretation |
|---|---:|---|
| Algorithm: relint rc disabled stemcell high cpu regression | 35.754s | Dominates that suite's final worker |
| Algorithm: hush-house 6.0 upgrade unsatisfiable job | 14.046s | Includes imported fixture preparation |
| API: buffered wait error coincides with idle timeout | 8.156s | 20 required successful repetitions, not flaky retries |
| Algorithm: concourse deploy high cpu regression | 7.520s | Includes imported fixture preparation |
| Runtime: detached command survives exec-session hangup | 7.281s | Real process/signal scenario |
| Algorithm: relint rc high cpu regression | 7.149s | Includes imported fixture preparation |
| Runtime: takes over still-running command | 5.514s | Supervisor/process lifecycle |
| DB: concurrent pipeline setting and checks/gets do not deadlock | 5.271s | Concurrency assertion |

DB workers execute 318/331/337/329 specs and finish within 0.152s. Their
aggregate spec durations are 91.47–91.63s per worker: this suite is well balanced.
Algorithm workers execute 25/25/24/24 specs, but one finishes **28.34s after**
the first worker because its final regression takes 35.754s. More processes
cannot split that one spec. The algorithm suite nevertheless ends more than
a minute before the unit command, so reducing it alone has no demonstrated
direct wall-time saving in this run.

API's last worker trails the others by about 2.9s, with the 20-repeat race
regression at its end. Its source sleeps twice for 100ms per repetition,
providing four seconds of prescribed waits across the 20 attempts. Replacing
the sleeps with synchronization that proves the error is buffered and expiry
is offered could preserve or strengthen the concurrency assertion. Simply
reducing `MustPassRepeatedly(20)` would weaken its coverage.
[Race regression](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/api/containers_test.go#L1295)

Runtime workers finish within 3.128s. Some reported `SynchronizedAfterSuite`
time on process one is waiting for other workers, so it cannot all be called
database teardown: API records 3.163s and runtime 3.576s there while peers
record effectively zero. Exact spec names, source locations, attempts and
per-worker starts/ends are retained in the reconstruction JSON.

## CPU, allocation, block and mutex profiles

All eight heavy suites have all four profile types, **32 profiles total**.
Ginkgo merges the four workers' profiles. Consequently, pprof's `Duration`
is summed process duration, not suite wall time: runtime shows 227.74s even
though its Ginkgo interval is 26.342s. CPU samples are sampled CPU-seconds,
allocation space is cumulative allocation rather than live heap, and block
delay sums concurrent goroutine waits. None should be added to the wall-time
table. Go test profiling does not include compiler/linker, PostgreSQL, Helm,
shell or daemon child-process CPU.
[Ginkgo profile merge implementation](https://github.com/onsi/ginkgo/blob/v2.27.4/ginkgo/internal/profiles_and_reports.go#L195)

| Suite | Sampled test-process CPU | Allocated space, across workers |
|---|---:|---:|
| DB | 51.61s | 3.28GB |
| API | 23.23s | 1.27GB |
| Algorithm | 11.84s | 1,686.58MB |
| Exec | 9.09s | 640.27MB |
| Runs | 4.67s | 280.94MB |
| Runtime | 4.17s | 605.91MB |
| Engine | 3.85s | 258.98MB |
| Migration | 3.60s | 198.90MB |

The total sampled CPU in these test processes is 112.06s. It covers only a
small part of the task's 3,687.923 cgroup CPU-seconds; the substantial omitted
work includes compilation and subprocesses. It is not valid to infer from
low test-process CPU that the whole task is idle.

Algorithm's focused CPU profile attributes 4.20s cumulatively to fixture import,
including 3.97s in JSON decoding, versus 2.29s in `Algorithm.Compute`. Its
allocation profile has 576.10MB in slice growth and 384.35MB in decoder refill;
its block profile attributes 45.95 aggregate seconds to fixture import/CopyFrom.
This supports investigating fixture decoding/import before assuming the
35.754s regression is all algorithm compute. Keep the complete regression
data and assertions; separately time decode, COPY and Compute, then consider
per-worker reusable imported template databases or cached immutable decoded
fixtures. Their setup cost and isolation still need measurement.
[Focused CPU](../../output/ci-performance-20261102/unit-research-algorithm-cpu.txt),
[focused block](../../output/ci-performance-20261102/unit-research-algorithm-block.txt),
[allocation profile](../../output/ci-performance-20261102/profile-atc-scheduler-algorithm-alloc.txt),
[fixture implementation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/scheduler/algorithm/table_helpers_test.go#L149)

DB CPU samples concentrate in syscalls/futexes; they do not profile the
PostgreSQL server. Its block profile reports about 11 aggregate hours, mostly
idle pool health checks and background goroutines. That number is not an
11-hour performance defect or obtainable saving. Runtime's block profile
likewise sums 1,377.76s of goroutine waits. `time.Sleep` is not a channel/mutex
block event, so the retry sleeps above are not measured by those block totals.
Algorithm has 13.94s sampled mutex delay, almost all attributed to pgconn's
message signal, while other suites range from about 34–283ms. Interpretation
requires call stacks and worker overlap rather than treating that total as
wall time.
[DB CPU](../../output/ci-performance-20261102/profile-atc-db-cpu.txt),
[DB block](../../output/ci-performance-20261102/profile-atc-db-block.txt),
[runtime native CPU](../../output/ci-performance-20261102/unit-research-runtime-native-cpu.txt),
[runtime native block](../../output/ci-performance-20261102/unit-research-runtime-native-block.txt)

## Scheduling and fixed-wait changes to evaluate without removing assertions

1. **Start the final integration work earlier under a coordinated build budget.**
   Preserve all 29 specs and its real ATC/database isolation. Measure compilation
   readiness and fixture phases, and test a dedicated early command removed
   from the remainder inventory. Its 44.342s execution and 159.612s late start
   are measured; the obtainable saving is not yet measured.
2. **Make peer timeout tests deterministic.** Three artifact-daemon tests each
   take 10.00s and already run in parallel, finishing together. The constructor
   hard-codes a 10s probe client versus a 3m fetch client. The concurrency test
   uses three unreachable addresses and actually tests concurrent misses, not
   a successful first hit. Use controlled transports/servers to observe all
   requests entering before release, return one success, and assert cancellation
   of losers. Keep a separate assertion on production timeout configuration
   and test cancellation with a short injected clock/budget. Endpoint discovery
   should assert selected addresses directly rather than waiting on fake IPs.
   Savings are bounded by their shared 10s package tail, not 30s, and that tail
   precedes integration's finish in this run.
   [Timeout configuration](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/peers.go#L115),
   [timeout and concurrency tests](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/peers_test.go#L161),
   [discovery test](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/behavioral_test.go#L932)
3. **Reuse correctly identified chart-validation binaries.**
   `TestChartRendersOnlyFlagsTheBinaryAccepts` consumes 10.00s of the chart's
   29.323s package. It already builds all required binaries in one `go build`
   invocation, then checks each template against real `--help`. Do not replace
   that guard with static flag names. A shared task fixture could reuse binaries
   built from the exact tested source with a provenance check and local-build
   fallback; measure build versus help time first. This saves redundant work,
   but the current chart package also finishes 8.154s before integration.
   [Build and validation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/chart/tests/flag_drift_test.go#L134)

## Separate focused profiles of the five final foreground packages

Successful build **1142575** runs the same core revision with five ordinary
`go test` commands **sequentially**, retaining test JSON and CPU, allocation,
block and mutex profiles for each package, plus Ginkgo reports for integration
and GC. All five commands pass with no cached test results. The environment is
Go 1.25.14 on Linux amd64, PostgreSQL 17.11, 28 visible CPUs, no CPU quota and
no cgroup memory limit. This is an attribution run, not the normal unit-job
schedule or an A/B experiment.
[Environment](../../output/ci-performance-20261102/tail-profile/environment.txt),
[phases](../../output/ci-performance-20261102/tail-profile/phases.txt),
[verdict](../../output/ci-performance-20261102/tail-profile/verdict.txt),
[parsed results](../../output/ci-performance-20261102/unit-research-tail-summary.json)

| Package, in command order | Command elapsed | Package execution | Before package starts | After package ends | Outcome |
|---|---:|---:|---:|---:|---|
| `atc/integration` | 87.672s | 38.906s | 48.136s | 0.630s | 29 Ginkgo specs pass |
| `atc/gc` | 23.970s | 21.863s | 2.031s | 0.076s | 135 Ginkgo specs pass |
| `atc/hangaroutput` | 30.896s | 23.153s | 7.531s | 0.213s | 53 Go test/subtest records pass |
| `deploy/chart/tests` | 25.845s | 23.236s | 2.505s | 0.104s | 274 Go test/subtest records pass |
| `cmd/artifact-daemon` | 24.963s | 20.263s | 4.550s | 0.150s | 516 Go test/subtest records pass; 2 root-permission skips |

The five commands span **193.436s**, including small gaps. Integration pays
the first command's cold-start work; subsequent commands share build outputs
warmed earlier in the task. Neither the pre-package interval nor its
48.766s total outside-package interval isolates compiler time: Go startup,
dependency resolution, compilation, vet, linking and output/profile handling
are not separately traced. The image's initial cache state remains uncontrolled.
These sequential command times must not be added to or substituted for the
parallel unit job's 204.319s makespan.

The sampler covers 192.370s through the last sample before command completion:
**725.991 CPU-seconds** (408.093 user, 317.897 system), a **3.918GiB** memory
peak and zero CPU throttling. That sample ends about 1.068s before the last
command. A final 30s evidence-retention sleep is excluded. These totals include
compilers and child processes; the profiles below cover the Go test process
only and exclude PostgreSQL, Helm and spawned daemon/compiler processes.
[Raw resource samples](../../output/ci-performance-20261102/tail-profile/resources.log)

All 20 profiles were decoded successfully. Each linked value opens its text
profile. Allocation is cumulative `alloc_space` in pprof's MB units, not peak
live memory; block and mutex totals sum concurrent goroutine delays and are
not package wall time. These are individual test-process profiles, unlike the
merged four-worker heavy-suite profiles above.

| Package | Sampled CPU | Cumulative allocation | Aggregate block delay | Aggregate mutex delay |
|---|---:|---:|---:|---:|
| Integration | [37.81s](../../output/ci-performance-20261102/unit-research-tail-atc-integration-cpu.txt) | [300.43MB](../../output/ci-performance-20261102/unit-research-tail-atc-integration-alloc.txt) | [2,375.34s](../../output/ci-performance-20261102/unit-research-tail-atc-integration-block.txt) | [2.74s](../../output/ci-performance-20261102/unit-research-tail-atc-integration-mutex.txt) |
| GC | [2.85s](../../output/ci-performance-20261102/unit-research-tail-atc-gc-cpu.txt) | [185.49MB](../../output/ci-performance-20261102/unit-research-tail-atc-gc-alloc.txt) | [318.30s](../../output/ci-performance-20261102/unit-research-tail-atc-gc-block.txt) | [16.07ms](../../output/ci-performance-20261102/unit-research-tail-atc-gc-mutex.txt) |
| Hangar output | [1.22s](../../output/ci-performance-20261102/unit-research-tail-atc-hangaroutput-cpu.txt) | [92.88MB](../../output/ci-performance-20261102/unit-research-tail-atc-hangaroutput-alloc.txt) | [141.39s](../../output/ci-performance-20261102/unit-research-tail-atc-hangaroutput-block.txt) | [4.39ms](../../output/ci-performance-20261102/unit-research-tail-atc-hangaroutput-mutex.txt) |
| Chart | [0.98s](../../output/ci-performance-20261102/unit-research-tail-deploy-chart-tests-cpu.txt) | [289.63MB](../../output/ci-performance-20261102/unit-research-tail-deploy-chart-tests-alloc.txt) | [29.68s](../../output/ci-performance-20261102/unit-research-tail-deploy-chart-tests-block.txt) | [5.216ms](../../output/ci-performance-20261102/unit-research-tail-deploy-chart-tests-mutex.txt) |
| Artifact daemon | [11.33s](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-cpu.txt) | [2,893.76MB](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-alloc.txt) | [672.57s](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-block.txt) | [299.88s](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-mutex.txt) |

### Integration: repeated cryptographic fixture work dominates

The Ginkgo report's `Node (End)` events time each hook and spec body. Their
sum is 38.6807s against a 38.6853s suite interval, leaving only 4.6ms of
bookkeeping. Because this is one Ginkgo process, these node durations provide
an almost complete sequential wall-time partition:

| Phase | Time | Share of Ginkgo interval |
|---|---:|---:|
| Suite setup and cleanup | 0.913s | 2.4% |
| `BeforeEach` | 1.737s | 4.5% |
| `JustBeforeEach`, including ATC startup and scenario setup | 12.764s | 33.0% |
| Actual `It` bodies | 5.193s | 13.4% |
| `AfterEach`, almost entirely common ATC shutdown and database drop | 18.073s | 46.7% |

The common startup hook accounts for 9.392s of `JustBeforeEach`; RBAC setup
adds 1.346s across 12 specs. The common teardown accounts for 18.0728s across
29 specs. The longest named spec, owner access to pipelines, takes 2.778s
including hooks, of which 2.228s is teardown. Ranking spec names alone would
therefore assign shared fixture cost to unrelated authorization assertions.
[Exact node and spec timings](../../output/ci-performance-20261102/unit-research-tail-summary.json),
[raw Ginkgo report](../../output/ci-performance-20261102/tail-profile/atc-integration/specs.json),
[common hooks](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L79)

CPU profiling distinguishes two separate RSA keys. The fixture explicitly
generates a 1024-bit session key, but the expensive path is the production
ID-token lifecycler's **4096-bit RSA key generation: 23.67s, 62.60% of sampled
CPU**. Each spec clones a database without those keys; captured logs contain
29 missing-RSA-key messages. The production lifecycle runs on each ATC start
and reuses a fresh stored key when one exists.
[Focused key CPU profile](../../output/ci-performance-20261102/unit-research-tail-integration-key-cpu.txt),
[fixture session key and database clone](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L67),
[existing-key reuse](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/creds/idtoken/lifecycle.go#L49),
[4096-bit generation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/creds/idtoken/lifecycle.go#L113)

The teardown cost is consistent with waiting for ongoing key generation:
the fixture signals the ATC and waits for exit; the component runner performs
its startup run synchronously; the signing-key lifecycle ignores its context.
Thus cancellation does not interrupt key generation already in progress.
This is a source-supported explanation, not a direct attribution of all
18.073s of teardown to RSA: the hook also waits for other shutdown work and
drops the database, and no per-key start/end trace was collected.
[Fixture shutdown](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L93),
[component startup and cancellation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/component/runner.go#L45),
[lifecycle context handling](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/creds/idtoken/lifecycle.go#L29)

Bcrypt contributes another **10.36 CPU-seconds**: **6.85s** generating hashes
for the five plaintext fixture users and **3.51s** comparing passwords during
real login. Dex already accepts prehashed bcrypt passwords. The expensive
hash-generation fixture can be reused while preserving the real authentication
path and password verification.
[Authentication CPU profile](../../output/ci-performance-20261102/unit-research-tail-integration-auth-cpu.txt),
[fixture users](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/integration/integration_suite_test.go#L49),
[plaintext versus prehashed handling](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/skymarshal/dexserver/dexserver.go#L236)

The concrete experiment is to generate valid production-strength RSA and EC
test keys once per suite and seed them into each fresh database, plus supply
prehashed versions of the same fixture passwords at the existing bcrypt cost.
Keep isolated database clones, all real ATC starts, all authorization and
restart assertions, real password comparison, and explicit coverage of startup
with no signing keys. Dedicated lifecycle tests already cover absent keys,
rotation and old-key removal; retain those paths. Seeding through the signing
key factory avoids introducing a global override into concurrent suites.
[Key insertion API](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/db/signing_key_factory.go#L35),
[existing lifecycle tests](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/creds/idtoken/lifecycle_test.go#L31)

The targeted **30.52 CPU-seconds** are not a promised 30.52s unit-job saving.
In the original concurrent run the next latest foreground package, chart,
finishes only **8.154s before integration**. Holding every other observed
finish fixed, at most that much of integration's visible tail is exposed
before another package becomes the bottleneck. Compilation scheduling and
shared contention may change too; the complete job must be remeasured.

### GC: fixture construction is the measured cost

GC's 135 passing specs take 21.663s of Ginkgo wall time but only 2.85 sampled
Go CPU-seconds. Node events account for 21.648s: **14.643s `BeforeEach`**,
**3.173s deferred cleanup**, **2.806s spec bodies**, 0.091s `JustBeforeEach`
and 0.936s suite setup/cleanup. The shared fixture alone takes **11.714s**
across 135 calls and database-drop cleanup takes **3.154s**. It clones a
template database, opens connections and builds a baseline team, pipeline,
job, build and resource state. PostgreSQL server work is outside this profile.
[Ginkgo report](../../output/ci-performance-20261102/tail-profile/atc-gc/specs.json),
[shared fixture](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/gc/gc_suite_test.go#L65)

The useful experiment is to construct only the fixture state each collector
needs, or put common immutable baseline rows into the already-existing
template while retaining a fresh clone per spec. Measure PostgreSQL clone,
connection and insert work before considering connection or database reuse,
since those changes can affect lock and state isolation. Volume grace-period
tests already backdate records with SQL; they do not wait out the grace period.
Reducing test assertions or timeout ceilings would miss this measured cost.
[Volume fixture](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/gc/volume_collector_test.go#L31),
[existing grace-period backdating](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/gc/volume_collector_test.go#L108)

### Hangar output: three deliberate waits are concrete targets

Hangar output takes 23.153s with 1.22 sampled Go CPU-seconds. Its longest
top-level tests are `TestA2ASealWaitsForEveryWriterOfThePod` (5.22s),
`TestASealSlowerThanTheClientTimeoutCompletesByPolling` (3.23s),
`TestACaptureIsOneRowFromHoldToRelease` (2.75s) and
`TestAPublishingRowPastItsDeadlineFails` (2.69s). The first, second and fourth
contain explicit 5s, 3s and 2.5s sleeps respectively. The lower Go CPU figure
does not include the real artifact-daemon child processes.
[Per-test events](../../output/ci-performance-20261102/tail-profile/atc-hangaroutput/tests.jsonl)

Preserve the late-writer test by gating writer completion on an observed seal
request, asserting no seal completion before the marker, then releasing the
writer and checking its late bytes. Preserve the polling test's real HTTP
timeout and retry assertion with a short injected client timeout and a writer
released after the timeout/retry is observed. This replaces elapsed-time
proxies with the ordering those tests intend to guarantee; arbitrary shorter
sleeps alone would make them flaky.
[Late writer and 5s sleep](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/hangaroutput/capture_test.go#L573),
[client timeout and 3s sleep](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/hangaroutput/capture_test.go#L850)

The 2.5s publishing-expiry wait needs a more deliberate test seam.
`Coordinator.Now` alone cannot remove it: the repository query compares the
deadline against PostgreSQL `now()`. Updating an existing row's deadline is
also rejected by the capture table's immutable-field guard. A harness that
creates a valid historical fixture, or a repository clock seam whose default
still uses the database clock, could exercise the same SQL predicate without
sleeping. Until that seam is designed, retain the wait and both expired and
not-yet-expired boundary coverage; do not bypass the database guard.
[Expiry test](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/hangaroutput/capture_test.go#L915),
[database clock predicate](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/db/hangar_capture.go#L362),
[immutable capture fields](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/db/migration/migrations/1789793152_add_hangar_capture_rows.up.sql#L64)

### Chart: subprocess work needs explicit attribution

The chart package takes 23.236s, while the parent Go test process samples only
0.98 CPU-seconds. The flag-drift guard takes 7.22s here, versus 10.00s in the
full concurrent run; this is consistent with different contention and warmed
build inputs, not evidence of a code speedup. Helm rendering and binary builds
run as child processes and are outside these CPU profiles. Existing render
helpers invoke Helm repeatedly, and the package cumulatively allocates
289.63MB parsing and checking the results.
[Per-test events](../../output/ci-performance-20261102/tail-profile/deploy-chart-tests/tests.jsonl),
[render helper](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/chart/tests/render_test.go#L20),
[second render helper](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/deploy/chart/tests/securitycontext_test.go#L73)

First instrument child build, `--help`, Helm invocation and YAML decoding
separately. Exact-source binary reuse remains the clearest shared fixture
opportunity. Share identical rendered fixtures only where the assertions do
not require independent certificate/key generation, and include all release,
namespace, values and flags in fixture identity. Unqualified output caching
could conceal tests of generated TLS secrets or distinct values. Bounded
render concurrency is an experiment after measuring the shared build budget.

### Artifact daemon: overlapping network waits and persistent-map work

The package completes in 20.263s. Its three 10.00s peer tests finish together;
the registry stress test (7.31s) and failed-fetch cleanup test (9.02s) overlap
them. Retry/refusal cases commonly take another 3.00–3.01s each, also in
parallel. Their elapsed times cannot be added to estimate the package saving.
Deterministic peer and retry tests remain the first fixed-wait experiment.
[Per-test events](../../output/ci-performance-20261102/tail-profile/cmd-artifact-daemon/tests.jsonl)

The new profiles reveal substantial real persistence work underneath those
waits: `Registry.persistAliases` accounts for **6.68 CPU-seconds** (58.96%)
and **2,627.06MB** of allocation (90.78%). `MarshalIndent` contributes
2.63 CPU-seconds and 1,859.53MB within that call tree. The mutex profile's
299.88 aggregate seconds mostly come from registration/persistence in the
concurrent registry test (**284.02s**, 94.71%). These are many goroutines
contending during a 7.31s test, not a 299.88s serial delay.
[Focused CPU](../../output/ci-performance-20261102/unit-research-tail-artifact-registry-cpu.txt),
[allocation profile](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-alloc.txt),
[mutex profile](../../output/ci-performance-20261102/unit-research-tail-cmd-artifact-daemon-mutex.txt)

That test launches 50 registering, 50 looking-up and 50 removing goroutines,
each performing 100 operations. Persistence already coalesces mutations whose
generation was written by another caller; remaining calls copy the entire
alias map, encode indented JSON and atomically replace the file under a
persistence mutex. Benchmark compact serialization and snapshot-allocation
improvements against this existing contract before changing operation counts
or persistence semantics. A separate 64-writer regression explicitly requires
each completed registration to be visible on disk; an asynchronous batch
writer would weaken that guarantee. Retain both concurrent persistence and
in-memory consistency assertions.
[Stress test](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/behavioral_test.go#L626),
[generation coalescing and snapshot](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/registry.go#L307),
[JSON and atomic replacement](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/alias_store.go#L47),
[completed-mutation guarantee](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/cmd/artifact-daemon/registry_persistence_test.go#L82)

## Limits and reproducibility

These are two instrumented runs with CPU/block/mutex sampling and JSON output
overhead; they are not an A/B speedup demonstration. Remaining gaps include
native test timing inside the heavy binaries, per-package compile/link/vet
spans, PostgreSQL CPU/query waits and child-process CPU. The focused run adds
individual spec and hook timing for integration and GC, but its sequential
schedule must not be substituted into the full unit run's wall timeline.

The offline [analysis script](../../output/ci-performance-20261102/unit-research-analyze.py)
reconstructs suite counts, worker spans, foreground package events and resource
samples from retained evidence. It writes only `unit-research-*` outputs.
The separate [tail parser](../../output/ci-performance-20261102/unit-research-tail-analyze.py)
reconstructs command/package boundaries, exact Ginkgo node durations, outcomes,
resource totals and all 20 decoded profile totals. The raw binary profiles
remain alongside each package's test JSON under `tail-profile/`.
No test execution or production changes were performed for this analysis.
