package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
	"github.com/concourse/concourse/hangar/output/publisher"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

func executionIdentity() executioncontrol.Identity {
	return executioncontrol.Identity{
		ExecutionID: "33333333-3333-4333-8333-333333333333",
		Fence:       1,
	}
}

const publishTimeout = 10 * time.Second

// canonicalBytes stands in for a sealed canonical archive. Nothing here
// canonicalizes: the canonicalizer is the foundation's and has its own tables,
// and a conformance suite that re-derived a digest would be asserting hangar's
// tree code through the object store.
func canonicalBytes(text string) []byte { return []byte(text) }

func publisherFor(t *testing.T, tier substrate, namespace output.OutputNamespace) (*publisher.Publisher, *gcstest.Recorder) {
	t.Helper()

	recorder := gcstest.Record(tier.client)
	role, err := publisher.New(namespace, publisher.Restrict(recorder), publishTimeout)
	if err != nil {
		t.Fatalf("building the publisher: %v", err)
	}

	return role, recorder
}

// ---------------------------------------------------------------------------
// The API-level profile. Both tiers run it; tier 1 is expected to agree, and
// where it cannot the case says so rather than being skipped silently.
// ---------------------------------------------------------------------------

func TestCreateIfAbsentPublishesOnceAndReportsAnExactGeneration(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("ab")
		reservation := reservationFor(t, namespace, testReservation, digest)

		object, err := role.EnsureObject(ctx, reservation,
			bytes.NewReader(canonicalBytes("sealed tree")), 11)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}
		if object.Deduplicated {
			t.Error("the first create of a key reported deduplication; nothing was there")
		}
		if object.Attributes.Ref.Generation <= 0 {
			t.Errorf("the store reported generation %d; a generation is assigned at creation "+
				"and is what an exact ref is", object.Attributes.Ref.Generation)
		}
		if object.Attributes.Ref.Scope != namespace.Scope() {
			t.Errorf("the object was published into scope %q, the derived namespace is %q",
				object.Attributes.Ref.Scope, namespace.Scope())
		}
		if object.Metageneration != 1 {
			t.Errorf("a newly created object reports metageneration %d, expected 1",
				object.Metageneration)
		}
		if object.Marker.Version != output.MarkerVersion {
			t.Errorf("the object came back marked %q", object.Marker.Version)
		}
		if object.Marker.ReservationID != testReservation {
			t.Errorf("the marker names reservation %q, this capture is %q",
				object.Marker.ReservationID, testReservation)
		}

		// An exact-generation stat is a body-capable objects.get used for its
		// metadata. Requirement 41 says IAM has no metadata-only object
		// permission, so this is the call the plane actually makes and the
		// call the profile has to admit.
		stat, err := role.StatExactObject(ctx, object.Attributes.Ref)
		if err != nil {
			t.Fatalf("stat of the exact generation: %v", err)
		}
		if stat.Attributes.Ref != object.Attributes.Ref {
			t.Errorf("the stat reports %v and the create reported %v",
				stat.Attributes.Ref, object.Attributes.Ref)
		}

		// A stat of a generation that is not there is absence, and stays
		// absence. Req 27: none of these becomes a cache miss.
		absent := object.Attributes.Ref
		absent.Generation++
		if _, err := role.StatExactObject(ctx, absent); !errors.Is(err, output.ErrNotFound) {
			t.Errorf("a stat of a generation that is not there returned %v, expected ErrNotFound", err)
		}

		// A reservation resolved to another namespace's scope is refused before
		// anything is written, not after.
		//
		// The order is the whole assertion. Without the refusal the publisher
		// derives its OWN key from its own namespace, creates the object there
		// carrying a foreign-scope marker, and only the read-back classify
		// notices -- by which time the bytes have landed at a key create-if-absent
		// will refuse forever. So this checks the error AND that the key is empty.
		elsewhere, err := output.DeriveNamespace(output.NamespaceConfig{
			Store:            output.StoreGCS,
			Bucket:           tier.bucket,
			DeploymentPrefix: "deployments/blue",
			TenantID:         "tenant-elsewhere",
			ActivationEpoch:  testEpoch,
		})
		if err != nil {
			t.Fatalf("deriving another tenant's namespace: %v", err)
		}
		if elsewhere.Scope() == namespace.Scope() {
			t.Fatal("the two tenants derived the same scope; this case would assert nothing")
		}

		foreignDigest := digestOf("ef")
		foreign := reservationFor(t, elsewhere, otherReservation, foreignDigest)
		if _, err := role.EnsureObject(ctx, foreign,
			bytes.NewReader(canonicalBytes("another namespace's tree")), 24); !errors.Is(err, output.ErrUnauthorized) {
			t.Errorf("a reservation resolved to scope %q was published into %q and answered with "+
				"%v, expected ErrUnauthorized", foreign.Scope, namespace.Scope(), err)
		}

		stranded, err := namespace.ObjectKey(foreignDigest)
		if err != nil {
			t.Fatalf("deriving the key: %v", err)
		}
		if _, present := read(t, tier, stranded); present {
			t.Errorf("the refused publish left bytes at %s. A capture publishes into the "+
				"namespace its epoch derived and no other, and an object created under this "+
				"namespace's key with another scope's marker is unmanaged the moment it lands",
				stranded)
		}
		foreignKey, err := elsewhere.ObjectKey(foreignDigest)
		if err != nil {
			t.Fatalf("deriving the foreign key: %v", err)
		}
		if _, present := read(t, tier, foreignKey); present {
			t.Errorf("the refused publish wrote into the other namespace at %s", foreignKey)
		}
	})
}

