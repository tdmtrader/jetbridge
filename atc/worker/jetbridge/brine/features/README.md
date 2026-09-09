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
`features/pending/`, and that is the point.

The Hangar output family's six feature files describe a plane that does not
exist yet. Their step definitions are written — every phrase in
`steps/hangar_*.go` is real, registered, and returns a typed
"not yet implemented in production" error naming the phase that closes it — but
the production code behind them lands across Phases 2 through 8. A scenario over
those steps can only be red.

Three things had to be true at once, and `features/pending/` is what makes them
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
