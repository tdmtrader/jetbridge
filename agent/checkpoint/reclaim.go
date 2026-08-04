package checkpoint

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/hangar"
)

// ReclaimStore is the durable checkpoint authority a reclamation pass needs.
// It is deliberately narrower than the full Store: a pass may retire metadata
// and release durable objects, and it may do nothing else.
type ReclaimStore interface {
	ClaimCheckpointExpirations(context.Context, int) ([]ExpirationClaim, error)
	FinalizeCheckpointExpiration(context.Context, ExpirationClaim) error
	ClaimUnreferencedObjects(context.Context, int) ([]ObjectDeleteClaim, error)
	ReconcileUnreferencedUploadingObject(context.Context, ObjectDeleteClaim, *hangar.ObjectRef) (bool, error)
	FinalizeObjectDeletion(context.Context, ObjectDeleteClaim) error
	CleanupTerminalMetadata(context.Context, int) (int, error)
}

// DurableObjectStore is the reclamation pass's view of Hangar. Both operations
// are identified by the exact object the database judged: Inspect answers
// whether an abandoned upload ever landed, and Delete is pinned to a single
// generation so a re-upload fails the delete rather than losing new content.
type DurableObjectStore interface {
	InspectObject(context.Context, hangar.Kind, hangar.Digest) (hangar.ObjectRef, error)
	DeleteObject(context.Context, hangar.ObjectRef) error
}

// ReclaimReport counts one pass. Every field is reported, including zero, so a
// pass that reclaims nothing is distinguishable from a pass that never ran.
type ReclaimReport struct {
	CheckpointsExpired int
	ObjectsScanned     int
	ObjectsDeleted     int
	UploadsAdopted     int
	UploadsForgotten   int
	UploadsDeferred    int
	MetadataRemoved    int
	Failed             int
}

// Reclaimer releases the durable checkpoint objects and metadata of runs that
// are over. It owns no schedule of its own; a component drives Collect.
type Reclaimer struct {
	store   ReclaimStore
	objects DurableObjectStore
	batch   int
}

func NewReclaimer(store ReclaimStore, objects DurableObjectStore) (*Reclaimer, error) {
	if store == nil {
		return nil, errors.New("checkpoint: reclamation store is required")
	}
	if objects == nil {
		return nil, errors.New("checkpoint: reclamation durable object store is required")
	}
	return &Reclaimer{store: store, objects: objects, batch: defaultReclaimBatchSize}, nil
}

const defaultReclaimBatchSize = 100

// Collect runs one bounded pass over the three stages that release a finished
// run's durable footprint, in the only order that makes progress: expiring a
// checkpoint is what clears the reference that pins its object, and retiring a
// head's metadata is only permitted once no checkpoint of that head still
// names an object.
//
// A failure in one stage never suppresses the next. The stages read disjoint
// work from the database under their own leases, so a store that cannot expire
// anything this minute can still be releasing last hour's expirations, and the
// pass reports every failure it accumulated rather than the first.
func (reclaimer *Reclaimer) Collect(ctx context.Context) (ReclaimReport, error) {
	if reclaimer == nil {
		return ReclaimReport{}, errors.New("checkpoint: reclaimer is required")
	}
	report := ReclaimReport{}
	failures := []error{}
	for _, stage := range []func(context.Context, *ReclaimReport) []error{
		reclaimer.expireCheckpoints,
		reclaimer.releaseObjects,
		reclaimer.retireMetadata,
	} {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(append(failures, err)...)
		}
		failures = append(failures, stage(ctx, &report)...)
	}
	return report, errors.Join(failures...)
}

func (reclaimer *Reclaimer) expireCheckpoints(ctx context.Context, report *ReclaimReport) []error {
	claims, err := reclaimer.store.ClaimCheckpointExpirations(ctx, reclaimer.batch)
	if err != nil {
		return []error{fmt.Errorf("checkpoint: claim expirations: %w", err)}
	}
	failures := []error{}
	for _, claim := range claims {
		if err := ctx.Err(); err != nil {
			return append(failures, err)
		}
		if err := reclaimer.store.FinalizeCheckpointExpiration(ctx, claim); err != nil {
			report.Failed++
			failures = append(failures, fmt.Errorf("checkpoint: finalize expiration %d: %w", claim.CheckpointID, err))
			continue
		}
		report.CheckpointsExpired++
	}
	return failures
}

func (reclaimer *Reclaimer) releaseObjects(ctx context.Context, report *ReclaimReport) []error {
	claims, err := reclaimer.store.ClaimUnreferencedObjects(ctx, reclaimer.batch)
	if err != nil {
		return []error{fmt.Errorf("checkpoint: claim unreferenced objects: %w", err)}
	}
	failures := []error{}
	for _, claim := range claims {
		if err := ctx.Err(); err != nil {
			return append(failures, err)
		}
		report.ObjectsScanned++
		release := reclaimer.deleteObject
		if claim.NeedsUploadInspection {
			release = reclaimer.reconcileUpload
		}
		if err := release(ctx, claim, report); err != nil {
			report.Failed++
			failures = append(failures, err)
		}
	}
	return failures
}

