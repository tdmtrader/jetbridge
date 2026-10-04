# Entire test estate performance work

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

The target is a 20% reduction in total serial test execution time across every
tier, retaining every existing test, assertion, real dependency and deadline.
It has not yet been demonstrated. Compute the result from summed elapsed
times, not an average of suite percentages. Historical snapshots and different
baselines must not be combined into a whole-estate claim.

Additional measured improvements beyond the published unit and local Brine work:

| Change | Baseline | Candidate | Validation scope |
| --- | ---: | ---: | --- |
| Build the chart guard's seven binaries in one Go invocation | 44.08 s | 41.42 s | Full package, 244 matching native records |
| Same chart comparison in reverse order | 44.06 s | 41.36 s | Full package, same application source |
| Stop SSE replay at the protocol end event | 70.79 s | 10.16 s | Two unchanged original tests on real K3s |

The SSE figures sum the two test cases. Including fresh cluster setup and
cleanup, the focused commands took 216.51 s and 151.66 s. Both used the same
frozen d01185fbc application image and fixtures; only the candidate SSE helper
changed. That helper's original source is identical on current core 08e8be2af.
This remains focused validation, not a current-core full-suite comparison.
The helper retains the 30-second failure deadline and reads every event before
the terminal frame.

A default Eventually polling change showed no measurable improvement over the
full behavioral suite and was reverted. A faster supervisor completion check
was also reverted: real BusyBox boundary cases showed lost trailing output.
Neither experiment counts toward the target. Six explicit readiness polling
changes remain unvalidated.

Seven original Python diagnostic tests and the original real OAuth lifecycle
probe passed on both current-core roots. OAuth showed no speedup. Full current-
core images, matching complete tier inventories and outcomes, coverage checks,
and the runtime-weighted final comparison remain required.
