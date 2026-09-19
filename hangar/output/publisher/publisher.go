// Package publisher is the only output-plane role that creates an object.
//
// It cannot list and it cannot delete, and that is structural rather than
// documented: the store interface it accepts has no List method and its handle
// has no Delete, so there is no call to write by accident. The adapter call log
// says the same thing at run time, and the two together are what the role
// honesty assertion reads. Neither is evidence about IAM -- no fake enforces a
// binding -- and the review note beside the conformance suite says so.
//
// It also never updates metadata. The ownership marker is written once, at
// creation, and the handle offers no way to change it afterwards; that is what
// makes the marker evidence rather than a label (Req 22).
package publisher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
)

// Store is the publisher's whole view of the object store: create and read, at
// one key, under preconditions. There is deliberately no List and no Delete.
type Store interface {
	Object(bucket, key string) Handle
}

// Handle is one object. Note the absent methods.
type Handle interface {
	If(objectstore.Conditions) Handle
	Generation(int64) Handle
	NewWriter(ctx context.Context) objectstore.Writer
	NewReader(ctx context.Context) (io.ReadCloser, error)
	Attrs(ctx context.Context) (objectstore.Attrs, error)
}

// Restrict narrows a full client to the publisher's role.
//
// It is the one place the wide client and the narrow one meet, so "the
// publisher cannot delete" is a fact about a type rather than about
// everybody's discipline.
func Restrict(client objectstore.Client) Store { return restricted{client: client} }

type restricted struct{ client objectstore.Client }

func (store restricted) Object(bucket, key string) Handle {
	return restrictedHandle{handle: store.client.Object(bucket, key)}
}

type restrictedHandle struct{ handle objectstore.Handle }

func (handle restrictedHandle) If(conditions objectstore.Conditions) Handle {
	return restrictedHandle{handle: handle.handle.If(conditions)}
}

func (handle restrictedHandle) Generation(generation int64) Handle {
	return restrictedHandle{handle: handle.handle.Generation(generation)}
}

func (handle restrictedHandle) NewWriter(ctx context.Context) objectstore.Writer {
	return handle.handle.NewWriter(ctx)
}

func (handle restrictedHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return handle.handle.NewReader(ctx)
}

func (handle restrictedHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	return handle.handle.Attrs(ctx)
}

// sizeUnknown says a caller holds no canonical size to compare an object's body
// against. Only the stat path passes it: every publish path canonicalized the
// tree itself and therefore knows exactly how many bytes it wrote.
const sizeUnknown int64 = -1

// Publisher creates and reads objects in one derived namespace.
type Publisher struct {
	namespace output.OutputNamespace
	store     Store
	timeout   time.Duration
}

// New builds a publisher for one namespace.
func New(namespace output.OutputNamespace, store Store, timeout time.Duration) (*Publisher, error) {
	if namespace.IsZero() {
		return nil, fmt.Errorf("%w: the publisher needs a derived output namespace",
			output.ErrIncomplete)
	}
	if store == nil {
		return nil, fmt.Errorf("%w: the publisher needs an object store", output.ErrIncomplete)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: the publisher needs a positive operation timeout",
			output.ErrIncomplete)
	}

	return &Publisher{namespace: namespace, store: store, timeout: timeout}, nil
}

