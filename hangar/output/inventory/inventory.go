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
	clock     output.Clock
}

// New builds an inventory for one namespace.
//
// The clock is a parameter for the reason every clock in this plane is one: the
// per-pass duration budget is the one bound a test cannot otherwise move, and a
// bound nothing can reach is a bound nothing checks.
func New(namespace output.OutputNamespace, store Store, clock output.Clock) (*Inventory, error) {
	if namespace.IsZero() {
		return nil, fmt.Errorf("%w: the inventory needs a derived output namespace",
			output.ErrIncomplete)
	}
	if store == nil {
		return nil, fmt.Errorf("%w: the inventory needs an object store", output.ErrIncomplete)
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: the inventory needs a clock", output.ErrIncomplete)
	}

	return &Inventory{namespace: namespace, store: store, clock: clock}, nil
}

var _ output.Inventory = (*Inventory)(nil)

// ListPage reads one bounded page under the server-derived prefix.
//
// Every field of the budget is a stop condition, and the page is Complete only
// when the sweep stopped because the listing did. A page that stopped on a
// budget is replayed rather than skipped, which is why the cursor it returns is
// the position after the objects it actually DISPOSITIONED -- classified as an
// object, or recorded as debt. Those two are the same thing to a cursor, which
// is the whole of Req 44: an object that cannot be classified must still be got
// past, or it starves every key behind it.
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

	deadline := inventory.clock.Now().Add(budget.MaxDuration)

	page, err := inventory.store.List(ctx, inventory.namespace.Bucket(), objectstore.ListRequest{
		Prefix:   inventory.namespace.ListPrefix(),
		PageSize: budget.MaxObjects,
		After:    cursor.AfterKey,
		// The generation half of the after-key. Without it a listing resumed
		// from a key cannot tell an object it finished from a NEW object
		// recreated at the same name, and the new one would be invisible for a
		// whole cycle.
		AfterGeneration: cursor.AfterGeneration,
	})
	if err != nil {
		return output.InventoryPage{}, translate(err)
	}

	result := output.InventoryPage{Next: cursor, Complete: true}
	var metadataBytes int64
	dispositioned := 0

	for _, attrs := range page.Objects {
		if inventory.clock.Now().After(deadline) {
			result.Complete = false

			break
		}

		size := metadataSize(attrs)
		if size > budget.MaxMetadataBytes {
			// An object whose metadata alone is larger than a WHOLE pass can
			// never fit in any pass. Stopping before it and leaving the cursor
			// behind it replays it forever, and every key after it with it, so
			// this is a disposition rather than a stop condition: debt now, and
			// the cursor goes past.
			result.Debt = append(result.Debt, inventory.debtFor(attrs, output.DebtPoisonMetadata,
				fmt.Sprintf("object metadata is %d bytes, past the %d-byte budget for a whole "+
					"pass; it is recorded and passed rather than replayed forever",
					size, budget.MaxMetadataBytes)))
			result.Next.AfterKey = attrs.Key
			result.Next.AfterGeneration = attrs.Generation
			dispositioned++
			result.Complete = false

			break
		}
		if metadataBytes+size > budget.MaxMetadataBytes {
			// It would fit in a pass; it does not fit in the REST of this one.
			result.Complete = false

			break
		}
		metadataBytes += size

		object, debt, classified := inventory.classify(attrs)
		if classified {
			result.Objects = append(result.Objects, object)
		} else {
			result.Debt = append(result.Debt, debt)
		}
		dispositioned++

		// The cursor advances to the last object this page actually
		// DISPOSITIONED, not to the end of what the store returned. A page cut
		// short by a budget is replayed from where that stopped, so nothing is
		// skipped -- which is the whole reason the advance is inside the loop.
		result.Next.AfterKey = attrs.Key
		result.Next.AfterGeneration = attrs.Generation
	}

	// A page that dispositioned nothing leaves the cursor exactly where it was.
	if dispositioned == 0 {
		result.Next = cursor
	}

	// A cycle ends only when the listing said it had nothing more AND every
	// object it returned was dispositioned. Either half alone would restart a
	// sweep over a bucket it had not finished reading.
	if page.Done && result.Complete && dispositioned == len(page.Objects) {
		result.Next.Cycle = cursor.Cycle + 1
		result.Next.AfterKey = ""
		result.Next.AfterGeneration = 0
	}
	result.Next.UpdatedAt = output.NewTimestamp(inventory.clock.Now().UTC())

	return result, nil
}

