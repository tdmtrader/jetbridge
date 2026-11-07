# Performance investigations

The dated reports describe the source revisions and CI runs named in each
report. Links under `output/` refer to locally retained, untracked profiling
artifacts, rather than files included in a source checkout. Each investigation's
evidence directory contains a portable bundle, checksums and replay scripts.
CI build links remain in the reports.

Start with the [2026-11-02 CI test-suite review](ci-test-performance-2026-11-02.md).
It covers core `1d0e377594`, the observed automatic pipeline critical path,
historical cohorts and retries, fresh CI profiling, and ranked optimization
experiments.

The [2026-11-03 Brine scheduling follow-up](brine-overlap-2026-11-03.md)
implements the highest-value overlap opportunity and records a matched CI pair
with the complete real test inventory and coverage gate preserved.

The [2026-11-05 real eviction profile](ci-eviction-profiles-2026-11-05.md)
attributes the recovery tests' delay to kubelet volume accounting using eight
repeated real integrations on disposable nodes. Its replayable evidence is in
`output/eviction-profile-20261105/evidence-bundle.tar.gz`.

The [2026-11-05 recovery scheduling follow-up](brine-recovery-overlap-2026-11-05.md)
overlaps the complete recovery feature files at the existing kubelet settings.
Its matched full-suite runs saved another 5m24s (20.1%), preserving every
scenario, step and covered production block. Evidence is in
`output/brine-recovery-20261105/evidence-bundle.tar.gz`.

The [2026-11-05 live-group rebalance](brine-balance-2026-11-05.md) moves another
32 unchanged scenarios into the existing recovery group. Its matched CI pair
saved 5m29s (25.5%), with both live groups finishing within five seconds and all
1,050 scenarios, 5,687 steps and covered production blocks preserved. The next
target is shared preparation before integrations start. Evidence is in
`output/brine-balance-20261105/evidence-bundle.tar.gz`.

Supporting evidence:

- [Pipeline structure and test inventory](ci-suite-structure.md)
- [CI history and critical path](ci-history-analysis.md)
- [Unit tests and focused package profiles](ci-unit-profiles.md)
- [Brine local, live and cross-node timings](ci-brine-profiles.md)
- [Kubernetes behavioral suite](ci-behavioral-profiles.md)

Earlier controlled optimization studies use different source revisions,
toolchains and test inventories. Their results remain useful but are not the
current CI baseline:

- [Serial Go test performance](serial-go-tests.md)
- [Brine test performance](brine-tests.md)
