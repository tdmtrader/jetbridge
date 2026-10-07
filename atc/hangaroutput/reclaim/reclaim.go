// Package reclaim is the web's two deleting passes over the output namespace:
// the reclaim pass (admission, delete, finalization) and the orphan sweep.
//
// They are web components and not a separate workload because the web is the
// only process that can both read the lifecycle rows and hold the output
// namespace's delete credential; node daemons publish and never delete. Both
// passes take the same PostgreSQL advisory lock, so across every web replica at
// most one of them is deleting at a time, and no reclaim admission interleaves
// with an orphan verdict.
//
// This package and the durable cache tier are the only places a delete client
// over a real backend is constructed (hangar/architecture_test.go).
package reclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/hangar/disk"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// LockID is the one advisory lock both passes run under.
var LockID = lock.NewTaskLockID("hangar_output_deletes")

// StoreConfig is how the web reaches the output namespace's store.
type StoreConfig struct {
	Store    string // output.StoreGCS or output.StoreDisk
	Endpoint string
	StoreID  string
	CACert   string
	Timeout  time.Duration

	// ListTokenFile and DeleteTokenFile are the disk store's two role
	// credentials: list-and-stat for the sweep, stat-and-delete for the
	// delete. GCS uses the web's ambient credential for both.
	ListTokenFile   string
	DeleteTokenFile string
}

// OpenStore constructs the sweep's lister and the reclaimer -- the one delete
// role -- over the output namespace. It is the only place in the web that
// names a delete constructor.
func OpenStore(ctx context.Context, config StoreConfig, namespace output.OutputNamespace) (Lister, *reclaimer.Reclaimer, func() error, error) {
	lister, deleter, closer, err := openClients(ctx, config)
	if err != nil {
		return nil, nil, nil, err
	}
	deletes, err := reclaimer.New(namespace, reclaimer.Restrict(deleter))
	if err != nil {
		_ = closer()
		return nil, nil, nil, err
	}
	return lister, deletes, closer, nil
}

func openClients(ctx context.Context, config StoreConfig) (objectstore.Client, objectstore.DeleteClient, func() error, error) {
	if config.Store == output.StoreDisk {
		lister, err := disk.NewClient(disk.ClientConfig{Endpoint: config.Endpoint, StoreID: config.StoreID,
			TokenFile: config.ListTokenFile, CACert: config.CACert, Timeout: config.Timeout})
		if err != nil {
			return nil, nil, nil, err
		}
		deleter, err := disk.NewDeleteClient(disk.ClientConfig{Endpoint: config.Endpoint, StoreID: config.StoreID,
			TokenFile: config.DeleteTokenFile, CACert: config.CACert, Timeout: config.Timeout})
		if err != nil {
			return nil, nil, nil, err
		}
		return lister, deleter, func() error { return nil }, nil
	}

	lister, closeLister, err := hangargcs.NewClient(ctx, config.Endpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	deleter, closeDeleter, err := hangargcs.NewDeleteClient(ctx, config.Endpoint)
	if err != nil {
		_ = closeLister()
		return nil, nil, nil, err
	}
	return lister, deleter, func() error { return errors.Join(closeLister(), closeDeleter()) }, nil
}

// exclusively runs fn under LockID, or not at all when another web holds it.
func exclusively(ctx context.Context, locker lock.LockFactory, fn func() error) error {
	logger := lagerctx.FromContext(ctx)
	held, acquired, err := locker.Acquire(logger, LockID)
	if err != nil {
		return err
	}
	if !acquired {
		logger.Debug("hangar-output-deletes-held-elsewhere")
		return nil
	}
	defer func() { _ = held.Release() }()

	return fn()
}

// Pass is the reclaim component: admission, then delete, then finalization.
type Pass struct {
	Locker     lock.LockFactory
	Transactor hangaroutput.Transactor
	Repository *db.HangarOutputRepository
	Reclaimer  *reclaimer.Reclaimer

	// Grace is the publication grace; admission requires it elapsed since
	// registration, measured on the database clock.
	Grace time.Duration

	// DeleteTimeout bounds one conditional delete; a job's lease term is
	// derived from it.
	DeleteTimeout time.Duration

	// Batch bounds how many generations one pass admits and how many jobs it
	// advances. Zero is defaultBatch.
	Batch int

	// OwnerID names this web process on the jobs it admits and holds.
	OwnerID string
}

const defaultBatch = 10

func (pass *Pass) batch() int {
	if pass.Batch <= 0 {
		return defaultBatch
	}
	return pass.Batch
}

func (pass *Pass) term() time.Duration {
	return output.LeaseTermFor(deleteTimeout(pass.DeleteTimeout))
}

// DefaultDeleteTimeout bounds one conditional delete when none is configured.
const DefaultDeleteTimeout = 2 * time.Minute

func deleteTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultDeleteTimeout
	}
	return configured
}

