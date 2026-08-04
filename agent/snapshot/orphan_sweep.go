package snapshot

import (
	"context"
	"fmt"
	"time"
)

// MinOrphanSweepAge is the floor an operator may configure. The sweep deletes
// durable content, so the age threshold is the last defense that does not
// depend on metadata being correct, and it is not allowed to be tuned down to
// a value that could race a legitimate upload.
const MinOrphanSweepAge = time.Hour

// DefaultOrphanSweepAge is deliberately far larger than any legitimate window
// between an object appearing in durable storage and the database learning
// about it.
//
// The sealer stages a database row *before* it uploads bytes, so an in-flight
// upload is already protected by the staged row that DigestState observes; the
// age threshold exists for the cases that reasoning does not cover — clock skew
// between the object store and the web node, a write path that has not yet
// recorded any row, and an ATC that was down while a stage lease expired.
// Twenty-four hours is roughly twenty-four times the default one-hour stage
// lease and orders of magnitude beyond any plausible skew, while still bounding
// unreclaimed garbage to a single day of failed uploads.
const DefaultOrphanSweepAge = 24 * time.Hour

// OrphanSweepMode gates a pass that deletes durable data. The default is the
// zero value on purpose: an operator has to opt in twice — once to run the
// sweep at all, and again to let it delete rather than only report.
type OrphanSweepMode string

const (
	// OrphanSweepOff performs no listing and no deletion.
	OrphanSweepOff OrphanSweepMode = "off"
	// OrphanSweepReport identifies and logs reclaimable objects without
	// deleting any of them, so an operator can inspect a real inventory before
	// authorizing deletion.
	OrphanSweepReport OrphanSweepMode = "report"
	// OrphanSweepReclaim deletes the objects it reports.
	OrphanSweepReclaim OrphanSweepMode = "reclaim"
)

func (mode OrphanSweepMode) Validate() error {
	switch mode {
	case OrphanSweepOff, OrphanSweepReport, OrphanSweepReclaim:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid orphan sweep mode %q", mode)
	}
}

// DurableObject is one object the durable store actually holds. Generation is
// carried end to end so a deletion can be pinned to the exact bytes that were
// judged unreferenced: if the digest is re-uploaded between the listing and the
// delete, the pinned delete fails instead of destroying the new content.
type DurableObject struct {
	Digest     Digest
	Key        string
	Generation int64
	Bytes      int64
	CreatedAt  time.Time
}

func (object DurableObject) Validate() error {
	if err := object.Digest.Validate(); err != nil {
		return err
	}
	if object.Key == "" {
		return fmt.Errorf("snapshot: durable object key is required")
	}
	if object.Generation <= 0 {
		return fmt.Errorf("snapshot: durable object generation must be positive")
	}
	if object.Bytes < 0 {
		return fmt.Errorf("snapshot: durable object byte size must not be negative")
	}
	if object.CreatedAt.IsZero() {
		// Without a creation time the age threshold cannot be evaluated, and an
		// object that cannot be aged must never be deleted.
		return fmt.Errorf("snapshot: durable object creation time is required")
	}
	return nil
}

// DurableInventory is the storage-side authority the sweep needs. Metadata
// cannot answer "what does storage hold" for an object it never recorded, which
// is precisely the class of object this exists to find.
//
//counterfeiter:generate . DurableInventory
type DurableInventory interface {
	ListDurableObjects(context.Context, func(DurableObject) error) error
	// DeleteDurableObject must honor the supplied generation.
	DeleteDurableObject(context.Context, DurableObject) error
}

// SweepOrphans reclaims durable objects that metadata does not reference.
//
// Why this pass has to exist at all: every other collection path discovers work
// by querying the database, so an object the database never learned about — or
// has since forgotten — is unreachable by construction and would otherwise
// survive forever. That is the accumulation that filled the store.
//
// The safety argument for deleting durable content is four independent gates,
// each of which alone is sufficient to prevent the worst case:
//
//  1. Age. An object younger than minAge is never considered, so an upload that
//     is still running cannot be reclaimed even if no row exists yet.
//  2. The digest lease. The state check happens under the same lease the sealer
//     holds across stage, upload, and commit, so the sweep cannot interleave
//     with a seal of the same digest.
//  3. Any row wins. A snapshot row in any content state, any staged upload, or
//     any recorded location retains the object. The sweep deletes only when
//     metadata knows nothing whatsoever about the digest.
//  4. Generation pinning. The delete is pinned to the exact object that was
//     judged, so a concurrent re-upload cannot be destroyed by a decision made
//     about older bytes.
func (lifecycle *Lifecycle) SweepOrphans(
	ctx context.Context,
	inventory DurableInventory,
	mode OrphanSweepMode,
	minAge time.Duration,
) (LifecycleReport, error) {
	if lifecycle == nil {
		return LifecycleReport{}, fmt.Errorf("snapshot: lifecycle is required")
	}
	if err := mode.Validate(); err != nil {
		return LifecycleReport{}, err
	}
	if mode == OrphanSweepOff {
		return LifecycleReport{}, nil
	}
	if interfaceIsNil(inventory) {
		return LifecycleReport{}, fmt.Errorf("snapshot: durable inventory is required")
	}
	if minAge < MinOrphanSweepAge {
		return LifecycleReport{}, fmt.Errorf(
			"snapshot: orphan sweep age must be at least %s, got %s", MinOrphanSweepAge, minAge,
		)
	}

	lifecycle.sweepMu.Lock()
	defer lifecycle.sweepMu.Unlock()

	report := LifecycleReport{}
	var failures []error
	listErr := inventory.ListDurableObjects(ctx, func(object DurableObject) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		report.Scanned++

		if err := object.Validate(); err != nil {
			// An object this pass cannot fully describe is one it must not
			// delete. Count it as a failure so the malformed entry is visible.
			report.Failed++
			failures = append(failures, fmt.Errorf("snapshot: unusable durable object: %w", err))
			return nil
		}

		now := lifecycle.now()
		if now.Sub(object.CreatedAt) < minAge {
			report.Deferred++
			return nil
		}

		reclaimable := false
		err := WithDigestLease(ctx, lifecycle.locks, []Digest{object.Digest}, func(lease DigestLease) error {
			state, err := lifecycle.metadata.DigestState(ctx, lease, object.Digest, now)
			if err != nil {
				return err
			}
			if err := state.Validate(); err != nil {
				return err
			}
			// Any trace of the digest in metadata retains the object. This is
			// deliberately broader than "available": a snapshot that is still
			// uploading, already expired, or otherwise not yet readable is
			// still a reference, and so is a bare location row.
			if len(state.Snapshots) > 0 || len(state.Stages) > 0 || len(state.Locations) > 0 {
				return nil
			}
			reclaimable = true
			if mode != OrphanSweepReclaim {
				return nil
			}
			return inventory.DeleteDurableObject(ctx, object)
		})
		if err != nil {
			if ctxErr := contextError(ctx, err); ctxErr != nil {
				return ctxErr
			}
			report.Failed++
			failures = append(failures, fmt.Errorf("snapshot: sweep digest %s: %w", object.Digest, err))
			return nil
		}
		if !reclaimable {
			report.Deferred++
			return nil
		}
		report.OrphansReclaimable++
		report.OrphanBytes += object.Bytes
		if mode == OrphanSweepReclaim {
			report.OrphansReclaimed++
		}
		return nil
	})
	if listErr != nil {
		if ctxErr := contextError(ctx, listErr); ctxErr != nil {
			return report, ctxErr
		}
		failures = append(failures, listErr)
	}
	return report, joinLifecycleFailures(failures)
}