func TestIdenticalBytesDeduplicateAndADifferentVariantIsATypedCollision(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("cd")
		first := reservationFor(t, namespace, testReservation, digest)

		created, err := role.EnsureObject(ctx, first,
			bytes.NewReader(canonicalBytes("the first tree")), 14)
		if err != nil {
			t.Fatalf("the first publish: %v", err)
		}

		// The control, and the whole reason convention 6 asks for a twin: a
		// second capture of the same canonical bytes deduplicates to one
		// object, and gets its own answer rather than the first one's.
		second := reservationFor(t, namespace, otherReservation, digest)
		deduplicated, err := role.EnsureObject(ctx, second,
			bytes.NewReader(canonicalBytes("the first tree")), 14)
		if err != nil {
			t.Fatalf("the second publish of identical bytes: %v", err)
		}
		if !deduplicated.Deduplicated {
			t.Error("a second capture of identical canonical bytes created a second object")
		}
		if deduplicated.Attributes.Ref != created.Attributes.Ref {
			t.Errorf("the deduplicated capture named %v and the object is %v",
				deduplicated.Attributes.Ref, created.Attributes.Ref)
		}
		if deduplicated.Marker.ReservationID != testReservation {
			t.Errorf("the deduplicated object's marker names %q; deduplication does not relabel "+
				"the object it deduplicated against", deduplicated.Marker.ReservationID)
		}

		// The twin. A different digest at the same key cannot be produced by
		// the publisher -- the key is derived from the digest -- so the store
		// is seeded with the wrong variant, which is the only honest way to
		// tell dedup from overwrite.
		collisionDigest := digestOf("ef")
		key, err := namespace.ObjectKey(collisionDigest)
		if err != nil {
			t.Fatalf("deriving the collision key: %v", err)
		}
		strangerMarker := namespace.MarkerFor(otherReservation, digestOf("99"),
			output.NewTimestamp(fixedInstant))
		seed(t, tier, key, canonicalBytes("somebody else's bytes"), strangerMarker.Metadata())

		colliding := reservationFor(t, namespace, testReservation, collisionDigest)
		_, err = role.EnsureObject(ctx, colliding,
			bytes.NewReader(canonicalBytes("our bytes")), 9)
		if !errors.Is(err, output.ErrConflict) {
			t.Fatalf("a different variant at the key was answered with %v, expected ErrConflict", err)
		}
		if !strings.Contains(err.Error(), "never overwritten") {
			t.Errorf("the collision refusal does not say the object is not overwritten: %v", err)
		}

		body, found := read(t, tier, key)
		if !found {
			t.Fatal("the seeded object is gone; the publisher overwrote a collision")
		}
		if string(body) != "somebody else's bytes" {
			t.Errorf("the object at the key now reads %q; the collision was overwritten", body)
		}
	})
}

func TestAnUnmarkedObjectIsATypedCollisionAndAMarkedOneStillDeduplicates(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		// The positive control, asserted first (convention 5): a marked object
		// at the key deduplicates. Without it, the refusal below would pass on
		// a publisher that refused everything at an occupied key.
		markedDigest := digestOf("11")
		markedKey, err := namespace.ObjectKey(markedDigest)
		if err != nil {
			t.Fatalf("deriving the marked key: %v", err)
		}
		marker := namespace.MarkerFor(otherReservation, markedDigest, output.NewTimestamp(fixedInstant))
		seed(t, tier, markedKey, canonicalBytes("already published"), marker.Metadata())

		object, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, markedDigest),
			bytes.NewReader(canonicalBytes("already published")), 17)
		if err != nil {
			t.Fatalf("publishing against a correctly marked object: %v", err)
		}
		if !object.Deduplicated {
			t.Error("a correctly marked object at the key did not deduplicate")
		}

		// And the absence: no marker at all.
		unmarkedDigest := digestOf("22")
		unmarkedKey, err := namespace.ObjectKey(unmarkedDigest)
		if err != nil {
			t.Fatalf("deriving the unmarked key: %v", err)
		}
		seed(t, tier, unmarkedKey, canonicalBytes("stranger"), map[string]string{"note": "not ours"})

		_, err = role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, unmarkedDigest),
			bytes.NewReader(canonicalBytes("stranger")), 8)
		if !errors.Is(err, output.ErrConflict) {
			t.Fatalf("an unmarked object at the key was answered with %v, expected ErrConflict", err)
		}
		if !strings.Contains(err.Error(), "unmanaged") {
			t.Errorf("the refusal does not say the object is unmanaged: %v", err)
		}

		// A wrong marker version is a deliberate statement by another cohort
		// and is likewise never overwritten.
		wrongDigest := digestOf("33")
		wrongKey, err := namespace.ObjectKey(wrongDigest)
		if err != nil {
			t.Fatalf("deriving the wrong-version key: %v", err)
		}
		wrongMetadata := namespace.MarkerFor(otherReservation, wrongDigest,
			output.NewTimestamp(fixedInstant)).Metadata()
		wrongMetadata[output.MarkerKeyVersion] = "hangar-output-v2"
		seed(t, tier, wrongKey, canonicalBytes("another cohort"), wrongMetadata)

		_, err = role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, wrongDigest),
			bytes.NewReader(canonicalBytes("another cohort")), 14)
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("a wrong marker version was answered with %v, expected ErrConflict", err)
		}
	})
}