// Run is one pass: admit what is eligible, then advance every due job by one
// conditional delete and finalize what that delete settled.
func (pass *Pass) Run(ctx context.Context) error {
	return exclusively(ctx, pass.Locker, func() error {
		admitted, admitErr := pass.Admit(ctx)
		finalized, open, deleteErr := pass.DeleteDue(ctx)

		metric.HangarOutputReclaimPass{Admitted: admitted, Finalized: finalized, Open: open}.
			Emit(lagerctx.FromContext(ctx))

		return errors.Join(admitErr, deleteErr)
	})
}

// Admit turns grace-elapsed, unprotected generations into reclaim jobs.
//
// The candidate query excludes a generation with an open claim, a live read
// lease, a pending or publishing capture of its tree, an unregistered input
// publication of its tree, or an unfinalized job; AdmitReclaim rechecks every
// one under the exact-lifecycle lock, and the schema's exclusion trigger
// refuses at commit whatever slipped between.
func (pass *Pass) Admit(ctx context.Context) (int, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return 0, err
	}
	candidates, err := pass.Repository.ReclaimCandidates(ctx, tx, pass.Grace, pass.batch())
	_ = tx.Rollback()
	if err != nil {
		return 0, err
	}

	admitted := 0
	var firstErr error
	for _, candidate := range candidates {
		err := pass.admitOne(ctx, candidate)
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, output.ErrConflict), errors.Is(err, output.ErrAtRisk):
			// Something took a claim, a lease or a capture between the query
			// and the lock, or an integrity finding is open: "not yet".
		default:
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return admitted, firstErr
}

func (pass *Pass) admitOne(ctx context.Context, candidate db.HangarReclaimCandidate) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.AdmitReclaim(ctx, tx, candidate.Ref, pass.OwnerID,
		candidate.Metageneration, pass.term(), pass.Grace); err != nil {
		return err
	}

	return tx.Commit()
}

// DeleteDue advances each admitted job by exactly one conditional delete and
// finalizes the ones that delete settled. It returns how many jobs it
// finalized and how many it left open.
func (pass *Pass) DeleteDue(ctx context.Context) (int, int, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return 0, 0, err
	}
	jobs, err := pass.Repository.DueReclaimJobs(ctx, tx, pass.batch())
	_ = tx.Rollback()
	if err != nil {
		return 0, 0, err
	}

	finalized, open := 0, 0
	var firstErr error
	for _, job := range jobs {
		settled, err := pass.deleteOne(ctx, job)
		if settled {
			finalized++
		} else {
			open++
		}
		if err != nil && !errors.Is(err, output.ErrConflict) && firstErr == nil {
			firstErr = err
		}
	}

	return finalized, open, firstErr
}

