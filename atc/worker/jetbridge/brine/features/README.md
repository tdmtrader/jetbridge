# Feature files, tags, and the pending directory

## `@HOP-<n>`: how a Hangar scenario cites its requirement

`n` is the requirement number in the Hangar output publication spec
(`hearth/tracks/20260826T2224_hangar_output_publication_and_claims/spec.md`).
Tags go **on the scenario**, not only on the feature, and a scenario may carry
several — `@HOP-23 @HOP-25` is a scenario about dedup *and* about receipts.

The rule exists because traceability, not coverage, was the measured problem:
of the 143 archived requirement IDs, 98 are greppable as tags in this directory
and 45 are not, and the migration proposal's finding was that 137 of 143 already
had test evidence nobody could find. A citation grep can only find a tag that is
there, so `steps/vocabulary_test.go`'s
`TestEveryHangarScenarioCitesARequirement` fails any `hangar-*.feature` scenario
without one.

It is scoped to `hangar-*.feature` deliberately. The 35 files that predate the
rule are not retro-fitted here; families migrated from Go suites that never had
IDs say so in their headers and cite mutations instead.

Other families keep their own prefixes (`@CO-`, `@RF-`, `@PW-`, …); nothing
about those changes.

## `features/pending/`: checked, never run

`.brine` says `features: "features/*.feature"`. That glob does **not** reach
`features/pending/` — the mechanism is a directory that both `.brine` manifests
(this one and `live/.brine`) exclude, while every vocabulary and citation guard
still walks it, so a scenario can be checked for shape without ever being run.

As of `c96fac16a7`, `features/pending/` does not exist: the Hangar output
family it held has landed, and every `hangar-*.feature` scenario now runs from
`features/` like any other. This section documents the arrangement generically,
for the next family that needs to stage step definitions ahead of the
production code they exercise — described below as it worked for Hangar, not
as a claim about what is pending today.

The pattern applies when a family's step definitions are written — every
phrase real, registered, and returning a typed "not yet implemented in
production" error naming the phase that closes it — but the production code
behind them has not landed yet. A scenario over those steps can only be red.

Three things have to be true at once, and `features/pending/` is what makes them
compatible:

1. **No phrase may be dead.** `TestEveryStepDefinitionIsUsedByAScenario` fails on
   a definition no sentence reaches, and a family of thirty phrases defined
   ahead of its first executable scenario is thirty dead definitions.
2. **No scenario may be red in the running corpus.** CI runs
   `brine run --mode sync` over the whole glob and `tag-rc` waits on it.
3. **No scenario may be green against nothing.** A stub that returned `nil`
   would be exactly the defect `MIGRATION-EVIDENCE.md` records as a test that
   cannot fail, so every stub fails loudly instead.

Pending files are therefore held to every guard except execution:

| guard | covers `features/pending/`? |
| --- | --- |
| `TestEveryStepDefinitionIsUsedByAScenario` (dead phrase) | yes |
| `TestEveryScenarioStepResolvesToADefinition` (undefined phrase) | yes |
| `TestNoStepLineMatchesTwoDefinitions` (shadowing) | yes |
| `TestEveryHangarScenarioCitesARequirement` (`@HOP-` tag) | yes |
| `TestPendingHangarFeaturesAreNotRun` (the arrangement itself) | yes |
| `./pendingcheck.sh` (`brine check`: every chain walks) | yes |
| `brine run` (execution) | **no, by design** |
| `./timecheck.sh` (per-feature wall clock) | no — nothing runs |

**A pending scenario is not coverage.** Every file says `NOT RUN YET` in its
description and a test enforces that it does.

### Moving one up

When the phase a scenario's steps name has landed, the doer for that phase:

1. replaces the stubbed bodies in `steps/hangar_*.go` with real ones;
2. moves the scenario out of `features/pending/<file>` into `features/<file>`;
3. runs `brine run --mode sync` and `./timecheck.sh`, and records the
   `Reddened by:` mutation and its both-red result in `DISPOSITION-hangar.md`.

The `Reddened by:` comments already in the pending files come from the plan and
name the one production line and the one step that should redden. They are the
mutation to run, not evidence that it was run.

## `features/live/pending/`: the same arrangement, for the opposite reason

`live/.brine` says `features: ../features/live/*.feature`, which does not reach
a `features/live/pending/` directory either.

This directory existed for part of 2026-09-18 and is gone: commit
`388692eb54` reverted every production fix the migration had made, and the
scenarios that were red against `origin/core` as a result were parked there
rather than deleted or left red in the running corpus. The fix round
(`brine-v5-fix/prod-fixes`) re-applied the seven fixes and moved every parked
scenario back into its `features/live/` file, removing the directory.
`TestPendingHangarFeaturesAreNotRun` now asserts it stays absent and that no
running live feature carries the `NOT RUN YET` marker.

The difference from `features/pending/` was why the scenarios were there. A
`features/pending/` scenario is **ahead of production** — its plane has not
been written. A `features/live/pending/` scenario was **behind a revert**: its
production change *was* written, and a commit took it back out. Each file named
the function and file that would make it green and documented the defect core
had. Everything else was identical: `NOT RUN YET` in the Feature description,
`TestPendingHangarFeaturesAreNotRun` (which reads both manifests and both
directories) enforcing it, and every vocabulary guard applying because
`featurePaths` walks all of `../features`. Moving one up meant restoring the
production change the file named, then moving the scenario back into
`features/live/<file>`.

`./pendingcheck.sh` still covers only `features/pending/`; the live tier's
`brine check` runs on the cluster image, not here.
