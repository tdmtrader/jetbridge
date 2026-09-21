# Per-scenario mutation evidence: the Hangar output family

The legend and the columns are `DISPOSITION-jetbridge.md`'s (`:11-27`), turned
around. That file records what evidence killed a DELETED Go test. This one
records, for each of the 58 scenarios in the six `hangar-*.feature` files plus
the fixture's own, what mutation it claims to be pinned by and what a real run
with that mutation applied actually did.

Written in Phase 9 from a real `brine run --mode sync` per mutation, one
mutation at a time, applied and then reverted, with `shasum` compared on every
touched file afterwards and `git status` clean at the end. A green suite cannot
produce this table.

## Verdict key

- **MEASURED** — the mutation named by the scenario (or the one this phase chose
  for it) was applied to production and a real run reddened the named step. The
  row says which step.
- **BROADER THAN NAMED** — it reddened, but also reddened scenarios the comment
  says would stay green. Recorded because that is the fourth residual pattern
  the learning warns about: a mutation that reddens everything proves nothing
  about which scenario carries which behaviour.
- **DIFFERENT STEP** — it reddened the scenario, on a step other than the one the
  comment names. Recorded because it looks exactly like success.
- **UNPINNED** — the mutation reddened NOTHING. Written down beside the claim it
  disproves, not quietly dropped, and the scenario is not quietly deleted.
- **NOT MEASURED IN PHASE 9** — no mutation in this phase's set targets the
  scenario directly. Most such scenarios were nonetheless reddened transitively
  by a mutation aimed at a sibling, and the row says so.

## The run

- brine CLI: `~/brine-private/target/debug/brine`, **version `brine 0.1.0`**.
  It is NOT on `PATH` and it can vanish — `cargo build` remakes it in about
  36 s. A missing CLI writes an EMPTY log, so a harness that greps a log for
  failures reports a clean pass for a run that never executed. This table's
  harness reads the JSON event stream and treats a run with no `run_end` event
  as a failure for exactly that reason.
- adapter: rebuilt (`go build -o .build/brine-adapter-jetbridge
  ./cmd/brine-adapter-jetbridge`) before **every** mutation, because a stale one
  silently tests old steps.
- 23 mutations applied and reverted, each against the ONE feature file whose
  scenario names it, so a red in another family cannot be mistaken for evidence.
- 32 of the 58 scenarios were reddened by at least one of them.

## Three findings, before the rows

### 1. Three mutations reddened NOTHING

| mutation | the claim it disproves | why |
| --- | --- | --- |
| **M22** — the hold handler takes the request's incarnation as authoritative instead of issuing it | `The daemon acknowledges a hold for a server-issued source incarnation` (`@HOP-3 @HOP-7`), whose comment names exactly this mutation | **16 passed, 0 failed.** No scenario in the family ever OFFERS an incarnation different from the one the daemon reserved, so the equality this mutation removes never fires. Req 7's "a handle string alone is never an identity" has a phrase for a PATH (`the hold request names a path instead of an incarnation`) and none for a wrong incarnation. The behaviour is real and is pinned in Go; brine does not pin it, and the comment says it does. |
| **M42** — `pre_reservation_cancel` forks on the acknowledged hold instead of the reserved source | `A cancelled handoff with no acknowledged hold releases its reservation and closes` (`@HOP-11`), whose comment names exactly this mutation | **10 passed, 0 failed.** The scenario's own state has a reservation AND no acknowledged hold; removing the `!Reserved()` fork changes the answer only for a handoff that reserved NOTHING, which no scenario reaches. |
| **M50** — the Ed25519 signer omits `Generation` from the signed claim set | `A sealed source publishes one marked object and a receipt naming its scope, digest and generation` (`@HOP-21 @HOP-22 @HOP-25`), whose comment names exactly this mutation and says the whole-receipt check reddens on its one line | **9 passed, 0 failed, and it can never be otherwise.** Signer and verifier both call `CanonicalReceiptBytes`, so dropping a field there is self-consistent: every signature this run produced still verified. What that mutation reddens is the Go golden fixtures (`protocol_golden_test.go`, `receipt_coverage_test.go`), not brine. The `Reddened by:` line is mis-stated rather than the scenario being weak — the scenario DOES assert the whole receipt, and M55 reddens it. |

