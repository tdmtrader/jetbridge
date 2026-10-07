package reclaim

import (
	"context"
	"errors"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// Lister is the sweep's view of the output namespace: one bucket-wide page at
// a time, with each object's marker metadata.
type Lister interface {
	List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error)
}

// The classes the sweep sorts every listed object into. Every one but
// SweepDeleted is an object the sweep left alone, and each is counted.
const (
	SweepDeleted    = "deleted"
	SweepUnmarked   = "unmarked"   // no marker, or one this cohort cannot read
	SweepForeign    = "foreign"    // a marker naming another store
	SweepYoung      = "young"      // not yet older than twice the capture deadline
	SweepRegistered = "registered" // a lifecycle row exists; reclamation owns it
	SweepProtected  = "protected"  // a capture or input publication may still register it
	SweepMismatched = "mismatched" // listed at a key its own marker does not derive
	SweepAbsent     = "absent"     // gone by the time the delete arrived
	SweepConflict   = "conflict"   // the generation moved; never retried unconditionally
	SweepFailed     = "failed"     // the delete was refused or did not answer
)

// SweepClasses is every class, in a fixed order, so a metric emits each one
// on every pass including the zeroes.
func SweepClasses() []string {
	return []string{SweepDeleted, SweepUnmarked, SweepForeign, SweepYoung, SweepRegistered,
		SweepProtected, SweepMismatched, SweepAbsent, SweepConflict, SweepFailed}
}

// Sweep is the orphan sweep component.
//
// It lists the whole output namespace under the deployment prefix and deletes
// an object only when all of these hold: its marker names this store, it has
// no lifecycle row, nothing pending or publishing could still give it one, and
// it is older than twice the capture deadline. The delete is the exact
// generation the listing reported, through the reclaimer's conditional delete;
// there is no key-only delete anywhere. Every other object is counted, and the
// counts are logged and published.
type Sweep struct {
	Locker     lock.LockFactory
	Transactor hangaroutput.Transactor
	Repository *db.HangarOutputRepository
	Namespace  output.OutputNamespace
	Lister     Lister
	Reclaimer  *reclaimer.Reclaimer

	// CaptureDeadline is the configured capture deadline; an object younger
	// than twice it is never an orphan.
	CaptureDeadline time.Duration
	DeleteTimeout   time.Duration

	// PageSize bounds one List call. Zero is defaultPageSize.
	PageSize int
}

const defaultPageSize = 100

// Run is one sweep over the namespace, under the shared deletion lock.
func (sweep *Sweep) Run(ctx context.Context) error {
	return exclusively(ctx, sweep.Locker, func() error {
		counts, err := sweep.Once(ctx)
		logger := lagerctx.FromContext(ctx)
		data := lager.Data{}
		for class, count := range counts {
			data[class] = count
		}
		logger.Info("hangar-output-orphan-sweep", data)
		metric.HangarOutputOrphanSweep{Objects: counts}.Emit(logger)
		return err
	})
}

// Once lists and classifies every object once, and returns the count per
// class. It is exported for the acceptance specs; Run is the component.
func (sweep *Sweep) Once(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	for _, class := range SweepClasses() {
		counts[class] = 0
	}

	now, err := sweep.databaseNow(ctx)
	if err != nil {
		return counts, err
	}
	threshold := 2 * sweep.CaptureDeadline
	store := sweep.Namespace.StoreIdentity()

	pageSize := sweep.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	request := objectstore.ListRequest{Prefix: sweep.Namespace.ListPrefix(), PageSize: pageSize}

	var firstErr error
	for {
		page, err := sweep.Lister.List(ctx, sweep.Namespace.Bucket(), request)
		if err != nil {
			return counts, errors.Join(firstErr, err)
		}
		for _, object := range page.Objects {
			class, err := sweep.classify(ctx, object, store, now, threshold)
			counts[class]++
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if page.Done || page.LastKey == "" {
			return counts, firstErr
		}
		request.After = page.LastKey
	}
}

func (sweep *Sweep) databaseNow(ctx context.Context) (time.Time, error) {
	tx, err := sweep.Transactor.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now, err := sweep.Repository.HangarDatabaseNow(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

func (sweep *Sweep) classify(ctx context.Context, object objectstore.Attrs, store string, now time.Time, threshold time.Duration) (string, error) {
	marker, err := output.ParseObjectMarker(object.Metadata)
	if err != nil {
		return SweepUnmarked, nil
	}
	if marker.Store != store {
		return SweepForeign, nil
	}
	if object.Created.IsZero() || now.Sub(object.Created) <= threshold {
		return SweepYoung, nil
	}

	ref := hangar.TreeRef{Scope: marker.Scope, Digest: marker.Digest, Generation: object.Generation}
	key, err := hangar.TreeKey(sweep.Namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil || key != object.Key || ref.Validate() != nil {
		return SweepMismatched, nil
	}

	tx, err := sweep.Transactor.Begin()
	if err != nil {
		return SweepFailed, err
	}
	defer func() { _ = tx.Rollback() }()

	verdict, err := sweep.Repository.JudgeOrphan(ctx, tx, ref)
	if err != nil {
		return SweepFailed, err
	}
	switch verdict {
	case db.HangarOrphanRegistered:
		return SweepRegistered, nil
	case db.HangarOrphanProtected:
		return SweepProtected, nil
	}

	// The verdict's locks are held across the delete: nothing registers this
	// generation, or starts publishing its tree, between the judgement and the
	// act.
	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout(sweep.DeleteTimeout))
	defer cancel()
	outcome, err := sweep.Reclaimer.DeleteExactGeneration(deleteCtx, ref,
		output.DeletePrecondition{Generation: object.Generation, Metageneration: object.Metageneration})
	switch outcome {
	case output.DeleteConfirmed:
		return SweepDeleted, tx.Commit()
	case output.DeleteAlreadyAbsent:
		return SweepAbsent, nil
	case output.DeleteGenerationConflict:
		return SweepConflict, nil
	default:
		return SweepFailed, err
	}
}
