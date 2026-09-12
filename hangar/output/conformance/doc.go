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
//     GenerationMatch on get, a body-capable get used as an exact-generation
//     stat, bucket-wide list with a server-derived prefix, pagination,
//     metageneration, immutable-at-creation metadata, a same-key new
//     generation, and the 404/412/403 split. The in-memory fake cannot answer
//     any of those honestly, because it would be answering about itself.
//
//     Delete preconditions are NOT in that list, and the omission is measured
//     rather than assumed: fake-gcs-server v1.52.3 deletes whatever is at the
//     key whether or not the delete carries a generation pin or a
//     GenerationMatch. probeCapabilities measures it, knownSubstrateGaps names
//     it, the conditional-delete case is gated on the measured capability, and
//     tier 1 and real GCS (Phase 9) are where that row is answered.
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
//
// # What the real-GCS observation must gather, and why each item is here
//
// The Phase 9 box enumerates six items, and the review that found the IAM
// traversal defect established that none of the six would have revealed it:
// they verify each principal's EFFECTIVE permissions by attempting operations
// and observing 403s, and no number of those says whether the attestor's own
// parse of the policy agrees with the policy. These are the additional items,
// each with the failure it would have caught. They are here, in code, because
// the next person designing that observation reads this package rather than a
// phase document.
//
//  1. **The attestor's derived bindings against a real policy dump.** Run the
//     real attestor against the real bucket and diff its PrincipalBindings
//     against `gcloud storage buckets get-iam-policy gs://<bucket>
//     --format=json`, WITH a deliberately conditional binding added and then
//     removed -- a `resource.name.startsWith(...)` prefix scope is the standard
//     spelling and is exactly what Req 54 calls an activation failure. A
//     duplicate role binding belongs in the same dump. Both were invisible to
//     the traversal while all six enumerated items passed.
//
//  2. **The role table against the live role definitions.** `gcloud iam roles
//     describe roles/storage.{objectCreator,objectViewer,objectUser,objectAdmin,
//     legacyBucketReader,legacyBucketWriter,bucketViewer,legacyObjectReader,admin}`
//     diffed against permissionsOf. That table is a SNAPSHOT OF SOMETHING
//     GOOGLE OWNS, dated in its own comment, and nothing in this repository can
//     notice Google changing a predefined role.
//
//  3. **A benign configuration that wedges the reclaimer.** Item 4 of the box
//     proves a stale metageneration 412s; nothing proves that a spec-PERMITTED
//     bucket configuration produces one. Put a SetStorageClass lifecycle rule
//     (or Autoclass) on the bucket, let one object transition, observe the
//     generation unchanged and the metageneration incremented. No fake in this
//     tree can move a metageneration on its own -- which is a third unreachable
//     class beside lifecycle rules and IAM, and the one that was costing a
//     silent production bug.
//
//  4. **Versioning, soft delete and retention.** `gcloud storage buckets
//     describe` showing `versioning.enabled: false`, the `softDeletePolicy`,
//     and the absence of a retention policy or default hold -- and activation
//     should refuse a bucket with versioning on or a retention policy set.
//     `reclaimed_confirmed` claims the object is gone and SOFT DELETE retains
//     it (seven days by default on new buckets), so the claim is an
//     overstatement; a retention policy 403s every delete, which becomes
//     RecordRuntimePrincipalDenial and a FALSE platform-principal mismatch.
//
//  5. **`storage.objects.create` as an overwrite.** Item 2 of the box reads;
//     this writes. As the publisher principal, overwrite a published object
//     with no precondition and observe it succeed. GCS has no separate
//     content-update permission, so create IS the overwrite permission and the
//     publisher can destroy a published object without holding delete. That
//     observation is what turns the chart's honesty sentence from an argument
//     into a documented fact.
//
//  6. **One bucket-not-found delete.** The box's 404s are per-object. Delete
//     into a bucket that does not exist and confirm what the plane does with
//     the answer: the JSON API says "not found" for a missing bucket and a
//     missing object alike, so the reclaim path cannot tell them apart and the
//     inference has to come from the job's own attempt history.
//
//  7. **The XML read path.** Items 2-5 exercise the JSON API. Production reads
//     go over the XML API, because the emulator path forces
//     storage.WithJSONReads() and the production path does not, so translate's
//     404/412/403 split has only ever been exercised against JSON-API error
//     shapes. One body read through the production seam closes it.
//
//  8. **A list page's continuation token.** Req 44's decoded-metadata budget
//     bounds what a pass processes and not what the SDK decodes; the fix is one
//     line (`PageInfo().MaxSize`) and is withheld because fake-gcs-server
//     truncates to maxResults and returns no nextPageToken, so setting it makes
//     every tier-2 sweep stop at the first page. Send maxResults against the
//     real API, confirm the token, then set it.
package conformance