func (reclaimer *Reclaimer) retireMetadata(ctx context.Context, report *ReclaimReport) []error {
	removed, err := reclaimer.store.CleanupTerminalMetadata(ctx, reclaimer.batch)
	if err != nil {
		return []error{fmt.Errorf("checkpoint: cleanup terminal metadata: %w", err)}
	}
	report.MetadataRemoved += removed
	return nil
}

// deleteObject releases the exact object the database judged unreferenced. The
// delete is pinned to the claimed generation, and the row survives any failure:
// the row is the only thing that still names the object, so dropping it before
// the bytes are gone is what turns a failed delete into permanent garbage.
func (reclaimer *Reclaimer) deleteObject(ctx context.Context, claim ObjectDeleteClaim, report *ReclaimReport) error {
	if err := validateReclaimedObject(claim.Object); err != nil {
		return fmt.Errorf("checkpoint: object delete claim %d: %w", claim.ObjectID, err)
	}
	if err := reclaimer.objects.DeleteObject(ctx, claim.Object); err != nil {
		return fmt.Errorf("checkpoint: delete durable object %d: %w", claim.ObjectID, err)
	}
	if err := reclaimer.store.FinalizeObjectDeletion(ctx, claim); err != nil {
		return fmt.Errorf("checkpoint: finalize object deletion %d: %w", claim.ObjectID, err)
	}
	report.ObjectsDeleted++
	return nil
}

// reconcileUpload settles an abandoned upload whose lease expired. Storage is
// the only authority on whether the bytes landed, so the pass asks and reports
// the answer; the database decides what that means. An object that did land is
// adopted as available and becomes an ordinary deletion candidate on a later
// pass, which keeps the "delete only what a lease-held claim named" rule
// intact instead of deleting something this pass never judged.
func (reclaimer *Reclaimer) reconcileUpload(ctx context.Context, claim ObjectDeleteClaim, report *ReclaimReport) error {
	ticket := claim.UploadTicket
	if ticket.Kind != hangar.KindCheckpoint {
		return fmt.Errorf("checkpoint: upload claim %d is not a checkpoint object", claim.ObjectID)
	}
	if err := ticket.Digest.Validate(); err != nil {
		return fmt.Errorf("checkpoint: upload claim %d: %w", claim.ObjectID, err)
	}
	key, err := hangar.Key(ticket.Kind, ticket.Digest)
	if err != nil {
		return fmt.Errorf("checkpoint: upload claim %d: %w", claim.ObjectID, err)
	}
	if key != ticket.Key {
		return fmt.Errorf("checkpoint: upload claim %d has a noncanonical key", claim.ObjectID)
	}

	var observed *hangar.ObjectRef
	landed, err := reclaimer.objects.InspectObject(ctx, ticket.Kind, ticket.Digest)
	switch {
	case err == nil:
		if err := validateReclaimedObject(landed); err != nil {
			return fmt.Errorf("checkpoint: inspected object %d: %w", claim.ObjectID, err)
		}
		if landed.Digest != ticket.Digest || landed.Key != ticket.Key {
			return fmt.Errorf("checkpoint: inspected object %d does not match the claimed upload", claim.ObjectID)
		}
		observed = &landed
	case errors.Is(err, hangar.ErrNotFound):
	default:
		return fmt.Errorf("checkpoint: inspect abandoned upload %d: %w", claim.ObjectID, err)
	}

	settled, err := reclaimer.store.ReconcileUnreferencedUploadingObject(ctx, claim, observed)
	if err != nil {
		return fmt.Errorf("checkpoint: reconcile abandoned upload %d: %w", claim.ObjectID, err)
	}
	switch {
	case observed != nil:
		report.UploadsAdopted++
	case settled:
		report.UploadsForgotten++
	default:
		// The store wants a second observation before it forgets an upload it
		// cannot see. Nothing was reclaimed and nothing failed.
		report.UploadsDeferred++
	}
	return nil
}

// validateReclaimedObject is the pass's own proof that a reference names a
// canonical checkpoint object. ObjectRef.Validate already binds key to digest;
// this adds the kind, because nothing else on the delete path would stop a
// checkpoint pass from destroying a snapshot.
func validateReclaimedObject(ref hangar.ObjectRef) error {
	if ref.Kind != hangar.KindCheckpoint {
		return fmt.Errorf("hangar: object kind %q is not a checkpoint", ref.Kind)
	}
	return ref.Validate()
}