// RecoverCursor is Req 44's corrupt-cursor branch.
//
// A cursor that does not validate is a fact about the cursor and never
// authoritative absence: the corruption is recorded as debt and the sweep
// restarts at the validated output prefix, so poison, stale ownership and
// cursor corruption cannot strand later objects.
//
// It refuses a cursor that DOES validate, and the refusal is not pedantry: a
// recovery that accepted a healthy cursor would restart every sweep at the
// prefix, which is the same head of the bucket read forever and no object
// beyond one page ever reached.
func (inventory *Inventory) RecoverCursor(cursor output.InventoryCursor) (output.InventoryCursor, output.InventoryDebt, error) {
	corruption := cursor.Validate()
	if corruption == nil {
		return output.InventoryCursor{}, output.InventoryDebt{}, fmt.Errorf(
			"%w: the cursor at %q#%d validates; there is nothing to recover, and restarting a "+
				"healthy sweep at the prefix would mean no object past the first page is ever "+
				"reached", output.ErrConflict, cursor.AfterKey, cursor.AfterGeneration)
	}

	restarted := cursor
	restarted.ProtocolVersion = output.ProtocolVersion
	restarted.AfterKey = ""
	restarted.AfterGeneration = 0
	restarted.UpdatedAt = output.NewTimestamp(inventory.clock.Now().UTC())
	if err := restarted.Validate(); err != nil {
		return output.InventoryCursor{}, output.InventoryDebt{}, fmt.Errorf(
			"%w: the cursor is corrupt in a way a restart at the output prefix does not repair: "+
				"%v", output.ErrCorrupt, err)
	}

	// The debt names the position the cursor CLAIMED, because that is the only
	// object identity a corrupt cursor has, and the output prefix when it
	// claimed none. Inventing a key would be recording a fact about an object
	// nothing observed.
	key := cursor.AfterKey
	if key == "" {
		key = inventory.namespace.ListPrefix()
	}

	return restarted, output.InventoryDebt{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: cursor.ActivationEpoch,
		ObjectKey:       key,
		Generation:      cursor.AfterGeneration,
		Reason:          output.DebtCorruptCursor,
		Attempts:        1,
		ObservedAt:      output.NewTimestamp(inventory.clock.Now().UTC()),
		Detail:          truncateDetail(corruption.Error()),
	}, nil
}

// classify says whether an object is this plane's, and never guesses.
//
// Three answers, not two, and the difference between them is what may be done
// to the object. An object with no marker is UNMANAGED: a fact about the
// object, not an error, recorded for diagnosis and never relabelled, adopted or
// deleted -- and deliberately not debt, because debt is work this deployment
// owes and somebody else's object is not work. A marker of another cohort's
// version is a deliberate statement by that cohort, so it is debt and this one
// does not act on it. A marker that will not decode at all is poison, and it is
// debt for the same reason: an object whose own evidence is unreadable is one
// nothing may conclude anything about.
//
// The boolean says which list the caller puts the result in, and an object can
// never be in both: a sweep that returned a poisoned object beside its debt row
// would let a caller adopt exactly the thing it had just recorded it could not
// read.
func (inventory *Inventory) classify(attrs objectstore.Attrs) (output.InventoryObject, output.InventoryDebt, bool) {
	object := output.InventoryObject{
		ObjectKey:      attrs.Key,
		Generation:     attrs.Generation,
		Metageneration: attrs.Metageneration,
		Size:           attrs.Size,
		CreatedAt:      output.NewTimestamp(attrs.Created.UTC()),
	}

	marker, err := output.ParseObjectMarker(attrs.Metadata)
	switch {
	case err == nil:
		// A marker carrying another epoch's scope is still this deployment's
		// object and still managed; it is just not this epoch's, which is what
		// the marker's own epoch says.
		object.Marker = marker
		object.Managed = true

		return object, output.InventoryDebt{}, true

	case errors.Is(err, output.ErrNotFound):
		return object, output.InventoryDebt{}, true

	case errors.Is(err, output.ErrConflict):
		return output.InventoryObject{},
			inventory.debtFor(attrs, output.DebtMarkerMismatch, err.Error()), false

	default:
		return output.InventoryObject{},
			inventory.debtFor(attrs, output.DebtPoisonMetadata, err.Error()), false
	}
}

// debtFor is the one place a debt row is built, so every one of them carries
// the same epoch, the same bounded detail, and a server-observed key.
func (inventory *Inventory) debtFor(attrs objectstore.Attrs, reason output.DebtReason, detail string) output.InventoryDebt {
	return output.InventoryDebt{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: inventory.namespace.ActivationEpoch(),
		ObjectKey:       attrs.Key,
		Generation:      attrs.Generation,
		Reason:          reason,
		Attempts:        1,
		ObservedAt:      output.NewTimestamp(inventory.clock.Now().UTC()),
		Detail:          truncateDetail(detail),
	}
}

// truncateDetail bounds the free text a poisoned object can put into a row.
func truncateDetail(detail string) string {
	if len(detail) <= output.MaxDebtDetailBytes {
		return detail
	}

	return detail[:output.MaxDebtDetailBytes]
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
