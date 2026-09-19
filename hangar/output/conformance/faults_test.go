package conformance

// Phase 9's injected-fault flows, and the two typed delete outcomes nothing
// asserted.
//
// The conformance suite beside this file covers the SUBSTRATE: what a store
// does with create-if-absent, a generation pin, a prefix list, a metadata stat.
// What it did not cover is the fault side of two of Req 47's six outcomes.
// `output.DeleteUnauthorized` and `output.DeleteTimedOut` appeared in
// `DeleteOutcomes()`, in the schema's enum and in the reclaimer's switch, and a
// repository-wide grep over `*_test.go` returned zero assertions on either --
// while `gcstest.Faults` already carried `Unauthorized` and `DeleteTimeout`
// seams that no test injected. An outcome nothing exercises is a branch, not a
// contract: Req 47's whole point is that these six are distinguished rather than
// collapsed, and two of them were only distinguished on paper.
//
// Tier 1 for every case here, and the reason is the plan's own: fault injection
// is what the in-package memory fake exists for, and fake-gcs-server has no seam
// for "this credential may not delete". Where a case CAN run on both, it does.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
	testsupport "github.com/concourse/concourse/hangar/output/testsupport"
)

// publishedForDeletion puts one object in a tier-1 store and returns the
// reclaimer over it, the ref, and its exact delete precondition.
func publishedForDeletion(t *testing.T, tier substrate, fill string) (
	*reclaimer.Reclaimer, hangar.TreeRef, output.DeletePrecondition) {
	t.Helper()

	ctx := context.Background()
	namespace := testsupport.Namespace(t, tier.bucket, testTenant, testEpoch)
	role, _ := publisherFor(t, tier, namespace)

	object, err := role.EnsureObject(ctx,
		testsupport.Reservation(t, namespace, testReservation, testsupport.Digest(fill)),
		bytes.NewReader(canonicalBytes("to be reclaimed")), 15)
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}

	sweeper, err := reclaimer.New(namespace, reclaimer.Restrict(tier.deleter))
	if err != nil {
		t.Fatalf("building the reclaimer: %v", err)
	}

	return sweeper, object.Attributes.Ref, output.DeletePrecondition{
		Generation:     object.Attributes.Ref.Generation,
		Metageneration: object.Metageneration,
	}
}

// The CONTROL for both rows below, asserted first because a "delete was
// refused" assertion passes against a reclaimer that refuses everything.
func TestADeleteWithNoFaultInjectedIsConfirmed(t *testing.T) {
	tier := tier1(t)
	sweeper, ref, precondition := publishedForDeletion(t, tier, "a1")

	outcome, err := sweeper.DeleteExactGeneration(context.Background(), ref, precondition)
	if err != nil {
		t.Fatalf("deleting an object with no fault injected: %v", err)
	}
	if outcome != output.DeleteConfirmed {
		t.Fatalf("a clean conditional delete reported %q, not %q",
			outcome, output.DeleteConfirmed)
	}
}

// Req 47 names `unauthorized` as its own outcome, and it has to be its own
// outcome: a reclaimer whose cloud principal lost its grant is a DEPLOYMENT
// fault an operator must see, and collapsing it into infrastructure_failure
// makes it look like a retryable blip that the next pass will clear. It will
// not; every pass will fail the same way.
func TestADeleteRefusedByIAMIsUnauthorizedAndNotAnInfrastructureFailure(t *testing.T) {
	tier := tier1(t)
	sweeper, ref, precondition := publishedForDeletion(t, tier, "a2")

	tier.memory.Inject(gcstest.Faults{Unauthorized: true})

	outcome, err := sweeper.DeleteExactGeneration(context.Background(), ref, precondition)
	if outcome != output.DeleteUnauthorized {
		t.Errorf("a delete the store refused with 403 reported %q, expected %q (%v)",
			outcome, output.DeleteUnauthorized, err)
	}
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("the error is not typed as unauthorized: %v", err)
	}
	if errors.Is(err, output.ErrInfrastructure) {
		t.Error("an authorization refusal is typed as an infrastructure failure; a pass that " +
			"retried it would retry forever, and the operator would never learn the grant is gone")
	}

	// WHAT IS NOT ASSERTED HERE, and why. "And the object is still there" reads
	// like the half that matters and it cannot fail on this substrate:
	// `memoryHandle.Delete` returns the injected 403 BEFORE it looks the object
	// up (`hangar/gcstest/memory.go:334-336`), so no mutation in the reclaimer
	// -- whose whole body is `ObjectToDelete(...).Delete(ctx)` -- could make the
	// object vanish. Asserting it would be asserting the fake's early return.
	// A substrate that evaluated authorization after locating the object could
	// say it; none here does, and real GCS is where it belongs.
}

// And `timeout`, for the same reason inverted: a delete that timed out MAY have
// happened. Reporting it as confirmed would finalize a reclaim on no evidence,
// and reporting it as already_absent would be worse -- that is the inferred
// path, and it requires a stat nobody took.
func TestADeleteThatTimedOutIsTimedOutAndNeverConfirmed(t *testing.T) {
	tier := tier1(t)
	sweeper, ref, precondition := publishedForDeletion(t, tier, "a3")

	tier.memory.Inject(gcstest.Faults{DeleteTimeout: true})

	outcome, err := sweeper.DeleteExactGeneration(context.Background(), ref, precondition)
	if outcome != output.DeleteTimedOut {
		t.Errorf("a delete that exceeded its deadline reported %q, expected %q (%v)",
			outcome, output.DeleteTimedOut, err)
	}
	if !errors.Is(err, output.ErrTimeout) {
		t.Errorf("the error is not typed as a timeout: %v", err)
	}
	// No second loop over the outcomes a timeout must NOT be: the equality
	// above already excludes every one of them, and a check that can only fire
	// when the check above it has fired is a sentence about an assertion rather
	// than an assertion.
}

