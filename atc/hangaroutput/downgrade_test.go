package hangaroutput_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Downgrade, and what it refuses.
//
// Requirement 58's shape is an ORDER, not a switch: emission stops first, safe
// releases and settlement continue, and the compatibility plane stays installed
// while any managed publication, claim or lease exists. Unsafe removal is
// BLOCKED rather than promised after a finite drain -- claim tombstone purge is
// outside this track, so a deployment may legitimately sit in `draining`
// forever, and that is a supported state rather than a failure to finish.
//
// Every row below seeds the live state through the real schema and then asks the
// real predicate, because the predicate's whole job is to be the same set the
// controllers are working on.

// drainFixture is an epoch with both facets enabled, ready to be taken down.
func drainFixture(t *testing.T, epoch executioncontrol.ActivationEpoch) (activation.Epochs, *sql.DB) {
	t.Helper()

	epochs, conn := activationFixture(t)
	mustBegin(t, epochs, epoch)
	mustAttestAndEnableBase(t, epochs, epoch)
	mustAttestAndEnableOutput(t, epochs, epoch)

	// A safe policy reading. The schema refuses a predeclaration under an epoch
	// with no lifetime-policy attestation -- "we have not checked" and "the
	// check failed" are the same amount of evidence -- so a fixture that seeded
	// live state without one would be testing that refusal instead.
	if _, err := conn.Exec(`
		INSERT INTO hangar_policy_snapshots
			(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
			 lifecycle_delete_rules, state)
		VALUES ($1, 'gs://activation-output', 3, 'policy-hash-1', 0, 'safe')`,
		int64(epoch)); err != nil {
		t.Fatalf("seeding the policy snapshot: %v", err)
	}

	return epochs, conn
}

// seedLiveGeneration puts one registered exact generation in the plane, which
// is the simplest thing that must block a terminal downgrade: the object is in
// the bucket and this plane is the only thing that would ever delete it.
func seedLiveGeneration(t *testing.T, conn *sql.DB, epoch executioncontrol.ActivationEpoch) int64 {
	t.Helper()

	const (
		scope  = "downgrade-scope"
		digest = "sha256:" + "11111111111111111111111111111111" + "11111111111111111111111111111111"
	)

	// Only the exact lifecycle is seeded, and deliberately so.
	//
	// It is what "the object is in the bucket" means to the drain predicate,
	// and it references nothing but the activation epoch -- the whole
	// handoff/disposition/reservation/hold chain above it is how a lifecycle
	// row is PRODUCED, not what it depends on. Reconstructing that chain here
	// would make this a test of Phases 3-5 whose failure mode is a schema
	// trigger the drain predicate never sees.
	var lifecycle int64
	if err := conn.QueryRow(`
		INSERT INTO hangar_exact_lifecycles
			(scope, digest, generation, metageneration, activation_epoch,
			 marker_version, origin, state)
		VALUES ($1, $2, 41, 1, $3, 'hangar-output-v1', 'registered', 'registered')
		RETURNING id`, scope, digest, int64(epoch)).Scan(&lifecycle); err != nil {
		t.Fatalf("seeding the exact lifecycle: %v", err)
	}

	return lifecycle
}

func TestAnOutputFacetWithLiveStateStopsEmissionAndRefusesToBeDisabled(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(70)
	epochs, conn := drainFixture(t, epoch)
	ctx := context.Background()

	seedLiveGeneration(t, conn, epoch)

	// Emission stops FIRST, and it is not gated on the predicate. A drain that
	// checked before it stopped admission would refuse on state that arrived in
	// between and admit state that arrived after the check.
	if err := epochs.Drain(ctx, epoch, activation.FacetOutput); err != nil {
		t.Fatalf("draining the output facet: %v", err)
	}
	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Output != "draining" {
		t.Fatalf("the output facet is %q after a drain", state.Output)
	}
	if state.Base != "enabled" {
		t.Errorf("draining OUTPUT took the base facet out of service too; requirement 59's "+
			"output-only downgrade leaves base control available (base=%s)", state.Base)
	}

	residue, err := epochs.DrainResidue(ctx, epoch, activation.FacetOutput)
	if err != nil {
		t.Fatalf("reading the residue: %v", err)
	}
	if len(residue) == 0 {
		t.Fatal("a plane with a registered exact generation reported no residue; the object " +
			"is in the bucket and this plane is what reclaims it")
	}

	refusal := activation.RefuseDrain(epoch, activation.FacetOutput, residue)
	if !errors.Is(refusal, output.ErrConflict) {
		t.Errorf("the refusal is not a typed conflict: %v", refusal)
	}
	for _, phrase := range []string{"live exact generations", "BLOCKED", "tombstone"} {
		if !strings.Contains(refusal.Error(), phrase) {
			t.Errorf("the refusal does not say %q:\n%v", phrase, refusal)
		}
	}

	// And the terminal step is refused by the caller rather than attempted:
	// Disable does not decide emptiness, and this is what makes "the check
	// passed" and "the check was skipped" different rows.
	if len(residue) != 0 {
		return
	}
	t.Fatal("unreachable")
}

