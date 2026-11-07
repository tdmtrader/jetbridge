# Real kubelet eviction latency

The slow scratch-volume eviction tests spend most of their time waiting for
kubelet volume accounting. In eight repeated runs of the **unchanged real
integration scenarios**, changing only an isolated kubelet's volume-statistics
period from 60s to 5s reduced the four eviction steps' median combined duration
from **192.331s to 58.087s (69.8%)**. The nine selected recovery scenarios went
from a median **248.716s to 122.411s (50.8%)**.

These are focused diagnostic results, not a full-suite or whole-pipeline
speedup. No shared cluster setting was changed. The implementation being
evaluated separately overlaps the complete recovery feature files with the
other live scenarios at the existing kubelet configuration.

## Experiment and preservation

Source: core `28a0e8d391ff196ab295cc393386994d1d00e50a`.
[Repeated CI build 1152487](https://concourse.home/builds/1152487) passed all
**72 scenarios, 240 steps and 72 complete, non-partial fixture drains**. Each
iteration ran the existing `@RF-05` selection, then `@pause-recovery`, through
the real pinned Brine CLI. The tag selections contain nine scenarios, including
four actual emptyDir evictions, two real cgroup OOMs and TERM recovery cases.
Scenario and step identity multisets match the CLI's expanded inventory.

Every iteration used a fresh disposable K3s **v1.34.3+k3s1** node inside the
owned CI task, matching the shared nodes' Kubernetes version. All used the same
resolved K3s image, imported BusyBox images, adapter binary and source. Effective
configuration was read back through kubelet configz. Only
`volumeStatsAggPeriod` differed. Cluster startup, image import and a real
readiness pod preceded the timed selections. That per-iteration preparation
took 17.584–35.163s (median 19.984s), separate from the timings below.

The feature files, Go step implementations, 24MiB write into a 16MiB emptyDir,
90-second eviction context and real status/event/UID assertions were unchanged.
No reported status was substituted for the kubelet's result. The detailed
observer independently correlated every eviction with its owned pod UID,
`Failed/Evicted` status, matching kubelet Warning event and scratch-volume usage.
All 24 observed evictions had write, accounting and event timestamps; observer
error count was zero.

| Run | Volume-statistics period | Detailed observer | Nine scenarios | Four eviction steps |
|---|---:|---|---:|---:|
| 1 | 60s | on | 248.678s | 192.401s |
| 2 | 5s | on | 116.367s | 53.934s |
| 3 | 5s | on | 127.588s | 59.072s |
| 4 | 60s | on | 283.812s | 219.428s |
| 5 | 60s | on | 248.754s | 187.106s |
| 6 | 5s | on | 126.424s | 60.953s |
| 7 | 5s | off | 118.398s | 57.102s |
| 8 | 60s | off | 248.571s | 192.260s |

The order alternates within pairs to reduce simple ordering bias. The last pair
disables detailed observation and remains consistent with the observed runs;
one pair is not a precise estimate of profiler overhead. Low-rate cgroup
sampling remains enabled throughout. This is a small experiment on a shared
host, not a general latency distribution or reliability guarantee.

## Where the eviction time goes

The external observer watched pods and events, streamed timestamped container
logs and read the owned kubelet's stats summary once per second. The container
log records completion of the actual 25,165,824-byte scratch write.

| Per-pod observation, 12 evictions per setting | 60s period median | 5s period median |
|---|---:|---:|
| Pod creation to completed write | 1.744s | 1.952s |
| Completed write to observed usage above the limit | 44.118s | 3.234s |
| Observed over-limit usage to observed eviction event | 3.777s | 1.298s |
| Completed write to observed eviction event | 44.486s | 4.534s |

These medians are calculated independently and must not be added. Creation
timestamps have one-second precision; stats polls and watch arrival add
observation error. A few accounting-to-event differences are slightly negative
(at most 0.036s), reflecting delivery order rather than physical causality.
At 60s, write-to-accounting ranged from 1.090s to 68.305s; at 5s it ranged from
0.303s to 9.180s.

This matches the implementation: Kubernetes caches per-pod volume statistics
using a jittered periodic calculation, and its emptyDir eviction check consumes
those statistics. A configured 60s period is not a strict 60s upper bound.
See the pinned Kubernetes
[volume-statistics calculator](https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/kubelet/server/stats/volume_stat_calculator.go)
and [eviction manager](https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/kubelet/eviction/eviction_manager.go).

## Resource cost and limits

The driver cgroup does **not** contain all the experiment's work. Nested Docker
placed the K3s node under a separate `/docker/...` cgroup, while K3s placed its
pods under separate `/kubepods/...` groups. Resource collection samples all
three sets, using only the disposable cluster's pod UID inventory.

| Run | Driver CPU-seconds | K3s-node CPU-seconds | Inner-pod CPU-seconds, lower bound |
|---|---:|---:|---:|
| 2, 5s | 5.34 | 8.93 | 2.24 |
| 3, 5s | 5.99 | 9.19 | 4.49 |
| 4, 60s | 12.44 | 16.90 | 2.42 |
| 5, 60s | 9.83 | 13.93 | 2.60 |
| 6, 5s | 5.70 | 8.89 | 3.96 |
| 7, 5s, observer off | 5.28 | 11.79 | 2.43 |
| 8, 60s, observer off | 8.98 | 11.83 | 1.54 |

Five-second sampling misses window edges and short-lived pod cgroups, so these
are sampled costs, not exact total CPU. The node/pod collectors started partway
through run 1; its incomplete resource values are retained but excluded from
this table. Sampled node memory stayed between roughly 0.81 and 0.85GiB at its
per-run maxima. Memory includes cache. Neither the driver nor the Docker-node
cgroup was CPU throttled; inner pods retain their original resource limits.
The experiment does not establish the cost of a 5s period on a busy production
node with many volumes.

## Operational checks and evidence

- Runner digest: `sha256:1e1fa432a1d399400bdc140452f610bd4efab8259eb989df2dfb0ea32fdb8fe3`;
  K3s digest: `sha256:71abd3a56f57884c62732e0e0d87606052cb5f8555b7db7e8e33c04570b8175c`.
  Go 1.25.14, PostgreSQL 17.11, node `k3s-agent-y`, 4 CPU / 12GB requests,
  no task CPU or memory limits.
- Every iteration verified its owned namespaces were removed. The nested
  containers and all three profiling task pods were also verified removed.
- Initial preflight [1152429](https://concourse.home/builds/1152429) failed before
  any integration scenario ran: Node Ready preceded the default ServiceAccount.
  The harness now awaits both that account and a real readiness pod.
  Exploratory pilot [1152451](https://concourse.home/builds/1152451) passed, but is
  kept separate from the repeated matrix.
- A shared CI web rollout during run 5 briefly made a status read return 503.
  The owned task continued and fly replayed console output on reconnect. The
  retained in-task protocol logs, rather than replayed console text, supply the
  measurements.

The [retained evidence directory](../../output/eviction-profile-20261105/README.md)
contains raw observations and protocol logs, exact harness/task configurations,
source/image receipts, per-run effective kubelet settings and cleanup receipts.
[Analysis](../../output/eviction-profile-20261105/matrix-analysis.json),
[comparison](../../output/eviction-profile-20261105/matrix-comparison.json) and
[resource attribution](../../output/eviction-profile-20261105/matrix-resources-analysis.json)
can be replayed with the included scripts. These `output/` artifacts are local,
untracked evidence; this report is retained in the repository.
