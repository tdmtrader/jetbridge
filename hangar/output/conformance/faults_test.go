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
)

// publishedForDeletion puts one object in a tier-1 store and returns the
// reclaimer over it, the ref, and its exact delete precondition.
func publishedForDeletion(t *testing.T, tier substrate, fill string) (
	*reclaimer.Reclaimer, hangar.TreeRef, output.DeletePrecondition) {
	t.Helper()

	ctx := context.Background()
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	object, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digestOf(fill)),
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

	// And the object is STILL THERE. That is the half that matters: a refused
	// delete must leave the generation intact, so the lifecycle row is still
	// about an object that exists.
	tier.memory.Inject(gcstest.Faults{})
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)
	if _, err := role.StatExactObject(context.Background(), ref); err != nil {
		t.Errorf("the object is gone after a delete the store refused: %v", err)
	}
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
	for _, forbidden := range []output.DeleteOutcome{
		output.DeleteConfirmed, output.DeleteAlreadyAbsent,
	} {
		if outcome == forbidden {
			t.Errorf("a timed-out delete reported %q; \"we did not hear back\" is not proof",
				forbidden)
		}
	}
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
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)
	reservation := reservationFor(t, namespace, testReservation, digestOf("a4"))

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
	if !second.Deduplicated {
		t.Error("the retry reported a fresh create rather than a deduplication; the object the " +
			"lost response committed is what it must have found")
	}
	if second.Marker.ReservationID != testReservation {
		t.Errorf("the retry's marker names reservation %q, not the one that published the "+
			"bytes (%q)", second.Marker.ReservationID, testReservation)
	}
}
