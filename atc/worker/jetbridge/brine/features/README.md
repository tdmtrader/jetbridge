# Feature files and tags

## `@HOP-<n>`: how a Hangar scenario cites its requirement

`n` is the requirement number in the Hangar output publication spec
(`hearth/tracks/20260826T2224_hangar_output_publication_and_claims/spec.md`).
Tags go **on the scenario**, not only on the feature, and a scenario may carry
several — `@HOP-23 @HOP-25` is a scenario about dedup *and* about object markers.

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
