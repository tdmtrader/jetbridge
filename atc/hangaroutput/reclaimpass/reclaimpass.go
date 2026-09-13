// Package reclaimpass is the reclaimer's two bounded units of work: admitting
// reclamations, and advancing the ones already admitted.
//
// They are two passes and not one because they are two operation kinds with two
// durable leases -- `reclaim_admission` and `reclaim_delete` -- and the schema
// separates them for the reason the whole plane separates owners: admission is
// the decision to delete and the delete is the act, and a single lease over both
// would let one stuck external call hold the decision queue behind it.
//
// It names exactly one output role package -- hangar/output/reclaimer -- which
// is the one this repository's delete-capability guard allows in exactly one
// binary.
package reclaimpass

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// AdmissionPass turns grace-elapsed, unprotected generations into reclaim jobs.
//
// Without it the reclaimer drains a queue nothing fills: DueReclaimJobs returns
// nothing forever, every pass reports 0/ok, and no published object in the
// deployment is ever collected. That was R1-F1, and it is why this pass exists
// as production code with a lease of its own rather than as a method tests call.
//
// It makes no external call at all. Admission is a database decision -- state,
// claims, read leases, reservations, grace and the policy trust state -- and the
// object store is not asked anything until the delete pass below.
type AdmissionPass struct {
	Repository *db.HangarOutputRepository
	Transactor controller.Transactor

	// Grace is the configured publication grace, and it is passed to
	// AdmitReclaim as well as used to select candidates. The selection is a
	// bound on how much work one pass opens; the admission is the precondition.
	// Deriving the answer twice from one configured value is not two rules --
	// both comparisons are made by the database against the row's own
	// registered_at, and the second one holds the exact-lifecycle lock.
	Grace time.Duration

	// Term is the lease term a newly admitted job starts with.
	Term time.Duration

	// Batch bounds one pass. Zero means DefaultAdmissionBatch.
	Batch int

	// OwnerID is the controller identity admitted jobs are minted under.
	OwnerID string
}

// DefaultAdmissionBatch is how many generations one admission pass may admit.
const DefaultAdmissionBatch = 10

var _ controller.Pass = (*AdmissionPass)(nil)