// EnsureObject creates the canonical tree if absent and reports the exact
// generation either way.
//
// The whole method is one rule: an object is deduplicated against only after it
// has been verified whole. A 412 on the create means *something* is at the key,
// and "something at the key with the right name" is precisely the weaker check
// Req 23 forbids -- so the 412 path stats the object, parses its marker, and
// refuses as a typed collision unless the scope, the digest, the marker version
// and the marker's own logical identity all agree.
func (publisher *Publisher) EnsureObject(ctx context.Context, reservation output.ResolvedReservation, canonical io.Reader, size int64) (output.PublishedObject, error) {
	if err := reservation.Validate(); err != nil {
		return output.PublishedObject{}, err
	}
	if canonical == nil {
		return output.PublishedObject{}, fmt.Errorf("%w: no canonical bytes to publish",
			output.ErrIncomplete)
	}
	if size < 0 {
		return output.PublishedObject{}, fmt.Errorf("%w: a negative canonical size",
			output.ErrIncomplete)
	}
	if reservation.Scope != publisher.namespace.Scope() {
		return output.PublishedObject{}, fmt.Errorf("%w: the reservation is resolved to scope %q "+
			"and this publisher's derived namespace is %q. A capture publishes into the namespace "+
			"its epoch derived and no other", output.ErrUnauthorized,
			reservation.Scope, publisher.namespace.Scope())
	}
	if reservation.Marker.ActivationEpoch != publisher.namespace.ActivationEpoch() {
		return output.PublishedObject{}, fmt.Errorf("%w: the reservation's marker names epoch %d "+
			"and this namespace was derived under %d", output.ErrConflict,
			reservation.Marker.ActivationEpoch, publisher.namespace.ActivationEpoch())
	}

	key, err := publisher.namespace.ObjectKey(reservation.Digest)
	if err != nil {
		return output.PublishedObject{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, publisher.timeout)
	defer cancel()

	created, createErr := publisher.create(ctx, key, reservation, canonical)
	switch {
	case createErr == nil:
		object, err := publisher.verifyExact(ctx, key, reservation, created.Generation, size)
		if err != nil {
			return output.PublishedObject{}, err
		}

		return object, nil

	case errors.Is(createErr, objectstore.ErrPreconditionFailed):
		// Something is already at the key. This is the dedup candidate and the
		// collision candidate, and telling them apart is the point.
		return publisher.reconcileExisting(ctx, key, reservation, size)

	case errors.Is(createErr, context.DeadlineExceeded), errors.Is(createErr, context.Canceled):
		return output.PublishedObject{}, fmt.Errorf("%w: creating %s: %v",
			output.ErrTimeout, key, createErr)

	case errors.Is(createErr, objectstore.ErrUnauthorized):
		return output.PublishedObject{}, fmt.Errorf("%w: creating %s: %v",
			output.ErrUnauthorized, key, createErr)

	default:
		// The ambiguous upload. The response was lost, so the bytes may or may
		// not be there, and the only honest way to find out is to look. A
		// retry that assumed failure would either publish twice or report a
		// collision against itself.
		return publisher.reconcileAmbiguous(ctx, key, reservation, size, createErr)
	}
}

func (publisher *Publisher) create(ctx context.Context, key string, reservation output.ResolvedReservation, canonical io.Reader) (objectstore.Attrs, error) {
	conditions := objectstore.Conditions{DoesNotExist: true}
	if err := conditions.Validate(); err != nil {
		return objectstore.Attrs{}, err
	}

	writer := publisher.store.Object(publisher.namespace.Bucket(), key).
		If(conditions).
		NewWriter(ctx)
	writer.SetMetadata(reservation.Marker.Metadata())

	if _, err := io.Copy(writer, canonical); err != nil {
		// Abort rather than Close: closing commits, and committing a truncated
		// tree at a key create-if-absent will then refuse forever is the one
		// ambiguity that is expensive to walk back.
		_ = writer.Abort(err)

		return objectstore.Attrs{}, err
	}
	if err := writer.Close(); err != nil {
		return objectstore.Attrs{}, err
	}

	return writer.Attrs(), nil
}

// reconcileExisting is the 412 path: full marked exact verification, or a typed
// collision.
func (publisher *Publisher) reconcileExisting(ctx context.Context, key string, reservation output.ResolvedReservation, size int64) (output.PublishedObject, error) {
	attrs, err := publisher.store.Object(publisher.namespace.Bucket(), key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			// It was there for the create and gone for the stat. That is an
			// out-of-band deletion, and it is infrastructure, not a collision.
			return output.PublishedObject{}, fmt.Errorf("%w: %s refused a create as already "+
				"present and then reported absent; something outside this plane is writing to "+
				"the output bucket", output.ErrInfrastructure, key)
		}

		return output.PublishedObject{}, translate(err, key)
	}

	object, err := publisher.classify(attrs, reservation, size)
	if err != nil {
		return output.PublishedObject{}, err
	}
	object.Deduplicated = true

	return object, nil
}

