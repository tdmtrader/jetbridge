# Whole-estate performance candidate

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

This is an unpublished review candidate based on `test-performance` at
`ee723325af6b66ff4224d2b40a372939f38c1a5e`. The target is a 20% reduction
in the sum of serial execution times across all 20 required test tiers, with
all original tests, assertions, coverage, deadlines and parallelism retained.
The target is not yet proven.

## Changes beyond the published unit and Brine work

- Skip initdb's final disk sync for disposable PostgreSQL data, retaining
  each real server lifetime and the existing non-durable server settings.
- Cache identical architecture dependency queries within the test process.
- Coalesce overlapping scheduler wakeups without losing a newer committed
  request; exercise the behavior against the real database.
- Serialize artifact registry persistence and skip generations already saved
  by another writer; retain retry behavior after real write failures.
- Stream Docker image exports into the owned K3s container's image importer.
  Wait for both processes and cancel the other process when either fails.
- Release test-only step-pod waits after their original inspection assertions
  pass. Keep the original timers as fallbacks and retain build-success checks.
- Cache the live artifact daemon payload after checking its actual file
  contents; give each caller an independent reader.
- Let the metrics-reader fixture respond to termination while preserving its
  image, read-only metrics volume, collector configuration and grace period.

Common live-fixture corrections are also explicit in this tree: pause-pod
recovery setup, OOM readiness, sidecar log collection, and starting the real
metrics exporter within the collector fixture's existing observation window.
These were applied to both sides of their comparisons. They receive no
performance credit.

The scheduler retains its existing 10-second fallback, supplied by
`constructMembers` in `atc/atccmd/command.go`. Current migrations have
removed the old database component-interval column; no SQL interval tuning
is part of this candidate.

## Evidence and limits as of 2026-09-26

The earlier, broader source pair validated 13 of the 20 tiers:
8,278.339 seconds before versus 7,354.713 seconds after, saving 923.626 seconds
(11.16%). Its full Brine local/live coverage run retained all 1,324 cases and
measured production statement coverage, and took 2,146.256 versus 1,741.334
seconds. Those figures belong to that earlier source pair.

Before the clean behavioral comparison, the combined source pair had eleven validated tiers. Its complete unit
selection passed twice in opposite run orders, preserving the original
package and case outcomes in all 113 packages. Baseline samples were
1,554.855 and 1,559.008 seconds; candidate samples were 1,155.514 and
1,007.910 seconds. Their means are 1,556.931 and 1,081.712 seconds, a
475.219-second saving (30.52%). Both samples are retained.

At that stage, using that unit mean, the incomplete eleven-tier subtotal was 4,021.030
seconds before versus 3,132.796 seconds after: 888.234 seconds saved
(22.09%). Regressions in Elm and Brine kubelet remain in that sum. This is
not a whole-estate result or a result for this assembled review tree.

| Complete validated selection | Baseline seconds | Candidate seconds | Reduction |
| --- | ---: | ---: | ---: |
| Unit, mean of both run orders | 1,556.931 | 1,081.712 | 30.52% |
| Brine native | 204.372 | 108.838 | 46.75% |
| Kubernetes integration | 1,712.667 | 1,399.439 | 18.29% |

Brine native retained all 255 original passing outcomes and added three real
filesystem guards. The integration comparison retained all 135 original
outcomes (129 passed and six pending) on both sides. Its original 30-minute
suite deadline, serial execution, source and binary hashes, runtime images,
and owned-resource cleanup were independently checked. Identical diagnostic
hooks on both sides run only after a failure; neither side invoked them.

Earlier failed attempts remain recorded and excluded from timing totals.
Two subsequent full Brine local/live candidate attempts each passed 1,323
of 1,324 original scenarios, with all original scenarios started and all
1,324 disposers completed. Both baselines passed all 1,324 scenarios and
7,593 steps. Neither pair qualifies as performance evidence.

The first candidate failed a pause-pod eviction prerequisite. Four subsequent
full original lifecycle-feature trials passed all 40 scenario executions,
including twelve real evictions; they did not reproduce that failure and
receive no timing credit. The second candidate failed while creating a watch
fixture pod: Kubernetes rejected missing CPU/memory requests and limits before
the watch behavior ran. The evidence suggests a race between creating a
namespace LimitRange and admission seeing its defaults. An isolated common
fixture correction takes defaults from the created LimitRange and applies
them explicitly to both watch-pod constructors. The original control and four corrected trials passed the complete original
watch feature, including actual admitted-resource equivalence and namespace
cleanup checks. The correction is now included here and applied identically
to both sides of the next full Brine comparison. That full original coverage
comparison remains required; the focused trials receive no timing credit.