// A marker is a claim ABOUT bytes, and dedup checked the claim and never the
// bytes.
//
// `classify` compared the marker's scope, the marker's digest, and that the
// object had some size and some metageneration -- so an object whose BODY had
// been replaced under the same marker metadata was deduplicated against and
// registered, and the receipt then attested a generation whose contents are not
// the tree the capture canonicalized. Req 23 lists corrupt metadata or body as
// a typed collision "even if a weaker content check appears to match", and
// "the marker says the right digest" is exactly that weaker check.
//
// Reaching it needs a writer on the output bucket, which the trust model puts
// in the at-risk class. It is a cheap check regardless: the canonical size is
// already in hand at every call site, and comparing it costs one field.
//
// The positive control is first: the same bytes at the right size deduplicate.
func TestAnObjectWhoseBodyDoesNotMatchTheCaptureIsATypedCollision(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("44")
		key, err := namespace.ObjectKey(digest)
		if err != nil {
			t.Fatalf("deriving the key: %v", err)
		}
		body := canonicalBytes("the tree this capture canonicalized")
		marker := namespace.MarkerFor(otherReservation, digest, output.NewTimestamp(fixedInstant))
		seed(t, tier, key, body, marker.Metadata())

		object, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("publishing against identical bytes: %v", err)
		}
		if !object.Deduplicated {
			t.Fatal("identical bytes under a correct marker did not deduplicate")
		}

		// And the same key, the same marker metadata, a different body. This
		// is the lost-response path: the capture repeats its create, the store
		// answers "already there", and what is there is not what it wrote.
		replacedDigest := digestOf("55")
		replacedKey, err := namespace.ObjectKey(replacedDigest)
		if err != nil {
			t.Fatalf("deriving the replaced key: %v", err)
		}
		replacedMarker := namespace.MarkerFor(otherReservation, replacedDigest,
			output.NewTimestamp(fixedInstant))
		seed(t, tier, replacedKey, canonicalBytes("somebody else's much longer body"),
			replacedMarker.Metadata())

		captured := canonicalBytes("short")
		_, err = role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, replacedDigest),
			bytes.NewReader(captured), int64(len(captured)))
		if !errors.Is(err, output.ErrConflict) {
			t.Fatalf("an object whose body is not this capture's tree was answered with %v, "+
				"expected ErrConflict", err)
		}
		if !strings.Contains(err.Error(), "stored bytes") {
			t.Errorf("the refusal does not name the size disagreement: %v", err)
		}
	})
}

func TestTheMarkerIsImmutableAtCreationAndThePublisherCannotChangeIt(t *testing.T) {
	// This is a statement about the *type*, and it is checked by compilation
	// rather than by a call: publisher.Handle offers a writer, a reader and a
	// stat, and no method that updates metadata. There is no runtime assertion
	// that can prove the absence of a call the language does not admit.
	//
	// The runtime half that can be checked is the other direction: two
	// captures of one key never produce two markers, because the second never
	// writes.
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, recorder := publisherFor(t, tier, namespace)

		digest := digestOf("44")
		created, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("once")), 4)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}

		recorder.Reset()
		if _, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, otherReservation, digest),
			bytes.NewReader(canonicalBytes("once")), 4); err != nil {
			t.Fatalf("the deduplicating publish: %v", err)
		}

		stat, err := role.StatExactObject(ctx, created.Attributes.Ref)
		if err != nil {
			t.Fatalf("stat after deduplication: %v", err)
		}
		if stat.Metageneration != 1 {
			t.Errorf("the object's metageneration is %d after a second capture. Metageneration "+
				"counts metadata updates, so anything above 1 means the marker was rewritten -- "+
				"which is the one thing that would turn ownership evidence into a label",
				stat.Metageneration)
		}
		if stat.Marker.ReservationID != testReservation {
			t.Errorf("the marker now names %q, the creating capture was %q",
				stat.Marker.ReservationID, testReservation)
		}
	})
}