None of the three scenarios is deleted. Two of them (M22, M42) name a real
behaviour with no brine discriminator, which is a scenario brine owes rather
than a scenario to remove; the third names the wrong runner.

### 2. Two mutations were much broader than their comment claims

**M49** (the publish handler takes its scope from the request's bucket, plus the
caller-namespace refusal removed) and **M53** (create-if-absent drops its
`DoesNotExist` precondition) each reddened **8 of the 9** publication scenarios.
Their comments each say "one step reddens ... and the control line above it
stays green". That is true of the narrower single-line forms the comments
describe; it is not true of the forms applied here, and the difference matters:
a mutation that reddens the whole file says the file moves together and says
nothing about which scenario carries which behaviour. Recorded as BROADER THAN
NAMED on every row it touched.

**M38** (Stage 2 commits the reservation already `resolved`) reddened 5 of the 10
disposition scenarios, including the three announcement ones its comment does not
mention.

### 3. Thirty-two of the fifty-eight scenarios name no mutation at all

Convention 1 as the plan states it is "a `Reddened by:` comment naming one
production line and the one step that reddens". Twenty-six scenarios carry one;
**thirty-two do not**. Most of those are the second half of a pair whose comment
sits above the first, which is defensible — but it is not what the convention
says, and a reader cannot tell a deliberately-shared comment from a missing one.
Every such row below says so explicitly rather than leaving the column blank.

This is a finding about the FILES, not about the behaviour: 26 of the 32 were
reddened here anyway, transitively, by a mutation aimed at a sibling.

### 4. Added after an independent review of Phase 9

Two adversarial reviewers were run over this phase's diff. Two findings land on
this table:

- **`An exact replacement generation supersedes the old one, and the old ref no
  longer resolves` has no `Then` a production mutation can redden.** The
  replacement is published through the DAEMON -- `captureAgain` admits,
  reserves, holds, seals and publishes, and stops -- so nothing settles it and
  no lifecycle row exists for the new generation. Asserting the registration a
  second time after the replacement was tried and **reddens**, correctly,
  because there is nothing to find. Its one product assertion is the
  different-generation check inside the `When`, which surfaces as a step ERROR
  rather than a failing `Then`. The M55 row below is the scenario's real
  evidence, and it reddens on the FIRST `Then` -- the control -- rather than on
  the replacement half. The feature file now says all of this; closing it needs
  a second control-plane settle this family does not have.

- **`A capture-selected step's strict input is untouched by the output plane`
  was unbounded.** `strictInputMaterializationIsUnchanged` asserted "at least
  one request names the ref"; a plane that ADDED a bogus request beside the
  right one passed. It now requires exactly one, which is what the scenario
  declares.

---

## The rows


### `hangar-binding.feature`

**A consumer binds the output and acquires its claim in one transaction**  `@HOP-30`  (:13)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A rolled-back consumer transaction leaves no claim behind, while the committed one leaves exactly one**  `@HOP-30 @HOP-32`  (:27)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Releasing the claim twice is idempotent and the tombstone is permanent**  `@HOP-32`  (:43)
  - **Reddened by (as the file states it):** ReleaseClaim deleting the claim row instead of writing the tombstone — the permanence line reddens while the idempotence line above it stays green.
  - **MEASURED M02** — `ReleaseClaim deletes the row instead of tombstoning` — RED at `the tombstone is permanent`

**Re-acquiring a released claim ID is a typed conflict, and a fresh ID still succeeds**  `@HOP-32`  (:59)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **MEASURED M02** — `ReleaseClaim deletes the row instead of tombstoning` — RED at `the binding is refused as "lifecycle conflict"`

**A hidden-to-published transition keeps the same candidate claim ID**  `@HOP-29`  (:74)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A claim for an unregistered tree ref is refused, and the registered one is granted**  `@HOP-28 @HOP-34`  (:86)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**The binding is not visible before verification, and is visible after**  `@HOP-28 @HOP-34`  (:99)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.


### `hangar-capture-pod.feature`

