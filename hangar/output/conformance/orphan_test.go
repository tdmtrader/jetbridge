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

		grace := output.DefaultPublicationGrace
		createdAt := found.CreatedAt.UTC()

		// Registered: the lifecycle row exists, so nothing may adopt it however
		// old it is. This is the control, and it comes first because every
		// other row below is an absence.
		if class := inventory.Classify(found, true, grace, createdAt.Add(30*24*time.Hour)); class != inventory.ClassificationRegistered {
			t.Errorf("a registered object 30 days old classified as %q", class)
		}

		// Unregistered inside its grace: its capture may still be retrying.
		if class := inventory.Classify(found, false, grace, createdAt.Add(time.Hour)); class != inventory.ClassificationWithinGrace {
			t.Errorf("an unregistered object one hour old classified as %q, expected %q. Adopting "+
				"it would race the capture that created it", class, inventory.ClassificationWithinGrace)
		}

		// Unregistered past its grace: an orphan. Not a miss, and not a
		// binding.
		if class := inventory.Classify(found, false, grace, createdAt.Add(grace+time.Hour)); class != inventory.ClassificationOrphan {
			t.Errorf("an unregistered object past its publication grace classified as %q, "+
				"expected %q", class, inventory.ClassificationOrphan)
		}

		// And an unmanaged object is never any of those.
		unmanaged := found
		unmanaged.Managed = false
		unmanaged.Marker = output.ObjectMarker{}
		if class := inventory.Classify(unmanaged, false, grace, createdAt.Add(grace+time.Hour)); class != inventory.ClassificationUnmanaged {
			t.Errorf("an unmarked object classified as %q; this plane never relabels, adopts or "+
				"deletes one", class)
		}
	})
}

func TestPublicationGraceCannotBeShortenedIntoACaptureWindow(t *testing.T) {
	floor := output.MaxCaptureDeadline + output.PublicationGraceMargin

	// The control: the default is admissible.
	if err := inventory.ValidateGrace(output.DefaultPublicationGrace, output.MaxCaptureDeadline); err != nil {
		t.Fatalf("the default publication grace was refused: %v", err)
	}
	if err := inventory.ValidateGrace(floor, output.MaxCaptureDeadline); err != nil {
		t.Errorf("the floor itself was refused: %v", err)
	}

	if err := inventory.ValidateGrace(floor-time.Minute, output.MaxCaptureDeadline); err == nil {
		t.Errorf("a publication grace one minute below the floor of %s was accepted. Below it an "+
			"object becomes adoptable while its own capture may still be retrying", floor)
	}
	if err := inventory.ValidateGrace(output.MaxPublicationGrace+time.Hour, output.MaxCaptureDeadline); err == nil {
		t.Error("a publication grace past the maximum bound was accepted")
	}

	// The vocabulary is closed and each member is distinct, so a classifier
	// that collapsed two of them would be visible.
	seen := map[inventory.Classification]bool{}
	for _, member := range inventory.Classifications() {
		if seen[member] {
			t.Errorf("%q appears twice in the closed vocabulary", member)
		}
		seen[member] = true
	}
	if len(seen) != 4 {
		t.Errorf("the classification vocabulary has %d members, expected 4", len(seen))
	}
}
