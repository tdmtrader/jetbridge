package steps

// Disposition: which outcome a finished producer selects, and what the build is
// told about it.
//
// This family is brine-shaped because it is sequential and observable: an
// arbiter picks one member of a closed vocabulary, and a build event is a row
// production writes and production reads back — the same shape as the exit
// annotation step-closing.feature reads.
//
// What stays in Go and is cited rather than duplicated: conflicting replay (a
// race), and deadline expiry (a clock, and convention 9 forbids a chain that
// waits).

import (
	"github.com/brine-dev/brine-go/pkg/brine"
)

const dispositionPhase = "Phase 5 Green"

// HangarDispositionDefinitions is the disposition family.
func HangarDispositionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		stubMap[CaptureOutcome, CaptureOutcome](
			"the second half of the no_capture close runs",
			dispositionPhase,
			"the two-half no_capture completion, whose outcome stays pending between them"),

		stubCheck[CaptureOutcome](
			"the disposition is {string}",
			dispositionPhase,
			"the arbiter that selects exactly one disposition"),

		stubCheck[CaptureOutcome](
			"the no_capture reason is {string}",
			dispositionPhase,
			"the closed no_capture reason vocabulary"),

		stubCheck[CaptureOutcome](
			"the producer checkpoint is committed with an unresolved reservation",
			dispositionPhase,
			"CommitCaptureReservation, which commits the checkpoint and a distinct unresolved reservation row"),

		stubCheck[CaptureOutcome](
			"the outcome is still pending",
			dispositionPhase,
			"the intermediate state the two-half close passes through"),

		// Over the new DRAFT, not the old outcome: the identities a new build
		// gets are predeclared at admission, which is the only moment they are
		// knowable and the only state that carries both of them.
		stubCheck[CaptureDraft](
			"the new build's handoff identity is not the old one",
			dispositionPhase,
			"per-build handoff and source-lease identity minting"),

		// Req 18's announcements, in EMISSION ORDER — order is part of the
		// assertion, because brine stops at the first red step.
		stubCheck[CaptureOutcome](
			"the build announces {string}",
			dispositionPhase,
			"the build-event emitter on the capture recovery component"),

		stubCheck[CaptureOutcome](
			"the build announces nothing about capture",
			dispositionPhase,
			"the emitter's silence on an ordinary step"),

		// The payload asserted WHOLE, which is the only form in which "never a
		// grant, key, path or consumer ref" can fail.
		stubCheck[CaptureOutcome](
			"the announcement carries the disposition and reason and nothing else",
			dispositionPhase,
			"the announcement payload's exact field set"),

		stubCheck[CaptureOutcome](
			"no daemon release was needed, and the hold is still there to release",
			dispositionPhase,
			"the cancellation path that closes without calling a daemon it never held anything on"),
	}
}