func TestBucketWideListPagesUnderTheServerDerivedPrefix(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		for _, fill := range []string{"55", "66", "77"} {
			digest := digestOf(fill)
			if _, err := role.EnsureObject(ctx,
				reservationFor(t, namespace, testReservation, digest),
				bytes.NewReader(canonicalBytes("tree "+fill)), 7); err != nil {
				t.Fatalf("seeding %s: %v", fill, err)
			}
		}
		// An object outside the deployment prefix, which a bucket-wide list
		// will see and the sweep must not: this is what makes "under the
		// server-derived prefix" a claim rather than a hope.
		seed(t, tier, "someone-elses-prefix/hangar/v1/scopes/x/trees/sha256/dead.tar.zst",
			canonicalBytes("outside"), nil)

		sweep, err := inventory.New(namespace, inventory.Restrict(gcstest.Record(tier.client)))
		if err != nil {
			t.Fatalf("building the inventory: %v", err)
		}

		cursor := output.InventoryCursor{
			ProtocolVersion: output.ProtocolVersion,
			ActivationEpoch: testEpoch,
			CursorFence:     1,
			UpdatedAt:       output.NewTimestamp(fixedInstant),
		}
		budget := output.DefaultPageBudget()
		budget.MaxObjects = 2

		var keys []string
		passes := 0
		for pass := 0; pass < 5; pass++ {
			passes++
			page, err := sweep.ListPage(ctx, cursor, budget)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, object := range page.Objects {
				keys = append(keys, object.ObjectKey)
				if !object.Managed {
					t.Errorf("%s came back unmanaged; every object this sweep seeded is marked",
						object.ObjectKey)
				}
			}
			if page.Next.AfterKey == "" {
				break
			}
			cursor = page.Next
		}

		if len(keys) != 3 {
			t.Fatalf("the sweep read %d objects, expected the 3 under the deployment prefix: %v",
				len(keys), keys)
		}
		if passes < 2 {
			t.Errorf("the sweep read 3 objects at a page size of 2 in %d pass(es); it did not "+
				"page, so this case is asserting the prefix rule and not the paging rule", passes)
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, namespace.ListPrefix()) {
				t.Errorf("the sweep returned %q, which is not under %q", key, namespace.ListPrefix())
			}
		}
	})
}

func TestTheExactGenerationDeleteIsConditionalAndTyped(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("88")
		object, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("to be reclaimed")), 15)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}

		recorder := gcstest.Record(tier.client)
		sweeper, err := reclaimer.New(namespace, reclaimer.Restrict(recorder))
		if err != nil {
			t.Fatalf("building the reclaimer: %v", err)
		}

		// The wrong generation first: a conditional delete that missed is a
		// typed conflict and never broadens.
		//
		// This half runs only where the substrate enforces the precondition at
		// all. fsouza/fake-gcs-server v1.52.3 does not -- it deletes whatever
		// is at the key, whether the delete carries a generation pin, a
		// GenerationMatch, or both -- so asserting it there would assert the
		// emulator's shortcut rather than this code. The row is named in
		// TestTheSubstrateGapsAreNamed and is covered on tier 1, where the fake
		// does enforce it, and on real GCS in Phase 9.
		if tier.can.EnforcesDeletePreconditions {
			wrong := output.DeletePrecondition{
				Generation:     object.Attributes.Ref.Generation + 1,
				Metageneration: object.Metageneration,
			}
			wrongRef := object.Attributes.Ref
			wrongRef.Generation = wrong.Generation
			outcome, err := sweeper.DeleteExactGeneration(ctx, wrongRef, wrong)
			switch outcome {
			case output.DeleteGenerationConflict, output.DeleteAlreadyAbsent:
				// Both are honest answers to "delete generation N+1": the store
				// may report a missing generation as absence or as a
				// precondition failure, and neither is a licence to delete
				// generation N.
			default:
				t.Errorf("deleting the wrong generation reported %q (%v); the object must not be "+
					"removed on a precondition that did not match", outcome, err)
			}
			if _, err := role.StatExactObject(ctx, object.Attributes.Ref); err != nil {
				t.Fatalf("the object is gone after a delete of another generation: %v", err)
			}

			// And the metageneration half, which Req 47 names beside the
			// generation ("replacement generation/metageneration ... conflict
			// becomes debt") and which the generation pin alone cannot answer
			// for: the ref is the right one, the generation matches, and only
			// the metageneration has moved -- which is what a metadata change
			// between the read and the delete looks like. Without the
			// `If(conditions)` on the delete this row deletes the object and the
			// wrong-generation row above does not notice, because the store
			// reports a missing generation as absence.
			//
			// Tier 1 only, and not because the fake is more convenient: tier 2's
			// fake-gcs-server v1.52.3 ignores delete preconditions outright
			// (measured in probeCapabilities, named in knownSubstrateGaps), so
			// there the row would assert the emulator's shortcut. Real GCS
			// answers it in Phase 9.
			staleMetageneration := output.DeletePrecondition{
				Generation:     object.Attributes.Ref.Generation,
				Metageneration: object.Metageneration + 1,
			}
			outcome, err = sweeper.DeleteExactGeneration(ctx, object.Attributes.Ref, staleMetageneration)
			if outcome != output.DeleteGenerationConflict {
				t.Errorf("deleting the right generation at a metageneration it is not at reported "+
					"%q (%v), expected %q. The metageneration moved between the read and the "+
					"delete, so this delete is about an object nobody looked at",
					outcome, err, output.DeleteGenerationConflict)
			}
			if !errors.Is(err, output.ErrGenerationConflict) {
				t.Errorf("the metageneration conflict is typed %v; Req 47 makes it debt and never "+
					"an unconditional retry", err)
			}
			if _, err := role.StatExactObject(ctx, object.Attributes.Ref); err != nil {
				t.Fatalf("the object is gone after a delete whose metageneration precondition did "+
					"not match: %v", err)
			}
		} else {
			t.Logf("%s does not enforce delete preconditions, so neither the wrong-generation "+
				"nor the wrong-metageneration row runs here; both are tier 1's and real GCS's "+
				"(Phase 9). knownSubstrateGaps names the gap.", tier.name)
		}

		// And the right one.
		outcome, err := sweeper.DeleteExactGeneration(ctx, object.Attributes.Ref,
			output.DeletePrecondition{
				Generation:     object.Attributes.Ref.Generation,
				Metageneration: object.Metageneration,
			})
		if err != nil {
			t.Fatalf("the conditional delete: %v", err)
		}
		if outcome != output.DeleteConfirmed {
			t.Errorf("the conditional delete reported %q, expected %q", outcome, output.DeleteConfirmed)
		}

		// Repeating it is absence, not confirmation. Confirming a delete that
		// removed nothing is how reclamation claims evidence it does not have.
		outcome, err = sweeper.DeleteExactGeneration(ctx, object.Attributes.Ref,
			output.DeletePrecondition{
				Generation:     object.Attributes.Ref.Generation,
				Metageneration: object.Metageneration,
			})
		if err != nil {
			t.Fatalf("the repeated delete: %v", err)
		}
		if outcome != output.DeleteAlreadyAbsent {
			t.Errorf("the repeated delete reported %q, expected %q", outcome, output.DeleteAlreadyAbsent)
		}
	})
}