// Every one of Req 47's six outcomes is now reachable from a test, and this row
// is what keeps that true: it fails if a new outcome is added to the enum with
// nothing exercising it, and it names which.
//
// The map is written out rather than derived, because deriving it from the same
// list it checks would make it a tautology.
func TestEveryTypedDeleteOutcomeIsExercisedSomewhere(t *testing.T) {
	exercised := map[output.DeleteOutcome]string{
		output.DeleteConfirmed: "TestADeleteWithNoFaultInjectedIsConfirmed, and " +
			"TestTheExactGenerationDeleteIsConditionalAndTyped",
		output.DeleteAlreadyAbsent:      "TestTheExactGenerationDeleteIsConditionalAndTyped",
		output.DeleteGenerationConflict: "TestTheExactGenerationDeleteIsConditionalAndTyped (tier 1)",
		output.DeleteUnauthorized:       "TestADeleteRefusedByIAMIsUnauthorizedAndNotAnInfrastructureFailure",
		output.DeleteTimedOut:           "TestADeleteThatTimedOutIsTimedOutAndNeverConfirmed",
		output.DeleteInfrastructure:     "TestALostDeleteResponseIsNeverReportedAsConfirmed",
	}

	// The names above are strings, and a string is not a reference: deleting or
	// renaming one of these tests would leave the map claiming it. This makes
	// them load-bearing -- a deletion breaks the build rather than leaving a
	// guard that quietly vouches for nothing.
	_ = []func(*testing.T){
		TestADeleteWithNoFaultInjectedIsConfirmed,
		TestTheExactGenerationDeleteIsConditionalAndTyped,
		TestADeleteRefusedByIAMIsUnauthorizedAndNotAnInfrastructureFailure,
		TestADeleteThatTimedOutIsTimedOutAndNeverConfirmed,
		TestALostDeleteResponseIsNeverReportedAsConfirmed,
	}

	for _, outcome := range output.DeleteOutcomes() {
		if _, ok := exercised[outcome]; !ok {
			t.Errorf("output.DeleteOutcomes() includes %q and no test in this package "+
				"exercises it. Req 47's claim is that these outcomes are DISTINGUISHED; an "+
				"outcome nothing reaches is a branch, not a contract.", outcome)
		}
	}
	if len(exercised) != len(output.DeleteOutcomes()) {
		t.Errorf("this map names %d outcomes and DeleteOutcomes() has %d; one of them was "+
			"removed and this map still claims it",
			len(exercised), len(output.DeleteOutcomes()))
	}
}

// AC 9's last clause: "an ambiguous create response converges only through
// verified per-capture retry."
//
// TestAnAmbiguousUploadIsReconciledByAnExactStat proves one reconciliation.
// What it does not say is that REPEATING the capture converges -- that the
// retry finds the bytes the lost response committed rather than creating a
// second generation beside them. That is the difference between an ambiguous
// upload and a duplicate one, and only the second call can show it.
func TestARetryAfterAnAmbiguousCreateConvergesOnOneGeneration(t *testing.T) {
	tier := tier1(t)
	ctx := context.Background()
	namespace := testsupport.Namespace(t, tier.bucket, testTenant, testEpoch)
	role, _ := publisherFor(t, tier, namespace)
	reservation := testsupport.Reservation(t, namespace, testReservation, testsupport.Digest("a4"))

	// The first attempt commits the object and loses the response.
	tier.memory.Inject(gcstest.Faults{CreateResponseLost: true})
	first, err := role.EnsureObject(ctx, reservation,
		bytes.NewReader(canonicalBytes("ambiguous")), 9)
	if err != nil {
		t.Fatalf("the ambiguous create did not reconcile: %v", err)
	}

	// The retry is the SAME capture: the same reservation, the same bytes.
	tier.memory.Inject(gcstest.Faults{})
	second, err := role.EnsureObject(ctx, reservation,
		bytes.NewReader(canonicalBytes("ambiguous")), 9)
	if err != nil {
		t.Fatalf("retrying the capture after an ambiguous create: %v", err)
	}

	if second.Attributes.Ref.Generation != first.Attributes.Ref.Generation {
		t.Errorf("the retry produced generation %d where the reconciled create found %d; an "+
			"ambiguous upload that converges on two generations has published the same bytes "+
			"twice and left one of them unregistered",
			second.Attributes.Ref.Generation, first.Attributes.Ref.Generation)
	}
	if second.Marker.ReservationID != testReservation {
		t.Errorf("the retry's marker names reservation %q, not the one that published the "+
			"bytes (%q)", second.Marker.ReservationID, testReservation)
	}

	// `Deduplicated` IS DELIBERATELY NOT ASSERTED, and that is a finding rather
	// than a gap.
	//
	// The two reconciliation paths disagree about what the flag means for a
	// capture's OWN bytes. `reconcileAmbiguous` compares the marker's
	// reservation against this one (`publisher.go:255`), so the lost-response
	// path reports `false` -- and `TestAnAmbiguousUploadIsReconciledByAnExactStat`
	// asserts exactly that, calling the opposite "deduplication against this
	// capture's own object". `reconcileExisting` sets it `true`
	// unconditionally (`publisher.go:227`) even though it has the same marker
	// and could make the same comparison. So this retry -- which takes the
	// second path -- reports `true` for bytes the first path would call its own.
	//
	// Asserting either value here would pin the inconsistency as a contract.
	// What this case is about is CONVERGENCE, which both paths agree on: one
	// generation, and a marker naming the reservation that published it.
	// Recorded in phase-9-demonstrations.md for the reviewer to rule on.
}
