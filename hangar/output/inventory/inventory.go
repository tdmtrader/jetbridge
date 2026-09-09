// Package inventory is the output-plane role that lists and stats. It cannot
// create and it cannot delete.
//
// The store interface it accepts has no writer and no delete, so those calls do
// not exist to make. What it does have is List, and List is the one permission
// GCS cannot narrow: storage.objects.list is bucket-wide and cannot be scoped
// to a prefix. The boundary is therefore the dedicated bucket plus a
// server-derived prefix this package applies itself, and the caller is given no
// way to ask for a different one -- which is why ListPage takes a cursor and a
// budget, and no prefix.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
)

// Store is the inventory's view: list a page, stat an exact generation.
type Store interface {
	List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error)
	Object(bucket, key string) Handle
}

// Handle offers a stat and nothing else.
type Handle interface {
	Generation(int64) Handle
	Attrs(ctx context.Context) (objectstore.Attrs, error)
}

// Restrict narrows a full client to the inventory's role.
func Restrict(client objectstore.Client) Store { return restricted{client: client} }

type restricted struct{ client objectstore.Client }

func (store restricted) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	return store.client.List(ctx, bucket, request)
}

func (store restricted) Object(bucket, key string) Handle {
	return restrictedHandle{handle: store.client.Object(bucket, key)}
}

type restrictedHandle struct{ handle objectstore.Handle }

func (handle restrictedHandle) Generation(generation int64) Handle {
	return restrictedHandle{handle: handle.handle.Generation(generation)}
}

func (handle restrictedHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	return handle.handle.Attrs(ctx)
}

// Inventory sweeps one namespace.
type Inventory struct {
	namespace output.OutputNamespace
	store     Store
}

// New builds an inventory for one namespace.
func New(namespace output.OutputNamespace, store Store) (*Inventory, error) {
	if namespace.IsZero() {
		return nil, fmt.Errorf("%w: the inventory needs a derived output namespace",
			output.ErrIncomplete)
	}
	if store == nil {
		return nil, fmt.Errorf("%w: the inventory needs an object store", output.ErrIncomplete)
	}

	return &Inventory{namespace: namespace, store: store}, nil
}

var _ output.Inventory = (*Inventory)(nil)

// ListPage reads one bounded page under the server-derived prefix.
//
// Every field of the budget is a stop condition, and the page is Complete only
// when the sweep stopped because the listing did. A page that stopped on a
// budget is replayed rather than skipped, which is why the cursor it returns is
// the position after the objects it actually classified.
func (inventory *Inventory) ListPage(ctx context.Context, cursor output.InventoryCursor, budget output.PageBudget) (output.InventoryPage, error) {
	if err := cursor.Validate(); err != nil {
		return output.InventoryPage{}, err
	}
	if err := budget.Validate(); err != nil {
		return output.InventoryPage{}, err
	}
	if cursor.ActivationEpoch != inventory.namespace.ActivationEpoch() {
		return output.InventoryPage{}, fmt.Errorf("%w: the cursor is for epoch %d and this "+
			"namespace was derived under %d", output.ErrConflict,
			cursor.ActivationEpoch, inventory.namespace.ActivationEpoch())
	}

	deadline := time.Now().Add(budget.MaxDuration)

	page, err := inventory.store.List(ctx, inventory.namespace.Bucket(), objectstore.ListRequest{
		Prefix:   inventory.namespace.ListPrefix(),
		PageSize: budget.MaxObjects,
		After:    cursor.AfterKey,
	})
	if err != nil {
		return output.InventoryPage{}, translate(err)
	}

	result := output.InventoryPage{Next: cursor, Complete: true}
	var metadataBytes int64

	for _, attrs := range page.Objects {
		if time.Now().After(deadline) {
			result.Complete = false

			break
		}
		metadataBytes += metadataSize(attrs)
		if metadataBytes > budget.MaxMetadataBytes {
			result.Complete = false

			break
		}

		result.Objects = append(result.Objects, inventory.classify(attrs))

		// The cursor advances to the last object this page actually
		// CLASSIFIED, not to the end of what the store returned. A page cut
		// short by a budget is replayed from where classification stopped, so
		// nothing is skipped -- which is the whole reason the advance is inside
		// the loop.
		result.Next.AfterKey = attrs.Key
		result.Next.AfterGeneration = attrs.Generation
	}

	// A page that classified nothing leaves the cursor exactly where it was.
	if len(result.Objects) == 0 {
		result.Next = cursor
	}

	// A cycle ends only when the listing said it had nothing more AND every
	// object it returned was classified. Either half alone would restart a
	// sweep over a bucket it had not finished reading.
	if page.Done && result.Complete && len(result.Objects) == len(page.Objects) {
		result.Next.Cycle = cursor.Cycle + 1
		result.Next.AfterKey = ""
		result.Next.AfterGeneration = 0
	}
	result.Next.UpdatedAt = output.NewTimestamp(time.Now().UTC())

	return result, nil
}