// deleteOne is the act: hold the job, durably record that a delete is being
// asked, and ask. Everything after the commit of the admitted delete is the
// answer to a question this system has already said it asked, which is what
// lets a lost response be told apart from somebody else's deletion.
func (pass *Pass) deleteOne(ctx context.Context, job db.HangarReclaimJob) (bool, error) {
	job, err := pass.hold(ctx, job)
	if err != nil {
		return false, err
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return false, err
	}
	attempt, err := pass.Repository.AdmitDelete(ctx, tx, job, deleteTimeout(pass.DeleteTimeout))
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	// Outside every lock and transaction: a store that does not answer holds
	// up this one generation and nothing else.
	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout(pass.DeleteTimeout))
	outcome, deleteErr := pass.Reclaimer.DeleteExactGeneration(deleteCtx, job.Ref,
		output.DeletePrecondition{Generation: job.Ref.Generation, Metageneration: job.Metageneration})
	cancel()

	return pass.Finalize(ctx, job, attempt, outcome, deleteErr)
}

// hold renews a job this web owns, or takes over one whose owner let its
// lease lapse (advancing the fence, so the lapsed owner's late writes refuse).
func (pass *Pass) hold(ctx context.Context, job db.HangarReclaimJob) (db.HangarReclaimJob, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return job, err
	}
	defer func() { _ = tx.Rollback() }()

	var held db.HangarReclaimJob
	if job.OwnerID == pass.OwnerID {
		held, err = pass.Repository.RenewReclaimLease(ctx, tx, job, pass.term())
	} else {
		held, err = pass.Repository.TakeOverReclaimJob(ctx, tx, job, pass.OwnerID, pass.term())
	}
	if err != nil {
		return job, err
	}

	return held, tx.Commit()
}

// Finalize records what the store answered and, where that answer settles
// the job, finalizes it, in one transaction. It reports whether the job was
// finalized.
//
// A confirmed delete is confirmed. Absence is inferred only when an earlier
// delete of this job lost its response; absence with nothing behind it is an
// out-of-band lifetime violation, recorded as an integrity finding that blocks
// admission. A generation conflict is never broadened into another delete. A
// refused delete is a runtime principal denial. A timeout or an infrastructure
// failure leaves the job open for the next pass.
func (pass *Pass) Finalize(ctx context.Context, job db.HangarReclaimJob, attempt int64, outcome output.DeleteOutcome, deleteErr error) (bool, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.RecordDeleteOutcome(ctx, tx, job, attempt, outcome); err != nil {
		return false, err
	}

	var reported error
	settled := true
	switch outcome {
	case output.DeleteConfirmed:
		err = pass.Repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)

	case output.DeleteAlreadyAbsent:
		var explained bool
		explained, err = pass.Repository.AbsenceExplainedByALostResponse(ctx, tx, job, attempt)
		if err != nil {
			break
		}
		if explained {
			err = pass.Repository.FinalizeReclaim(ctx, tx, job, output.ReclaimInferred, true)
			break
		}
		if err = pass.Repository.FinalizeReclaim(ctx, tx, job, output.ReclaimAbandoned, false); err != nil {
			break
		}
		err = pass.Repository.RecordOutOfBandAbsence(ctx, tx, job.Ref)
		reported = fmt.Errorf("%w: reclaim job %d found %s/%s/%d already absent with no earlier "+
			"delete to explain it; recorded as an out-of-band lifetime violation", output.ErrAtRisk,
			job.ID, job.Ref.Scope, job.Ref.Digest, job.Ref.Generation)

	case output.DeleteGenerationConflict:
		err = pass.Repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConflicted, false)

	case output.DeleteUnauthorized:
		if err = pass.Repository.FinalizeReclaim(ctx, tx, job, output.ReclaimAbandoned, false); err != nil {
			break
		}
		err = pass.Repository.RecordRuntimePrincipalDenial(ctx, tx, output.PrincipalReclaimer,
			"the object store refused the web a conditional delete its reclaim credential is "+
				"configured to hold")
		reported = fmt.Errorf("%w: the object store refused reclaim job %d a conditional delete",
			output.ErrUnauthorized, job.ID)

	default:
		settled = false
		reported = deleteErr
		if deleteErr != nil && !errors.Is(deleteErr, output.ErrTimeout) &&
			!errors.Is(deleteErr, output.ErrInfrastructure) {
			return false, deleteErr
		}
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	return settled, reported
}