**Selecting capture for a declared output puts the hold init container before every writer**  `@HOP-1 @HOP-3 @HOP-12`  (:20)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A task whose output is not selected for capture builds the pod it builds today**  `@HOP-1 @HOP-59`  (:43)
  - **Reddened by (as the file states it):** Container.buildPod adding the capture control init when DurableOutputCapture is nil, and equally by adding it whenever the output plane is enabled.
  - **DIFFERENT STEP — M08** — `buildCaptureControlInitContainer emits the init with no capture selected` — RED at `the pod carries no output-plane container`. The comment says it "reddens on its init-count line"; **this scenario has no init-count line** — its steps are `the pod has 3 volumes`, `the pod carries no output-plane container`, the mount-resolution line and the mount line. The behaviour is pinned; the comment names a step that is not there.

**The capture control init is the only container that carries the source-control grant**  `@HOP-24`  (:74)
  - **Reddened by (as the file states it):** ` was off by one line for exactly that reason.  Reddened by: buildPod copying the source-control grant env into the main container — the PRESENCE line reddens ("the source-control grant is carried by [hangar-capture-control main]").  Reddened by, for the absence line's own vector: buildPod copying t
  - **MEASURED M09** — `buildPod copies the source-control grant into the main container` — RED at `the capture control init is the only container carrying the source-control grant`

**Exactly one declared output is selected, and a second selection is refused**  `@HOP-1`  (:84)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A capture pod carries the base control handshake and the exact Downward API pod and node fields**  `@HOP-3 @HOP-58`  (:95)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A capture pod's mounts all resolve to a declared Volume**  `@HOP-12 @HOP-20`  (:107)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A capture pod requires both ready labels, and neither alone admits it**  `@HOP-58`  (:136)
  - **Reddened by (as the file states it):** BuildAffinity emitting only concourse.dev/hangar-output-v1 and dropping concourse.dev/hangar-execution-control-v1 — this reddens on the SECOND line (the count falls to 1) while the ordinary-pod control above stays green.  The node line is the Phase 4 round-2 ruling 3: a reservation is issued by ONE
  - **MEASURED M13** — `BuildAffinity drops the execution-control ready label` — RED at `the capture pod requires 2 ready labels`

**A worker whose output facet is not enabled builds no capture pod, while the base-only cohort still builds an ordinary one**  `@HOP-58 @HOP-59`  (:160)
  - **Reddened by (as the file states it):** Container.buildPod dropping the OutputPlaneEnabled arm, so a base-only cohort builds the capture pod anyway — the refusal line reddens and the ordinary-pod control stays green.
  - **MEASURED M14** — `buildPod drops the OutputPlaneEnabled arm` — RED at `the pod build is refused saying "output facet is not enabled"`

**A ready label without a matching handshake admits nothing, while the handshaken cohort admits**  `@HOP-58`  (:184)
  - **Reddened by (as the file states it):** Container.buildPod dropping the activation-epoch arm — the refusal line reddens and the matching-epoch control stays green.
  - **MEASURED M15** — `buildPod drops the activation-epoch arm` — RED at `the pod build is refused saying "speaks for epoch"`

**A capture-selected step's strict input is untouched by the output plane**  `@HOP-19 @HOP-59`  (:216)
  - **Reddened by (as the file states it):** BuildFetchInitContainers skipping its HangarTree branch when the output plane is enabled -- the strict-input init disappears and the capture scenarios above stay green.
  - **MEASURED M16** — `BuildFetchInitContainers skips the HangarTree branch when the plane is on` — RED at `the pod's strict-input materialization is unchanged by the output plane`


### `hangar-consumer-pod.feature`

**The consumer's Hangar init verifies exactly the receipt for its TreeRef**  `@HOP-26 @HOP-35`  (:17)
  - **Reddened by (as the file states it):** BuildFetchInitContainers base64-encoding a receipt whose Generation has been zeroed before it reaches the init command — the exact receipt line reddens and the mount scenarios stay green.
  - **MEASURED M17** — `the expected receipt's Generation is zeroed before it reaches the init` — RED at `the consumer's Hangar init verifies exactly the receipt for its tree`

**Each verified tree gets one fixed read-only verification mount**  `@HOP-26 @HOP-37`  (:30)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A user-controlled destination never enters the verification command**  `@HOP-7 @HOP-26`  (:44)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A consumer pod asks for exactly the receipt's tree**  `@HOP-35 @HOP-37`  (:57)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Without an active claim the consumer's read is refused, while the same read with a claim succeeds**  `@HOP-35 @HOP-36`  (:72)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.


