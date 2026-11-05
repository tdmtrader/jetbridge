# Performance investigations

The dated reports describe the source revisions and CI runs named in each
report. Links under `output/` refer to locally retained, untracked profiling
artifacts, rather than files included in a source checkout. The portable
bundles are `output/ci-performance-20261102/evidence.tar.gz` and
`output/brine-overlap-20261103/evidence-bundle.tar.gz`; their directories also
contain checksums and replay scripts. CI build links remain in the reports.

Start with the [2026-11-02 CI test-suite review](ci-test-performance-2026-11-02.md).
It covers core `1d0e377594`, the observed automatic pipeline critical path,
historical cohorts and retries, fresh CI profiling, and ranked optimization
experiments.

The [2026-11-03 Brine scheduling follow-up](brine-overlap-2026-11-03.md)
implements the highest-value overlap opportunity and records a matched CI pair
with the complete real test inventory and coverage gate preserved.

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