The next complete Brine attempt stopped in its native prerequisite on both
sides, before either coverage command started. The common watch helper
indexed an empty LimitRange response in an original namespace-cleanup test.
That failed attempt and its incomplete inventories remain retained and
excluded from performance evidence. The revised helper selects the Container
limit by type and preserves the original empty-response behavior, without
changing the test or its assertions. The complete original native steps
package passed on both sides (193 baseline and 196 candidate outcomes).
The original-control and four corrected live watch trials also passed all
30 scenario executions, 125 steps and 35 actual admitted-resource checks,
with all disposers completed and owned namespaces and cluster removed.
These diagnostics earn no timing credit. A new complete native plus
1,324-scenario, 7,593-step coverage comparison is still required.


An earlier behavioral comparison had a failed baseline and a passing
candidate. The subsequent comparison completed all 304 original outcomes on
both sides: 300 passed, three skipped and one pending. Its timings are
excluded because local algorithm tests and database audits overlapped the
baseline on the same kernel. The correctness results remain retained. A clean
full comparison subsequently passed, as recorded below. All heavy local and
runner work is serialized, including compilation. The excluded timings remain
excluded and are not used in either subtotal.

The earlier image-import diagnostic used the larger, uncompacted candidate
image: original imports averaged 160.198 seconds and streaming imports
76.342 seconds. A later original/streaming/streaming/original comparison
used the actual compact image from the accepted integration pair, on four
fresh clusters. Original imports averaged 112.526 seconds and streaming
imports 52.331 seconds (53.49% less). All eight actual image IDs, original
native deadlines and concurrency, two real failure guards per run, and
owned-cluster cleanup were verified. These focused setup observations support
the implementation; their savings are not added separately to the subtotal.

Two repetitions of the real BusyBox reader diagnostic reduced shutdown from
approximately 30 seconds to approximately 0.1 seconds while preserving
access to the same metrics file. The clean full behavioral comparison passed with this change included.
Its effects are counted within that complete suite result, never separately.

The measured candidate image had accumulated obsolete binary layers during
iterative benchmark builds. Removing overwritten payloads reduced its Docker
image size from 1,142,112,241 to 573,154,671 bytes. Independent exports of four
never-started containers matched all 3,961 paths, file contents, and metadata,
with ten creation-time timestamps independently reproduced within each image.
This corrects benchmark image history; it is not a claimed repository
optimization or a separately counted test-time saving.

A further scheduler-fixture import change orders COPY writes by build and
resource using a separate index permutation. Each row keeps its original
index-derived name and all original values. Eight complete 98-case algorithm
runs in ABBA and BAAB order passed on local PostgreSQL 15. Those runs
overlapped a behavioral benchmark on the same kernel, so their timings are
excluded as performance evidence. The size of any contention effect is unknown.
Two additional full-suite audits matched all 1,858,425 stored input/output
rows across the six original fixtures, including every column, and matched
column definitions, constraints and indexes. This change is staged only in
the review tree. The complete assembled review-r4 unit comparison passed all
original package and case outcomes; no separate importer saving is added to
the eleven-tier subtotal above.

A separate full comparison of the assembled review-r4 unit selection passed
all 113 original packages on both sides, with the exact original case
outcomes retained and the added real filesystem guards passing. It measured
1,127.569 seconds before versus 971.703 seconds after (13.82% reduction).
This is a distinct source pair and is not combined with the combined-source
subtotal. Its unchanged-source baseline was materially faster than the
earlier unit samples; the cause of that variation is unknown. The review-r5
update changes only this evidence document, with every other source file
verified identical to the tested review-r4 snapshot. Later review revisions
include the common watch fixture correction described above. The subsequent
complete unit validation is recorded below; consistent whole-estate validation
remains incomplete.

The subsequent complete unit comparison used the assembled review-r8 sources,
including the revised watch fixture and disposable initdb optimization. All
113 original packages passed on both sides, with exact original case outcomes
retained: 6,040 Ginkgo and 2,185 native records on the baseline, and 6,049
Ginkgo and 2,189 native records on the candidate. It measured 1,133.190 seconds
before versus 949.937 seconds after, saving 183.253 seconds (16.17%). The
independent audit verified all 3,706 source files; a separate cleanup check
found no owned PostgreSQL processes or data directories. This is a complete
unit result, not a whole-estate result, and is not mixed into the older
combined-source subtotal. Its baseline was within 5.622 seconds of the prior
assembled comparison; that does not establish the cause of earlier variability
or attribute the entire difference between runs to initdb. The review-r9
update changed only this evidence document; its other source files were
byte-identical to the tested review-r8 snapshot.

