package steps

// Publication: what a sealed source becomes, and what the bucket then holds.
//
// The store here is the emulated output bucket the fixture created, read back
// through the same client the daemon uses. That is what makes "holds exactly
// one object" an OUTCOME: there is no request log on the fixture, so dedup can
// only be told from overwrite by seeding the key with a different variant and
// naming which bytes are there afterwards.
//
// Both halves of a receipt assertion are production. The daemon signs with its
// epoch private key and the check verifies with the production verifier under
// the activation-pinned public key — never against a string a test wrote, which
// is the defect step-closing.feature records.
//
// The bodies are stubs until Phase 2 Green adds the output namespace, marker
// and receipt, and Phase 3 Green the publish route.

import (
	"github.com/brine-dev/brine-go/pkg/brine"
)

const publicationPhase = "Phase 2 Green"

// HangarPublicationDefinitions is the publication family.
func HangarPublicationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		stubMap[FinishWitnessed, CaptureOutcome](
			"the capture settles",
			"Phase 5 Green",
			"the capture recovery component that seals, canonicalizes, publishes and resolves the reservation"),

		stubMap[CaptureDraft, CaptureOutcome](
			"seal and publish are attempted from the predeclaration",
			"Phase 5 Green",
			"the refusal a predeclaration must return when asked to seal or publish"),

		stubMap[CaptureOutcome, CaptureDraft](
			"a new build of the same step is admitted",
			"Phase 5 Green",
			"a second admission, which must mint a new handoff identity and a new source lease"),

		stubMap[CaptureOutcome, PublishedTree](
			"the published tree is read back from the output bucket",
			publicationPhase,
			"the output namespace's key derivation, so a read knows where to look"),

		stubMap[PublishedTree, PublishedTree](
			"the same canonical bytes are captured again",
			publicationPhase,
			"the create-if-absent path that deduplicates to one object and issues a second receipt"),

		stubMap[PublishedTree, PublishedTree](
			"an exact replacement generation is published",
			"Phase 7 Green",
			"the exact replacement that supersedes a generation and unresolves the old ref"),

		stubMap[BoundOutput, BoundOutput](
			"the ref moves from hidden to published",
			"Phase 6 Green",
			"the hidden-to-published lifecycle transition"),

		stubMap[BoundOutput, BoundOutput](
			"the consumer names an unregistered exact ref",
			"Phase 6 Green",
			"the registration check a claim is refused against"),

		stubMap[BoundOutput, BoundOutput](
			"the consumer holds no active claim",
			"Phase 6 Green",
			"the active-claim precondition a managed read is granted against"),

		// Checks over the outcome.
		stubCheck[CaptureOutcome](
			"the capture returns a receipt for {string} at generation {int}",
			publicationPhase,
			"the Ed25519 receipt over the whole signed claim set, scope, digest and generation included"),

		stubCheck[CaptureOutcome](
			"the capture is refused as {string}",
			publicationPhase,
			"the typed outcomes — collision, unauthorized, cancelled, seal-unconfirmed — none of which is ever a cache miss"),

		stubCheck[CaptureOutcome](
			"the receipt verifies under the activation-pinned public key",
			publicationPhase,
			"the receipt verifier and the epoch's pinned public key"),

		stubCheck[CaptureOutcome](
			"the receipt does not verify under any other key",
			publicationPhase,
			"key-id binding, so a receipt names the epoch whose key can check it"),

		// Checks over the bucket.
		stubCheck[PublishedTree](
			"the output bucket holds exactly one object, marked {string}",
			publicationPhase,
			"the immutable-at-creation marker metadata written with the object"),

		stubCheck[PublishedTree](
			"the two captures share one object and carry two distinct receipts",
			publicationPhase,
			"a per-capture receipt over deduplicated bytes"),

		stubCheck[PublishedTree](
			"the registered exact ref names the published generation",
			"Phase 6 Green",
			"RegisterReceipt, which must register the ref WITH its generation"),

		stubCheck[PublishedTree](
			"the old ref no longer resolves",
			"Phase 7 Green",
			"the supersession that unresolves a replaced generation"),
	}
}
