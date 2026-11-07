# Balancing the existing live integration groups

The full Brine task went from **21m28s to
15m59s**, saving **5m29s
(25.5%)** in a matched CI pair. Both runs
passed **1,050 scenarios and 5,687 steps**.

This increment moves 32 scenarios in the complete task-command, pod-watch and
startup-failure feature files into the existing recovery group. It adds no process and changes
no test body, assertion, fixture implementation, timeout or kubelet setting.

Base: core `c8f7645cace6ce86759a5fea24d0e345bf715084`. The baseline
includes the [previous recovery split](brine-recovery-overlap-2026-11-05.md),
rebased onto this newer core. [Baseline build 1157934](https://concourse.home/builds/1157934)
and [candidate build 1158016](https://concourse.home/builds/1158016)
use identical task recipes. This comparison measures the rebalance alone.
It is one consecutive pair on a shared cluster; it does not establish a latency
distribution or a guaranteed saving for the entire pipeline.

## Where the time went

| Phase / group | Baseline | Rebalanced |
|---|---|---|
| Full task script | 21m28s | 15m59s |
| Preparation to first integration | 2m53s | 2m54s |
| Mandatory integration elapsed | 17m34s | 12m05s |
| Local | 7m13s | 7m21s |
| Main live | 17m34s | 12m05s |
| Recovery | 5m19s | 12m00s |
| Cross-node | 0m50s | 0m49s |

The three mandatory groups overlap; their durations must not be summed. Full
script duration includes builds, vet, native tests, integrations, coverage checks
and adapter restoration. It excludes the observer's final 45-second export hold.
Protocol start/end timestamps have one-second precision; phase/script receipts
and protocol durations have finer resolution.

The live groups finish about five seconds apart. Their reported durations differ
by 4.893 seconds.

The moved work retained its real execution and waiting:

| Moved feature | Baseline scenario time | Candidate scenario time |
|---|---|---|
| Real Kubernetes startup refusals | 87.097s | 77.878s |
| Running a task command | 123.401s | 121.214s |
| Watching one pod on a real Kubernetes API server | 118.336s | 151.792s |

The moved scenarios total 328.834s before
and 350.884s after, including recorder
disposal. Common main-live scenario time changed by
+2.539s. Full
[step differences](../../output/brine-balance-20261105/step-timing-differences.json)
retain ordinary timing variation rather than treating every difference as a
scheduling effect.

For example, the real watch-history expiration step took 35.0 seconds longer,
and the first compatibility eviction took 46.0 seconds longer in the candidate.
The pair cannot establish why these individual waits varied. Their conditions
and deadlines are unchanged; the total saving comes from overlapping the work.

Candidate decomposition:

| Group | Step bodies | Recorder drain | Other fixture/runner time | Total |
|---|---|---|---|---|
| local | 359.326s | 3.775s | 77.750s | 440.851s |
| live | 618.820s | 87.571s | 18.655s | 725.046s |
| recovery | 693.343s | 11.017s | 15.793s | 720.153s |
| multinode | 34.974s | 7.839s | 6.528s | 49.341s |

## Isolation and preserved integration coverage

The mandatory partition is now **918 local + 79 main live + 52 recovery**.
The existing cross-node scenario follows all three groups. The
[main manifest](../../atc/worker/jetbridge/brine/live/main/.brine) and
[recovery manifest](../../atc/worker/jetbridge/brine/live/recovery/.brine) retain
independent verdicts, scenario floors and undefined-step budgets. The real CLI
guard proves nonempty, disjoint selection and exact union with default discovery.

- Task commands use fresh scenario databases and generated namespaces. Their
  PostgreSQL sidecar and mounted workspace stay inside the step pod.
- Watch routes allocate local ports; permission revocation uses only a
  namespace-scoped Role/RoleBinding and a namespace-specific identity. History
  expiration still requires a real Expired/410 response and stream closure.
- Startup failures retain actual registry pulls, kubelet events and deadlines.
  Artifact-daemon host-port users remain together in the main group.
- Scenarios stay serial within each adapter, isolating process-local metrics
  and trace capture. Shared API/image-cache/node load can affect timings; the
  complete runs exercise those interactions.

All 108 feature files and
182 non-test Go step files have identical
filename/content multisets between snapshots. Production Go, guard tests,
launcher and coverage logic are unchanged by this increment. Exact scenario
and step identity multisets match, and all 1,050 recorder drains
are complete and non-partial in each run.

Coverage is 2860/3610
statements (79.224377%) before and
2860/3610
(79.224377%) after. Coverage block denominators match;
there are 0 candidate-only and
0 baseline-only covered blocks.
Independent `covdata` replay proves every merged counter equals the sum of all
four raw groups. The existing 50% production statement gate remains enforced.

## Resources and measurement limits

Task cgroup, bounded to the mandatory integration window:

| Metric | Baseline | Candidate |
|---|---|---|
| CPU-seconds | 332.642 | 334.809 |
| Mean cores | 0.318 | 0.465 |
| Maximum sampled memory, GiB | 4.949 | 4.938 |
| Throttled periods | 0 | 0 |

These five-second samples include local fixtures and adapters, exclude external
live pods, and omit interval edges. Memory includes page cache.

Supplemental owned-live-pod observations:

| Run | Observed pods | Pods with ≥2 samples | Observed lifetime CPU, seconds | Peak sampled working set, GiB |
|---|---|---|---|---|
| Baseline | 175 | 24 | 7.591 | 0.068 |
| Candidate | 174 | 24 | 5.320 | 0.049 |

Ten-second kubelet sampling misses short-lived pods and lifetime edges. CPU
values sum the largest observed lifetime counter for each fresh owned pod and
are **lower bounds**, omitting unseen pods and work after the final sample.
They are not total live-pod cost. Node counters are also retained in the
[node profiles](../../output/brine-balance-20261105/candidate-node-profile.json),
but include shared services and are not attributed to the test alone. Neither
collector reported errors.

The samples observed namespaces from only
95/131
baseline and
99/131
candidate scenarios that created live namespaces. These sparse pod CPU numbers
are unsuitable for comparing total live-pod CPU cost between runs.

Both runs used the same runner image and resource configuration:
`registry.home/concourse-test-runner@sha256:1e1fa432a1d399400bdc140452f610bd4efab8259eb989df2dfb0ea32fdb8fe3`; observed resources
`[{"requests":{"cpu":"1"}}]`.
Both task pods ran on `k3s-agent-y`. The benchmark does not
override pipeline CPU/memory requests or limits. Kubelet versions, volume-statistics periods and sync periods
are captured separately for each run and must match.

## Next target: preparation shared by every group

The candidate spends **173.8 seconds**
before its first integration starts. Adapter compilation takes
**65.1s**, native module tests
**68.8s**, vet
6.4s and artifact-daemon compilation
13.4s. Setup and the coverage adapter's
instrumented preparation account for the remaining time.

This is the next investigation target because reducing a shared prerequisite
advances both live groups. Profile compilation, test orchestration and real
fixture startup before selecting a change. The native test log reports
96 top-level tests with
47.26s of rounded test durations;
these may overlap and exclude compilation/orchestration, so subtracting them
from the phase duration would not prove compiler cost. The largest reported
tests are selection validation (9.90s), real daemon release (8.29s), and real
resource hold/drain (5.32s). Preserve these integrations and their assertions.

Artifact-daemon fixture startup remains measurable: the main group performs
31 launches, with
47.9s reaching
readiness before upload and
82.5s uploading,
decompressing, setting permissions and checking the binary. Those are useful
optimization candidates, but improving only this group would save about five
seconds of full-task time before recovery became the bottleneck, holding all
other timings fixed. Shared preparation has more immediate scope for reducing
the critical path.

## Validation and evidence

Both full CI runs include adapter compilation, vet, native module suites,
complete integrations, cross-node execution and the production coverage gate.
Local structural guards, real CLI partition discovery, shell syntax and pipeline
validation passed. All 262 observed owned namespaces
were verified removed. Existing unrelated namespaces are listed separately in
[cleanup verification](../../output/brine-balance-20261105/cleanup-verification.json).
Both owned task pods were also removed, and the preexisting namespace retained
its original UID; see the
[final cluster receipt](../../output/brine-balance-20261105/final-cluster-cleanup.json).

The launcher was not changed in this increment. Its previous repeated-signal
probe is documented in the recovery report; it is not presented as a new
cancellation run on this revision.

[Raw evidence and replay instructions](../../output/brine-balance-20261105/README.md)
include source hashes, apply-checked baseline/candidate/incremental patches,
exact recipes, raw protocol logs and coverage, phase timestamps, task/node/pod
samples, cleanup receipts and per-step differences. The bundle and checksums
are local untracked artifacts. These pipeline-bound one-offs wait for
conflicting jobs before launch but do not acquire pipeline serial groups;
[activity analysis](../../output/brine-balance-20261105/cluster-activity-analysis.json)
found no competing CI task pods in 46 baseline
and 35 candidate samples. Thirty-second sampling
gaps can miss short-lived tasks, and shared cluster services remain active.
