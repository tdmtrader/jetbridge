# Brine preparation: retaining Go compiler artifacts

Preparation averaged **326.0s cold → 147.2s warm**,
saving **178.8s (54.9%)** across two alternating
CI pairs. Both warm builds issued **zero package compilation commands**, while
still linking ten executables and passing the same 96 top-level tests and 251
test/subtest results. This measures preparation, not a whole-Brine or pipeline
percentage reduction. Other CI jobs were active throughout; the pair savings
were 200.5s and 157.1s.

The earlier native-package optimization is landed on core as
[`2cd9d9e7d1`](https://github.com/tdmtrader/jetbridge/commit/2cd9d9e7d16ffa3633768bcfd0c5b4830c100f12) and activated
in the installed pipeline. Its exact-commit build/vet gate
[1162492](https://concourse.home/builds/1162492) and full unit gate
[1162501](https://concourse.home/builds/1162501) passed. The compiler-cache
measurements below used a separate source candidate; the experiments installed
only an owned, temporary profiling job.

## What changes

The Brine task mounts a job/task-scoped cache at `/tmp/brine-go-build-cache`
and selects it with `GOCACHE`. Go reuses compiled packages using its normal
source, dependency, toolchain and build-flag keys. Adapter and artifact-daemon
executables are still linked from current source, and native tests still use
`go test -v -count=1 -timeout=5m ./...`. All real integration manifests,
fixtures, assertions, production waits and coverage checks remain unchanged.
Go also reuses source-keyed vet analysis; the vet command remains in place.
The cache stores no database, Kubernetes fixture or Brine scenario verdict.

Go's own `go help cache` documents that external C-library changes are not
detected. The final wrapper therefore records `BRINE_GO_CACHE_IMAGE` and
clears the compiler cache when it changes. The pipeline derives that identity
from the task's image declaration through one YAML anchor. Runner rebuilds
must use new tags, as already required by `PullIfNotPresent`. This avoids
retaining cgo artifacts across a declared runner upgrade without inventing a
second source/dependency cache-key system.

`scripts/with-go-cache` logs allocated disk usage at entry and exit. Above
8 GiB it runs `go clean -cache`, preserving a failing child's exit status and
failing a successful task if retention cleanup fails. Cleanup occurs outside
the task's Go commands; the Brine job remains serial. This is a retention
threshold, not a runtime disk quota. Go also performs its own age-based
pruning, which does not impose a size bound. Clearing the cache makes the next
compilation cold. An interrupted pod can leave a larger cache until the next
entry check.

The absolute path makes the location explicit. It does not fix a relative-path
cache bug: `atc/engine/step_factory.go` derives the working directory from the
task name, so that directory is already stable across builds. Task caches are
node-local; a different node has its own contents. Each active node can retain
up to the threshold after a successful check, and old job/task directories
still require operator reclamation. This change does not fix that broader
runtime lifecycle issue.

## Controlled preparation trials

All four builds used source `2cd9d9e7d16ffa3633768bcfd0c5b4830c100f12`, toolchain
`go version go1.27.3 linux/amd64`, node `k3s-agent-y`, a 1-CPU
request with no CPU/memory limit, and actual runner image:

`registry.home/concourse-test-runner@sha256:5c400a4c10ea8038c0ddad525f41e1e90aa3dc13810e9ec975b8effceb7c6c25`

| Trial | CI build | Preparation | Adapter + local daemon | Vet | Native tests | Static daemon | Package compile commands |
|---|---|---|---|---|---|---|---|
| cold1 | [1162572](https://concourse.home/builds/1162572) | 322.9s | 149.8s | 9.3s | 113.4s | 48.2s | 2583 |
| warm1 | [1162608](https://concourse.home/builds/1162608) | 122.3s | 42.3s | 4.5s | 68.4s | 4.1s | 0 |
| warm2 | [1162620](https://concourse.home/builds/1162620) | 172.0s | 33.3s | 5.5s | 121.4s | 9.2s | 0 |
| cold2 | [1162635](https://concourse.home/builds/1162635) | 329.2s | 149.7s | 42.3s | 112.7s | 22.7s | 2583 |

Setup accounts for the remaining small interval. Preparation includes the
wrapper's size checks and excludes archive compression/export. All timed
trials have equal command/action tracing. The cold trials call `go clean
-cache` before timing; they do not flush the node's operating-system page
cache. Each trial has a new step pod and a new source checkout at the same
task working directory. Warm trials require the preceding build's marker in
the real mounted cache; no cache was copied into a one-off pod.

The pod receipts show the same host path across all four builds:
`/var/cache/concourse/job-3191-cache-preparation-50d9d7e8f916`.
Exact test-name multisets match in every trial, with no skips or reused
test results. The compiler action graphs retain separate adapter, ordinary
daemon and static `CGO_ENABLED=0` daemon variants. Their cache keys differ;
retaining all variants eliminates their repeated compilation.

| Trial | Sampled CPU-seconds | Peak sampled cgroup memory GiB | Disk reads GiB | Disk writes GiB | Throttled periods |
|---|---|---|---|---|---|
| cold1 | 1386.5 | 5.06 | 0.42 | 7.72 | 0 |
| warm1 | 136.8 | 3.82 | 1.45 | 1.70 | 0 |
| warm2 | 179.4 | 2.50 | 0.18 | 2.17 | 0 |
| cold2 | 1390.7 | 4.95 | 0.91 | 7.45 | 0 |

CPU figures sum sampled phase intervals and omit their edges. Memory includes
page cache, not just process RSS. Disk I/O counts backing-device operations;
cached reads and asynchronous writeback prevent interpreting it as logical
Go input/output bytes or exact operation latency. Raw one-second cgroup CPU/memory/I/O samples
and 15-second observations of competing CI jobs are retained. The substantial
variation between warm builds limits any latency guarantee. Four builds are
evidence of the mechanism and observed savings, not a latency distribution.
The matched trials keep source identical. A later commit recompiles affected
packages and their dependents, so its saving depends on what changed; the
179-second result is not a promise for every push.

## Correctness, full integrations and retention

[Verification build 1162668](https://concourse.home/builds/1162668)
re-ran the native tests, then used `GODEBUG=gocacheverify=1` on real adapter and
static-daemon builds. Go rebuilt and checked cached content; both executable
SHA256s remained identical. A separate real Go program changed behavior and
binary hash after a source edit while using the same cache. No suite source
was edited for this check. The expensive verification flag was scoped to
builds, leaving test deadlines intact.

[Full integration build 1162720](https://concourse.home/builds/1162720)
passed **1,050 scenarios and 5,687 steps**, including
the cross-node tier. Every scenario has a complete recorder drain. The normal
manifest-verification gates passed and merged production coverage was
**77.39%**
(2,919/3,772 statements).

| Tier | Scenarios | Steps | Elapsed |
|---|---|---|---|
| local | 918 | 5015 | 607.7s |
| live | 79 | 407 | 914.6s |
| recovery | 52 | 262 | 721.3s |
| multinode | 1 | 3 | 52.8s |

This full regression used the existing authorized `main/one-off` step pod
grant via `fly execute -j jetbridge/brine`. Its cache was an **emptyDir**,
so it validates real full-suite execution and the complete cache working set,
not cross-build persistence or warm full-suite performance. Persistence is
established separately by the four actual pipeline builds. No step pod grant
or Kubernetes permission changed. All 326 Brine Go/feature files are byte
identical to the pinned source.

Preparation retained about 2.67 GiB;
the complete task retained 2.89 GiB, below the 8 GiB threshold.
[Cleanup build 1162805](https://concourse.home/builds/1162805)
exercised the actual wrapper's over-limit branch against the populated Linux
cache with a 1-KiB test threshold. It preserved child exit 37 and reduced the
owned cache to 16 KiB of metadata. The temporary profiling
job was removed, owned step pods were verified absent, and Brine's original
unpaused state was restored. All 131 owned full-regression namespaces
were verified absent. The hostPath root itself was not deleted.

The image-identity guard was added after the timed trials and full regression
were launched. Those recipes retain the preceding wrapper; the cleanup build
tests the final wrapper separately against real compiler entries. Keeping the
same image identity retained them, changing the identity cleared them, and a
real rebuild reproduced the binary hash. That validation changes the identity
input while keeping the actual runner pinned; it is not an image-upgrade
benchmark. The four timings describe compiler-cache reuse before this added
entry check.

## Remaining work and replay

The first warm adapter build still spent about 21s in its linker action and
6s loading packages. Executables are linked again even when compilation is
fully cached; native tests also build fresh protocol/guard binaries. The next
preparation investigation should attribute repeated linking and package
loading before changing any build boundary. The larger real integration
phases remain visible in the full run; their elapsed times must not be added
when their manifests overlap.

An initial cold build [1162523](https://concourse.home/builds/1162523) passed its
tests but lost part of its profiling archive to a connection reset. Its logs
are retained and it is excluded from the comparison. The replacement exporter
uses retryable chunks and verifies the complete archive against the pod's
SHA256 before acknowledging export.

The [evidence README](../../output/brine-compiler-cache-20261107/README.md)
describes exact recipes, raw action graphs and traces, source hashes, native
inventories, full coverage data, resource observations, validation and cleanup
receipts. The portable bundle and SHA256 manifest live under that untracked
output directory. Offline replay regenerates the analysis from the original
archives in a fresh directory and checks equality with the reported results.