// classify says whether an object is this plane's, and never guesses.
//
// An object with no marker is unmanaged, which is a fact about the object and
// not an error: the sweep records it and moves on, and nothing ever relabels,
// adopts or deletes it. A marker that is present and wrong is different -- it
// is debt, because some other cohort said something here and this one cannot
// safely act.
func (inventory *Inventory) classify(attrs objectstore.Attrs) output.InventoryObject {
	object := output.InventoryObject{
		ObjectKey:      attrs.Key,
		Generation:     attrs.Generation,
		Metageneration: attrs.Metageneration,
		Size:           attrs.Size,
		CreatedAt:      output.NewTimestamp(attrs.Created.UTC()),
	}

	marker, err := output.ParseObjectMarker(attrs.Metadata)
	if err != nil {
		return object
	}
	if marker.Scope != inventory.namespace.Scope() {
		// Another epoch's scope in the same bucket under the same deployment
		// prefix. It is this deployment's object and it is managed; it is just
		// not this epoch's, which is what the marker's own epoch says.
		object.Marker = marker
		object.Managed = true

		return object
	}

	object.Marker = marker
	object.Managed = true

	return object
}

// StatExactObject reads metadata for one exact generation.
func (inventory *Inventory) StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	if err := ref.Validate(); err != nil {
		return output.PublishedObject{}, err
	}

	key, err := hangar.TreeKey(inventory.namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil {
		return output.PublishedObject{}, err
	}

	attrs, err := inventory.store.Object(inventory.namespace.Bucket(), key).
		Generation(ref.Generation).
		Attrs(ctx)
	if err != nil {
		return output.PublishedObject{}, translate(err)
	}

	marker, err := output.ParseObjectMarker(attrs.Metadata)
	if err != nil {
		return output.PublishedObject{}, err
	}

	object := output.PublishedObject{
		Attributes: hangar.TreeAttributes{
			Ref:          ref,
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

func metadataSize(attrs objectstore.Attrs) int64 {
	size := int64(len(attrs.Key))
	for key, value := range attrs.Metadata {
		size += int64(len(key) + len(value))
	}

	return size
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, objectstore.ErrNotFound):
		return fmt.Errorf("%w: %v", output.ErrNotFound, err)
	case errors.Is(err, objectstore.ErrUnauthorized):
		return fmt.Errorf("%w: %v", output.ErrUnauthorized, err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %v", output.ErrTimeout, err)
	default:
		return fmt.Errorf("%w: %v", output.ErrInfrastructure, err)
	}
}

// Classification is what a sweep concluded about one object it found.
//
// The vocabulary is closed and the members are deliberately not "found" and
// "not found". An object in the output bucket is one of exactly four things,
// and the difference between them is what may be done to it: an unmanaged
// object is never touched, a registered one is protected by its lifecycle row,
// an orphan may eventually be adopted, and a candidate inside its publication
// grace is a capture that may still legitimately be retrying.
//
// None of them is a cache miss. Req 27 says so about every typed outcome in
// this plane, and inventory is where the temptation is strongest: an object
// with no lifecycle row looks exactly like a miss if the only question asked is
// "is it registered".
type Classification string

const (
	// ClassificationUnmanaged is an object with no Hangar marker. It is never
	// relabelled, adopted or deleted.
	ClassificationUnmanaged Classification = "unmanaged"

	// ClassificationRegistered is a marked object with a committed lifecycle
	// row naming its exact generation.
	ClassificationRegistered Classification = "registered"

	// ClassificationWithinGrace is a marked object with no lifecycle row,
	// found before its publication grace elapsed. Its capture may still be
	// retrying, and adopting it would race the capture that made it.
	ClassificationWithinGrace Classification = "within_publication_grace"

	// ClassificationOrphan is a marked object with no lifecycle row whose
	// publication grace has elapsed. It is adoptable; it is not a miss, and it
	// is never a binding.
	ClassificationOrphan Classification = "orphan"
)

// Classifications is the closed set.
func Classifications() []Classification {
	return []Classification{
		ClassificationUnmanaged,
		ClassificationRegistered,
		ClassificationWithinGrace,
		ClassificationOrphan,
	}
}

// Classify decides what one found object is.
//
// `registered` is supplied by the caller rather than read here, because the
// lifecycle row is the control plane's and inventory holds no database handle.
// `grace` and `now` are parameters for the same reason every deadline in this
// plane is: the sweep must not measure an object's age against a node's wall
// clock, and a grace window that could be shortened below the maximum capture
// deadline would let an object become adoptable while its own capture was still
// legitimately retrying -- which is why ValidateGrace refuses one.
func Classify(object output.InventoryObject, registered bool, grace time.Duration, now time.Time) Classification {
	if !object.Managed {
		return ClassificationUnmanaged
	}
	if registered {
		return ClassificationRegistered
	}
	if now.Sub(object.CreatedAt.UTC()) < grace {
		return ClassificationWithinGrace
	}

	return ClassificationOrphan
}

// ValidateGrace refuses a publication grace that could race a capture.
func ValidateGrace(grace, maxCaptureDeadline time.Duration) error {
	floor := maxCaptureDeadline + output.PublicationGraceMargin
	if grace < floor {
		return fmt.Errorf("%w: a publication grace of %s is below the maximum capture deadline "+
			"plus %s (%s). Below that floor an object becomes adoptable while the capture that "+
			"created it may still be legitimately retrying, and adoption would race publication",
			output.ErrIncomplete, grace, output.PublicationGraceMargin, floor)
	}
	if grace > output.MaxPublicationGrace {
		return fmt.Errorf("%w: a publication grace of %s exceeds the %s bound",
			output.ErrLimitExceeded, grace, output.MaxPublicationGrace)
	}

	return nil
}