// The predicate reports EVERY class in one pass.
//
// An operator draining a plane wants the whole list at once: discovering the
// next blocker only after clearing the previous one is how a downgrade turns
// into six hours of incremental surprises.
func TestTheDrainPredicateReportsEveryClassAtOnce(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(71)
	epochs, conn := drainFixture(t, epoch)

	lifecycle := seedLiveGeneration(t, conn, epoch)

	if _, err := conn.Exec(`
		INSERT INTO hangar_claims
			(claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
		VALUES (gen_random_uuid(), $1, $2, 'downgrade/claim-one')`,
		lifecycle, int64(epoch)); err != nil {
		t.Fatalf("seeding a claim: %v", err)
	}

	residue, err := epochs.DrainResidue(context.Background(), epoch, activation.FacetOutput)
	if err != nil {
		t.Fatalf("reading the residue: %v", err)
	}

	classes := map[string]bool{}
	for _, one := range residue {
		classes[one.Class] = true
		if one.Count <= 0 {
			t.Errorf("%s was reported with a count of %d", one.Class, one.Count)
		}
		if one.Why == "" {
			t.Errorf("%s was reported with no reason. A refusal an operator cannot act on is "+
				"a refusal they will work around", one.Class)
		}
	}
	for _, expected := range []string{"live exact generations", "active claims"} {
		if !classes[expected] {
			t.Errorf("the predicate did not report %q; it reported %v", expected, classes)
		}
	}
}

// An empty facet reaches `disabled`, so the refusal above is a refusal and not
// an inability.
func TestAnEmptyOutputFacetCanBeDisabledAndBaseThenFollows(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(72)
	epochs, _ := drainFixture(t, epoch)
	ctx := context.Background()

	if err := epochs.Drain(ctx, epoch, activation.FacetOutput); err != nil {
		t.Fatalf("draining output: %v", err)
	}
	residue, err := epochs.DrainResidue(ctx, epoch, activation.FacetOutput)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(residue) != 0 {
		t.Fatalf("an untouched plane reported residue: %v", residue)
	}
	if err := epochs.Disable(ctx, epoch, activation.FacetOutput); err != nil {
		t.Fatalf("disabling output: %v", err)
	}

	// Only NOW can base drain. hangar_output_epoch_needs_base holds base_state
	// in ('attested','enabled') while output_state is anything but 'initial' or
	// 'disabled', which is the ordering `drain --facet=all` exists to follow:
	// output down first, base second, because base is what settles a capture
	// and a plane that lost it mid-capture could finish nothing.
	if err := epochs.Drain(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("draining base after output was disabled: %v", err)
	}
	if err := epochs.Disable(ctx, epoch, activation.FacetBase); err != nil {
		t.Fatalf("disabling base: %v", err)
	}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "disabled" || state.Output != "disabled" {
		t.Errorf("after a full drain the epoch is base=%s output=%s", state.Base, state.Output)
	}
}

// Base cannot be drained ahead of output, and the constraint says so rather than
// the command remembering to.
func TestTheBaseFacetCannotBeDrainedWhileOutputIsInService(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(73)
	epochs, _ := drainFixture(t, epoch)
	ctx := context.Background()

	if err := epochs.Drain(ctx, epoch, activation.FacetBase); err == nil {
		t.Fatal("the base facet was drained while output was still enabled. Base is what " +
			"settles a capture: a plane that lost exact execution control while an output " +
			"capture was nonterminal would have removed the only thing that could finish it")
	}

	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Base != "enabled" {
		t.Errorf("the refused drain moved the base facet anyway: %q", state.Base)
	}
}

// The base facet's own residue is capture-selected executions that still need an
// exact finish or stop acknowledgement.
//
// Narrowed by decision F13: widening it to every controlled execution belongs to
// the sibling track `exact_execution_control`, which is where the non-capture
// callers live. This track has no way to enumerate them and would be asserting
// an empty set.
func TestTheBaseFacetRefusesWhileACaptureSelectedExecutionIsUnsettled(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(74)
	epochs, conn := drainFixture(t, epoch)
	ctx := context.Background()

	if _, err := conn.Exec(`
		INSERT INTO hangar_handoff_predeclarations
			(handoff_id, source_lease_id, execution_id, execution_fence, output_name,
			 activation_epoch, capture_deadline_at)
		VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 1, 'result',
			$1, now() + interval '1 hour')`, int64(epoch)); err != nil {
		t.Fatalf("seeding a predeclaration: %v", err)
	}

	residue, err := epochs.DrainResidue(ctx, epoch, activation.FacetBase)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}

	found := false
	for _, one := range residue {
		if one.Class == "predeclared handoffs with no disposition" {
			found = true
		}
	}
	if !found {
		t.Errorf("a capture-selected execution with no disposition did not block the base "+
			"facet; the residue was %v", residue)
	}
}

