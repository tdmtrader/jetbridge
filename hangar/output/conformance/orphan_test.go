package conformance

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

// A marked object with no lifecycle row is an orphan, and telling it apart from
// the three things it is not is the whole content of Req 40's classification.
//
// This is Go rather than a scenario for the reason the plan gives: the
// precondition is elapsed publication grace, and Req 39 forbids configuring
// grace below the maximum capture deadline plus an hour -- so no runnable chain
// can reach the state without waiting, and a suite that waited would be a suite
// that hangs. Here the clock is a parameter.

func TestAMarkedUnregisteredObjectIsAnOrphanAndNotAMiss(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("7a")
		object, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("published, never registered")), 27)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}

		sweep, err := inventory.New(namespace, inventory.Restrict(tier.client), output.ClockFunc(time.Now))
		if err != nil {
			t.Fatalf("building the inventory: %v", err)
		}

		page, err := sweep.ListPage(ctx, output.InventoryCursor{
			ProtocolVersion: output.ProtocolVersion,
			ActivationEpoch: testEpoch,
			CursorFence:     1,
			UpdatedAt:       output.NewTimestamp(fixedInstant),
		}, output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("listing: %v", err)
		}

		var found output.InventoryObject
		for _, candidate := range page.Objects {
			if candidate.Generation == object.Attributes.Ref.Generation {
				found = candidate
			}
		}
		if found.ObjectKey == "" {
			t.Fatalf("the sweep did not find the object it just published: %v", page.Objects)
		}
		if !found.Managed {
			t.Fatal("a marked object came back unmanaged")
		}
	})
}

func TestPublicationGraceCannotBeShortenedIntoACaptureWindow(t *testing.T) {
	floor := output.MaxCaptureDeadline + output.PublicationGraceMargin

	// The rule under test is the one the two controller binaries actually call
	// at startup -- output.ValidatePublicationGrace -- and not a second copy of
	// it in the inventory role. The second copy existed, stated the same rule
	// with different sentinels, and had no production caller; it is gone, and
	// the reachability guard is what stops the next one being written.
	//
	// The orphan/within-grace distinction itself is proved where it is decided:
	// against real PostgreSQL in atc/db's adoption specs, through
	// AdoptManagedOrphan, on the database clock.

	// The control: the default is admissible.
	if err := output.ValidatePublicationGrace(output.DefaultPublicationGrace); err != nil {
		t.Fatalf("the default publication grace was refused: %v", err)
	}
	if err := output.ValidatePublicationGrace(floor); err != nil {
		t.Errorf("the floor itself was refused: %v. Req 39 admits a grace that exceeds the "+
			"maximum capture deadline by AT LEAST an hour", err)
	}

	if err := output.ValidatePublicationGrace(floor - time.Minute); err == nil {
		t.Errorf("a publication grace one minute below the floor of %s was accepted. Below it an "+
			"object becomes adoptable while its own capture may still be retrying", floor)
	}
	if err := output.ValidatePublicationGrace(output.MaxPublicationGrace + time.Hour); err == nil {
		t.Error("a publication grace past the maximum bound was accepted")
	}
}