### `hangar-daemon-handoff.feature`

**The daemon acknowledges a hold for a server-issued source incarnation**  `@HOP-3 @HOP-7`  (:21)
  - **Reddened by (as the file states it):** the hold handler deriving the incarnation from the request instead of issuing it — the acknowledgement line reddens.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.
  - **UNPINNED — M22** — the mutation this scenario's own comment names was applied and reddened NOTHING (0 failed). No scenario offers an incarnation different from the reserved one, so the equality this mutation removes never fires. brine owes a discriminator; the behaviour is pinned in Go.

**The daemon witnesses a natural finish, and the witness is what the step reports**  `@HOP-4 @HOP-5`  (:29)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Destructive cleanup is permitted once the witness and the release both exist**  `@HOP-9 @HOP-14`  (:41)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **MEASURED M37** — `the release route leaves the hold gate open` — RED at `destructive cleanup is permitted`

**A source-preserving stop leaves the pod's artifact path in place**  `@HOP-14 @HOP-16`  (:62)
  - **Reddened by (as the file states it):** RequestSourcePreservingStop removing the incarnation directory as part of the stop — the source line reddens while the acknowledgement line above it stays green.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Destructive cleanup is refused until the finish witness exists**  `@HOP-9 @HOP-14`  (:81)
  - **Reddened by (as the file states it):** DestructiveCleanupEligible returning true whenever the execution record exists, instead of requiring the finish acknowledgement — this reddens on its one line while the scenario above stays green, which is what tells it apart from a daemon that is simply down.  That mutation is BOTH arms of CleanupE
  - **MEASURED M26** — `CleanupEligible answers yes whenever the execution record exists` — RED at `destructive cleanup is refused`

**A stale fence is refused while the current fence is served**  `@HOP-10 @HOP-11`  (:90)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A repeated hold with the same identity returns the same acknowledgement**  `@HOP-6`  (:101)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A repeated hold with a different fence is a typed conflict, and the first hold still stands**  `@HOP-6 @HOP-10`  (:115)
  - **Reddened by (as the file states it):** the hold handler comparing only the handoff UUID and not the fence before returning the stored acknowledgement.
  - **MEASURED M29** — `the hold replay compares only the execution id, not the fence` — RED at `the daemon's refusal says "conflict"`

**A hold request naming a path instead of an incarnation is refused**  `@HOP-7 @HOP-8`  (:128)
  - **Reddened by (as the file states it):** the source-control route resolving its target with filepath.Join on the request's path instead of the server-derived incarnation root under the daemon's os.Root handle.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A symlink swapped under the source path is refused, and the unswapped path is not**  `@HOP-8`  (:137)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **MEASURED M30** — `the source-control route string-joins the request's path` — RED at `the daemon's refusal says "containment"`

**A base control capability cannot hold, seal or publish**  `@HOP-3 @HOP-24`  (:150)
  - **Reddened by (as the file states it):** the capability middleware checking that a token is VALID without checking that its facet matches the route it arrived on. The control is the same token succeeding at its own operation, asserted first, so this cannot go green on a daemon that rejects the token outright.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A writer ticket issued after the seal is a typed refusal, and one issued before it is not**  `@HOP-12 @HOP-13`  (:164)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**An ordinary step's terminal pause pod is still replaced**  `@HOP-12 @HOP-16`  (:181)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Pause pod recreation for a capture-held source is refused, and an ordinary one still recreates**  `@HOP-12 @HOP-16`  (:198)
  - **Reddened by (as the file states it):** Container.Run replacing the terminal pause Pod without first consulting the ledger classifier -- this scenario reddens on `the pause pod is not recreated` and the control scenario above stays green.
  - **MEASURED M35** — `Container.Run replaces the terminal pause pod without asking the ledger` — RED at `the pause pod is not recreated`

**A lease takeover bumps the epoch, and the previous owner's fence stops being served**  `@HOP-10`  (:209)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A released hold is gone from the node, and a held one is not**  `@HOP-9 @HOP-11`  (:232)
  - **Reddened by (as the file states it):** the release route leaving the hold's gate open -- the first check, the presence half, stays green and only "the source has been released" reddens.
  - **MEASURED M37** — `the release route leaves the hold gate open` — RED at `the source has been released`