func TestASameKeyNewGenerationIsANewExactObject(t *testing.T) {
	// The store may hold a second generation at one key -- a replacement is
	// exactly that -- and the plane's identity has to follow the generation
	// rather than the key. This is the API fact that makes an exact ref exact.
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		role, _ := publisherFor(t, tier, namespace)

		digest := digestOf("99")
		first, err := role.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("generation one")), 14)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}

		key, err := namespace.ObjectKey(digest)
		if err != nil {
			t.Fatalf("deriving the key: %v", err)
		}
		replacement := namespace.MarkerFor(otherReservation, digest, output.NewTimestamp(fixedInstant))
		attrs := seed(t, tier, key, canonicalBytes("generation two"), replacement.Metadata())

		if attrs.Generation == first.Attributes.Ref.Generation {
			t.Fatalf("the store reused generation %d for a new write; generations are assigned "+
				"per object version and an exact ref would name two different objects",
				attrs.Generation)
		}

		// The old ref no longer resolves; the new one does. Whether the plane
		// treats that as supersession is Phase 7's; what the profile has to
		// admit is that the two are distinguishable at all.
		if _, err := role.StatExactObject(ctx, first.Attributes.Ref); !errors.Is(err, output.ErrNotFound) {
			t.Errorf("the superseded generation still resolves: %v", err)
		}
		if _, err := role.StatExactObject(ctx, namespace.Ref(digest, attrs.Generation)); err != nil {
			t.Errorf("the new generation does not resolve: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Tier 1 only: the faults a server-backed fake cannot inject on demand.
// ---------------------------------------------------------------------------

func TestAnAmbiguousUploadIsReconciledByAnExactStat(t *testing.T) {
	tier := tier1(t)
	ctx := context.Background()
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	digest := digestOf("aa")
	reservation := reservationFor(t, namespace, testReservation, digest)

	// The object commits and the response is lost. A caller that assumed
	// failure would retry into its own object and call it a collision; a
	// caller that assumed success would sign a receipt over a generation it
	// never read.
	tier.memory.Inject(gcstest.Faults{CreateResponseLost: true})

	object, err := role.EnsureObject(ctx, reservation,
		bytes.NewReader(canonicalBytes("ambiguous")), 9)
	if err != nil {
		t.Fatalf("the ambiguous create was not reconciled: %v", err)
	}
	if object.Attributes.Ref.Generation <= 0 {
		t.Error("the reconciled object has no generation")
	}
	if object.Deduplicated {
		t.Error("the reconciliation reported deduplication against this capture's own object; " +
			"the marker names this reservation, so these bytes are its own")
	}
	if object.Marker.ReservationID != testReservation {
		t.Errorf("the reconciled marker names %q", object.Marker.ReservationID)
	}

	// And the other half: an ambiguous create where nothing landed is
	// infrastructure, and the capture has not passed its publish point.
	tier.memory.Inject(gcstest.Faults{CreateTimeout: true})
	if _, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digestOf("bb")),
		bytes.NewReader(canonicalBytes("never landed")), 12); !errors.Is(err, output.ErrTimeout) {
		t.Errorf("a create that timed out was answered with %v, expected ErrTimeout", err)
	}
}