The clean behavioral comparison retained all 304 original outcomes on both
sides: 300 passed, three skipped and one pending. It measured
2,367.993 versus 2,171.666 seconds, saving 196.327 seconds (8.29%).
Source, binary, runtime configuration, command budgets and cleanup checks
passed; neither side invoked failure diagnostics. Raw evidence remains on
the runner and its retained archive was independently read back.

This expands the incomplete combined-source comparison to twelve of twenty
tiers: 6,389.023 seconds before versus 5,304.462 seconds after,
saving 1,084.562 seconds (16.98%). Regressions remain included. This is
below the 20% goal and is not a final assembled-tree or whole-estate result.

## Remaining validation

The required tiers are unit, Brine native, Kubernetes integration, Kubernetes
behavioral, Fly integration, Elm, Brine kubelet, Go live/hangar_live, OTel
connectivity, Compose LDAP, Brine local/live coverage, Python diagnostics,
OAuth lifecycle, TestFlight, Compose credentials, Compose pauser, Compose ops,
WATS, subscription-model tests and tool-shape-model tests.

Eight tiers have no accepted comparison for the combined candidate yet:
Brine local/live coverage, TestFlight, Compose
credentials, Compose pauser, Compose ops, WATS, and the two model-backed tiers. Model-backed
execution also awaits separate authorization for account usage and data
egress; cluster-testing approval does not resolve that gate.

This assembled tree needs its own consistent frozen baseline/candidate
validation. Diagnostic-only kubelet probes and manifest-path/hash overrides
used by the measurement harness are omitted from the review code. Original
runtime configuration and test coverage still need to be checked across all
20 tiers. Failed, interrupted, focused and partial runs cannot establish the
goal. Regressions must remain in the sum; Brine native execution must be
counted only once. Run the repository's documented CI checks before offering
this branch for merge.


The subsequent complete Brine comparison passed all 1,324 baseline scenarios,
but its candidate passed 1,323 and failed the original dead-pause-pod replacement
case (row 6) while waiting for real kubelet eviction. All 1,324 scenario disposers
completed on both sides, and owned cluster cleanup was independently verified.
The recurring failure excludes the entire comparison from timing credit.
That failed comparison did not change the original deadline, assertions or
kubelet configuration. A later phase-targeted diagnostic reproduced the same
original case failure while the kubelet returned a single 4 KiB volume sample
for 90.507 seconds after observing the real 24 MiB write. This proves the
stale-sample failure in the diagnostic; historical failures without sampled
volume evidence remain an inference.

Changing only the disposable kubelet's volume collection interval to five
seconds passed four complete original lifecycle-feature trials in baseline /
candidate / candidate / baseline order: 40 scenario executions, 128 steps and
12 real evictions. Both sources encountered an initially below-limit sample
and observed it refresh; the longest stale interval after an observed write
was 8.522 seconds. Original assertions, payloads, volume limits, deadlines and
serial execution were retained. Independent audits verified complete drains,
owned cleanup and retained evidence. These focused trials add no estate timing
credit and used the earlier Brine source pair, before the initdb change.

Review-r10 includes the verified disposable K3s configuration at
`atc/worker/jetbridge/brine/fixtures/k3s.yaml` and its usage notes. A fresh
cluster on the pinned K3s image loaded the file with no kubelet CLI override,
reported `volumeStatsAggPeriod=5s` and unchanged `syncFrequency=1m0s`, became
ready and cleaned up successfully. This is a common fixture correction for
both sides of future comparisons. The existing in-cluster CI task does not
automatically consume this file. All Go runtime and test sources remain
byte-identical to the complete unit validation; full Brine validation with
the current assembled sources and the explicit fixture remains outstanding.

The current review also skips initdb's final disk sync for the disposable
PostgreSQL fixture, whose running server already disables durability. All
per-test server lifetimes, real database operations and assertions are retained.
Four complete original postgresrunner package trials in baseline/candidate/
candidate/baseline order passed all seven cases and independently checked
process and data-directory cleanup. Mean package time was 8.167377s before and
6.3504335s after (22.25%); this is diagnostic evidence only and adds no estate
timing credit. The complete unit comparison with this change passed as
recorded above. Affected Brine/live validation and a consistent twenty-tier
comparison remain required. Brine shares one PostgreSQL server per suite, so
the package startup improvement cannot be extrapolated to every scenario. The
accepted twelve-tier subtotal above continues to describe its older measured
sources, not this new assembly.