### `hangar-disposition.feature`

**A successful finish selects capture and commits the producer checkpoint with an unresolved reservation**  `@HOP-5`  (:29)
  - **Reddened by (as the file states it):** CommitCaptureReservation committing the reservation already `resolved` — Stage 2 creates a DISTINCT unresolved reservation and resolves nothing, so a Stage 2 that resolves has skipped the canonicalization the scope and digest come from. It reddens this scenario at its FIRST line (the settle stops at
  - **BROADER THAN NAMED — M38** — `Stage 2 commits the reservation already resolved` — RED at `the disposition is "capture"`

**A failed producer selects no_capture, and the same producer succeeding selects capture**  `@HOP-2 @HOP-5`  (:43)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**no_capture completes in two halves, and the outcome stays pending between them**  `@HOP-5 @HOP-9`  (:63)
  - **Reddened by (as the file states it):** the arbiter asking about the producer's outcome BEFORE the cancellation — which is how a cancellation-selected outcome reaches no_capture. The two pre_reservation_cancel scenarios below redden on their one disposition line and this one stays green, which is what tells a branch confusion apart from a
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**Cancellation before Stage 2 selects pre_reservation_cancel, never no_capture**  `@HOP-11`  (:86)
  - **Reddened by (as the file states it):** the arbiter asking about the producer's outcome BEFORE the cancellation. The no_capture scenarios above stay green, which is what tells a branch confusion apart from a broken arbiter.
  - **MEASURED M40** — `the arbiter asks the producer's outcome before the cancellation` — RED at `the disposition is "pre_reservation_cancel"`

**A cancelled handoff with no acknowledged hold releases its reservation and closes**  `@HOP-11`  (:114)
  - **Reddened by (as the file states it):** the pre_reservation_cancel branch forking on the acknowledged hold instead of the reserved source — this reddens on the release line while the disposition line above it stays green.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.
  - **UNPINNED — M42** — the mutation this scenario's own comment names was applied and reddened NOTHING (0 failed). This scenario HAS a reservation, so removing the `!Reserved()` fork changes the answer only for a handoff that reserved nothing -- a state no scenario reaches.

**Seal and publish are refused from a predeclaration, and served from the Stage 2 reservation**  `@HOP-5 @HOP-21`  (:126)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A new build gets a new handoff identity and a new source hold**  `@HOP-6`  (:133)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A capture-selected step announces selection, seal start and its terminal disposition**  `@HOP-18`  (:151)
  - **Reddened by (as the file states it):** the Req 18 emitter dropping the selection announcement and emitting only seal start and the outcome — this reddens on its first announcement line, and the payload scenario below stays green.
  - **BROADER THAN NAMED — M38** — `Stage 2 commits the reservation already resolved` — RED at `the build announces "capture-seal-started"`
  - **BROADER THAN NAMED — M38** — `Stage 2 commits the reservation already resolved` — RED at `the build announces "capture-disposition"`
  - **MEASURED M45** — `the Req 18 emitter drops the capture-selected announcement` — RED at `the build announces "capture-selected"`, which is exactly the first announcement line the comment names, and the payload scenario it says stays green did stay green
  - **MEASURED M45** — `the Req 18 emitter drops the capture-selected announcement` — RED at `the build announces "capture-disposition"`

**An ordinary step announces none of them, while the capture step beside it announces all three**  `@HOP-18`  (:167)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**The announcement carries the disposition and reason and nothing else**  `@HOP-18`  (:183)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **BROADER THAN NAMED — M38** — `Stage 2 commits the reservation already resolved` — RED at `the announcement carries the disposition and reason and nothing else`


### `hangar-fixture.feature`

**The real daemon stores a strict tree in the emulated Hangar bucket and names it itself**  `@HOP-19`  (:50)
  - **Reddened by (as the file states it):** dropping option.WithEndpoint from hangar/gcs.NewStorageClient (hangar/gcs/gcs.go:58) — the daemon then validates its bucket against real GCS, exits at boot, and the GIVEN reddens, which is the fixture failing loudly rather than a scenario failing later. Second mutation, for the rest of the chain: bu
  - **MEASURED M48** — `the storage client drops its endpoint override` — RED at the GIVEN, `a real artifact daemon publishing to a Hangar output bucket`, which is what the comment predicts: "the Given reddens, which is the fixture failing loudly rather than a scenario failing later". **0 scenarios passed** in that feature. Note the comment cites `hangar/gcs.NewStorageClient` at `hangar/gcs/gcs.go:58`; that function and that file no longer exist — the capability moved behind `hangar/internal/gcsclient` — so the mutation was applied there instead


### `hangar-publication.feature`

**A caller-supplied bucket, scope or object key is refused, and the server-derived one is served**  `@HOP-7 @HOP-20 @HOP-23`  (:28)
  - **Reddened by (as the file states it):** the publish handler reading the request's `bucket` field instead of the namespace resolved from the active epoch — one step reddens, the refusal line, and the control line above it stays green.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**A sealed source publishes one marked object and a receipt naming its scope, digest and generation**  `@HOP-21 @HOP-22 @HOP-25`  (:53)
  - **Reddened by (as the file states it):** the Ed25519 signer omitting Generation from the signed claim set, so the whole-receipt check reddens on its one line while the object assertion above it stays green.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the capture returns a receipt for the server-derived scope at a store-assigned generation`
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the output bucket holds exactly one object, marked "hangar-output-v1"`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the capture returns a receipt for the server-derived scope at a store-assigned generation`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the output bucket holds exactly one object, marked "hangar-output-v1"`
  - **UNPINNED — M50** — the mutation this scenario's own comment names was applied and reddened NOTHING (0 failed). Signer and verifier share `CanonicalReceiptBytes`, so dropping a signed field is self-consistent and every signature still verified. The mutation reddens the Go golden fixtures, not brine. The scenario is fine -- M55 reddens it; the comment names the wrong runner.

**The receipt verifies under the activation-pinned public key and fails under any other**  `@HOP-25 @HOP-27`  (:64)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the receipt does not verify under any other key`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the receipt does not verify under any other key`

**Publishing identical canonical bytes twice deduplicates to one object and two receipts**  `@HOP-23 @HOP-25`  (:77)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the two captures share one object and carry two distinct receipts`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the two captures share one object and carry two distinct receipts`
  - **MEASURED M55** — `the exact lifecycle is registered without its generation` — RED at `the two captures share one object and carry two distinct receipts`

**The same key holding a DIFFERENT variant is a typed collision, never an overwrite**  `@HOP-23`  (:94)
  - **Reddened by (as the file states it):** the publisher's create-if-absent path falling back to an unconditional write when GenerationMatch: 0 returns 412 — this scenario reddens on its refusal line while the dedup scenario's `holds exactly` line above stays green, which is what distinguishes the mutation from a broken publisher.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the output bucket's key for this tree already holds a different variant`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the output bucket's key for this tree already holds a different variant`

**An object at the key with no marker is a typed collision, and the marked one still deduplicates**  `@HOP-22 @HOP-23`  (:104)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the output bucket's key for this tree already holds an object with no marker`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the output bucket's key for this tree already holds an object with no marker`

**A sealed source becomes a marked object, a signed receipt and a registered tree ref**  `@HOP-21 @HOP-22 @HOP-25 @HOP-26`  (:120)
  - **Reddened by (as the file states it):** RegisterReceipt registering the logical ref without its generation.
  - **BROADER THAN NAMED — M49** — `the publish handler takes its scope from the request's bucket` — RED at `the registered tree ref names the published generation`
  - **BROADER THAN NAMED — M53** — `create-if-absent drops its does-not-exist precondition` — RED at `the registered tree ref names the published generation`
  - **MEASURED M55** — `the exact lifecycle is registered without its generation` — RED at `the registered tree ref names the published generation`

**Two concurrent-in-sequence captures of identical bytes get one object and two distinct receipts**  `@HOP-23 @HOP-25`  (:135)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.

**An exact replacement generation supersedes the old one, and the old ref no longer resolves**  `@HOP-34 @HOP-45`  (:146)
  - **Reddened by:** *no mutation is named in the feature file.* See "32 scenarios name no mutation" below.
  - **Not measured in Phase 9.** No mutation in this phase's set targets it directly.