func (pass *AdmissionPass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	candidates, err := pass.candidates(ctx, lease)
	if err != nil {
		return 0, err
	}

	admitted := 0
	var firstErr error
	for _, candidate := range candidates {
		err := pass.admit(ctx, candidate)
		switch {
		case err == nil:
			admitted++

		case errors.Is(err, output.ErrConflict), errors.Is(err, output.ErrAtRisk):
			// A fact about one generation: something took a claim, a read
			// lease or a reservation between the query and the lock, or the
			// epoch is at risk. Either is a "not yet" and neither is this
			// pass failing.

		default:
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return admitted, firstErr
}

func (pass *AdmissionPass) candidates(ctx context.Context, lease output.OperationLease) ([]db.HangarReclaimCandidate, error) {
	batch := pass.Batch
	if batch <= 0 {
		batch = DefaultAdmissionBatch
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	candidates, err := pass.Repository.ReclaimCandidates(ctx, tx,
		int64(lease.ActivationEpoch), pass.Grace, batch)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return candidates, nil
}

func (pass *AdmissionPass) admit(ctx context.Context, candidate db.HangarReclaimCandidate) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.AdmitReclaim(ctx, tx, candidate.Ref, pass.OwnerID,
		candidate.Metageneration, pass.Term, pass.Grace); err != nil {
		return err
	}

	return tx.Commit()
}

// DeletePass advances each admitted job by one bounded step.
//
// One step, not one job to completion. A job whose store will not answer holds
// up this one generation and nothing else, which is the same rule the capture
// recoverer follows for the same reason.
type DeletePass struct {
	Reclaimer     *reclaimer.Reclaimer
	Repository    *db.HangarOutputRepository
	Transactor    controller.Transactor
	DeleteTimeout time.Duration
	Batch         int

	// OwnerID is this controller's identity, and Term the lease it works
	// under. A job this owner already holds is RENEWED before the step, which
	// is Req 48's "renewed at least once per minute"; a job whose owner let its
	// lease lapse is TAKEN OVER, which advances the fence and is what makes the
	// previous owner's in-flight writes refuse. A live other owner's job is
	// neither, and is skipped.
	OwnerID string
	Term    time.Duration
}

var _ controller.Pass = (*DeletePass)(nil)

// Run advances a bounded batch, and REPORTS what it could not advance.
//
// It used to `continue` on every per-job error under a comment saying
// "recorded and skipped", and nothing recorded anything: an unreachable store,
// an unauthorized principal and a healthy empty queue all reported `class=ok`
// with a processed count of zero. That is precisely the state the Reporter's own
// documentation says the count exists to make distinguishable -- "a controller
// that did nothing and a controller that is stuck look identical without it" --
// and the count alone cannot say it, because both are zero.
//
// One representative error is enough and a list would be worse: the class is a
// bounded metric label, a stuck plane is stuck for one reason at a time in
// practice, and every per-job outcome is already durable in its own row. What
// the pass owes telemetry is "this was not ok", said in the leaf's vocabulary.
//
// A lease conflict is NOT one of those. A job whose owner is alive is somebody
// else's this minute, which is a correctly configured pair of replicas racing,
// and reporting it would make that look broken every time it happened.
func (pass *DeletePass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	jobs, err := pass.due(ctx)
	if err != nil {
		return 0, err
	}

	advanced := 0
	var firstErr error
	for _, job := range jobs {
		err := pass.advance(ctx, job)
		switch {
		case err == nil:
			advanced++

		case errors.Is(err, output.ErrConflict):
			// Another owner holds this job and has not expired. Not this
			// pass's work and not a failure.

		default:
			// One unreachable store must not stop every other generation in
			// the deployment from being collected, so the loop goes on -- but
			// the pass does not then attest healthy.
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return advanced, firstErr
}

func (pass *DeletePass) due(ctx context.Context) ([]db.HangarReclaimJob, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	return pass.Repository.DueReclaimJobs(ctx, tx, pass.Batch)
}

// advance takes one job through exactly one external call.
//
// The admitted-delete record is COMMITTED first. Everything after it is the
// answer to a question this system has already durably said it asked.
func (pass *DeletePass) advance(ctx context.Context, job db.HangarReclaimJob) error {
	job, err := pass.hold(ctx, job)
	if err != nil {
		return err
	}

	attempt, err := pass.admitDelete(ctx, job)
	if err != nil {
		return err
	}

	// Outside every database lock and every transaction. A store that does not
	// answer holds up this one generation.
	outcome, deleteErr := pass.Reclaimer.DeleteExactGeneration(ctx, job.Ref,
		output.DeletePrecondition{
			Generation:     job.Ref.Generation,
			Metageneration: job.Metageneration,
		})

	return pass.record(ctx, job, attempt, outcome, deleteErr)
}

// hold takes or refreshes the authority this step runs under.
//
// Expiry alone releases nothing and proves nothing. A job this controller
// already owns is renewed so the step begins with a full term; a job whose owner
// let its lease lapse is taken over, and the takeover advances the fence, which
// is what makes the lapsed owner's later writes -- including a delete outcome it
// is still holding -- refuse. A job whose owner is alive is somebody else's this
// minute and comes back as a conflict.
func (pass *DeletePass) hold(ctx context.Context, job db.HangarReclaimJob) (db.HangarReclaimJob, error) {
	term := pass.Term
	if term <= 0 {
		term = output.LeaseTermFor(pass.DeleteTimeout)
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return job, err
	}
	defer func() { _ = tx.Rollback() }()

	var held db.HangarReclaimJob
	if job.OwnerID == pass.OwnerID {
		held, err = pass.Repository.RenewReclaimLease(ctx, tx, job, term)
	} else {
		held, err = pass.Repository.TakeOverReclaimJob(ctx, tx, job, pass.OwnerID, term)
	}
	if err != nil {
		return job, err
	}
	if err := tx.Commit(); err != nil {
		return job, err
	}

	return held, nil
}

func (pass *DeletePass) admitDelete(ctx context.Context, job db.HangarReclaimJob) (int64, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	attempt, err := pass.Repository.AdmitDelete(ctx, tx, job, pass.DeleteTimeout)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return attempt, nil
}

// record writes the outcome and, where the evidence supports one, the
// finalization.
//
// The mapping from a store's answer to a job's outcome is the whole of Req 49,
// and every arm of it is about what this plane is entitled to CLAIM. A
// confirmed delete finalizes confirmed. Absence finalizes inferred, because
// there IS a prior admitted delete -- that record was committed above. A
// generation conflict is debt and never a broader retry. Anything else leaves
// the job open for the next pass under a renewed lease.
func (pass *DeletePass) record(ctx context.Context, job db.HangarReclaimJob, attempt int64, outcome output.DeleteOutcome, deleteErr error) error {
	unauthorized := false

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.RecordDeleteOutcome(ctx, tx, job, attempt, outcome); err != nil {
		return err
	}

	switch {
	case outcome == output.DeleteConfirmed:
		if err := pass.Repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimConfirmed, false); err != nil {
			return err
		}

	case outcome == output.DeleteAlreadyAbsent:
		// Absent, with a durable admitted delete behind it. That is INFERRED
		// and never confirmed: this plane did not see the acknowledgement, and
		// claiming it would be claiming evidence it does not have.
		if err := pass.Repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimInferred, true); err != nil {
			return err
		}

	case outcome == output.DeleteGenerationConflict:
		if err := pass.Repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimConflicted, false); err != nil {
			return err
		}

	case outcome == output.DeleteUnauthorized:
		// The principal lost its grant. The job is finalized and the epoch goes
		// at risk -- and the pass says so, because a reclaimer whose store
		// refuses it is not a healthy reclaimer that happened to finish a job.
		// The error is returned AFTER the commit below, so the record stands
		// whatever telemetry does with it.
		// The generation goes back to being protected rather than being
		// deleted on a guess, the EPOCH goes at risk because this is Req 52's
		// platform-principal mismatch in its runtime form, and the pass reports
		// it. Finalizing the one job and returning nil, which is what this did,
		// leaves a plane admitting new work under an identity the store has
		// just refused, reporting class=ok while it does.
		unauthorized = true
		if err := pass.Repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimAbandoned, false); err != nil {
			return err
		}
		if err := pass.Repository.RecordRuntimePrincipalDenial(ctx, tx,
			job.ActivationEpoch, output.PrincipalReclaimer,
			"the object store refused this principal a conditional delete its role is "+
				"configured to hold; either the grant was removed or this is not the "+
				"principal the deployment attested"); err != nil {
			return err
		}

	default:
		// A timeout or an infrastructure failure. The job stays open: the next
		// pass re-admits a delete under the same lease, and the object's
		// absence -- if the first one did land -- is then inferred rather than
		// mistaken for somebody else's deletion.
		if deleteErr != nil && !errors.Is(deleteErr, output.ErrTimeout) &&
			!errors.Is(deleteErr, output.ErrInfrastructure) {
			return deleteErr
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	if unauthorized {
		return fmt.Errorf("%w: the object store refused reclaim job %d a conditional delete; "+
			"the generation is protected again and the epoch is at risk", output.ErrUnauthorized,
			job.ID)
	}
	if deleteErr != nil {
		// A timeout or an infrastructure failure, recorded and left open for
		// the next pass. The job survived; the PASS did not do what it set out
		// to do, and a plane whose store is unreachable must not read healthy.
		return deleteErr
	}

	return nil
}