func TestALostDeleteResponseIsNeverReportedAsConfirmed(t *testing.T) {
	tier := tier1(t)
	ctx := context.Background()
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	digest := digestOf("cc")
	object, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digest),
		bytes.NewReader(canonicalBytes("to be reclaimed")), 15)
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}

	sweeper, err := reclaimer.New(namespace, reclaimer.Restrict(tier.client))
	if err != nil {
		t.Fatalf("building the reclaimer: %v", err)
	}

	tier.memory.Inject(gcstest.Faults{DeleteResponseLost: true})
	outcome, err := sweeper.DeleteExactGeneration(ctx, object.Attributes.Ref,
		output.DeletePrecondition{
			Generation:     object.Attributes.Ref.Generation,
			Metageneration: object.Metageneration,
		})
	if outcome == output.DeleteConfirmed {
		t.Error("a delete whose response was lost was reported as confirmed. The object is gone " +
			"either way; the difference is whether reclamation may claim it proved that, and it " +
			"may not -- inferred reclamation needs the admitted-delete record plus observed " +
			"absence, which is a different piece of evidence")
	}
	if outcome != output.DeleteInfrastructure {
		t.Errorf("a lost delete response reported %q, expected %q", outcome, output.DeleteInfrastructure)
	}
	if !errors.Is(err, output.ErrInfrastructure) {
		t.Errorf("a lost delete response returned %v", err)
	}

	// Absence is then observable, which is the other half of what an inferred
	// reclamation needs.
	tier.memory.Inject(gcstest.Faults{})
	absent, err := sweeper.ObserveExactAbsence(ctx, object.Attributes.Ref)
	if err != nil {
		t.Fatalf("observing absence: %v", err)
	}
	if !absent {
		t.Error("the object is still there after a delete whose response was lost")
	}
}

func TestATruncatedOrCorruptBodyIsNotAValidRead(t *testing.T) {
	tier := tier1(t)
	ctx := context.Background()
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	digest := digestOf("dd")
	object, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digest),
		bytes.NewReader(canonicalBytes("a whole canonical tree")), 22)
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}

	lease := output.ReadLease{
		ProtocolVersion: output.ProtocolVersion,
		ReadLeaseID:     "77777777-7777-4777-8777-777777777777",
		ClaimID:         "66666666-6666-4666-8666-666666666666",
		Ref:             object.Attributes.Ref,
		ActivationEpoch: testEpoch,
		LeaseFence:      1,
		GrantedAt:       output.NewTimestamp(fixedInstant),
		ExpiresAt:       output.NewTimestamp(fixedInstant.Add(20 * time.Minute)),
	}

	// The control: a whole read returns the whole object.
	body, _, err := role.OpenExactObject(ctx, object.Attributes.Ref, lease)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	whole, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if len(whole) != int(object.Attributes.StoredBytes) {
		t.Fatalf("the whole read returned %d of %d bytes", len(whole), object.Attributes.StoredBytes)
	}

	// Truncated: fewer bytes than the object says it has. A caller that
	// trusted Size would call this a complete tree.
	tier.memory.Inject(gcstest.Faults{TruncateReadAfter: 4})
	body, attrs, err := role.OpenExactObject(ctx, object.Attributes.Ref, lease)
	if err != nil {
		t.Fatalf("opening the truncated object: %v", err)
	}
	short, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading the truncated object: %v", err)
	}
	_ = body.Close()
	if int64(len(short)) == attrs.Attributes.StoredBytes {
		t.Error("the truncated read returned as many bytes as the object reports; the fault did " +
			"not fire, so this case is asserting nothing")
	}

	// A read that a lease does not cover is refused before the store is
	// reached at all.
	otherRef := object.Attributes.Ref
	otherRef.Generation++
	if _, _, err := role.OpenExactObject(ctx, otherRef, lease); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a read outside the lease was answered with %v, expected ErrUnauthorized", err)
	}
}

func TestACancelledContextIsNeverAnAbsentObject(t *testing.T) {
	tier := tier1(t)
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digestOf("ee")),
		bytes.NewReader(canonicalBytes("cancelled")), 9)
	if errors.Is(err, output.ErrNotFound) {
		t.Fatalf("a cancelled publish was reported as absence: %v", err)
	}
	if !errors.Is(err, output.ErrTimeout) && !errors.Is(err, output.ErrInfrastructure) {
		t.Errorf("a cancelled publish returned %v; cancellation is a typed outcome and never a "+
			"cache miss", err)
	}
}

func TestAnUnauthorizedStoreIsNeverACacheMiss(t *testing.T) {
	tier := tier1(t)
	ctx := context.Background()
	namespace := namespaceFor(t, tier.bucket)
	role, _ := publisherFor(t, tier, namespace)

	tier.memory.Inject(gcstest.Faults{Unauthorized: true})

	_, err := role.EnsureObject(ctx,
		reservationFor(t, namespace, testReservation, digestOf("ff")),
		bytes.NewReader(canonicalBytes("forbidden")), 9)
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("an unauthorized create returned %v, expected ErrUnauthorized", err)
	}
	if errors.Is(err, output.ErrNotFound) {
		t.Error("an unauthorized create was reported as absence")
	}
}

// ---------------------------------------------------------------------------
// Role honesty, over the adapter call log, on both tiers.
// ---------------------------------------------------------------------------