// The sequence itself, which had no test at all until Phase 9.
//
// `Epochs.Disable` deliberately does not decide emptiness -- its own comment
// says so, and that separation is right -- so the only thing standing between a
// live plane and `disabled` was the ordering in `cmd/hangar-output-activate`'s
// `drain`, in package main, interleaved with its printing. Requirement 58's
// strongest sentence rested on twenty-five untested lines. Phase 9 moved the
// DECISION into activation.DrainStep and left the printing in the command; these
// are the rows that decision now has.
//
// Order matters here in the way the learning warns about: the ACCEPTING case is
// asserted first, because a refusal assertion passes against a DrainStep that
// refuses unconditionally.

func TestADrainStepOnAnEmptyFacetFinalizesIt(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(75)
	epochs, _ := drainFixture(t, epoch)
	ctx := context.Background()

	outcome, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, true)
	if err != nil {
		t.Fatalf("draining an empty output facet: %v", err)
	}
	if !outcome.Drained {
		t.Error("the step did not report making the enabled -> draining transition")
	}
	if !outcome.Disabled || outcome.State != "disabled" {
		t.Errorf("an empty facet was not finalized: disabled=%v state=%q",
			outcome.Disabled, outcome.State)
	}
}

func TestADrainStepRefusesToFinalizeAFacetThatStillHoldsState(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(76)
	epochs, conn := drainFixture(t, epoch)
	ctx := context.Background()

	seedLiveGeneration(t, conn, epoch)

	outcome, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, true)
	if !errors.Is(err, activation.ErrDrainRefused) {
		t.Fatalf("finalizing a facet with a live generation was not refused: %v", err)
	}
	if !errors.Is(err, output.ErrConflict) {
		t.Errorf("the refusal is not a typed conflict: %v", err)
	}
	if outcome.Disabled {
		t.Error("the refused step reported the facet disabled")
	}

	// And the refusal is a refusal, not a rollback: emission stopped, because
	// stopping it is unconditionally the right thing and is what keeps the set
	// the predicate counted from growing.
	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state.Output != "draining" {
		t.Errorf("a refused finalize left the facet %q; emission must still have stopped",
			state.Output)
	}
}

// Without --finalize the same live state is a report, not an error. This is the
// common operator action and it has to be safe to repeat.
func TestADrainStepWithoutFinalizeReportsResidueRatherThanFailing(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(77)
	epochs, conn := drainFixture(t, epoch)
	ctx := context.Background()

	seedLiveGeneration(t, conn, epoch)

	first, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, false)
	if err != nil {
		t.Fatalf("reporting residue without --finalize: %v", err)
	}
	if len(first.Residue) == 0 {
		t.Fatal("the step reported no residue for a plane with a live generation")
	}
	if first.Disabled {
		t.Error("a step without --finalize disabled the facet")
	}

	second, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, false)
	if err != nil {
		t.Fatalf("the second drain step failed: %v", err)
	}
	if second.Drained {
		t.Error("the second step reported making the enabled -> draining transition again")
	}
	if len(second.Residue) != len(first.Residue) {
		t.Errorf("repeating the step changed the residue: %v then %v",
			first.Residue, second.Residue)
	}
}

// A facet that is neither enabled nor draining is skipped rather than moved.
func TestADrainStepOnADisabledFacetIsASkip(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(78)
	epochs, _ := drainFixture(t, epoch)
	ctx := context.Background()

	if _, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, true); err != nil {
		t.Fatalf("first drain: %v", err)
	}

	outcome, err := epochs.DrainStep(ctx, epoch, activation.FacetOutput, true)
	if err != nil {
		t.Fatalf("draining an already-disabled facet: %v", err)
	}
	if !outcome.Skipped || outcome.State != "disabled" {
		t.Errorf("an already-disabled facet was not skipped: %+v", outcome)
	}
}

// --facet=all takes output down first and base second, and the order is a
// function rather than a comment in the command.
func TestDrainFacetsTakesOutputDownBeforeBase(t *testing.T) {
	all := activation.DrainFacets(true, activation.FacetBase)
	if len(all) != 2 || all[0] != activation.FacetOutput || all[1] != activation.FacetBase {
		t.Errorf("--facet=all drains %v; base is what settles a capture, so output goes first",
			all)
	}

	one := activation.DrainFacets(false, activation.FacetBase)
	if len(one) != 1 || one[0] != activation.FacetBase {
		t.Errorf("a single-facet drain took %v", one)
	}
}