// reconcileAmbiguous is the lost-response path.
func (publisher *Publisher) reconcileAmbiguous(ctx context.Context, key string, reservation output.ResolvedReservation, size int64, cause error) (output.PublishedObject, error) {
	attrs, err := publisher.store.Object(publisher.namespace.Bucket(), key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			// Nothing landed. The capture may retry, and it is not past its
			// irreversible publish point on the strength of this attempt.
			return output.PublishedObject{}, fmt.Errorf("%w: creating %s: %v",
				output.ErrInfrastructure, key, cause)
		}

		return output.PublishedObject{}, translate(err, key)
	}

	object, err := publisher.classify(attrs, reservation, size)
	if err != nil {
		return output.PublishedObject{}, err
	}

	// The marker names the reservation that created it. If it is this one, the
	// lost response was a success and this capture created the object; if it
	// is another, this is somebody else's identical bytes and the capture
	// deduplicates against them.
	object.Deduplicated = object.Marker.ReservationID != reservation.ReservationID

	return object, nil
}

// verifyExact reads back what was just written.
//
// It is not paranoia about the store: the writer's Attrs carry the generation
// but a marker read back off the object is the only thing that proves the
// metadata landed with it, and Req 26 says a receipt is signed over an exact
// stat rather than over what the writer believed.
func (publisher *Publisher) verifyExact(ctx context.Context, key string, reservation output.ResolvedReservation, generation, size int64) (output.PublishedObject, error) {
	if generation <= 0 {
		return output.PublishedObject{}, fmt.Errorf("%w: the store reported no generation for "+
			"%s; an object with no generation cannot be registered as an exact ref",
			output.ErrInfrastructure, key)
	}

	attrs, err := publisher.store.Object(publisher.namespace.Bucket(), key).
		Generation(generation).
		Attrs(ctx)
	if err != nil {
		return output.PublishedObject{}, translate(err, key)
	}

	return publisher.classify(attrs, reservation, size)
}

