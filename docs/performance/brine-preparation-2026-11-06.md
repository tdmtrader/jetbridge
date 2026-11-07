# Brine preparation: overlapping the native Go packages

The native-test phase averaged **80.2s → 63.1s**,
saving **17.1s (21.4%)** across two
alternating CI pairs. Every trial passed the same **96 top-level tests and 251
test/subtest results**, with no skips. This is a preparation-phase result, not
a measured percentage reduction for the full Brine task or pipeline.

The preceding live-group optimization is landed on core as
[`2f68fe6132`](https://github.com/tdmtrader/jetbridge/commit/2f68fe6132378489ebcd14e903be22be7893a2ba).
It was rebased onto `9c73367396`; build-and-vet
[1162121](https://concourse.home/builds/1162121) and the full unit tier
[1162120](https://concourse.home/builds/1162120) passed before push.
This preparation investigation uses that landed revision and the newer v12
runner. The earlier [live-group report](brine-balance-2026-11-05.md) retains its
original c8/v11 benchmark; the two investigations must not be combined as one
controlled comparison.

## Change and actual overlap

The [Brine task](../../deploy/concourse-pipeline.yml) invokes the nested module's
native tests with:

```sh
BRINE_PROTOCOL_LAUNCHER="$PWD/scripts/run-private-network" \
  go test -v -count=1 -timeout=5m ./...
```

These are plain Go tests. The previous Ginkgo CLI compiled their packages and
executed the three test binaries serially. Direct `go test` can execute the
packages concurrently. Test bodies within each package remain serial; there
are no `t.Parallel` calls in the measured native corpus. The real protocol
launcher, fresh adapter build, guard invocations, engine, artifact daemons,
databases and their assertions remain in place. `-count=1` prevents cached test
results from replacing execution. Compiler artifacts may still be reused under
Go's source-dependent cache keys.

Go's action timestamps confirm that the steps package ran entirely inside the
protocol package's interval: **17.569s**
of overlap in B1 and **17.344s** in B2.
The protocol package remains the native phase's critical path. The manifest
verification package also runs and returns its own verdict. Go additionally
compiles the module's helper package with no test files; it adds no test result.

The native packages own their temporary roots, free ports and database
instances. Protocol invocations still enter the existing private network
namespace. Process-local environment changes, metrics and trace capture remain
separate. The final process receipt contained no PostgreSQL, envtest API/etcd,
artifact daemon, Brine engine or native-test process. Both owned CI pods were
verified removed after asynchronous disposal.

All **326 Brine Go and feature files** match the landed source byte for byte.
Only the native runner command and its documentation change. The complete
Brine scenario launcher, coverage merge, production source and feature files
are unchanged.

## Paired measurement

[Comparison build 1162193](https://concourse.home/builds/1162193) ran A1, B1,
B2, A2 in one disposable CI step. A uses the original command; B uses the
candidate command. Before each trial, the harness copied the same
post-build/post-vet compiler cache into a fresh directory. Copying and deletion
were outside the trial timer. The source, module cache, node, runner and
resource configuration were constant. Exact native test-name multisets,
including subtests, match in all four trials.

| Trial | Runner | Elapsed | Top-level tests | Including subtests | Sampled CPU-seconds | Peak sampled memory GiB |
|---|---|---|---|---|---|---|
| A1 | ginkgo | 83.407s | 96 | 251 | 244.3 | 7.40 |
| B1 | go test | 61.852s | 96 | 251 | 227.8 | 6.96 |
| B2 | go test | 64.301s | 96 | 251 | 238.7 | 7.08 |
| A2 | ginkgo | 77.030s | 96 | 251 | 239.2 | 7.65 |

A1/B1 saves 21.556s; A2/B2 saves
12.729s. The candidate uses more CPU
concurrently, while both pairs complete sooner. No CPU throttling was observed.
Memory includes page cache and the trial's compiler-cache copy; these figures
are not process RSS. One-second resource samples omit interval edges.

This is four trials in one CI step, with other CI tasks active and recorded at
15-second intervals. Alternating order and equal cache contents reduce obvious
ordering/cache bias; they do not make this a latency distribution. Go command
and action tracing was enabled equally for every trial. The observed real
package overlap supports the cause of the saving, but this is not an
uninstrumented whole-pipeline benchmark.

Both preparation builds used `k3s-agent-y`, a 1-CPU request, no CPU/memory limit
and runner:

`registry.home/concourse-test-runner@sha256:5c400a4c10ea8038c0ddad525f41e1e90aa3dc13810e9ec975b8effceb7c6c25`

## Runner behavior and limits

Per-test context deadlines, waits and assertions are unchanged. The runner's
timeout enforcement differs: pinned Ginkgo's `runGoTest` starts a plain Go
binary without a test timeout, while its outer loop checks the five-minute
deadline before admitting later suites. Direct Go enforces **five minutes per
package**. The new cap did not fire in any trial. Direct Go also reports other
package results when one package fails and returns an overall failure.

The direct command uses Go's default test build flags; the Ginkgo CLI had added
`-ldflags='-w -s'` to its compiled test binaries. Production binaries and their
build flags are unaffected. Raw command/action receipts retain this distinction.
The installed pipeline has not been changed by this investigation; applying
the candidate task recipe requires the normal pipeline update when it lands.

## Where the rest of preparation goes

[Attribution build 1162158](https://concourse.home/builds/1162158) ran the entire
preparation sequence successfully on a fresh runner. It recorded:

| Phase | Elapsed |
|---|---|
| setup | 2.104s |
| adapter_build | 176.884s |
| vet | 12.446s |
| native_tests | 121.236s |
| artifact_build | 73.257s |

The adapter itself took 155.718s, followed by 21.022s for the local artifact
daemon. The later static artifact-daemon build took 73.249s. These timings
include concurrent CI load and must not be compared directly with the older
174-second preparation measurement. Go action graphs show that cold compilation
dominates: the adapter graph recorded 1,759 package-build actions with commands;
the subsequent daemon configurations recorded 258 and 556 more. Root/nested
dependency selections and the static build's `CGO_ENABLED=0` produce distinct
cache entries. Parallel action durations must not be added as wall time.

The initial PATH-only observer saw six outer Go invocations: Go prepends
`GOROOT/bin` for child commands. The comparison observer therefore wrapped the
Go entry point **only inside its disposable container**, retaining and invoking
the original binary beside it. It captured 41 invocations, including TestMain's
fresh adapter/guard builds and real daemon builds. A trials invoke Go 12 times;
B trials invoke it six times. No runner image was rebuilt or published.

The next larger target is compiler-cache reuse across CI runs, with real cache
hits, misses after source changes and disk reclamation measured. Ordinary
`fly execute` one-offs do not initialize task caches (`TaskCacheIdentity` is
absent), so simply adding a cache field to these probes would not validate a
persistent pipeline cache. Current task-cache host paths also need a deliberate
reclamation strategy. This investigation adds no persistent cache.

## Validation boundaries and replay

An earlier automatic Brine build
[1161926](https://concourse.home/builds/1161926), on earlier core `0b65a7f79c`,
failed before this performance commit was exercised. Its failures include
expectations for unpinned resource images after production images were pinned,
interrupted-submission cases and a live peer-read timeout. Raw failure evidence
is retained separately. This work does not alter those assertions or count a
faster failing full integration run as a success. The new success claim covers
the complete native preparation tests, not a new full-Brine pass on current core.

Pipeline validation and `git diff --check` pass. Source parity, exact test
inventories, actual package overlap, fixture-process drainage and owned-pod
cleanup are verified. [Evidence and replay instructions](../../output/brine-preparation-20261106/README.md)
include immutable source hashes, candidate patch, task recipes, raw logs,
compiler action graphs/traces, phase/cgroup samples and cleanup receipts.
The portable bundle is a local untracked artifact, as in the earlier reports.
