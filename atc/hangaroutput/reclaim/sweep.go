package reclaim

import (
	"context"
	"fmt"
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
	SweepUnmarked   = "unmarked"   // no marker, or one this store cannot read
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
// an object only when all of these hold: its marker names this store and this
// scope, it has no lifecycle row, nothing pending or publishing could still
// give it one, and it is older than twice the capture deadline. The delete is
// the exact generation the listing reported, through the reclaimer's
// conditional delete; there is no key-only delete anywhere. Every other object
// is counted, and the counts are logged and published.
//
// A page is judged in one statement, and the shared deletion lock is held one
// page at a time, so the reclaim pass is never shut out for a whole bucket. A
// pass stops at its duration budget and the next one resumes after the last
// page it finished.
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

	// MaxDuration bounds one pass. Zero is DefaultSweepDuration.
	MaxDuration time.Duration

	// judged says the last pass judged at least one page; a pass that found
	// the lock held elsewhere judged nothing and is neither a completed pass
	// nor a failure.
	judged bool

	// after is where the next pass resumes, in this process's memory: a pass
	// cut short by its budget, or by the lock being held elsewhere, is
	// continued rather than restarted. A restarted web starts from the top.
	after string
}

const (
	defaultPageSize = 100

	// DefaultSweepDuration bounds one sweep pass.
	DefaultSweepDuration = 5 * time.Minute
)

// Run is one sweep pass. Its counts are logged and published; a pass that
// failed counts as a failure and not as a completed pass.
func (sweep *Sweep) Run(ctx context.Context) error {
	counts, err := sweep.Once(ctx)
	logger := lagerctx.FromContext(ctx)
	data := lager.Data{}
	for class, count := range counts {
		data[class] = count
	}
	if err != nil {
		logger.Error("hangar-output-orphan-sweep-failed", err, data)
	} else {
		logger.Info("hangar-output-orphan-sweep", data)
	}
	metric.HangarOutputOrphanSweep{Objects: counts, Failed: err != nil, Skipped: err == nil && !sweep.judged}.Emit(logger)
	return err
}

// Once takes one pass and returns the count per class. It is exported for the
// acceptance specs; Run is the component.
func (sweep *Sweep) Once(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	for _, class := range SweepClasses() {
		counts[class] = 0
	}

	sweep.judged = false
	started := time.Now()
	budget := sweep.MaxDuration
	if budget <= 0 {
		budget = DefaultSweepDuration
	}
	now, err := sweep.databaseNow(ctx)
	if err != nil {
		return counts, err
	}
	threshold := 2 * sweep.CaptureDeadline

	pageSize := sweep.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	for {
		if time.Since(started) > budget {
			lagerctx.FromContext(ctx).Info("hangar-output-orphan-sweep-budget-spent",
				lager.Data{"resume-after-page": sweep.after != ""})
			return counts, nil
		}
		page, err := sweep.Lister.List(ctx, sweep.Namespace.Bucket(),
			objectstore.ListRequest{Prefix: sweep.Namespace.ListPrefix(), PageSize: pageSize, After: sweep.after})
		if err != nil {
			// Progress is kept: the next pass lists from the same place.
			return counts, fmt.Errorf("listing the output namespace: %w", err)
		}

		var pageErr error
		ran, err := exclusively(ctx, sweep.Locker, func() error {
			pageErr = sweep.judgePage(ctx, page.Objects, now, threshold, counts)
			return nil
		})
		if err != nil {
			return counts, err
		}
		if !ran {
			// The reclaim pass or another web's sweep holds the lock; this
			// page is the next pass's.
			return counts, nil
		}
		sweep.judged = true
		if pageErr != nil {
			return counts, pageErr
		}

		if page.Done || page.LastKey == "" {
			sweep.after = ""
			return counts, nil
		}
		sweep.after = page.LastKey
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

// judgePage sorts one page: everything decidable from the listing first, then
// one statement for the rest, then a locked re-judgement and a delete for each
// orphan.
func (sweep *Sweep) judgePage(ctx context.Context, objects []objectstore.Attrs, now time.Time, threshold time.Duration, counts map[string]int) error {
	store := sweep.Namespace.StoreIdentity()
	candidates := map[hangar.TreeRef]objectstore.Attrs{}
	var refs []hangar.TreeRef
	for _, object := range objects {
		ref, class := sweep.preclassify(object, store, now, threshold)
		if class != "" {
			counts[class]++
			continue
		}
		candidates[ref] = object
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}

	tx, err := sweep.Transactor.Begin()
	if err != nil {
		return err
	}
	verdicts, err := sweep.Repository.JudgeOrphans(ctx, tx, refs)
	_ = tx.Rollback()
	if err != nil {
		return err
	}

	var firstErr error
	for _, ref := range refs {
		switch verdicts[ref] {
		case db.HangarOrphanRegistered:
			counts[SweepRegistered]++
		case db.HangarOrphanProtected:
			counts[SweepProtected]++
		default:
			class, err := sweep.deleteOrphan(ctx, ref, candidates[ref])
			counts[class]++
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// preclassify decides what the listing alone can: unmarked, foreign, young or
// mismatched. An empty class is a candidate the database must judge.
func (sweep *Sweep) preclassify(object objectstore.Attrs, store string, now time.Time, threshold time.Duration) (hangar.TreeRef, string) {
	marker, err := output.ParseObjectMarker(object.Metadata)
	if err != nil {
		return hangar.TreeRef{}, SweepUnmarked
	}
	if marker.Store != store || marker.Scope != sweep.Namespace.Scope() {
		return hangar.TreeRef{}, SweepForeign
	}
	if object.Created.IsZero() || now.Sub(object.Created) <= threshold {
		return hangar.TreeRef{}, SweepYoung
	}
	ref := hangar.TreeRef{Scope: marker.Scope, Digest: marker.Digest, Generation: object.Generation}
	key, err := hangar.TreeKey(sweep.Namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil || key != object.Key || ref.Validate() != nil {
		return hangar.TreeRef{}, SweepMismatched
	}

	return ref, ""
}

// deleteOrphan judges one candidate again under the tree and row locks and,
// still holding them, deletes its exact generation: nothing registers it, or
// starts publishing its tree, between the judgement and the act.
func (sweep *Sweep) deleteOrphan(ctx context.Context, ref hangar.TreeRef, object objectstore.Attrs) (string, error) {
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

	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout(sweep.DeleteTimeout))
	defer cancel()
	outcome, err := sweep.Reclaimer.DeleteExactGeneration(deleteCtx, ref,
		output.DeletePrecondition{Generation: object.Generation})
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
