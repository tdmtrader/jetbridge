// Package conformance is the strict-GCS conformance suite the output plane's
// four cloud roles share.
//
// It exists because Requirement 41 says the profile must pass conformance
// before an epoch may be activated, and a conformance claim is only worth what
// the substrate it ran against can actually refuse. So the suite runs over two
// named tiers and says which is which in every failure:
//
//   - **Tier 1**, hangar/gcstest's in-memory client, is the fault-injection
//     tier: a timeout, an upload whose response was lost, a delete whose
//     response was lost, a truncated body and a cancelled context. A
//     server-backed fake cannot produce those on demand, and a test that
//     waited for them would be a test that hangs.
//
//   - **Tier 2**, fsouza/fake-gcs-server reached through the real
//     hangar/gcs adapter, is the API tier: create-if-absent, exact
//     GenerationMatch on get and delete, a body-capable get used as an
//     exact-generation stat, bucket-wide list with a server-derived prefix,
//     pagination, metageneration, immutable-at-creation metadata, a same-key
//     new generation, and the 404/412/403 split. The in-memory fake cannot
//     answer any of those honestly, because it would be answering about
//     itself.
//
// Tier 2 never silently degrades to tier 1. When HANGAR_CI is set it *fails*
// with a named reason if no endpoint is configured or the endpoint does not
// answer, because a skipped run reported as conformance is worse than no run.
// Outside CI it prints the same reason; on a developer machine with no
// endpoint configured it starts fake-gcs-server in-process instead, so the API
// tier is exercised on the branch rather than only after merge.
//
// What the suite proves about roles is narrow and stated in the assertions
// themselves: the *code* issues only its role's RPCs. It is not evidence about
// IAM. No fake enforces a binding, so a green here says the publisher never
// calls delete, not that the publisher's service account could not. AC 16's IAM
// honesty and AC 17's lifecycle-rule behaviour are real-GCS observations,
// recorded with their date and project in Phase 9.
package conformance