// classify is the full marked exact verification, in one place so that the
// create path, the dedup path and the ambiguous path cannot drift apart.
//
// `size` is the canonical size of the tree THIS capture holds, and it is
// checked against what the store reports because a marker is a claim ABOUT
// bytes and not the bytes. Without it, an object whose body was replaced under
// the same marker metadata deduplicates and is registered, and the receipt then
// attests a generation whose contents are not the tree that was canonicalized.
// Req 23 calls corrupt metadata or body a typed collision "even if a weaker
// content check appears to match", and "the marker says the right digest" is
// precisely the weaker check.
//
// A size is not a digest, and this does not pretend otherwise: what it refuses
// is a body that is not the same tree, and the store has no content hash this
// role can read to do better. It is the check that was available and absent.
func (publisher *Publisher) classify(attrs objectstore.Attrs, reservation output.ResolvedReservation, size int64) (output.PublishedObject, error) {
	marker, err := output.ParseObjectMarker(attrs.Metadata)
	switch {
	case errors.Is(err, output.ErrNotFound):
		// Unmarked: unmanaged. It is never relabelled, adopted or overwritten,
		// and it is a collision for this capture.
		return output.PublishedObject{}, fmt.Errorf("%w: an object at generation %d carries no "+
			"Hangar ownership marker. It is unmanaged: this plane does not relabel, adopt or "+
			"overwrite it, so these bytes cannot be published at that key",
			output.ErrConflict, attrs.Generation)
	case err != nil:
		return output.PublishedObject{}, fmt.Errorf("%w (object generation %d)", err, attrs.Generation)
	}

	if marker.Scope != publisher.namespace.Scope() {
		return output.PublishedObject{}, fmt.Errorf("%w: the object at generation %d is marked "+
			"for scope %q and this namespace is %q", output.ErrConflict,
			attrs.Generation, marker.Scope, publisher.namespace.Scope())
	}
	if marker.Digest != reservation.Digest {
		return output.PublishedObject{}, fmt.Errorf("%w: the object at generation %d is marked "+
			"with digest %q and this capture canonicalized %q. Different immutable content at "+
			"one key is a collision and is never overwritten", output.ErrConflict,
			attrs.Generation, marker.Digest, reservation.Digest)
	}
	if attrs.Size <= 0 {
		return output.PublishedObject{}, fmt.Errorf("%w: the object at generation %d reports %d "+
			"stored bytes", output.ErrCorrupt, attrs.Generation, attrs.Size)
	}
	// SIZE, and only size. It catches a replaced body of a different length and
	// nothing else: a same-size replacement still deduplicates.
	//
	// TODO(phase 9, real GCS): compare the store's CRC32C against the marker as
	// well. `objectstore.Attrs` carries no checksum, so this role cannot read
	// one today -- but GCS reports one and so does the emulator.
	//
	// Round-2 review finding R2-F5 pointed this at "when the inventory role
	// lands". The inventory role landed in Phase 7 and this did not move, for a
	// reason worth writing down: widening `Attrs` with a checksum changes the
	// PUBLISHER's dedup comparison, which is reachable only from a capture, and
	// the evidence that the value is what GCS actually returns is a real-store
	// observation. Adding the field on the strength of an emulator would be
	// adding a comparison whose input this tree has never seen from the real
	// thing.
	if size != sizeUnknown && attrs.Size != size {
		return output.PublishedObject{}, fmt.Errorf("%w: the object at generation %d holds %d "+
			"stored bytes and this capture canonicalized %d for %s. The marker claims this tree "+
			"and the body is not it, which is a collision and is never overwritten or "+
			"deduplicated against", output.ErrConflict, attrs.Generation, attrs.Size, size,
			reservation.Digest)
	}
	if attrs.Metageneration <= 0 {
		return output.PublishedObject{}, fmt.Errorf("%w: the object at generation %d reports "+
			"metageneration %d", output.ErrCorrupt, attrs.Generation, attrs.Metageneration)
	}

	object := output.PublishedObject{
		Attributes: hangar.TreeAttributes{
			Ref:          publisher.namespace.Ref(reservation.Digest, attrs.Generation),
			StoredBytes:  attrs.Size,
			LogicalBytes: attrs.Size,
			CreatedAt:    attrs.Created.UTC(),
		},
		Metageneration: attrs.Metageneration,
		Marker:         marker,
	}
	if err := object.Validate(); err != nil {
		return output.PublishedObject{}, err
	}

	return object, nil
}

// StatExactObject reads metadata for one exact generation.
func (publisher *Publisher) StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	if err := ref.Validate(); err != nil {
		return output.PublishedObject{}, err
	}
	if ref.Scope != publisher.namespace.Scope() {
		return output.PublishedObject{}, fmt.Errorf("%w: ref scope %q is not this namespace's %q",
			output.ErrUnauthorized, ref.Scope, publisher.namespace.Scope())
	}

	key, err := publisher.namespace.ObjectKey(ref.Digest)
	if err != nil {
		return output.PublishedObject{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, publisher.timeout)
	defer cancel()

	attrs, err := publisher.store.Object(publisher.namespace.Bucket(), key).
		Generation(ref.Generation).
		Attrs(ctx)
	if err != nil {
		return output.PublishedObject{}, translate(err, key)
	}

	// sizeUnknown, and deliberately: a stat of an already-registered ref is
	// asked about a generation this call did not canonicalize, so there is no
	// capture-side size to compare against. The size check belongs to the three
	// paths that DO hold one -- create, dedup and the ambiguous retry -- and
	// inventing an expectation here would be asserting a fact this role does
	// not have.
	return publisher.classify(attrs, output.ResolvedReservation{Digest: ref.Digest}, sizeUnknown)
}

