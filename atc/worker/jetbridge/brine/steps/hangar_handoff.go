package steps

// What the output daemon ANSWERS: holds, witnesses, refusals, containment.
//
// This family is driven against the real binary through the fixture in
// hangar_fixture.go, for the reason realdaemon.go records — a double can be
// made to refuse, but it cannot tell you the answer is RIGHT, and here half of
// what an operation does is a change to a node's filesystem that no response
// shows.
//
// NOTHING HERE COUNTS A REQUEST. "The daemon was not called" is never an
// assertion in this file; the scenarios that mean it say the source is still
// held, or that the store's copy is still the store's copy.
//
// The bodies are stubs until Phase 3 Green stands cmd/hangar-output-daemon up.
// They fail with a typed error naming that phase.

import (
	"github.com/brine-dev/brine-go/pkg/brine"
)

const handoffPhase = "Phase 3 Green"

// HangarHandoffDefinitions is the daemon-handoff family.
func HangarHandoffDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		stubMap[CaptureDraft, HeldSource](
			"the daemon holds the source",
			handoffPhase,
			"the output daemon's source ledger and its establish-hold route"),

		stubMap[HeldSource, HeldSource](
			"the same hold is repeated with the same identity",
			handoffPhase,
			"idempotent replay of an establish-hold request"),

		// The conflict twin. "Repeating the handoff returns the same state"
		// passes for a daemon that ignores the identity entirely, so the twin
		// has to show that reuse for DIFFERENT facts is a typed conflict.
		stubMap[HeldSource, HeldSource](
			"the same hold is repeated with a different fence",
			handoffPhase,
			"the fence comparison the hold handler makes before returning a stored acknowledgement"),

		stubMap[HeldSource, HeldSource](
			"a stale fence is presented",
			handoffPhase,
			"fence admission on every control operation"),

		stubMap[HeldSource, HeldSource](
			"the owner's lease is taken over",
			handoffPhase,
			"capture lease takeover, which bumps the epoch — the only concurrency this runner can say, and it says it sequentially"),

		stubMap[HeldSource, HeldSource](
			"the hold request names a path instead of an incarnation",
			handoffPhase,
			"the source-control route that accepts IDs and never a caller path"),

		stubMap[HeldSource, HeldSource](
			"the source path is replaced by a symlink to {string}",
			handoffPhase,
			"the os.Root-handle resolution of the server-derived incarnation root"),

		stubMap[HeldSource, HeldSource](
			"a base control capability is used to {string}",
			handoffPhase,
			"the capability middleware's facet check on the route a token arrived on"),

		stubMap[HeldSource, HeldSource](
			"a writer ticket is issued after the seal",
			handoffPhase,
			"the sealed refusal writer-ticket issuance must return once sealing has begun"),

		stubMap[HeldSource, HeldSource](
			"a writer ticket is issued before the seal",
			handoffPhase,
			"writer-ticket issuance on an open source"),

		// Two sentences rather than one, because the two cases start from
		// different states: eligibility is asked after a witness in the
		// permitted case and before one in its absence twin, and brine's
		// registry gives one pattern exactly one input type.
		stubMap[FinishWitnessed, FinishWitnessed](
			"destructive cleanup is requested",
			handoffPhase,
			"DestructiveCleanupEligible and the release it depends on"),

		stubMap[HeldSource, FinishWitnessed](
			"destructive cleanup is requested before any witness",
			handoffPhase,
			"the eligibility answer for an execution with no finish acknowledgement"),

		stubMap[HeldSource, HeldSource](
			"the hold is released",
			handoffPhase,
			"the exact hold release, which is one half of the destructive-cleanup gate"),

		stubMap[HeldSource, HeldSource](
			"the publish request also carries a caller-chosen {string}",
			"Phase 2 Green",
			"the publish route's refusal of a request body that names its own bucket, scope or key"),

		stubMap[HeldSource, FinishWitnessed](
			"the step finishes and the daemon witnesses it",
			handoffPhase,
			"the execution ledger's finish acknowledgement"),

		stubMap[HeldSource, FinishWitnessed](
			"the step fails",
			handoffPhase,
			"a non-success exit reaching the execution ledger"),

		stubMap[HeldSource, FinishWitnessed](
			"the step is stopped without destroying its source",
			handoffPhase,
			"RequestSourcePreservingStop, which classifies before it interrupts"),

		stubMap[HeldSource, FinishWitnessed](
			"the step is cancelled before Stage 2",
			"Phase 5 Green",
			"the pre_reservation_cancel arm of the disposition arbiter"),

		stubMap[CaptureDraft, FinishWitnessed](
			"the handoff is cancelled with no acknowledged hold",
			"Phase 5 Green",
			"the cancellation path that closes a handoff which never established a hold"),

		// Checks over a held source. The daemon's own status line is spelled
		// "the Hangar daemon answers" rather than reusing the durable tier's
		// "the daemon's answer is {int}": one pattern has exactly one input
		// type, and the shadowing guard catches a second definition of a
		// sentence that already exists.
		CheckInt[HeldSource]("the Hangar daemon answers {int}, holding the source",
			"the daemon's status",
			func(in HeldSource) (int, error) {
				if in.Err != nil {
					return 0, in.Err
				}
				return in.Status, nil
			}),

		stubCheck[HeldSource](
			"the daemon's refusal says {string}",
			handoffPhase,
			"the typed refusal bodies the output daemon returns"),

		stubCheck[HeldSource](
			"the hold acknowledgement is the one the first hold returned",
			handoffPhase,
			"the durable, immutable acknowledgement the source ledger stores"),

		// An absence with a positive control on the line above it: a source
		// that is still there is an outcome, not a call count.
		stubCheck[HeldSource](
			"the source is still held on the node",
			handoffPhase,
			"the incarnation directory a stop must leave in place"),

		stubCheck[HeldSource](
			"the source has been released",
			handoffPhase,
			"the exact hold release"),

		stubCheck[HeldSource](
			"the pause pod is recreated",
			"Phase 4 Green",
			"recreatePausePodIfTerminal on the ordinary, non-capture path"),

		// Checks over the witness.
		stubCheck[FinishWitnessed](
			"the witness is what the step reports",
			handoffPhase,
			"the acknowledgement the step's reported outcome must be read from"),

		stubCheck[FinishWitnessed](
			"the witnessed exit status is {int}",
			handoffPhase,
			"the exit outcome the execution ledger durably records"),

		stubCheck[FinishWitnessed](
			"the source is still there after the stop",
			handoffPhase,
			"the incarnation directory a source-preserving stop must leave in place"),

		stubCheck[FinishWitnessed](
			"destructive cleanup is permitted",
			handoffPhase,
			"the eligibility gate that opens once witness and release both exist"),

		stubCheck[FinishWitnessed](
			"destructive cleanup is refused",
			handoffPhase,
			"the eligibility gate that stays closed until the finish witness exists"),
	}
}
