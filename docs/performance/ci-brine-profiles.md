# Fresh Brine performance profile

Normal `jetbridge/brine` build **122** ([1142537](https://concourse.home/teams/main/pipelines/jetbridge/jobs/brine/builds/122)) passed on core `1d0e377594898bff615f1c222f9977e0ea094acc`.
Build elapsed time: **2,194s**. The existing pipeline ran its native suites, mandatory local/live manifests, cross-node manifest, statement-coverage gate and cleanup.
All **1,050 scenarios**, **5,687 steps**, and **1,050 recorder drains** completed successfully; no partial drain was reported.
Coverage: **2,846/3,593 production statements (79.209574%)**, above the unchanged 50% gate.

This run uses the existing CI credentials. Source and test code are unchanged.
Read-only collectors streamed temporary files before automatic pod cleanup.
The run overlaps profiling work in other pods, so it is a detailed attribution
run, not an uncontended baseline or an optimization experiment.

## Whole-build phase allocation

This table uses CI and protocol event timestamps at one-second precision.
It accounts for the full build, including work outside `RunPlan`. Preparation
overlapped other profiling pods and must not replace the unprofiled baseline.

| Phase | Approximate elapsed |
|---|---:|
| Startup, builds, vet, native suites, coverage preparation and local adapter preparation | 296s |
| Local manifest | 473s |
| Local exit / live preparation | 0s |
| Live manifest | 1,362s |
| Mandatory verdict verification and cross-node preparation | 3s |
| Cross-node manifest | 51s |
| Final verification, coverage, ordinary-adapter rebuild and task completion | 9s |

## Exact nested timing categories

These categories are non-overlapping. Recorder drain is subtracted from the
scenario residual before making its own column. Scope setup/disposal and
runner overhead outside scenario timers remain an explicitly named residual.
The numeric protocol durations are used; RFC3339 event timestamps have only
whole-second precision.

| Tier | Scenarios | Steps | Recorder drain | Other inside scenarios | Scope / runner residual | Run total |
|---|---:|---:|---:|---:|---:|---:|
| Local | 918 | 377.202s | 4.863s | 1.587s | 89.479s | 473.131s |
| Live, one-node scenarios | 131 | 1,244.370s | 97.527s | 0.163s | 20.500s | 1,362.560s |
| Live, cross-node | 1 | 35.907s | 8.061s | 0.000s | 6.565s | 50.533s |

These run totals exclude adapter preparation before `RunPlan` and exit checks
after it. The task also builds binaries, vets, runs native suites, verifies
manifest floors, calculates coverage and rebuilds the normal adapter. See
[the exact timer definitions](ci-history-analysis.md#what-the-brine-jsonl-timers-actually-include).

## Largest measured features

Feature totals below sum scenario durations and include recorder disposal.
They exclude scope work outside scenario timers; they are not full feature spans.

| Tier / feature | Scenarios | Scenario time | Of which recorder drain |
|---|---:|---:|---:|
| Live, one-node scenarios / [A real container across runs](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/container-lifecycle.feature) | 10 | 244.224s | 0.844s |
| Live, one-node scenarios / [Running a task command](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/task-command.feature) | 16 | 120.471s | 0.929s |
| Live, one-node scenarios / [Watching one pod on a real Kubernetes API server](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/pod-watch.feature) | 6 | 117.359s | 1.392s |
| Live, one-node scenarios / [Reading recorded artifacts from a real node](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/artifact-recording.feature) | 8 | 109.640s | 20.508s |
| Live, one-node scenarios / [Retrying remote artifacts and finding peer copies](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/remote-artifact.feature) | 5 | 95.759s | 12.922s |
| Live, one-node scenarios / [Reading artifacts from a real node](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/daemon-node-read.feature) | 8 | 93.975s | 19.453s |
| Live, one-node scenarios / [Artifact handoff through real tasks and the node daemon](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/artifact-handoff.feature) | 4 | 89.558s | 14.281s |
| Live, one-node scenarios / [Real Kubernetes startup refusals](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/startup-failure.feature) | 10 | 77.709s | 3.164s |
| Live, one-node scenarios / [Direct compatibility process completion](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/compatibility-process.feature) | 10 | 72.793s | 4.543s |
| Local / [Cancellation publishes one quiescent aborted Run](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/run-cancellation-finality.feature) | 8 | 59.547s | 0.053s |
| Live, one-node scenarios / [Artifact reads use a real producer and an independent peer](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/peer-read.feature) | 3 | 52.864s | 7.056s |
| Live, cross-node / [An artifact crosses nodes through the node daemons](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live-multinode/artifact-crossnode.feature) | 1 | 43.968s | 8.061s |
| Local / [A local receipt preserves a review invocation](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/review-submit.feature) | 13 | 39.984s | 0.126s |
| Live, one-node scenarios / [Actual resource publication and retry](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/time-resource.feature) | 7 | 39.130s | 0.037s |
| Live, one-node scenarios / [Intercepting real step pods](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/interception.feature) | 8 | 38.344s | 0.082s |
| Live, one-node scenarios / [Persisted artifacts cross real task and resource boundaries](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/features/live/artifact-integration.feature) | 2 | 37.478s | 7.304s |

## Largest repeated step costs

Rows aggregate exact step sentences within a tier. They include fixture work
performed lazily inside that step and do not isolate the named product operation.

| Tier / step | Calls | Total | Maximum |
|---|---:|---:|---:|
| Live, one-node scenarios / the pod finishes before its watched history expires | 1 | 94.425s | 94.425s |
| Live, one-node scenarios / the next step fetches the artifact from that node | 5 | 82.834s | 21.986s |
| Live, one-node scenarios / the pause pod stops "before startup" from "Evicted" before the task can execute | 1 | 73.374s | 73.374s |
| Live, one-node scenarios / an artifact daemon on a real node | 8 | 71.598s | 9.530s |
| Live, one-node scenarios / the pause pod stops "on the first dial" from "Evicted" before the task can execute | 1 | 63.337s | 63.337s |
| Live, one-node scenarios / the pause pod stops "before startup" from "Evicted then TERM" before the task can execute | 1 | 52.662s | 52.662s |
| Live, cross-node / a task on one approved node produces "nested/result.txt" containing "built on one node" for a following task on another | 1 | 35.907s | 35.907s |
| Live, one-node scenarios / a jetbridge worker with a real node's artifact daemon | 4 | 33.781s | 9.445s |
| Live, one-node scenarios / a jetbridge worker whose outputs are mirrored to an independent peer | 4 | 32.820s | 8.660s |
| Local / the periodic cancellation component recovers a lost notification | 1 | 30.023s | 30.023s |
| Live, one-node scenarios / the failed real init recovers after repeated observations | 1 | 21.058s | 21.058s |
| Live, one-node scenarios / a task produces "result.json" containing "built ok" and hands it to a following task using "raw" with fault "none" | 1 | 20.857s | 20.857s |
| Live, one-node scenarios / a task produces "nested/result.txt" containing "nested build data" and hands it to a following task using "gzip" with fault "none" | 1 | 20.161s | 20.161s |
| Local / a Run producer and a ready output node | 217 | 19.717s | 0.217s |
| Live, one-node scenarios / the compatibility watcher diagnoses a task repeatedly killed by its memory limit | 1 | 19.204s | 19.204s |
| Live, one-node scenarios / the Git get selects the "first" commit | 2 | 18.617s | 15.364s |
| Local / its runtime producer publishes a successful review | 151 | 17.378s | 0.288s |
| Live, one-node scenarios / a task produces "result.json" containing "built ok" and hands it to a following task using "raw" with fault "producer-offline" | 1 | 17.227s | 17.227s |
| Live, one-node scenarios / the "production" runtime diagnoses an image-pull failure in "main" | 1 | 17.204s | 17.204s |
| Live, one-node scenarios / a task's output is published by a Git put | 1 | 17.108s | 17.108s |

## Resource evidence and its limits

Task node: `k3s-agent-y`; request: one CPU, no CPU or memory limit.
Runner: `concourse-test-runner:v11`; the exact digest is retained in the
[pod receipt](../../output/ci-performance-20261102/brine-fresh-pod.json).
Go 1.25.14 and PostgreSQL 17.11 ran on Linux amd64.

| Tier | Sampled span | Task CPU-seconds | Mean task cores | Maximum sampled cgroup memory | Throttled periods |
|---|---:|---:|---:|---:|---:|
| Local | 470.3s | 313.728s | 0.667 | 4.579 GiB | 0 |
| Live, one-node scenarios | 1355.7s | 52.335s | 0.039 | 3.859 GiB | 0 |
| Live, cross-node | 45.0s | 1.206s | 0.027 | 3.994 GiB | 0 |

Samples are approximately five seconds apart and stop inside each tier boundary;
their spans are shorter than full tier durations. Cgroup memory includes cache
and is not application RSS. CPU includes local child processes, but excludes
the external live pods and the shared cluster control plane. Low live-task CPU
therefore shows that the driver is mostly waiting, not that the cluster is idle.
The initial collector accidentally read the host cgroup in this privileged pod;
`brine-fresh-resources.log` must not be used as task CPU/memory. The corrected
`brine-fresh-container-cgroup.log` follows `/proc/self/cgroup` and is the source
of the table above. No adapter CPU pprof or per-resource scope timers were collected.

## What can become faster without weakening the test

1. **Overlap local work with live work in separate isolated tasks.** The local
   run takes 473.131s. This is a potential overlap window, before additional
   startup and resource contention. Share the source-matched instrumented
   binary or build it independently from the same SHA; merge coverage and
   retain every manifest verdict. Keep live exclusion until its isolation is
   independently proven. Do not simply run two live drivers on the same port.
2. **Measure the actual eviction boundary on a dedicated test node.** Three
   pause-recovery step bodies take 73.374s, 63.337s and 52.662s. They create a
   real bounded 24MiB write into a 16MiB emptyDir and require the kubelet's
   `Evicted` status and matching event. Their poll is 100ms and pod grace is
   one second. Both approved nodes report `volumeStatsAggPeriod: 1m0s`.
   That cadence is a plausible contributor, not a measured causal fraction.
   Test a shorter volume-statistics interval on an isolated node and retain
   the actual eviction assertions; do not replace them with supplied status.
   [Fixture source](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/live_eviction.go#L64),
   [node configuration receipt](../../output/ci-performance-20261102/kubelet-timing-config.json),
   [Kubelet configuration semantics](https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/).
3. **Recognize the real watch-history test's time floor.** The slowest
   individual step takes 94.425s. It completes a real pod, advances an owned
   gated pod's metadata every 250ms and probes for actual Expired/410 plus
   stream closure every 20 updates. Kubernetes v1.34.3, matching the live
   cluster version, defaults to a 75-second minimum watch-history window.
   That source default is consistent with the duration; the exact configured
   server window and time within each sub-operation were not measured.
   Faster test polling is therefore not evidence of a large available saving.
   Preserve the real expiry/recovery test and investigate overlap in isolated
   live partitions rather than replacing the 410 with fabricated status.
   [Test source](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/podwatch.go#L147),
   [versioned Kubernetes default](https://github.com/kubernetes/kubernetes/blob/v1.34.3/staging/src/k8s.io/apiserver/pkg/storage/storagebackend/config.go#L40).
4. **Time live fixture acquisition and each disposer by resource name.**
   Artifact-store creation repeatedly creates an anchor, observer and daemon
   on real nodes. Disposal proves ownership, waits for quota accounting and
   independently observes kubelet volume reclamation. Reusing these fixtures
   needs a fresh-state and ownership design; deleting these checks is not an
   optimization. Namespace deletion already defers controller waiting to the
   end of the suite, so that is not a new opportunity.
   [Storage lifecycle](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/live_artifact_store.go#L84),
   [deferred namespace sweep](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/live_namespace_sweep.go#L33).
5. **Treat deliberate clock tests separately.** The local lost-notification
   cancellation step takes 30.023s and runs the real production component
   interval. It is not an accidental readiness sleep. An injected clock can
   be evaluated only alongside an independent assertion of the production
   interval and retention of notification-loss behavior.
   [Cancellation fixture](https://github.com/tdmtrader/jetbridge/blob/1d0e377594898bff615f1c222f9977e0ea094acc/atc/worker/jetbridge/brine/steps/run_cancellation_finality.go#L78).

## Retained evidence

- [Raw mandatory-tier events](../../output/ci-performance-20261102/brine-fresh-events.log)
- [Raw cross-node events](../../output/ci-performance-20261102/brine-fresh-multinode.log)
- [All scenarios](../../output/ci-performance-20261102/brine-scenarios.json) and [all steps](../../output/ci-performance-20261102/brine-steps.json)
- [Aggregate analysis](../../output/ci-performance-20261102/brine-analysis.json) and [resource summary](../../output/ci-performance-20261102/brine-resource-summary.json)
- [Coverage blocks](../../output/ci-performance-20261102/brine-fresh-coverage.log)
- [CI event stream](../../output/ci-performance-20261102/brine-fresh-ci-events.txt)
- [Offline event parser](../../output/ci-performance-20261102/analyze_brine.py) and [report generator](../../output/ci-performance-20261102/report_brine.py)