// Deferred: the publisher's read under a lease is the daemon end of the
// managed read, and the managed read has no consumer yet: the lease it would
// open under is acquired by the ATC before the Pod is built, and that
// acquisition is the half of the managed-read box this phase did not land

// OpenExactObject reads the bytes, under an active read lease.
func (publisher *Publisher) OpenExactObject(ctx context.Context, ref hangar.TreeRef, lease output.ReadLease) (io.ReadCloser, output.PublishedObject, error) {
	if err := lease.Validate(); err != nil {
		return nil, output.PublishedObject{}, err
	}
	if lease.Ref != ref {
		return nil, output.PublishedObject{}, fmt.Errorf("%w: the lease is for %s/%s/%d and the "+
			"read is of %s/%s/%d", output.ErrUnauthorized,
			lease.Ref.Scope, lease.Ref.Digest, lease.Ref.Generation,
			ref.Scope, ref.Digest, ref.Generation)
	}

	object, err := publisher.StatExactObject(ctx, ref)
	if err != nil {
		return nil, output.PublishedObject{}, err
	}

	key, err := publisher.namespace.ObjectKey(ref.Digest)
	if err != nil {
		return nil, output.PublishedObject{}, err
	}

	body, err := publisher.store.Object(publisher.namespace.Bucket(), key).
		Generation(ref.Generation).
		NewReader(ctx)
	if err != nil {
		return nil, output.PublishedObject{}, translate(err, key)
	}

	return body, object, nil
}

// translate maps a store status onto the plane's typed outcomes.
//
// Absence is ErrNotFound and stays ErrNotFound: Req 27 says none of these
// becomes a cache miss, and the way that rule is broken is by a helper that
// turns "not there" into a nil error and an empty value.
//
// It takes the key and DELIBERATELY DOES NOT PUT IT IN THE MESSAGE. Reqs 3, 7
// and 12 say the bucket, the prefix, the opaque scope and the object key are
// server-derived and never crossed the boundary in either direction: this
// plane refuses a request that names one, and it must not answer with one
// either. It did -- the exact stat of an object that was not there answered
// with the whole key, prefix and derived scope included -- which told a caller
// that had just been refused for naming a key exactly what the key was.
//
// The parameter stays because dropping it would make the next person add it
// back at the call site by hand. `_ = key` is the whole point: it is the fact
// that this function knows the key and says nothing about it.
//
// What is lost is real, and it is the cost Req 7 chose. A node operator
// reading a "not found" no longer sees which object. What they do have is the
// typed outcome and the reservation the caller named, and the daemon's own
// startup banner names the bucket and prefix once, at boot, to its own stdout.
//
// The vendor error text goes for the same reason: a GCS error names the bucket
// and the object in prose, so forwarding %v forwards the key by another route.
// The classification above is what a caller can act on; the text was never it.
func translate(err error, key string) error {
	_ = key

	switch {
	case err == nil:
		return nil
	case errors.Is(err, objectstore.ErrNotFound):
		return fmt.Errorf("%w: no object at the server-derived key for this reservation",
			output.ErrNotFound)
	case errors.Is(err, objectstore.ErrUnauthorized):
		return fmt.Errorf("%w: this plane's credential was refused for the server-derived key",
			output.ErrUnauthorized)
	case errors.Is(err, objectstore.ErrPreconditionFailed):
		return fmt.Errorf("%w: the object at the server-derived key is not at the generation "+
			"this operation required", output.ErrGenerationConflict)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: the store did not answer within this operation's deadline",
			output.ErrTimeout)
	default:
		return fmt.Errorf("%w: the object store could not be reached for this operation",
			output.ErrInfrastructure)
	}
}