func TestEachRoleIssuesOnlyItsOwnRPCs(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)

		digest := digestOf("12")

		publishRecorder := gcstest.Record(tier.client)
		publishRole, err := publisher.New(namespace, publisher.Restrict(publishRecorder), publishTimeout)
		if err != nil {
			t.Fatalf("building the publisher: %v", err)
		}
		object, err := publishRole.EnsureObject(ctx,
			reservationFor(t, namespace, testReservation, digest),
			bytes.NewReader(canonicalBytes("role honesty")), 12)
		if err != nil {
			t.Fatalf("publishing: %v", err)
		}
		if _, err := publishRole.StatExactObject(ctx, object.Attributes.Ref); err != nil {
			t.Fatalf("stat: %v", err)
		}
		assertOnly(t, "publisher", publishRecorder,
			objectstore.OpCreate, objectstore.OpStat, objectstore.OpRead)

		inventoryRecorder := gcstest.Record(tier.client)
		sweep, err := inventory.New(namespace, inventory.Restrict(inventoryRecorder))
		if err != nil {
			t.Fatalf("building the inventory: %v", err)
		}
		if _, err := sweep.ListPage(ctx, output.InventoryCursor{
			ProtocolVersion: output.ProtocolVersion,
			ActivationEpoch: testEpoch,
			CursorFence:     1,
			UpdatedAt:       output.NewTimestamp(fixedInstant),
		}, output.DefaultPageBudget()); err != nil {
			t.Fatalf("listing: %v", err)
		}
		if _, err := sweep.StatExactObject(ctx, object.Attributes.Ref); err != nil {
			t.Fatalf("inventory stat: %v", err)
		}
		assertOnly(t, "inventory", inventoryRecorder, objectstore.OpList, objectstore.OpStat)

		reclaimRecorder := gcstest.Record(tier.client)
		sweeper, err := reclaimer.New(namespace, reclaimer.Restrict(reclaimRecorder))
		if err != nil {
			t.Fatalf("building the reclaimer: %v", err)
		}
		if _, err := sweeper.ObserveExactAbsence(ctx, object.Attributes.Ref); err != nil {
			t.Fatalf("observing: %v", err)
		}
		if _, err := sweeper.DeleteExactGeneration(ctx, object.Attributes.Ref,
			output.DeletePrecondition{
				Generation:     object.Attributes.Ref.Generation,
				Metageneration: object.Metageneration,
			}); err != nil {
			t.Fatalf("deleting: %v", err)
		}
		assertOnly(t, "reclaimer", reclaimRecorder, objectstore.OpStat, objectstore.OpDelete)
	})
}

// assertOnly is the role rule, and its message says exactly what a green means.
func assertOnly(t *testing.T, role string, recorder *gcstest.Recorder, allowed ...objectstore.Operation) {
	t.Helper()

	permitted := map[objectstore.Operation]bool{}
	for _, operation := range allowed {
		permitted[operation] = true
	}

	issued := recorder.Kinds()
	if len(issued) == 0 {
		t.Fatalf("the %s issued no RPC at all; this assertion would pass vacuously", role)
	}
	for _, operation := range issued {
		if permitted[operation] {
			continue
		}
		t.Errorf("the %s issued %s. Its role does not include that call.\n\nWhat this proves: "+
			"the CODE issues only its role's RPCs. What it does not prove: any IAM binding. No "+
			"fake enforces one, so AC 16's honesty is a real-GCS observation and is recorded "+
			"with its date and project in Phase 9.", role, operation)
	}
}

// ---------------------------------------------------------------------------
// The tier-2 gate itself.
// ---------------------------------------------------------------------------

func TestTheTier2GateFailsRatherThanSkipsInCI(t *testing.T) {
	// The gate is a decision about two environment variables and one probe, and
	// the whole point of it is that it cannot be reached by accident, so it is
	// asserted as a decision rather than by running the suite twice.
	//
	// Both halves of it are here. An earlier version of this table modelled
	// only "no endpoint in CI", so the branch that fails on an endpoint nobody
	// answers -- the one an operator actually hits, by mistyping a service name
	// -- could be deleted with the table still green.
	unreachable := func(string) error { return errors.New("dial tcp: connection refused") }
	answers := func(string) error { return nil }
	const configured = "http://fake-gcs.cicd.svc:4443"

	for name, testCase := range map[string]struct {
		endpoint string
		ci       bool
		probe    func(string) error
		want     tier2Action
	}{
		"in CI with no endpoint":                  {endpoint: "", ci: true, probe: answers, want: tier2Fail},
		"in CI with an endpoint that answers":     {endpoint: configured, ci: true, probe: answers, want: tier2Remote},
		"in CI with an unreachable endpoint":      {endpoint: configured, ci: true, probe: unreachable, want: tier2Fail},
		"outside CI with no endpoint":             {endpoint: "", ci: false, probe: answers, want: tier2InProcess},
		"outside CI with an endpoint":             {endpoint: configured, ci: false, probe: answers, want: tier2Remote},
		"outside CI with an unreachable endpoint": {endpoint: configured, ci: false, probe: unreachable, want: tier2InProcess},
	} {
		action, reason := tier2Decision(testCase.endpoint, testCase.ci, testCase.probe)
		if action != testCase.want {
			t.Errorf("%s: the gate decided %s, expected %s", name, action, testCase.want)
		}
		if action != tier2Remote && !strings.Contains(reason, endpointVariable) {
			t.Errorf("%s: the reason does not name the variable an operator has to set: %q",
				name, reason)
		}
	}

	// And the real probe against a port nothing is listening on, so that the
	// table's `unreachable` is a stand-in for something that happens rather
	// than for something only this file believes.
	if err := reachable(closedEndpoint(t)); err == nil {
		t.Error("an endpoint with nothing listening was reported reachable; in CI that would " +
			"turn a missing service into a silent skip")
	}
	if action, _ := tier2Decision(closedEndpoint(t), true, reachable); action != tier2Fail {
		t.Errorf("in CI, an endpoint nothing is listening on decided %s, expected %s",
			action, tier2Fail)
	}
}

