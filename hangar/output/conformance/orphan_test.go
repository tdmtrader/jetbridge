package conformance

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	testsupport "github.com/concourse/concourse/hangar/output/testsupport"
)

// A marked object is one this store created, and the orphan sweep's whole
// decision rests on reading that off a listing: the marker comes back with
// List, and it names the store the publisher wrote it into. An object with no
// lifecycle row is an orphan only if that store is this one.

func TestAListedObjectCarriesItsMarkerAndItsStore(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := testsupport.Namespace(t, tier.bucket, testTenant, testEpoch)
		role, _ := publisherFor(t, tier, namespace)

		digest := testsupport.Digest("7a")
		object, err := role.EnsurePublication(ctx,
			testsupport.Reservation(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("published, never registered")), 27)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}

		page, err := tier.client.List(ctx, tier.bucket, objectstore.ListRequest{
			Prefix: namespace.ListPrefix(), PageSize: 100,
		})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}

		var found *objectstore.Attrs
		for i, candidate := range page.Objects {
			if candidate.Generation == object.Attributes.Ref.Generation {
				found = &page.Objects[i]
			}
		}
		if found == nil {
			t.Fatalf("the listing did not return the object just published: %v", page.Objects)
		}
		marker, err := output.ParseObjectMarker(found.Metadata)
		if err != nil {
			t.Fatalf("a published object listed without a readable marker: %v", err)
		}
		if marker.Store != namespace.StoreIdentity() {
			t.Errorf("the marker names store %q, and the publisher wrote it into %q",
				marker.Store, namespace.StoreIdentity())
		}
		if found.Created.IsZero() {
			t.Error("a listed object has no creation time; the sweep's age threshold has nothing to compare")
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