// ---------------------------------------------------------------------------
// Substrate helpers that reach past the roles, for seeding and reading back.
// ---------------------------------------------------------------------------

// seed puts an object at a key without going through a role, which is how a
// collision fixture is built: the wrong variant has to be there before the
// publisher runs, and a publisher that put it there would be the thing under
// test.
func seed(t *testing.T, tier substrate, key string, body []byte, metadata map[string]string) objectstore.Attrs {
	t.Helper()

	if tier.memory != nil {
		return tier.memory.Seed(tier.bucket, key, body, metadata)
	}

	writer := tier.client.Object(tier.bucket, key).NewWriter(context.Background())
	writer.SetMetadata(metadata)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("seeding %s: %v", key, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("seeding %s: %v", key, err)
	}

	return writer.Attrs()
}

func read(t *testing.T, tier substrate, key string) ([]byte, bool) {
	t.Helper()

	if tier.memory != nil {
		return tier.memory.Body(tier.bucket, key)
	}

	body, err := tier.client.Object(tier.bucket, key).NewReader(context.Background())
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("reading %s: %v", key, err)
	}
	defer body.Close()

	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading %s: %v", key, err)
	}

	return content, true
}

var _ = hangar.Scope("")

// knownSubstrateGaps is the closed list of profile rows a substrate is allowed
// not to answer for, each with where the row IS answered.
//
// It is a list rather than a comment because the failure this guards against is
// silent: a substrate that stopped enforcing something would otherwise turn a
// passing assertion into an assertion that never ran, and the suite would keep
// reporting conformance. Adding a row here is a decision somebody has to write
// down.
var knownSubstrateGaps = map[string]string{
	"EnforcesDeletePreconditions": "fsouza/fake-gcs-server v1.52.3 deletes whatever is at the " +
		"key regardless of a generation pin or a GenerationMatch precondition. The row is " +
		"covered on tier 1, whose fake does enforce it, and is real-GCS evidence in Phase 9. " +
		"Nothing in the plane may treat a tier-2 green as proof that a conditional delete is " +
		"conditional.",
}

func TestTheSubstrateGapsAreNamed(t *testing.T) {
	observed := map[string]bool{}

	for _, build := range []func(*testing.T) substrate{tier1, tier2} {
		tier := build(t)
		t.Run(tier.name, func(t *testing.T) {
			for name, supported := range map[string]bool{
				"EnforcesDeletePreconditions": tier.can.EnforcesDeletePreconditions,
				"Paginates":                   tier.can.Paginates,
			} {
				if supported {
					continue
				}
				observed[name] = true

				reason, known := knownSubstrateGaps[name]
				if !known {
					t.Errorf("%s does not support %s, and that gap is not in "+
						"knownSubstrateGaps. A profile row this substrate cannot answer is a row "+
						"the suite is not testing, and an unrecorded one is a green that means "+
						"less than it says.", tier.name, name)

					continue
				}
				t.Logf("%s does not support %s. %s", tier.name, name, reason)
			}
		})
	}

	// The other direction, and the one that bit: a listed gap that no substrate
	// actually has. `Paginates` was in this list until the seam stopped
	// carrying page tokens and started resuming from the last key, at which
	// point fake-gcs-server paged perfectly well -- the gap had been in this
	// code, not in the emulator. A stale entry here is a row the suite quietly
	// stops proving while the list says somebody else proves it.
	for name, reason := range knownSubstrateGaps {
		if !observed[name] {
			t.Errorf("knownSubstrateGaps names %s, but every substrate supports it now.\n\n"+
				"The entry says: %s\n\nRemove it. A recorded gap that has closed is a row this "+
				"suite could be asserting and is not.", name, reason)
		}
	}

	// Tier 1 must support both, or the gaps above would be uncovered
	// everywhere and the suite would be testing neither.
	memory := tier1(t)
	if !memory.can.EnforcesDeletePreconditions || !memory.can.Paginates {
		t.Errorf("tier 1 measured %+v. It is the substrate that covers what the emulator cannot; "+
			"a gap here would leave both rows untested on every tier", memory.can)
	}
}
